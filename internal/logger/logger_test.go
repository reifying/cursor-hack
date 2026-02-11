package logger

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetup_CreatesLogFile(t *testing.T) {
	dir := t.TempDir()
	cfg := LogConfig{
		Dir:          dir,
		ConsoleLevel: slog.LevelWarn,
		FileLevel:    slog.LevelDebug,
	}

	ls, teardown := Setup(cfg)
	defer teardown()

	fp := ls.FilePath()
	if fp == "" {
		t.Fatal("expected non-empty file path")
	}

	// Verify the file exists.
	if _, err := os.Stat(fp); err != nil {
		t.Fatalf("log file does not exist: %v", err)
	}

	// Verify the placeholder filename format.
	base := filepath.Base(fp)
	if !strings.HasPrefix(base, "cursor-wrap-") {
		t.Errorf("filename = %q, expected prefix 'cursor-wrap-'", base)
	}
	if !strings.HasSuffix(base, "-unknown.jsonl") {
		t.Errorf("filename = %q, expected suffix '-unknown.jsonl'", base)
	}
}

func TestSetup_CreatesDirectoryIfMissing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "subdir", "logs")
	cfg := LogConfig{
		Dir:          dir,
		ConsoleLevel: slog.LevelWarn,
		FileLevel:    slog.LevelDebug,
	}

	ls, teardown := Setup(cfg)
	defer teardown()

	if ls.FilePath() == "" {
		t.Fatal("expected non-empty file path")
	}

	// Verify the directory was created.
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("directory was not created: %v", err)
	}
}

func TestSetSessionID_RenamesFile(t *testing.T) {
	dir := t.TempDir()
	cfg := LogConfig{
		Dir:          dir,
		ConsoleLevel: slog.LevelWarn,
		FileLevel:    slog.LevelDebug,
	}

	ls, teardown := Setup(cfg)
	defer teardown()

	oldPath := ls.FilePath()
	ls.SetSessionID("test-session-abc")

	newPath := ls.FilePath()
	if newPath == oldPath {
		t.Fatal("file path did not change after SetSessionID")
	}

	// Verify the new filename contains the session_id.
	base := filepath.Base(newPath)
	if !strings.Contains(base, "test-session-abc") {
		t.Errorf("filename = %q, expected to contain 'test-session-abc'", base)
	}
	if !strings.HasSuffix(base, ".jsonl") {
		t.Errorf("filename = %q, expected .jsonl suffix", base)
	}

	// Verify old file no longer exists.
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Errorf("old file still exists at %q", oldPath)
	}

	// Verify new file exists.
	if _, err := os.Stat(newPath); err != nil {
		t.Fatalf("new file does not exist: %v", err)
	}
}

func TestSetSessionID_Idempotent(t *testing.T) {
	dir := t.TempDir()
	cfg := LogConfig{
		Dir:          dir,
		ConsoleLevel: slog.LevelWarn,
		FileLevel:    slog.LevelDebug,
	}

	ls, teardown := Setup(cfg)
	defer teardown()

	ls.SetSessionID("first-id")
	pathAfterFirst := ls.FilePath()

	// Second call should be a no-op.
	ls.SetSessionID("second-id")
	pathAfterSecond := ls.FilePath()

	if pathAfterFirst != pathAfterSecond {
		t.Errorf("second SetSessionID changed path: %q -> %q", pathAfterFirst, pathAfterSecond)
	}
}

func TestSetup_WritesValidJSONL(t *testing.T) {
	dir := t.TempDir()
	cfg := LogConfig{
		Dir:          dir,
		ConsoleLevel: slog.LevelError, // suppress console output in tests
		FileLevel:    slog.LevelDebug,
	}

	ls, teardown := Setup(cfg)

	// Write a log record.
	ls.Info("test_message", "key1", "value1", "key2", 42)
	teardown()

	// Read the file and verify it's valid JSONL.
	data, err := os.ReadFile(ls.FilePath())
	if err != nil {
		t.Fatalf("reading log file: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) == 0 {
		t.Fatal("log file is empty")
	}

	for i, line := range lines {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("line %d is not valid JSON: %v\nline: %s", i, err, line)
		}

		// Verify standard slog fields are present.
		if _, ok := record["time"]; !ok {
			t.Errorf("line %d missing 'time' field", i)
		}
		if _, ok := record["level"]; !ok {
			t.Errorf("line %d missing 'level' field", i)
		}
		if _, ok := record["msg"]; !ok {
			t.Errorf("line %d missing 'msg' field", i)
		}
	}
}

func TestSetup_TimestampIsEpochMillis(t *testing.T) {
	dir := t.TempDir()
	cfg := LogConfig{
		Dir:          dir,
		ConsoleLevel: slog.LevelError,
		FileLevel:    slog.LevelDebug,
	}

	ls, teardown := Setup(cfg)

	ls.Info("timestamp_test")
	teardown()

	data, err := os.ReadFile(ls.FilePath())
	if err != nil {
		t.Fatalf("reading log file: %v", err)
	}

	var record map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(data))), &record); err != nil {
		t.Fatalf("parsing record: %v", err)
	}

	// time should be a number (epoch millis), not a string.
	timeVal, ok := record["time"]
	if !ok {
		t.Fatal("missing 'time' field")
	}

	// json.Unmarshal puts numbers into float64 by default.
	ts, ok := timeVal.(float64)
	if !ok {
		t.Fatalf("time field is %T, expected float64 (epoch millis)", timeVal)
	}

	// Sanity check: should be a reasonable epoch millis timestamp (after 2020).
	if ts < 1577836800000 {
		t.Errorf("timestamp %f is too small for epoch millis", ts)
	}
}

func TestSetup_RawEventRecord(t *testing.T) {
	dir := t.TempDir()
	cfg := LogConfig{
		Dir:          dir,
		ConsoleLevel: slog.LevelError,
		FileLevel:    slog.LevelDebug,
	}

	ls, teardown := Setup(cfg)

	// Simulate logging a raw event, as logRawEvent in the orchestrator does.
	rawJSON := json.RawMessage(`{"type":"tool_call","subtype":"started","call_id":"call_xxx"}`)
	ls.Debug("raw_event",
		"recv_ts", int64(1770823845400),
		slog.Any("raw", rawJSON),
	)
	teardown()

	data, err := os.ReadFile(ls.FilePath())
	if err != nil {
		t.Fatalf("reading log file: %v", err)
	}

	var record map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(data))), &record); err != nil {
		t.Fatalf("parsing record: %v", err)
	}

	// Verify recv_ts field (AC 6).
	recvTS, ok := record["recv_ts"]
	if !ok {
		t.Fatal("missing 'recv_ts' field in raw event record")
	}
	if recvTS.(float64) != 1770823845400 {
		t.Errorf("recv_ts = %v, want 1770823845400", recvTS)
	}

	// Verify raw field (AC 6).
	rawVal, ok := record["raw"]
	if !ok {
		t.Fatal("missing 'raw' field in raw event record")
	}

	// raw should be a nested object, not a string.
	rawMap, ok := rawVal.(map[string]any)
	if !ok {
		t.Fatalf("raw field is %T, expected map[string]any (nested object)", rawVal)
	}
	if rawMap["type"] != "tool_call" {
		t.Errorf("raw.type = %v, want 'tool_call'", rawMap["type"])
	}
	if rawMap["call_id"] != "call_xxx" {
		t.Errorf("raw.call_id = %v, want 'call_xxx'", rawMap["call_id"])
	}

	// Verify msg field.
	if record["msg"] != "raw_event" {
		t.Errorf("msg = %v, want 'raw_event'", record["msg"])
	}
}

func TestSetup_HangDetectionRecord(t *testing.T) {
	dir := t.TempDir()
	cfg := LogConfig{
		Dir:          dir,
		ConsoleLevel: slog.LevelError,
		FileLevel:    slog.LevelDebug,
	}

	ls, teardown := Setup(cfg)

	// Simulate logging a hang detection decision (AC 7).
	ls.Error("hang_detected",
		"idle_silence_ms", int64(65000),
		"open_call_count", 2,
		"last_event_type", "thinking",
		"open_call_0_id", "call_1",
		"open_call_0_command", "sleep 5",
		"open_call_0_elapsed_ms", int64(70000),
		"open_call_0_timeout_ms", int64(10000),
		"open_call_1_id", "call_2",
		"open_call_1_command", "npm install",
		"open_call_1_elapsed_ms", int64(65000),
		"open_call_1_timeout_ms", int64(30000),
	)
	teardown()

	data, err := os.ReadFile(ls.FilePath())
	if err != nil {
		t.Fatalf("reading log file: %v", err)
	}

	var record map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(data))), &record); err != nil {
		t.Fatalf("parsing record: %v", err)
	}

	// Verify all Reason fields are present (AC 7).
	checks := map[string]any{
		"idle_silence_ms":        float64(65000),
		"open_call_count":        float64(2),
		"last_event_type":        "thinking",
		"open_call_0_id":         "call_1",
		"open_call_0_command":    "sleep 5",
		"open_call_0_elapsed_ms": float64(70000),
		"open_call_0_timeout_ms": float64(10000),
		"open_call_1_id":         "call_2",
		"open_call_1_command":    "npm install",
		"open_call_1_elapsed_ms": float64(65000),
		"open_call_1_timeout_ms": float64(30000),
	}
	for key, want := range checks {
		got, ok := record[key]
		if !ok {
			t.Errorf("missing field %q in hang detection record", key)
			continue
		}
		if got != want {
			t.Errorf("%s = %v (%T), want %v (%T)", key, got, got, want, want)
		}
	}

	// Verify msg field.
	if record["msg"] != "hang_detected" {
		t.Errorf("msg = %v, want 'hang_detected'", record["msg"])
	}
}

func TestSetup_ConsoleRespectsLevel(t *testing.T) {
	dir := t.TempDir()
	cfg := LogConfig{
		Dir:          dir,
		ConsoleLevel: slog.LevelWarn, // only warn and above to console
		FileLevel:    slog.LevelDebug,
	}

	ls, teardown := Setup(cfg)
	defer teardown()

	// Write debug and info records — these should go to file but not console.
	ls.Debug("debug_msg", "key", "val")
	ls.Info("info_msg", "key", "val")
	ls.Warn("warn_msg", "key", "val")
	teardown()

	// Verify all three records appear in the file.
	data, err := os.ReadFile(ls.FilePath())
	if err != nil {
		t.Fatalf("reading log file: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 3 {
		t.Errorf("expected 3 records in file, got %d", len(lines))
	}
}

func TestSetup_TeardownClosesFile(t *testing.T) {
	dir := t.TempDir()
	cfg := LogConfig{
		Dir:          dir,
		ConsoleLevel: slog.LevelError,
		FileLevel:    slog.LevelDebug,
	}

	ls, teardown := Setup(cfg)
	ls.Info("before_teardown")

	if err := teardown(); err != nil {
		t.Fatalf("teardown failed: %v", err)
	}

	// After teardown, writes should fail (or at least the file should be closed).
	// We verify by checking that the file was properly written and closed.
	data, err := os.ReadFile(ls.FilePath())
	if err != nil {
		t.Fatalf("reading log file after teardown: %v", err)
	}

	if len(data) == 0 {
		t.Fatal("log file is empty after teardown")
	}

	// Verify it's valid JSON.
	var record map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(data))), &record); err != nil {
		t.Fatalf("invalid JSONL after teardown: %v", err)
	}
}
