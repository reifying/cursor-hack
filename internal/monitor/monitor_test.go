package monitor

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"cursor-wrap/internal/events"
)

// --- fakeClock ---

type fakeClock struct {
	now time.Time
}

func newFakeClock(t time.Time) *fakeClock {
	return &fakeClock{now: t}
}

func (c *fakeClock) Now() time.Time { return c.now }

func (c *fakeClock) Advance(d time.Duration) {
	c.now = c.now.Add(d)
}

// --- test event helpers ---

var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func systemInitEvent(sessionID string) events.AnnotatedEvent {
	raw, _ := json.Marshal(map[string]any{
		"type":           "system",
		"subtype":        "init",
		"session_id":     sessionID,
		"model":          "test-model",
		"cwd":            "/tmp",
		"permissionMode": "default",
	})
	return events.AnnotatedEvent{
		RecvTime: t0,
		Raw:      raw,
		Parsed:   events.RawEvent{Type: "system", Subtype: "init"},
	}
}

func thinkingCompletedEvent(recvTime time.Time) events.AnnotatedEvent {
	raw, _ := json.Marshal(map[string]string{
		"type":    "thinking",
		"subtype": "completed",
	})
	return events.AnnotatedEvent{
		RecvTime: recvTime,
		Raw:      raw,
		Parsed:   events.RawEvent{Type: "thinking", Subtype: "completed"},
	}
}

func assistantEvent(recvTime time.Time) events.AnnotatedEvent {
	raw, _ := json.Marshal(map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"content": []map[string]string{{"type": "text", "text": "hello"}},
		},
	})
	return events.AnnotatedEvent{
		RecvTime: recvTime,
		Raw:      raw,
		Parsed:   events.RawEvent{Type: "assistant"},
	}
}

func toolCallStartedEvent(recvTime time.Time, callID string, timeoutMS int64) events.AnnotatedEvent {
	toolCall := map[string]any{
		"shellToolCall": map[string]any{
			"args": map[string]any{
				"command": fmt.Sprintf("cmd-%s", callID),
				"timeout": timeoutMS,
			},
		},
	}
	tcJSON, _ := json.Marshal(toolCall)
	raw, _ := json.Marshal(map[string]any{
		"type":          "tool_call",
		"subtype":       "started",
		"call_id":       callID,
		"model_call_id": "mc-1",
		"timestamp_ms":  recvTime.UnixMilli(),
		"tool_call":     json.RawMessage(tcJSON),
	})
	return events.AnnotatedEvent{
		RecvTime: recvTime,
		Raw:      raw,
		Parsed:   events.RawEvent{Type: "tool_call", Subtype: "started"},
	}
}

func nonShellToolCallStartedEvent(recvTime time.Time, callID string) events.AnnotatedEvent {
	toolCall := map[string]any{
		"lsToolCall": map[string]any{
			"args": map[string]any{
				"path": "/tmp",
			},
		},
	}
	tcJSON, _ := json.Marshal(toolCall)
	raw, _ := json.Marshal(map[string]any{
		"type":          "tool_call",
		"subtype":       "started",
		"call_id":       callID,
		"model_call_id": "mc-1",
		"timestamp_ms":  recvTime.UnixMilli(),
		"tool_call":     json.RawMessage(tcJSON),
	})
	return events.AnnotatedEvent{
		RecvTime: recvTime,
		Raw:      raw,
		Parsed:   events.RawEvent{Type: "tool_call", Subtype: "started"},
	}
}

func toolCallCompletedEvent(recvTime time.Time, callID string) events.AnnotatedEvent {
	toolCall := map[string]any{
		"shellToolCall": map[string]any{
			"result": map[string]any{
				"success": map[string]any{
					"exitCode": 0, "stdout": "", "stderr": "", "executionTime": 1000,
				},
			},
		},
	}
	tcJSON, _ := json.Marshal(toolCall)
	raw, _ := json.Marshal(map[string]any{
		"type":          "tool_call",
		"subtype":       "completed",
		"call_id":       callID,
		"model_call_id": "mc-1",
		"timestamp_ms":  recvTime.UnixMilli(),
		"tool_call":     json.RawMessage(tcJSON),
	})
	return events.AnnotatedEvent{
		RecvTime: recvTime,
		Raw:      raw,
		Parsed:   events.RawEvent{Type: "tool_call", Subtype: "completed"},
	}
}

func resultEvent(recvTime time.Time) events.AnnotatedEvent {
	raw, _ := json.Marshal(map[string]any{
		"type":        "result",
		"subtype":     "success",
		"duration_ms": 5000,
		"is_error":    false,
		"session_id":  "sess-result",
		"request_id":  "req-1",
	})
	return events.AnnotatedEvent{
		RecvTime: recvTime,
		Raw:      raw,
		Parsed:   events.RawEvent{Type: "result", Subtype: "success"},
	}
}

func unknownEvent(recvTime time.Time) events.AnnotatedEvent {
	raw, _ := json.Marshal(map[string]string{"type": "new_fancy_type"})
	return events.AnnotatedEvent{
		RecvTime: recvTime,
		Raw:      raw,
		Parsed:   events.RawEvent{Type: "new_fancy_type"},
	}
}

// --- tests ---

const (
	idleTimeout = 60 * time.Second
	toolGrace   = 30 * time.Second
)

func newTestMonitor(clk *fakeClock) *Monitor {
	return NewMonitor(idleTimeout, toolGrace, WithClock(clk))
}

func TestSequentialToolCall(t *testing.T) {
	// started → silence → completed → no hang
	clk := newFakeClock(t0)
	m := newTestMonitor(clk)

	// Tool starts
	m.ProcessEvent(toolCallStartedEvent(t0, "call-1", 10000))
	clk.Advance(5 * time.Second)

	v, _ := m.CheckTimeout(clk.Now())
	if v != VerdictWaiting {
		t.Fatalf("expected VerdictWaiting during tool execution, got %v", v)
	}

	// Tool completes
	m.ProcessEvent(toolCallCompletedEvent(t0.Add(5*time.Second), "call-1"))
	clk.Advance(1 * time.Second)

	v, _ = m.CheckTimeout(clk.Now())
	if v != VerdictOK {
		t.Fatalf("expected VerdictOK after tool completion, got %v", v)
	}
}

func TestParallelToolCalls(t *testing.T) {
	// two started → one completed → still waiting → second completed → no hang
	clk := newFakeClock(t0)
	m := newTestMonitor(clk)

	m.ProcessEvent(toolCallStartedEvent(t0, "call-a", 10000))
	m.ProcessEvent(toolCallStartedEvent(t0.Add(100*time.Millisecond), "call-b", 10000))
	clk.Advance(5 * time.Second)

	v, _ := m.CheckTimeout(clk.Now())
	if v != VerdictWaiting {
		t.Fatalf("expected VerdictWaiting with two tools running, got %v", v)
	}

	// First tool completes
	m.ProcessEvent(toolCallCompletedEvent(t0.Add(5*time.Second), "call-a"))
	clk.Advance(2 * time.Second)

	v, _ = m.CheckTimeout(clk.Now())
	if v != VerdictWaiting {
		t.Fatalf("expected VerdictWaiting with one tool still running, got %v", v)
	}

	// Second tool completes
	m.ProcessEvent(toolCallCompletedEvent(t0.Add(7*time.Second), "call-b"))
	clk.Advance(1 * time.Second)

	v, _ = m.CheckTimeout(clk.Now())
	if v != VerdictOK {
		t.Fatalf("expected VerdictOK after all tools complete, got %v", v)
	}
}

func TestIdleHang(t *testing.T) {
	// thinking/completed → long silence with no open tools → VerdictHang
	clk := newFakeClock(t0)
	m := newTestMonitor(clk)

	m.ProcessEvent(thinkingCompletedEvent(t0))

	// Still within idle timeout
	clk.Advance(30 * time.Second)
	v, _ := m.CheckTimeout(clk.Now())
	if v != VerdictOK {
		t.Fatalf("expected VerdictOK within idle timeout, got %v", v)
	}

	// Exceed idle timeout
	clk.Advance(31 * time.Second)
	v, reason := m.CheckTimeout(clk.Now())
	if v != VerdictHang {
		t.Fatalf("expected VerdictHang after idle timeout, got %v", v)
	}
	if reason.OpenCallCount != 0 {
		t.Fatalf("expected 0 open calls, got %d", reason.OpenCallCount)
	}
	if reason.IdleSilenceMS < 60000 {
		t.Fatalf("expected idle silence >= 60000ms, got %d", reason.IdleSilenceMS)
	}
}

func TestToolTimeoutHang(t *testing.T) {
	// tool started → silence exceeds tool.TimeoutMS + grace → VerdictHang
	clk := newFakeClock(t0)
	m := newTestMonitor(clk)

	// Tool with 10s timeout
	m.ProcessEvent(toolCallStartedEvent(t0, "call-1", 10000))

	// Within deadline (10s timeout + 30s grace = 40s)
	clk.Advance(39 * time.Second)
	v, _ := m.CheckTimeout(clk.Now())
	if v != VerdictWaiting {
		t.Fatalf("expected VerdictWaiting within tool deadline, got %v", v)
	}

	// Exceed deadline
	clk.Advance(2 * time.Second)
	v, reason := m.CheckTimeout(clk.Now())
	if v != VerdictHang {
		t.Fatalf("expected VerdictHang after tool timeout+grace, got %v", v)
	}
	if reason.OpenCallCount != 1 {
		t.Fatalf("expected 1 open call, got %d", reason.OpenCallCount)
	}
}

func TestPartialExpiry(t *testing.T) {
	// tool A expired, tool B still within deadline → VerdictWaiting
	clk := newFakeClock(t0)
	m := newTestMonitor(clk)

	// Tool A: 10s timeout (deadline = 10s + 30s = 40s)
	m.ProcessEvent(toolCallStartedEvent(t0, "call-a", 10000))

	// Tool B starts 30s later: 10s timeout (deadline = 10s + 30s = 40s from its start)
	clk.Advance(30 * time.Second)
	m.ProcessEvent(toolCallStartedEvent(t0.Add(30*time.Second), "call-b", 10000))

	// At T=41s: tool A expired (41s > 40s), but tool B only 11s in (< 40s)
	clk.Advance(11 * time.Second)
	v, _ := m.CheckTimeout(clk.Now())
	if v != VerdictWaiting {
		t.Fatalf("expected VerdictWaiting when only one tool expired, got %v", v)
	}
}

func TestNormalCompletion(t *testing.T) {
	// result event → VerdictOK regardless of subsequent silence
	clk := newFakeClock(t0)
	m := newTestMonitor(clk)

	m.ProcessEvent(assistantEvent(t0))
	m.ProcessEvent(resultEvent(t0.Add(1 * time.Second)))

	// Long silence after result
	clk.Advance(120 * time.Second)
	v, _ := m.CheckTimeout(clk.Now())
	if v != VerdictOK {
		t.Fatalf("expected VerdictOK after result event, got %v", v)
	}
}

func TestNonShellToolFallback(t *testing.T) {
	// non-shell tool with no timeout → falls back to idleTimeout
	clk := newFakeClock(t0)
	m := newTestMonitor(clk)

	m.ProcessEvent(nonShellToolCallStartedEvent(t0, "call-ls"))

	// The tool has TimeoutMS == 0, so deadline is idleTimeout (60s)
	clk.Advance(59 * time.Second)
	v, _ := m.CheckTimeout(clk.Now())
	if v != VerdictWaiting {
		t.Fatalf("expected VerdictWaiting within idleTimeout fallback, got %v", v)
	}

	clk.Advance(2 * time.Second)
	v, _ = m.CheckTimeout(clk.Now())
	if v != VerdictHang {
		t.Fatalf("expected VerdictHang when non-shell tool exceeds idleTimeout, got %v", v)
	}
}

func TestUnknownEventResetsLastEventAt(t *testing.T) {
	clk := newFakeClock(t0)
	m := newTestMonitor(clk)

	m.ProcessEvent(thinkingCompletedEvent(t0))

	// 50s silence, then unknown event resets the timer
	clk.Advance(50 * time.Second)
	m.ProcessEvent(unknownEvent(t0.Add(50 * time.Second)))

	// 50s after the unknown event (total 100s from start, but only 50s from last event)
	clk.Advance(50 * time.Second)
	v, _ := m.CheckTimeout(clk.Now())
	if v != VerdictOK {
		t.Fatalf("expected VerdictOK because unknown event reset LastEventAt, got %v", v)
	}

	// 11 more seconds → 61s from unknown event → hang
	clk.Advance(11 * time.Second)
	v, _ = m.CheckTimeout(clk.Now())
	if v != VerdictHang {
		t.Fatalf("expected VerdictHang after idle timeout from unknown event, got %v", v)
	}
}

func TestUnmatchedToolCallCompleted(t *testing.T) {
	// completed for unknown call_id must not panic
	clk := newFakeClock(t0)
	m := newTestMonitor(clk)

	// Complete a call that was never started
	m.ProcessEvent(toolCallCompletedEvent(t0, "nonexistent"))

	v, _ := m.CheckTimeout(clk.Now())
	if v != VerdictOK {
		t.Fatalf("expected VerdictOK after unmatched completion, got %v", v)
	}
}

func TestSessionID(t *testing.T) {
	clk := newFakeClock(t0)
	m := newTestMonitor(clk)

	if m.SessionID() != "" {
		t.Fatalf("expected empty session ID before init event, got %q", m.SessionID())
	}

	m.ProcessEvent(systemInitEvent("sess-abc-123"))

	if m.SessionID() != "sess-abc-123" {
		t.Fatalf("expected session ID 'sess-abc-123', got %q", m.SessionID())
	}
}

func TestReasonString(t *testing.T) {
	r := Reason{
		IdleSilenceMS: 65000,
		OpenCallCount: 0,
		LastEventType: "thinking/completed",
	}
	s := r.String()
	if !strings.Contains(s, "idle 65000ms") {
		t.Fatalf("expected idle millis in reason string, got %q", s)
	}
	if !strings.Contains(s, "0 open calls") {
		t.Fatalf("expected open call count in reason string, got %q", s)
	}
	if !strings.Contains(s, "last event: thinking/completed") {
		t.Fatalf("expected last event type in reason string, got %q", s)
	}
}

func TestReasonStringWithOpenCalls(t *testing.T) {
	r := Reason{
		IdleSilenceMS: 45000,
		OpenCallCount: 1,
		LastEventType: "tool_call/started",
		OpenCalls: []OpenCallDetail{
			{CallID: "call-1", Command: "npm test", ElapsedMS: 45000, TimeoutMS: 10000},
		},
	}
	s := r.String()
	if !strings.Contains(s, "call-1") {
		t.Fatalf("expected call ID in reason string, got %q", s)
	}
	if !strings.Contains(s, "npm test") {
		t.Fatalf("expected command in reason string, got %q", s)
	}
}

func TestVerdictString(t *testing.T) {
	tests := []struct {
		v    Verdict
		want string
	}{
		{VerdictOK, "OK"},
		{VerdictWaiting, "Waiting"},
		{VerdictHang, "Hang"},
		{Verdict(99), "Verdict(99)"},
	}
	for _, tt := range tests {
		if got := tt.v.String(); got != tt.want {
			t.Errorf("Verdict(%d).String() = %q, want %q", int(tt.v), got, tt.want)
		}
	}
}

func TestSessionDoneAccessor(t *testing.T) {
	clk := newFakeClock(t0)
	m := newTestMonitor(clk)

	if m.SessionDone() {
		t.Fatal("expected SessionDone() == false before result event")
	}

	m.ProcessEvent(resultEvent(t0))

	if !m.SessionDone() {
		t.Fatal("expected SessionDone() == true after result event")
	}
}

func TestNowAccessor(t *testing.T) {
	clk := newFakeClock(t0)
	m := newTestMonitor(clk)

	if !m.Now().Equal(t0) {
		t.Fatalf("expected Now() == t0, got %v", m.Now())
	}

	clk.Advance(5 * time.Second)
	if !m.Now().Equal(t0.Add(5 * time.Second)) {
		t.Fatalf("expected Now() to advance with clock")
	}
}

func TestToolCallStartedWithZeroTimeoutUsesIdleTimeout(t *testing.T) {
	// Shell tool with timeout=0 should use idleTimeout as fallback
	clk := newFakeClock(t0)
	m := newTestMonitor(clk)

	m.ProcessEvent(toolCallStartedEvent(t0, "call-1", 0))

	// Within idleTimeout (60s)
	clk.Advance(59 * time.Second)
	v, _ := m.CheckTimeout(clk.Now())
	if v != VerdictWaiting {
		t.Fatalf("expected VerdictWaiting within idleTimeout for zero-timeout tool, got %v", v)
	}

	// Exceed idleTimeout
	clk.Advance(2 * time.Second)
	v, _ = m.CheckTimeout(clk.Now())
	if v != VerdictHang {
		t.Fatalf("expected VerdictHang when zero-timeout tool exceeds idleTimeout, got %v", v)
	}
}

func TestToolHangThenPartialExpiry(t *testing.T) {
	// All tools must expire for VerdictHang — verify with three tools
	clk := newFakeClock(t0)
	m := newTestMonitor(clk)

	// Three tools with different timeouts: 5s, 10s, 20s
	// Deadlines with 30s grace: 35s, 40s, 50s
	m.ProcessEvent(toolCallStartedEvent(t0, "call-a", 5000))
	m.ProcessEvent(toolCallStartedEvent(t0, "call-b", 10000))
	m.ProcessEvent(toolCallStartedEvent(t0, "call-c", 20000))

	// At T=36s: A expired, B and C within deadline
	clk.Advance(36 * time.Second)
	v, _ := m.CheckTimeout(clk.Now())
	if v != VerdictWaiting {
		t.Fatalf("at T=36s: expected VerdictWaiting, got %v", v)
	}

	// At T=41s: A and B expired, C within deadline
	clk.Advance(5 * time.Second)
	v, _ = m.CheckTimeout(clk.Now())
	if v != VerdictWaiting {
		t.Fatalf("at T=41s: expected VerdictWaiting, got %v", v)
	}

	// At T=51s: all expired
	clk.Advance(10 * time.Second)
	v, _ = m.CheckTimeout(clk.Now())
	if v != VerdictHang {
		t.Fatalf("at T=51s: expected VerdictHang, got %v", v)
	}
}

func TestResultEventClearsHangAfterToolTimeout(t *testing.T) {
	// Tool started, exceeds deadline, but then result arrives before check
	clk := newFakeClock(t0)
	m := newTestMonitor(clk)

	m.ProcessEvent(toolCallStartedEvent(t0, "call-1", 5000))
	clk.Advance(50 * time.Second)

	// Result arrives (session done)
	m.ProcessEvent(resultEvent(t0.Add(50 * time.Second)))

	v, _ := m.CheckTimeout(clk.Now())
	if v != VerdictOK {
		t.Fatalf("expected VerdictOK after result, even with expired tool, got %v", v)
	}
}

func TestProcessEventReturnValue(t *testing.T) {
	clk := newFakeClock(t0)
	m := newTestMonitor(clk)

	// Non-tool events return VerdictOK
	v := m.ProcessEvent(thinkingCompletedEvent(t0))
	if v != VerdictOK {
		t.Fatalf("expected VerdictOK from thinking event, got %v", v)
	}

	// Tool started returns VerdictWaiting (open calls > 0)
	v = m.ProcessEvent(toolCallStartedEvent(t0, "call-1", 10000))
	if v != VerdictWaiting {
		t.Fatalf("expected VerdictWaiting from tool_call/started, got %v", v)
	}

	// Second tool started still returns VerdictWaiting
	v = m.ProcessEvent(toolCallStartedEvent(t0, "call-2", 10000))
	if v != VerdictWaiting {
		t.Fatalf("expected VerdictWaiting with two open tools, got %v", v)
	}

	// Completing one tool still returns VerdictWaiting (one still open)
	v = m.ProcessEvent(toolCallCompletedEvent(t0, "call-1"))
	if v != VerdictWaiting {
		t.Fatalf("expected VerdictWaiting with one tool still open, got %v", v)
	}

	// Completing last tool returns VerdictOK
	v = m.ProcessEvent(toolCallCompletedEvent(t0, "call-2"))
	if v != VerdictOK {
		t.Fatalf("expected VerdictOK after all tools completed, got %v", v)
	}
}
