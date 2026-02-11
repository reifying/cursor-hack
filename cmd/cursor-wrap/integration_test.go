package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// wrapperBin and fakeAgentBin are set by TestMain after building.
var (
	wrapperBin   string
	fakeAgentBin string
)

func TestMain(m *testing.M) {
	// Build the cursor-wrap binary.
	tmpDir, err := os.MkdirTemp("", "integration-test-*")
	if err != nil {
		panic(err)
	}

	wrapperBin = filepath.Join(tmpDir, "cursor-wrap")
	cmd := exec.Command("go", "build", "-o", wrapperBin, ".")
	cmd.Dir = "."
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		panic("failed to build cursor-wrap: " + err.Error())
	}

	// Build the fake agent binary.
	fakeAgentBin = filepath.Join(tmpDir, "fake-agent")
	cmd = exec.Command("go", "build", "-o", fakeAgentBin, "./testdata/fakeagent")
	cmd.Dir = "."
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		panic("failed to build fake-agent: " + err.Error())
	}

	// Run tests, clean up, then exit. os.Exit bypasses defer, so we
	// capture the exit code and remove the temp dir explicitly.
	exitCode := m.Run()
	os.RemoveAll(tmpDir)
	os.Exit(exitCode)
}

// --- Integration test: Normal completion (AC #1) ---

func TestIntegration_NormalCompletion(t *testing.T) {
	logDir := t.TempDir()

	cmd := exec.Command(wrapperBin,
		"-p",
		"--agent-bin", fakeAgentBin,
		"--idle-timeout", "2s",
		"--tool-grace", "1s",
		"--tick-interval", "500ms",
		"--log-dir", logDir,
		"--output-format", "stream-json",
		"test prompt",
	)
	cmd.Env = append(os.Environ(), "FAKE_AGENT_SCENARIO=normal")

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		t.Fatalf("wrapper exited with error: %v\nstderr: %s", err, stderr.String())
	}

	// Verify exit code 0.
	if cmd.ProcessState.ExitCode() != 0 {
		t.Fatalf("expected exit code 0, got %d", cmd.ProcessState.ExitCode())
	}

	// Verify output contains the expected events.
	output := stdout.String()
	if !strings.Contains(output, `"type":"system"`) {
		t.Error("stdout missing system/init event")
	}
	if !strings.Contains(output, `"type":"result"`) {
		t.Error("stdout missing result event")
	}
}

// --- Integration test: Idle hang detection (AC #2) ---

func TestIntegration_IdleHangDetection(t *testing.T) {
	logDir := t.TempDir()

	cmd := exec.Command(wrapperBin,
		"-p",
		"--agent-bin", fakeAgentBin,
		"--idle-timeout", "1s",
		"--tool-grace", "1s",
		"--tick-interval", "500ms",
		"--log-dir", logDir,
		"--output-format", "stream-json",
		"test prompt",
	)
	cmd.Env = append(os.Environ(), "FAKE_AGENT_SCENARIO=idle_hang")

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err == nil {
		t.Fatal("expected non-zero exit, got nil error")
	}

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v", err, err)
	}

	// Hang detection should produce exit code 2.
	if exitErr.ExitCode() != 2 {
		t.Fatalf("expected exit code 2, got %d\nstderr: %s", exitErr.ExitCode(), stderr.String())
	}

	// AC #7: Verify hang detection decision is logged.
	logContent := readLogFile(t, logDir)
	if !strings.Contains(logContent, "hang detected") {
		t.Error("expected 'hang detected' in log file (AC #7)")
	}
}

// --- Integration test: Tool-timeout hang (AC #3) ---

func TestIntegration_ToolTimeoutHang(t *testing.T) {
	logDir := t.TempDir()

	cmd := exec.Command(wrapperBin,
		"-p",
		"--agent-bin", fakeAgentBin,
		"--idle-timeout", "10s",
		"--tool-grace", "1s",
		"--tick-interval", "500ms",
		"--log-dir", logDir,
		"--output-format", "stream-json",
		"test prompt",
	)
	// Tool has a 1000ms timeout, grace is 1s, so hang after 2s + tick.
	cmd.Env = append(os.Environ(), "FAKE_AGENT_SCENARIO=tool_timeout_hang")

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err == nil {
		t.Fatal("expected non-zero exit, got nil error")
	}

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v", err, err)
	}

	if exitErr.ExitCode() != 2 {
		t.Fatalf("expected exit code 2, got %d\nstderr: %s", exitErr.ExitCode(), stderr.String())
	}
}

// --- Integration test: Transparent proxy stream-json (AC #8) ---

func TestIntegration_TransparentProxy(t *testing.T) {
	logDir := t.TempDir()

	cmd := exec.Command(wrapperBin,
		"-p",
		"--agent-bin", fakeAgentBin,
		"--idle-timeout", "5s",
		"--tool-grace", "1s",
		"--tick-interval", "500ms",
		"--log-dir", logDir,
		"--output-format", "stream-json",
		"test prompt",
	)
	cmd.Env = append(os.Environ(), "FAKE_AGENT_SCENARIO=normal")

	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = io.Discard

	if err := cmd.Run(); err != nil {
		t.Fatalf("wrapper exited with error: %v", err)
	}

	// Verify each line of output is byte-identical to what the fake agent emits.
	// The fake agent "normal" scenario outputs a known set of JSONL lines.
	// The wrapper in stream-json mode should pass them through verbatim.
	expectedLines := normalScenarioLines()
	actualLines := nonEmptyLines(stdout.String())

	if len(actualLines) != len(expectedLines) {
		t.Fatalf("line count mismatch: got %d, want %d\nactual:\n%s",
			len(actualLines), len(expectedLines), stdout.String())
	}
	for i, want := range expectedLines {
		if actualLines[i] != want {
			t.Errorf("line %d mismatch:\ngot:  %s\nwant: %s", i, actualLines[i], want)
		}
	}
}

// --- Integration test: Text format output (AC #13) ---

func TestIntegration_TextFormat(t *testing.T) {
	logDir := t.TempDir()

	cmd := exec.Command(wrapperBin,
		"-p",
		"--agent-bin", fakeAgentBin,
		"--idle-timeout", "5s",
		"--tool-grace", "1s",
		"--tick-interval", "500ms",
		"--log-dir", logDir,
		"--output-format", "text",
		"test prompt",
	)
	cmd.Env = append(os.Environ(), "FAKE_AGENT_SCENARIO=with_tool")

	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = io.Discard

	if err := cmd.Run(); err != nil {
		t.Fatalf("wrapper exited with error: %v", err)
	}

	output := stdout.String()

	// Should contain assistant text.
	if !strings.Contains(output, "I'll run a command for you.") {
		t.Errorf("missing assistant text in output:\n%s", output)
	}

	// Should contain tool call spinner.
	if !strings.Contains(output, "⏳ `echo hello`") {
		t.Errorf("missing tool call started indicator in output:\n%s", output)
	}

	// Should contain tool call completion.
	if !strings.Contains(output, "✓ `echo hello`") {
		t.Errorf("missing tool call completion indicator in output:\n%s", output)
	}

	// Should contain final assistant text.
	if !strings.Contains(output, "The command completed successfully.") {
		t.Errorf("missing final assistant text in output:\n%s", output)
	}
}

// --- Integration test: Multi-turn with --resume (AC #11, AC #14) ---

func TestIntegration_MultiTurn(t *testing.T) {
	logDir := t.TempDir()

	// Feed two prompts via stdin, then EOF.
	stdinContent := "first prompt\nsecond prompt\n"

	cmd := exec.Command(wrapperBin,
		"--agent-bin", fakeAgentBin,
		"--idle-timeout", "5s",
		"--tool-grace", "1s",
		"--tick-interval", "500ms",
		"--log-dir", logDir,
		"--output-format", "stream-json",
	)
	cmd.Env = append(os.Environ(), "FAKE_AGENT_SCENARIO=multi_turn")
	cmd.Stdin = strings.NewReader(stdinContent)

	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = io.Discard

	err := cmd.Run()
	if err != nil {
		t.Fatalf("wrapper exited with error: %v", err)
	}

	// Output should contain two result events (one per turn).
	output := stdout.String()
	resultCount := strings.Count(output, `"type":"result"`)
	if resultCount != 2 {
		t.Fatalf("expected 2 result events, got %d\noutput:\n%s", resultCount, output)
	}

	// Verify the second turn used --resume by checking the log file.
	// The fake agent logs its args to stderr, which the wrapper captures
	// in the log file at debug level.
	logContent := readLogFile(t, logDir)
	if !strings.Contains(logContent, "--resume") {
		t.Errorf("expected --resume in log file for second turn\nlog:\n%s", logContent)
	}
	// Verify the session_id was passed to --resume.
	if !strings.Contains(logContent, "test-session-id") {
		t.Errorf("expected test-session-id in log file\nlog:\n%s", logContent)
	}
}

// --- Integration test: Hang recovery in interactive mode (AC #12) ---

func TestIntegration_HangRecoveryInteractive(t *testing.T) {
	logDir := t.TempDir()

	// First prompt triggers a hang, second prompt completes normally.
	stdinContent := "hang prompt\nnormal prompt\n"

	cmd := exec.Command(wrapperBin,
		"--agent-bin", fakeAgentBin,
		"--idle-timeout", "1s",
		"--tool-grace", "1s",
		"--tick-interval", "500ms",
		"--log-dir", logDir,
		"--output-format", "stream-json",
	)
	cmd.Env = append(os.Environ(), "FAKE_AGENT_SCENARIO=hang_then_normal")
	cmd.Stdin = strings.NewReader(stdinContent)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		t.Fatalf("wrapper should exit 0 in interactive mode after hang recovery: %v\nstderr: %s",
			err, stderr.String())
	}

	output := stdout.String()

	// Should contain a hang indicator.
	if !strings.Contains(output, "hang_detected") {
		t.Errorf("expected hang_detected event in output:\n%s", output)
	}

	// Should contain a result event from the second (successful) turn.
	if !strings.Contains(output, `"type":"result"`) {
		t.Errorf("expected result event from second turn in output:\n%s", output)
	}
}

// --- Integration test: Log file output (AC #6, #7) ---

func TestIntegration_LogFileOutput(t *testing.T) {
	logDir := t.TempDir()

	cmd := exec.Command(wrapperBin,
		"-p",
		"--agent-bin", fakeAgentBin,
		"--idle-timeout", "5s",
		"--tool-grace", "1s",
		"--tick-interval", "500ms",
		"--log-dir", logDir,
		"--output-format", "stream-json",
		"test prompt",
	)
	cmd.Env = append(os.Environ(), "FAKE_AGENT_SCENARIO=normal")
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard

	if err := cmd.Run(); err != nil {
		t.Fatalf("wrapper exited with error: %v", err)
	}

	// Find the log file.
	entries, err := os.ReadDir(logDir)
	if err != nil {
		t.Fatalf("reading log dir: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no log files found")
	}

	logPath := filepath.Join(logDir, entries[0].Name())
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("reading log file: %v", err)
	}

	logContent := string(logData)
	lines := nonEmptyLines(logContent)
	if len(lines) == 0 {
		t.Fatal("log file is empty")
	}

	// AC #6: Every raw event should appear in the log with recv_ts.
	var rawEventCount int
	for _, line := range lines {
		var record map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Errorf("invalid JSONL line: %s", line)
			continue
		}

		var msg string
		if msgRaw, ok := record["msg"]; ok {
			json.Unmarshal(msgRaw, &msg)
		}

		if msg == "raw_event" {
			rawEventCount++
			// Verify recv_ts is present.
			if _, ok := record["recv_ts"]; !ok {
				t.Error("raw_event record missing recv_ts")
			}
			// Verify raw field is present and valid JSON.
			rawField, ok := record["raw"]
			if !ok {
				t.Error("raw_event record missing raw field")
			}
			if !json.Valid(rawField) {
				t.Errorf("raw field is not valid JSON: %s", rawField)
			}
		}
	}

	// The normal scenario emits 9 events: system/init, user, thinking/delta,
	// thinking/completed, assistant, tool_call/started, tool_call/completed,
	// assistant(final), result.
	expectedEventCount := len(normalScenarioLines())
	if rawEventCount != expectedEventCount {
		t.Errorf("expected %d raw_event records, got %d", expectedEventCount, rawEventCount)
	}

	// AC #7: Verify hang detection decisions are logged.
	// In the normal scenario, tool_call/started creates an open call, so the
	// monitor should log verdict_waiting at debug level.
	var verdictWaitingCount int
	for _, line := range lines {
		if strings.Contains(line, "verdict_waiting") {
			verdictWaitingCount++
		}
	}
	if verdictWaitingCount == 0 {
		t.Error("expected at least one verdict_waiting log entry (AC #7)")
	}

	// Verify the log file was renamed to include the session_id.
	if !strings.Contains(entries[0].Name(), "test-session-id") {
		t.Errorf("log file not renamed with session_id: %s", entries[0].Name())
	}
}

// --- Integration test: Signal handling (AC #9) ---

func TestIntegration_SignalHandling(t *testing.T) {
	logDir := t.TempDir()

	cmd := exec.Command(wrapperBin,
		"-p",
		"--agent-bin", fakeAgentBin,
		"--idle-timeout", "30s",
		"--tool-grace", "30s",
		"--tick-interval", "1s",
		"--log-dir", logDir,
		"--output-format", "stream-json",
		"test prompt",
	)
	// slow_normal emits events slowly — gives us time to send a signal.
	cmd.Env = append(os.Environ(), "FAKE_AGENT_SCENARIO=slow_normal")

	var stderr bytes.Buffer
	cmd.Stdout = io.Discard
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start wrapper: %v", err)
	}

	// Give the wrapper time to start and begin reading events.
	time.Sleep(500 * time.Millisecond)

	// Send SIGINT to the wrapper.
	if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatalf("failed to send SIGINT: %v", err)
	}

	// Wait for the wrapper to exit.
	err := cmd.Wait()
	if err == nil {
		t.Fatal("expected non-zero exit after SIGINT")
	}

	// The wrapper should exit with code 1 (context cancelled).
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v", err, err)
	}
	if exitErr.ExitCode() == 0 {
		t.Fatal("expected non-zero exit code after SIGINT")
	}

	// Verify the child process is no longer running.
	// (The wrapper should have killed it.)
	// We check indirectly: the wrapper exited, which means it waited
	// for the child and cleaned up. This is sufficient.
}

// --- Integration test: --resume on initial invocation ---

func TestIntegration_ResumeOnFirstTurn(t *testing.T) {
	logDir := t.TempDir()

	cmd := exec.Command(wrapperBin,
		"-p",
		"--agent-bin", fakeAgentBin,
		"--idle-timeout", "5s",
		"--tool-grace", "1s",
		"--tick-interval", "500ms",
		"--log-dir", logDir,
		"--output-format", "stream-json",
		"--resume", "sess-pre-seeded-456",
		"continue where we left off",
	)
	cmd.Env = append(os.Environ(), "FAKE_AGENT_SCENARIO=normal")

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		t.Fatalf("wrapper exited with error: %v\nstderr: %s", err, stderr.String())
	}

	// Verify --resume was passed to cursor-agent on the first (and only) turn
	// by checking the log file where fake-agent's stderr (args) are captured.
	logContent := readLogFile(t, logDir)
	if !strings.Contains(logContent, "--resume") {
		t.Errorf("expected --resume in log file for first turn\nlog:\n%s", logContent)
	}
	if !strings.Contains(logContent, "sess-pre-seeded-456") {
		t.Errorf("expected sess-pre-seeded-456 in log file\nlog:\n%s", logContent)
	}
}

// --- Integration test: -p flag behavior (AC #14) ---

func TestIntegration_PrintModeSingleTurn(t *testing.T) {
	logDir := t.TempDir()

	cmd := exec.Command(wrapperBin,
		"-p",
		"--agent-bin", fakeAgentBin,
		"--idle-timeout", "5s",
		"--tool-grace", "1s",
		"--tick-interval", "500ms",
		"--log-dir", logDir,
		"--output-format", "stream-json",
		"test prompt",
	)
	cmd.Env = append(os.Environ(), "FAKE_AGENT_SCENARIO=normal")

	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = io.Discard

	if err := cmd.Run(); err != nil {
		t.Fatalf("wrapper exited with error: %v", err)
	}

	// With -p, only one turn should execute. Only one result event expected.
	resultCount := strings.Count(stdout.String(), `"type":"result"`)
	if resultCount != 1 {
		t.Fatalf("expected exactly 1 result event with -p, got %d", resultCount)
	}
}

// --- Helpers ---

// normalScenarioLines returns the expected JSONL lines from the "normal" fake agent scenario.
// Must match exactly what fakeagent outputs for FAKE_AGENT_SCENARIO=normal.
func normalScenarioLines() []string {
	return []string{
		`{"type":"system","subtype":"init","session_id":"test-session-id","model":"test-model","cwd":"/tmp","permissionMode":"auto"}`,
		`{"type":"user","message":{"content":[{"type":"text","text":"test prompt"}]}}`,
		`{"type":"thinking","subtype":"delta","text":"Let me think about this."}`,
		`{"type":"thinking","subtype":"completed"}`,
		`{"type":"assistant","model_call_id":"mc_1","message":{"content":[{"type":"text","text":"Here is my response."}]}}`,
		`{"type":"tool_call","subtype":"started","call_id":"call_1","model_call_id":"mc_1","timestamp_ms":1000,"tool_call":{"shellToolCall":{"args":{"command":"echo test","timeout":120000}}}}`,
		`{"type":"tool_call","subtype":"completed","call_id":"call_1","model_call_id":"mc_1","timestamp_ms":1100,"tool_call":{"shellToolCall":{"args":{"command":"echo test","timeout":120000},"result":{"success":{"exitCode":0,"stdout":"test\n","stderr":"","executionTime":100}}}}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Final answer."}]}}`,
		`{"type":"result","subtype":"success","duration_ms":1000,"is_error":false,"session_id":"test-session-id","request_id":"req_1"}`,
	}
}

// readLogFile reads and returns the content of the first log file in the directory.
func readLogFile(t *testing.T, logDir string) string {
	t.Helper()
	entries, err := os.ReadDir(logDir)
	if err != nil {
		t.Fatalf("reading log dir: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no log files found")
	}
	logPath := filepath.Join(logDir, entries[0].Name())
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("reading log file: %v", err)
	}
	return string(data)
}

// nonEmptyLines splits text by newlines and returns non-empty lines.
func nonEmptyLines(s string) []string {
	var lines []string
	scanner := bufio.NewScanner(strings.NewReader(s))
	for scanner.Scan() {
		line := scanner.Text()
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}
