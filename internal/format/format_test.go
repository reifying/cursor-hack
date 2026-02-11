package format

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"cursor-wrap/internal/events"
	"cursor-wrap/internal/monitor"
)

// --- helpers ---

func annotated(raw string) events.AnnotatedEvent {
	line := []byte(raw)
	var parsed events.RawEvent
	_ = json.Unmarshal(line, &parsed)
	parsed.Line = line
	return events.AnnotatedEvent{
		RecvTime: time.Now(),
		Raw:      line,
		Parsed:   parsed,
	}
}

// --- New factory ---

func TestNew_StreamJSON(t *testing.T) {
	f := New("stream-json", &bytes.Buffer{})
	if _, ok := f.(*streamJSON); !ok {
		t.Fatal("expected *streamJSON")
	}
}

func TestNew_Text(t *testing.T) {
	f := New("text", &bytes.Buffer{})
	if _, ok := f.(*text); !ok {
		t.Fatal("expected *text")
	}
}

func TestNew_UnknownPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for unknown format")
		}
	}()
	New("unknown", &bytes.Buffer{})
}

// --- streamJSON tests ---

func TestStreamJSON_WriteEvent_ByteIdentical(t *testing.T) {
	raw := `{"type":"assistant","message":{"content":[{"type":"text","text":"hello"}]}}`
	var buf bytes.Buffer
	f := New("stream-json", &buf)

	ev := annotated(raw)
	if err := f.WriteEvent(ev); err != nil {
		t.Fatalf("WriteEvent: %v", err)
	}

	want := raw + "\n"
	if got := buf.String(); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestStreamJSON_WriteEvent_PreservesRawBytes(t *testing.T) {
	// Ensure whitespace, field ordering, etc. are preserved exactly.
	raw := `{ "type" : "thinking" , "subtype" : "delta" , "text" : "hmm" }`
	var buf bytes.Buffer
	f := New("stream-json", &buf)

	if err := f.WriteEvent(annotated(raw)); err != nil {
		t.Fatalf("WriteEvent: %v", err)
	}

	want := raw + "\n"
	if got := buf.String(); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestStreamJSON_WriteHangIndicator_ValidJSON(t *testing.T) {
	var buf bytes.Buffer
	f := New("stream-json", &buf)

	reason := monitor.Reason{
		IdleSilenceMS: 65000,
		OpenCallCount: 0,
		LastEventType: "thinking",
	}
	if err := f.WriteHangIndicator(reason); err != nil {
		t.Fatalf("WriteHangIndicator: %v", err)
	}

	output := strings.TrimSpace(buf.String())

	// Must be valid JSON.
	var parsed map[string]any
	if err := json.Unmarshal([]byte(output), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v\noutput: %s", err, output)
	}
	if parsed["type"] != "wrapper" {
		t.Fatalf("type = %v, want wrapper", parsed["type"])
	}
	if parsed["subtype"] != "hang_detected" {
		t.Fatalf("subtype = %v, want hang_detected", parsed["subtype"])
	}
	if parsed["message"] == nil || parsed["message"] == "" {
		t.Fatal("message should not be empty")
	}
}

func TestStreamJSON_WriteHangIndicator_EndsWithNewline(t *testing.T) {
	var buf bytes.Buffer
	f := New("stream-json", &buf)

	reason := monitor.Reason{IdleSilenceMS: 1000}
	if err := f.WriteHangIndicator(reason); err != nil {
		t.Fatalf("WriteHangIndicator: %v", err)
	}

	if !strings.HasSuffix(buf.String(), "\n") {
		t.Fatal("output should end with newline")
	}
}

func TestStreamJSON_Flush_NoOp(t *testing.T) {
	var buf bytes.Buffer
	f := New("stream-json", &buf)

	if err := f.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("expected no output from Flush, got %q", buf.String())
	}
}

// --- text formatter tests ---

func TestText_AssistantEvent_RendersText(t *testing.T) {
	raw := `{"type":"assistant","message":{"content":[{"type":"text","text":"Hello, world!"}]}}`
	var buf bytes.Buffer
	f := New("text", &buf)

	if err := f.WriteEvent(annotated(raw)); err != nil {
		t.Fatalf("WriteEvent: %v", err)
	}

	want := "Hello, world!\n"
	if got := buf.String(); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestText_AssistantEvent_MidTurn(t *testing.T) {
	raw := `{"type":"assistant","model_call_id":"mc_123","message":{"content":[{"type":"text","text":"thinking out loud"}]}}`
	var buf bytes.Buffer
	f := New("text", &buf)

	if err := f.WriteEvent(annotated(raw)); err != nil {
		t.Fatalf("WriteEvent: %v", err)
	}

	want := "thinking out loud\n"
	if got := buf.String(); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestText_ToolCallStarted_Shell(t *testing.T) {
	raw := `{"type":"tool_call","subtype":"started","call_id":"call_1","model_call_id":"mc_1","timestamp_ms":1000,"tool_call":{"shellToolCall":{"args":{"command":"npm install","timeout":120000}}}}`
	var buf bytes.Buffer
	f := New("text", &buf)

	if err := f.WriteEvent(annotated(raw)); err != nil {
		t.Fatalf("WriteEvent: %v", err)
	}

	want := "⏳ `npm install`\n"
	if got := buf.String(); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestText_ToolCallStarted_NonShell(t *testing.T) {
	raw := `{"type":"tool_call","subtype":"started","call_id":"call_2","model_call_id":"mc_2","timestamp_ms":2000,"tool_call":{"lsToolCall":{"args":{"path":"/tmp"}}}}`
	var buf bytes.Buffer
	f := New("text", &buf)

	if err := f.WriteEvent(annotated(raw)); err != nil {
		t.Fatalf("WriteEvent: %v", err)
	}

	want := "⏳ lsToolCall: /tmp\n"
	if got := buf.String(); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestText_ToolCallStarted_NonShell_NoArgs(t *testing.T) {
	// Unknown tool type with no args extracted by toolCallArgs — should not show trailing ": ".
	raw := `{"type":"tool_call","subtype":"started","call_id":"call_3","model_call_id":"mc_3","timestamp_ms":3000,"tool_call":{"readToolCall":{"args":{"file":"/etc/hosts"}}}}`
	var buf bytes.Buffer
	f := New("text", &buf)

	if err := f.WriteEvent(annotated(raw)); err != nil {
		t.Fatalf("WriteEvent: %v", err)
	}

	want := "⏳ readToolCall\n"
	if got := buf.String(); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestText_ToolCallCompleted_ShellExitZero(t *testing.T) {
	raw := `{"type":"tool_call","subtype":"completed","call_id":"call_1","model_call_id":"mc_1","timestamp_ms":6400,"tool_call":{"shellToolCall":{"args":{"command":"sleep 5","timeout":120000},"result":{"success":{"exitCode":0,"stdout":"","stderr":"","executionTime":5400}}}}}`
	var buf bytes.Buffer
	f := New("text", &buf)

	if err := f.WriteEvent(annotated(raw)); err != nil {
		t.Fatalf("WriteEvent: %v", err)
	}

	want := "✓ `sleep 5` (5.4s, exit 0)\n"
	if got := buf.String(); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestText_ToolCallCompleted_ShellExitNonZero(t *testing.T) {
	raw := `{"type":"tool_call","subtype":"completed","call_id":"call_1","model_call_id":"mc_1","timestamp_ms":3200,"tool_call":{"shellToolCall":{"args":{"command":"false","timeout":120000},"result":{"success":{"exitCode":1,"stdout":"","stderr":"error","executionTime":3200}}}}}`
	var buf bytes.Buffer
	f := New("text", &buf)

	if err := f.WriteEvent(annotated(raw)); err != nil {
		t.Fatalf("WriteEvent: %v", err)
	}

	want := "✗ `false` (3.2s, exit 1)\n"
	if got := buf.String(); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestText_ToolCallCompleted_NonShell(t *testing.T) {
	raw := `{"type":"tool_call","subtype":"completed","call_id":"call_2","model_call_id":"mc_2","timestamp_ms":3000,"tool_call":{"lsToolCall":{"args":{"path":"/tmp"},"result":{"success":["file1","file2"]}}}}`
	var buf bytes.Buffer
	f := New("text", &buf)

	if err := f.WriteEvent(annotated(raw)); err != nil {
		t.Fatalf("WriteEvent: %v", err)
	}

	want := "✓ lsToolCall\n"
	if got := buf.String(); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestText_ThinkingDelta_Silent(t *testing.T) {
	raw := `{"type":"thinking","subtype":"delta","text":"let me think"}`
	var buf bytes.Buffer
	f := New("text", &buf)

	if err := f.WriteEvent(annotated(raw)); err != nil {
		t.Fatalf("WriteEvent: %v", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("expected no output, got %q", buf.String())
	}
}

func TestText_ThinkingCompleted_Silent(t *testing.T) {
	raw := `{"type":"thinking","subtype":"completed"}`
	var buf bytes.Buffer
	f := New("text", &buf)

	if err := f.WriteEvent(annotated(raw)); err != nil {
		t.Fatalf("WriteEvent: %v", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("expected no output, got %q", buf.String())
	}
}

func TestText_SystemInit_Silent(t *testing.T) {
	raw := `{"type":"system","subtype":"init","session_id":"sess_1","model":"claude","cwd":"/tmp"}`
	var buf bytes.Buffer
	f := New("text", &buf)

	if err := f.WriteEvent(annotated(raw)); err != nil {
		t.Fatalf("WriteEvent: %v", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("expected no output, got %q", buf.String())
	}
}

func TestText_UserEvent_Silent(t *testing.T) {
	raw := `{"type":"user","message":{"content":[{"type":"text","text":"hello"}]}}`
	var buf bytes.Buffer
	f := New("text", &buf)

	if err := f.WriteEvent(annotated(raw)); err != nil {
		t.Fatalf("WriteEvent: %v", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("expected no output, got %q", buf.String())
	}
}

func TestText_ResultEvent_Silent(t *testing.T) {
	raw := `{"type":"result","subtype":"success","duration_ms":5000,"is_error":false,"session_id":"sess_1","request_id":"req_1"}`
	var buf bytes.Buffer
	f := New("text", &buf)

	if err := f.WriteEvent(annotated(raw)); err != nil {
		t.Fatalf("WriteEvent: %v", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("expected no output, got %q", buf.String())
	}
}

func TestText_UnknownEvent_Silent(t *testing.T) {
	raw := `{"type":"future_type","subtype":"new_subtype","data":"value"}`
	var buf bytes.Buffer
	f := New("text", &buf)

	if err := f.WriteEvent(annotated(raw)); err != nil {
		t.Fatalf("WriteEvent: %v", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("expected no output, got %q", buf.String())
	}
}

func TestText_ParseFailure_AssistantMalformed_NoPanic(t *testing.T) {
	// Missing message.content — ParseAssistantMessage should fail gracefully.
	raw := `{"type":"assistant","message":{}}`
	var buf bytes.Buffer
	f := New("text", &buf)

	if err := f.WriteEvent(annotated(raw)); err != nil {
		t.Fatalf("WriteEvent: %v", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("expected no output on parse failure, got %q", buf.String())
	}
}

func TestText_ParseFailure_ToolCallStartedMalformed_NoPanic(t *testing.T) {
	// tool_call field is missing entirely.
	raw := `{"type":"tool_call","subtype":"started","call_id":"call_bad"}`
	var buf bytes.Buffer
	f := New("text", &buf)

	if err := f.WriteEvent(annotated(raw)); err != nil {
		t.Fatalf("WriteEvent: %v", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("expected no output on parse failure, got %q", buf.String())
	}
}

func TestText_ParseFailure_ToolCallCompletedMalformed_NoPanic(t *testing.T) {
	// tool_call field is not valid JSON for tool type extraction.
	raw := `{"type":"tool_call","subtype":"completed","call_id":"call_bad","tool_call":"not an object"}`
	var buf bytes.Buffer
	f := New("text", &buf)

	if err := f.WriteEvent(annotated(raw)); err != nil {
		t.Fatalf("WriteEvent: %v", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("expected no output on parse failure, got %q", buf.String())
	}
}

func TestText_WriteHangIndicator(t *testing.T) {
	var buf bytes.Buffer
	f := New("text", &buf)

	reason := monitor.Reason{
		IdleSilenceMS: 65000,
		OpenCallCount: 0,
		LastEventType: "thinking",
	}
	if err := f.WriteHangIndicator(reason); err != nil {
		t.Fatalf("WriteHangIndicator: %v", err)
	}

	got := buf.String()
	if !strings.Contains(got, "Hang detected") {
		t.Fatalf("expected 'Hang detected' in output, got %q", got)
	}
	if !strings.Contains(got, "killed cursor-agent") {
		t.Fatalf("expected 'killed cursor-agent' in output, got %q", got)
	}
	// Should include the reason summary.
	if !strings.Contains(got, "65000ms") {
		t.Fatalf("expected idle time in output, got %q", got)
	}
}

func TestText_WriteHangIndicator_WithOpenCalls(t *testing.T) {
	var buf bytes.Buffer
	f := New("text", &buf)

	reason := monitor.Reason{
		IdleSilenceMS: 150000,
		OpenCallCount: 1,
		LastEventType: "tool_call/started",
		OpenCalls: []monitor.OpenCallDetail{
			{CallID: "call_1", Command: "npm install", ElapsedMS: 150000, TimeoutMS: 120000},
		},
	}
	if err := f.WriteHangIndicator(reason); err != nil {
		t.Fatalf("WriteHangIndicator: %v", err)
	}

	got := buf.String()
	if !strings.Contains(got, "1 open calls") {
		t.Fatalf("expected open call count in output, got %q", got)
	}
}

func TestText_Flush_WritesBlankLine(t *testing.T) {
	var buf bytes.Buffer
	f := New("text", &buf)

	if err := f.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if got := buf.String(); got != "\n" {
		t.Fatalf("expected single newline, got %q", got)
	}
}
