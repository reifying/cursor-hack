package events

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("loading fixture %s: %v", name, err)
	}
	return bytes.TrimSpace(data)
}

func TestParseRawEvent(t *testing.T) {
	tests := []struct {
		name        string
		fixture     string
		wantType    string
		wantSubtype string
	}{
		{
			name:        "system init",
			fixture:     "system_init.json",
			wantType:    "system",
			wantSubtype: "init",
		},
		{
			name:        "user",
			fixture:     "user.json",
			wantType:    "user",
			wantSubtype: "",
		},
		{
			name:        "thinking delta",
			fixture:     "thinking_delta.json",
			wantType:    "thinking",
			wantSubtype: "delta",
		},
		{
			name:        "thinking completed",
			fixture:     "thinking_completed.json",
			wantType:    "thinking",
			wantSubtype: "completed",
		},
		{
			name:        "assistant mid-turn",
			fixture:     "assistant_mid_turn.json",
			wantType:    "assistant",
			wantSubtype: "",
		},
		{
			name:        "assistant final",
			fixture:     "assistant_final.json",
			wantType:    "assistant",
			wantSubtype: "",
		},
		{
			name:        "tool call started",
			fixture:     "tool_call_started.json",
			wantType:    "tool_call",
			wantSubtype: "started",
		},
		{
			name:        "tool call completed",
			fixture:     "tool_call_completed.json",
			wantType:    "tool_call",
			wantSubtype: "completed",
		},
		{
			name:        "result",
			fixture:     "result.json",
			wantType:    "result",
			wantSubtype: "success",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := loadFixture(t, tt.fixture)
			var ev RawEvent
			if err := json.Unmarshal(data, &ev); err != nil {
				t.Fatalf("unexpected parse error: %v", err)
			}
			if ev.Type != tt.wantType {
				t.Errorf("type = %q, want %q", ev.Type, tt.wantType)
			}
			if ev.Subtype != tt.wantSubtype {
				t.Errorf("subtype = %q, want %q", ev.Subtype, tt.wantSubtype)
			}
		})
	}
}

func TestParseRawEvent_MalformedJSON(t *testing.T) {
	input := []byte(`{not valid json`)
	var ev RawEvent
	err := json.Unmarshal(input, &ev)
	if err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}
}

func TestParseRawEvent_UnknownType(t *testing.T) {
	input := []byte(`{"type":"future_event","subtype":"v2","some_field":"value"}`)
	var ev RawEvent
	if err := json.Unmarshal(input, &ev); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev.Type != "future_event" {
		t.Errorf("type = %q, want %q", ev.Type, "future_event")
	}
	if ev.Subtype != "v2" {
		t.Errorf("subtype = %q, want %q", ev.Subtype, "v2")
	}
}

func TestParseSystemInit(t *testing.T) {
	data := loadFixture(t, "system_init.json")
	var init SystemInit
	if err := json.Unmarshal(data, &init); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if init.SessionID != "d43015b9-0707-43f4-b2df-0bcea7891654" {
		t.Errorf("session_id = %q, want %q", init.SessionID, "d43015b9-0707-43f4-b2df-0bcea7891654")
	}
	if init.Model != "Auto" {
		t.Errorf("model = %q, want %q", init.Model, "Auto")
	}
	if init.PermissionMode != "default" {
		t.Errorf("permissionMode = %q, want %q", init.PermissionMode, "default")
	}
}

func TestParseToolCallStarted(t *testing.T) {
	data := loadFixture(t, "tool_call_started.json")
	var started ToolCallStarted
	if err := json.Unmarshal(data, &started); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	// call_id contains a literal newline
	if !strings.Contains(started.CallID, "\n") {
		t.Error("expected call_id to contain a literal newline")
	}
	if started.TimestampMS != 1770823845357 {
		t.Errorf("timestamp_ms = %d, want %d", started.TimestampMS, 1770823845357)
	}
	if started.ToolCall == nil {
		t.Fatal("tool_call should not be nil")
	}
}

func TestParseToolCallCompleted(t *testing.T) {
	data := loadFixture(t, "tool_call_completed.json")
	var completed ToolCallCompleted
	if err := json.Unmarshal(data, &completed); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if !strings.Contains(completed.CallID, "\n") {
		t.Error("expected call_id to contain a literal newline")
	}
	if completed.TimestampMS != 1770823850766 {
		t.Errorf("timestamp_ms = %d, want %d", completed.TimestampMS, 1770823850766)
	}
}

func TestParseResult(t *testing.T) {
	data := loadFixture(t, "result.json")
	var result Result
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if result.Subtype != "success" {
		t.Errorf("subtype = %q, want %q", result.Subtype, "success")
	}
	if result.DurationMS != 21509 {
		t.Errorf("duration_ms = %d, want %d", result.DurationMS, 21509)
	}
	if result.IsError {
		t.Error("expected is_error to be false")
	}
	if result.SessionID != "d43015b9-0707-43f4-b2df-0bcea7891654" {
		t.Errorf("session_id = %q, want %q", result.SessionID, "d43015b9-0707-43f4-b2df-0bcea7891654")
	}
}

func TestReader_FullSession(t *testing.T) {
	data := loadFixture(t, "full_session.jsonl")
	r := bytes.NewReader(data)

	ctx := context.Background()
	out := make(chan AnnotatedEvent, 64)
	errCh := make(chan error, 1)

	go Reader(ctx, r, out, errCh)

	var events []AnnotatedEvent
	for ev := range out {
		events = append(events, ev)
	}

	// full_session.jsonl has 9 events
	if len(events) != 9 {
		t.Fatalf("got %d events, want 9", len(events))
	}

	// Verify first event is system/init
	if events[0].Parsed.Type != "system" || events[0].Parsed.Subtype != "init" {
		t.Errorf("first event type = %q/%q, want system/init", events[0].Parsed.Type, events[0].Parsed.Subtype)
	}

	// Verify last event is result
	last := events[len(events)-1]
	if last.Parsed.Type != "result" {
		t.Errorf("last event type = %q, want result", last.Parsed.Type)
	}

	// Verify RecvTime is set for all events
	for i, ev := range events {
		if ev.RecvTime.IsZero() {
			t.Errorf("event %d has zero RecvTime", i)
		}
	}

	// Verify Raw bytes are preserved
	for i, ev := range events {
		if len(ev.Raw) == 0 {
			t.Errorf("event %d has empty Raw bytes", i)
		}
	}

	// Check no errors
	select {
	case err := <-errCh:
		t.Fatalf("unexpected reader error: %v", err)
	default:
	}
}

func TestReader_ClosesOnEOF(t *testing.T) {
	r := strings.NewReader(`{"type":"system","subtype":"init"}` + "\n")

	ctx := context.Background()
	out := make(chan AnnotatedEvent, 64)
	errCh := make(chan error, 1)

	go Reader(ctx, r, out, errCh)

	var count int
	for range out {
		count++
	}

	if count != 1 {
		t.Fatalf("got %d events, want 1", count)
	}

	// out channel should be closed — range exits
	select {
	case err := <-errCh:
		t.Fatalf("unexpected error: %v", err)
	default:
	}
}

func TestReader_SkipsNonJSONLines(t *testing.T) {
	input := "T: Named models unavailable on free plan\n" +
		`{"type":"system","subtype":"init"}` + "\n" +
		"another non-json line\n" +
		`{"type":"result","subtype":"success"}` + "\n"

	r := strings.NewReader(input)
	ctx := context.Background()
	out := make(chan AnnotatedEvent, 64)
	errCh := make(chan error, 1)

	go Reader(ctx, r, out, errCh)

	var events []AnnotatedEvent
	for ev := range out {
		events = append(events, ev)
	}

	// Only the 2 valid JSON lines should come through
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2", len(events))
	}
	if events[0].Parsed.Type != "system" {
		t.Errorf("first event type = %q, want system", events[0].Parsed.Type)
	}
	if events[1].Parsed.Type != "result" {
		t.Errorf("second event type = %q, want result", events[1].Parsed.Type)
	}
}

func TestReader_SkipsMalformedJSON(t *testing.T) {
	input := `{not valid json}` + "\n" +
		`{"type":"user"}` + "\n"

	r := strings.NewReader(input)
	ctx := context.Background()
	out := make(chan AnnotatedEvent, 64)
	errCh := make(chan error, 1)

	go Reader(ctx, r, out, errCh)

	var events []AnnotatedEvent
	for ev := range out {
		events = append(events, ev)
	}

	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].Parsed.Type != "user" {
		t.Errorf("event type = %q, want user", events[0].Parsed.Type)
	}
}

func TestReader_ContextCancellation(t *testing.T) {
	// Create a reader that blocks forever
	pr, pw := io.Pipe()

	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan AnnotatedEvent, 64)
	errCh := make(chan error, 1)

	go Reader(ctx, pr, out, errCh)

	// Write one event
	_, _ = pw.Write([]byte(`{"type":"system","subtype":"init"}` + "\n"))

	// Read it
	select {
	case <-out:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
	}

	// Cancel context, then close the pipe to unblock scanner.Scan().
	// In production, this happens when the caller kills the child process,
	// which closes its stdout pipe.
	cancel()
	pw.Close()

	// Channel should close
	select {
	case _, ok := <-out:
		if ok {
			// Got another event, that's fine as long as channel eventually closes
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for channel close after cancel")
	}
}

func TestReader_BrokenPipeSendsError(t *testing.T) {
	pr, pw := io.Pipe()

	ctx := context.Background()
	out := make(chan AnnotatedEvent, 64)
	errCh := make(chan error, 1)

	go Reader(ctx, pr, out, errCh)

	// Write one event, then close with error
	_, _ = pw.Write([]byte(`{"type":"system","subtype":"init"}` + "\n"))

	// Read the event
	select {
	case <-out:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
	}

	// Close the write end with an error to simulate broken pipe
	pw.CloseWithError(io.ErrUnexpectedEOF)

	// Drain the out channel
	for range out {
	}

	// Should get an error on errCh
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected non-nil error")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for error on errCh")
	}
}

func TestReader_PreservesRawBytes(t *testing.T) {
	// Ensure raw bytes are exactly what was on the line (minus the newline).
	line := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hello"}]},"session_id":"abc"}`
	input := line + "\n"

	r := strings.NewReader(input)
	ctx := context.Background()
	out := make(chan AnnotatedEvent, 64)
	errCh := make(chan error, 1)

	go Reader(ctx, r, out, errCh)

	ev := <-out
	if string(ev.Raw) != line {
		t.Errorf("raw bytes mismatch:\ngot:  %q\nwant: %q", string(ev.Raw), line)
	}

	select {
	case err := <-errCh:
		t.Fatalf("unexpected error: %v", err)
	default:
	}
}

func TestReader_RawBytesPreservedForParseFailures(t *testing.T) {
	// Non-JSON lines are skipped, so raw bytes aren't emitted for them.
	// But for valid JSON with unknown types, raw bytes should be preserved.
	line := `{"type":"new_event_type","data":"something"}`
	input := line + "\n"

	r := strings.NewReader(input)
	ctx := context.Background()
	out := make(chan AnnotatedEvent, 64)
	errCh := make(chan error, 1)

	go Reader(ctx, r, out, errCh)

	ev := <-out
	if string(ev.Raw) != line {
		t.Errorf("raw bytes mismatch:\ngot:  %q\nwant: %q", string(ev.Raw), line)
	}
	if ev.Parsed.Type != "new_event_type" {
		t.Errorf("parsed type = %q, want new_event_type", ev.Parsed.Type)
	}
}
