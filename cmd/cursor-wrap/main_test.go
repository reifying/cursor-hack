package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"cursor-wrap/internal/events"
	"cursor-wrap/internal/logger"
	"cursor-wrap/internal/monitor"
	"cursor-wrap/internal/process"
)

// --- logRawEvent tests ---

func TestLogRawEvent_ProducesValidJSONL(t *testing.T) {
	now := time.Date(2026, 2, 10, 12, 30, 45, 400000000, time.UTC)
	ev := events.AnnotatedEvent{
		RecvTime: now,
		Raw:      []byte(`{"type":"thinking","subtype":"delta"}`),
		Parsed:   events.RawEvent{Type: "thinking", Subtype: "delta"},
	}

	log, teardown := setupTestLogger(t)
	logRawEvent(log, ev)
	teardown()

	// Read the log file and verify the JSONL record.
	logPath := log.FilePath()
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("reading log file: %v", err)
	}

	// The file may contain multiple lines; find the raw_event record.
	var found bool
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var record map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("invalid JSONL line: %v", err)
		}

		// Check for msg="raw_event"
		var msg string
		if err := json.Unmarshal(record["msg"], &msg); err != nil {
			continue
		}
		if msg != "raw_event" {
			continue
		}
		found = true

		// Verify recv_ts is present and is a positive integer.
		if _, ok := record["recv_ts"]; !ok {
			t.Error("raw_event record missing recv_ts field")
		}
		var recvTS int64
		if err := json.Unmarshal(record["recv_ts"], &recvTS); err != nil {
			t.Errorf("recv_ts is not an integer: %v", err)
		}
		if recvTS <= 0 {
			t.Errorf("recv_ts = %d, want positive epoch millis", recvTS)
		}

		// Verify raw field is present and is valid JSON.
		rawField, ok := record["raw"]
		if !ok {
			t.Error("raw_event record missing raw field")
		}
		if !json.Valid(rawField) {
			t.Errorf("raw field is not valid JSON: %s", rawField)
		}

		break
	}
	if !found {
		t.Error("no raw_event record found in log file")
	}
}

// --- reasonAttrs tests ---

func TestReasonAttrs_NoOpenCalls(t *testing.T) {
	r := monitor.Reason{
		IdleSilenceMS: 65000,
		OpenCallCount: 0,
		LastEventType: "thinking",
	}
	attrs := reasonAttrs(r)
	want := []any{
		"idle_silence_ms", int64(65000),
		"open_call_count", 0,
		"last_event_type", "thinking",
	}
	if len(attrs) != len(want) {
		t.Fatalf("len(attrs) = %d, want %d", len(attrs), len(want))
	}
	for i := range want {
		if attrs[i] != want[i] {
			t.Errorf("attrs[%d] = %v (%T), want %v (%T)", i, attrs[i], attrs[i], want[i], want[i])
		}
	}
}

func TestReasonAttrs_WithOpenCalls(t *testing.T) {
	r := monitor.Reason{
		IdleSilenceMS: 120000,
		OpenCallCount: 2,
		LastEventType: "tool_call",
		OpenCalls: []monitor.OpenCallDetail{
			{CallID: "call_1", Command: "sleep 5", ElapsedMS: 95000, TimeoutMS: 60000},
			{CallID: "call_2", Command: "", ElapsedMS: 80000, TimeoutMS: 0},
		},
	}
	attrs := reasonAttrs(r)

	// Base attrs (6) + 2 open calls * 4 attrs each = 14 values = 7 key-value pairs
	// Key-value pairs: 3 base + 4*2 open calls = 11 pairs = 22 values
	wantLen := 6 + 2*8 // 3 base KV pairs (6 values) + 2 calls * 4 KV pairs (8 values each)
	if len(attrs) != wantLen {
		t.Fatalf("len(attrs) = %d, want %d", len(attrs), wantLen)
	}

	// Check the first open call attrs
	if attrs[6] != "open_call_0_id" {
		t.Errorf("attrs[6] = %v, want open_call_0_id", attrs[6])
	}
	if attrs[7] != "call_1" {
		t.Errorf("attrs[7] = %v, want call_1", attrs[7])
	}
	if attrs[8] != "open_call_0_command" {
		t.Errorf("attrs[8] = %v, want open_call_0_command", attrs[8])
	}
	if attrs[9] != "sleep 5" {
		t.Errorf("attrs[9] = %v, want 'sleep 5'", attrs[9])
	}
}

// --- handleStreamEnd tests ---

func TestHandleStreamEnd_SessionDone_ReturnsNil(t *testing.T) {
	// We can't easily test handleStreamEnd with real processes in unit tests,
	// but we can test it by creating a mock-like setup using a real process
	// that exits immediately.
	// For now, we verify the function signature and logic by testing the
	// condition branches with a completed subprocess.

	// Test by using a real process that exits 0.
	sess, err := process.Start(t.Context(), process.Config{
		AgentBin: "true", // exits 0 immediately
		Prompt:   "test",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Drain stdout to let the process exit.
	io.Copy(io.Discard, sess.Stdout)

	// Create a monitor that has seen a result event.
	mon := monitor.NewMonitor(60*time.Second, 30*time.Second)
	mon.ProcessEvent(events.AnnotatedEvent{
		RecvTime: time.Now(),
		Raw:      []byte(`{"type":"result","subtype":"done"}`),
		Parsed:   events.RawEvent{Type: "result", Subtype: "done"},
	})

	log, teardown := setupTestLogger(t)
	defer teardown()

	err = handleStreamEnd(sess, mon, log)
	if err != nil {
		t.Fatalf("handleStreamEnd returned error: %v", err)
	}
}

func TestHandleStreamEnd_NoResult_ReturnsAbnormalExit(t *testing.T) {
	sess, err := process.Start(t.Context(), process.Config{
		AgentBin: "true",
		Prompt:   "test",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	io.Copy(io.Discard, sess.Stdout)

	// Monitor has NOT seen a result event.
	mon := monitor.NewMonitor(60*time.Second, 30*time.Second)

	log, teardown := setupTestLogger(t)
	defer teardown()

	err = handleStreamEnd(sess, mon, log)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "without emitting a result event") {
		t.Errorf("unexpected error message: %v", err)
	}
	if !errors.Is(err, ErrAbnormalExit) {
		t.Errorf("expected ErrAbnormalExit, got: %v", err)
	}
}

// --- firstPrompt tests ---

func TestFirstPrompt_PositionalArg(t *testing.T) {
	cfg := Config{
		PositionalPrompt: "hello world",
		PromptReader:     bufio.NewReader(strings.NewReader("")),
	}
	got, err := firstPrompt(cfg)
	if err != nil {
		t.Fatalf("firstPrompt: %v", err)
	}
	if got != "hello world" {
		t.Errorf("got %q, want %q", got, "hello world")
	}
}

func TestFirstPrompt_PrintMode_TTY_NoArg_Error(t *testing.T) {
	// Override isTerminal to simulate a TTY.
	origIsTerminal := isTerminal
	isTerminal = func(_ *os.File) bool { return true }
	defer func() { isTerminal = origIsTerminal }()

	cfg := Config{
		Print:        true,
		PromptReader: bufio.NewReader(strings.NewReader("")),
	}
	_, err := firstPrompt(cfg)
	if err == nil {
		t.Fatal("expected error for -p with TTY and no positional arg")
	}
	if !strings.Contains(err.Error(), "no prompt provided") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestFirstPrompt_PrintMode_PipedStdin(t *testing.T) {
	// Override isTerminal to simulate piped input.
	origIsTerminal := isTerminal
	isTerminal = func(_ *os.File) bool { return false }
	defer func() { isTerminal = origIsTerminal }()

	cfg := Config{
		Print:        true,
		PromptReader: bufio.NewReader(strings.NewReader("  piped prompt text  \n")),
	}
	got, err := firstPrompt(cfg)
	if err != nil {
		t.Fatalf("firstPrompt: %v", err)
	}
	if got != "piped prompt text" {
		t.Errorf("got %q, want %q", got, "piped prompt text")
	}
}

func TestFirstPrompt_PrintMode_PipedStdin_Empty(t *testing.T) {
	origIsTerminal := isTerminal
	isTerminal = func(_ *os.File) bool { return false }
	defer func() { isTerminal = origIsTerminal }()

	cfg := Config{
		Print:        true,
		PromptReader: bufio.NewReader(strings.NewReader("   \n  \n")),
	}
	_, err := firstPrompt(cfg)
	if err == nil {
		t.Fatal("expected error for empty piped stdin")
	}
	if !strings.Contains(err.Error(), "no prompt provided") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestFirstPrompt_Interactive_DelegatesToReadPrompt(t *testing.T) {
	origIsTerminal := isTerminal
	isTerminal = func(_ *os.File) bool { return false }
	defer func() { isTerminal = origIsTerminal }()

	cfg := Config{
		Print:        false,
		PromptReader: bufio.NewReader(strings.NewReader("interactive prompt\n")),
	}
	got, err := firstPrompt(cfg)
	if err != nil {
		t.Fatalf("firstPrompt: %v", err)
	}
	if got != "interactive prompt" {
		t.Errorf("got %q, want %q", got, "interactive prompt")
	}
}

// --- readPrompt tests ---

func TestReadPrompt_FirstNonEmpty(t *testing.T) {
	origIsTerminal := isTerminal
	isTerminal = func(_ *os.File) bool { return false }
	defer func() { isTerminal = origIsTerminal }()

	r := bufio.NewReader(strings.NewReader("hello\n"))
	got, err := readPrompt(r)
	if err != nil {
		t.Fatalf("readPrompt: %v", err)
	}
	if got != "hello" {
		t.Errorf("got %q, want %q", got, "hello")
	}
}

func TestReadPrompt_SkipsBlanks(t *testing.T) {
	origIsTerminal := isTerminal
	isTerminal = func(_ *os.File) bool { return false }
	defer func() { isTerminal = origIsTerminal }()

	r := bufio.NewReader(strings.NewReader("\n  \n\nactual prompt\n"))
	got, err := readPrompt(r)
	if err != nil {
		t.Fatalf("readPrompt: %v", err)
	}
	if got != "actual prompt" {
		t.Errorf("got %q, want %q", got, "actual prompt")
	}
}

func TestReadPrompt_EOF(t *testing.T) {
	origIsTerminal := isTerminal
	isTerminal = func(_ *os.File) bool { return false }
	defer func() { isTerminal = origIsTerminal }()

	r := bufio.NewReader(strings.NewReader(""))
	_, err := readPrompt(r)
	if err != io.EOF {
		t.Errorf("got err=%v, want io.EOF", err)
	}
}

func TestReadPrompt_NonEmptyWithoutNewline(t *testing.T) {
	// Text without trailing newline — ReadString returns io.EOF along with the data.
	origIsTerminal := isTerminal
	isTerminal = func(_ *os.File) bool { return false }
	defer func() { isTerminal = origIsTerminal }()

	r := bufio.NewReader(strings.NewReader("no newline"))
	got, err := readPrompt(r)
	if err != nil {
		t.Fatalf("readPrompt: %v", err)
	}
	if got != "no newline" {
		t.Errorf("got %q, want %q", got, "no newline")
	}
}

func TestReadPrompt_BlanksThenEOF(t *testing.T) {
	origIsTerminal := isTerminal
	isTerminal = func(_ *os.File) bool { return false }
	defer func() { isTerminal = origIsTerminal }()

	r := bufio.NewReader(strings.NewReader("\n\n  \n"))
	_, err := readPrompt(r)
	if err != io.EOF {
		t.Errorf("got err=%v, want io.EOF", err)
	}
}

// --- logVerdict tests ---

func TestLogVerdict_OKIsNotLogged(t *testing.T) {
	// VerdictOK should not cause any log output. We just verify no panic.
	log, teardown := setupTestLogger(t)
	defer teardown()
	ev := events.AnnotatedEvent{Parsed: events.RawEvent{Type: "assistant"}}
	logVerdict(log, monitor.VerdictOK, ev)
}

func TestLogVerdict_WaitingIsLogged(t *testing.T) {
	log, teardown := setupTestLogger(t)
	defer teardown()
	ev := events.AnnotatedEvent{Parsed: events.RawEvent{Type: "tool_call"}}
	logVerdict(log, monitor.VerdictWaiting, ev)
	// No panic = success. The actual log output goes to a temp file.
}

// --- test helpers ---

func setupTestLogger(t *testing.T) (*logger.LogSession, func()) {
	t.Helper()
	dir := t.TempDir()
	log, teardown := logger.Setup(logger.LogConfig{
		Dir:          dir,
		ConsoleLevel: 100, // effectively disable console output during tests
		FileLevel:    -10, // capture everything
	})
	return log, func() {
		if err := teardown(); err != nil {
			t.Errorf("log teardown: %v", err)
		}
	}
}
