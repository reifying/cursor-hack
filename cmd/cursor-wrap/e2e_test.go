//go:build e2e

// E2E tests run cursor-wrap against a real cursor-agent invocation.
//
// Prerequisites:
//   - cursor-agent must be installed and available in $PATH
//   - cursor-agent must be authenticated (valid API credentials)
//   - Network access to the cursor-agent API
//
// Run all E2E tests:
//
//	go test -tags=e2e -v ./cmd/cursor-wrap/ -run TestE2E -timeout 300s
//
// Run a single E2E test:
//
//	go test -tags=e2e -v ./cmd/cursor-wrap/ -run TestE2E_BasicPrompt_StreamJSON -timeout 120s
//
// These tests are guarded by the "e2e" build tag and are NOT included
// in normal "go test ./..." runs. If cursor-agent is not found in $PATH,
// each test is skipped with a clear message.
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
	"testing"
	"time"
)

// e2eTimeout is the maximum time each E2E test waits for cursor-agent
// to complete. cursor-agent may need time for API calls.
const e2eTimeout = 90 * time.Second

// skipIfNoAgent skips the test if cursor-agent is not available.
func skipIfNoAgent(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("cursor-agent"); err != nil {
		t.Skip("cursor-agent not found in $PATH; skipping E2E test")
	}
}

// --- E2E test: Basic prompt with stream-json format (AC #1, AC #8) ---

func TestE2E_BasicPrompt_StreamJSON(t *testing.T) {
	skipIfNoAgent(t)

	logDir := t.TempDir()

	cmd := exec.Command(wrapperBin,
		"-p",
		"--idle-timeout", "60s",
		"--tool-grace", "30s",
		"--tick-interval", "5s",
		"--log-dir", logDir,
		"--output-format", "stream-json",
		"say hi",
	)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// Run with a timeout to avoid hanging forever.
	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start cursor-wrap: %v", err)
	}
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("cursor-wrap exited with error: %v\nstderr: %s", err, stderr.String())
		}
	case <-time.After(e2eTimeout):
		cmd.Process.Kill()
		t.Fatalf("cursor-wrap timed out after %v\nstderr: %s", e2eTimeout, stderr.String())
	}

	// Verify exit code 0.
	if cmd.ProcessState.ExitCode() != 0 {
		t.Fatalf("expected exit code 0, got %d\nstderr: %s",
			cmd.ProcessState.ExitCode(), stderr.String())
	}

	// Verify event stream structure. Don't assert on content (model output varies).
	output := stdout.String()
	lines := nonEmptyLines(output)
	if len(lines) == 0 {
		t.Fatal("no output from cursor-wrap")
	}

	// Assert on event lifecycle: system/init must be first, result must be last.
	var firstEvent, lastEvent struct {
		Type    string `json:"type"`
		Subtype string `json:"subtype,omitempty"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &firstEvent); err != nil {
		t.Fatalf("failed to parse first line: %v\nline: %s", err, lines[0])
	}
	if firstEvent.Type != "system" || firstEvent.Subtype != "init" {
		t.Errorf("expected first event to be system/init, got %s/%s", firstEvent.Type, firstEvent.Subtype)
	}

	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &lastEvent); err != nil {
		t.Fatalf("failed to parse last line: %v\nline: %s", err, lines[len(lines)-1])
	}
	if lastEvent.Type != "result" {
		t.Errorf("expected last event to be result, got %s", lastEvent.Type)
	}

	// Verify each line is valid JSON.
	for i, line := range lines {
		if !json.Valid([]byte(line)) {
			t.Errorf("line %d is not valid JSON: %s", i, line)
		}
	}

	// Verify system/init contains a session_id.
	var initEvent struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &initEvent); err != nil {
		t.Fatalf("failed to parse system/init: %v", err)
	}
	if initEvent.SessionID == "" {
		t.Error("system/init event missing session_id")
	}

	t.Logf("E2E basic prompt: %d events, session_id=%s", len(lines), initEvent.SessionID)
}

// --- E2E test: Text format output (AC #13) ---

func TestE2E_BasicPrompt_TextFormat(t *testing.T) {
	skipIfNoAgent(t)

	logDir := t.TempDir()

	cmd := exec.Command(wrapperBin,
		"-p",
		"--idle-timeout", "60s",
		"--tool-grace", "30s",
		"--tick-interval", "5s",
		"--log-dir", logDir,
		"--output-format", "text",
		"say hi",
	)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start cursor-wrap: %v", err)
	}
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("cursor-wrap exited with error: %v\nstderr: %s", err, stderr.String())
		}
	case <-time.After(e2eTimeout):
		cmd.Process.Kill()
		t.Fatalf("cursor-wrap timed out after %v\nstderr: %s", e2eTimeout, stderr.String())
	}

	// Verify exit code 0.
	if cmd.ProcessState.ExitCode() != 0 {
		t.Fatalf("expected exit code 0, got %d\nstderr: %s",
			cmd.ProcessState.ExitCode(), stderr.String())
	}

	output := stdout.String()
	if strings.TrimSpace(output) == "" {
		t.Fatal("no text output from cursor-wrap")
	}

	// Text format should NOT contain raw JSON event structures.
	// The output should be human-readable text.
	if strings.Contains(output, `"type":"system"`) {
		t.Error("text format output contains raw JSON system event — expected human-readable text")
	}
	if strings.Contains(output, `"type":"result"`) {
		t.Error("text format output contains raw JSON result event — expected human-readable text")
	}

	// Output should contain some text (the model's response).
	// We can't assert on exact content since model output varies.
	if len(strings.TrimSpace(output)) < 2 {
		t.Errorf("text output suspiciously short: %q", output)
	}

	t.Logf("E2E text format output (%d bytes):\n%s", len(output), output)
}

// --- E2E test: Two-turn interactive session (AC #11) ---

func TestE2E_MultiTurn_Interactive(t *testing.T) {
	skipIfNoAgent(t)

	logDir := t.TempDir()

	// First turn uses a positional arg; second turn reads from stdin.
	// This verifies the wrapper transitions from positional-arg first turn
	// to stdin-driven subsequent turns with --resume.
	stdinR, stdinW := io.Pipe()

	cmd := exec.Command(wrapperBin,
		"--idle-timeout", "60s",
		"--tool-grace", "30s",
		"--tick-interval", "5s",
		"--log-dir", logDir,
		"--output-format", "stream-json",
		"say hello",
	)
	cmd.Stdin = stdinR

	// Use StdoutPipe to read lines as they arrive. This avoids data races
	// from concurrent read/write on a shared bytes.Buffer, and eliminates
	// the need for polling goroutines.
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("failed to create stdout pipe: %v", err)
	}

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start cursor-wrap: %v", err)
	}

	// Scan stdout lines in a goroutine, forwarding each line to a channel.
	// The goroutine exits when the pipe closes (process exit).
	lineCh := make(chan string, 256)
	go func() {
		defer close(lineCh)
		scanner := bufio.NewScanner(stdoutPipe)
		for scanner.Scan() {
			lineCh <- scanner.Text()
		}
	}()

	// Collect all lines and wait for the first result event.
	var allLines []string
	resultCount := 0

	timeout := time.After(e2eTimeout)
	for resultCount < 1 {
		select {
		case line, ok := <-lineCh:
			if !ok {
				t.Fatalf("stdout closed before first result event\nlines: %v\nstderr: %s",
					allLines, stderr.String())
			}
			allLines = append(allLines, line)
			if strings.Contains(line, `"type":"result"`) {
				resultCount++
			}
		case <-timeout:
			cmd.Process.Kill()
			t.Fatalf("first turn timed out\nlines: %v\nstderr: %s", allLines, stderr.String())
		}
	}

	// Write the second prompt via stdin. The wrapper should use --resume.
	if _, err := io.WriteString(stdinW, "say goodbye\n"); err != nil {
		t.Fatalf("failed to write second prompt: %v", err)
	}

	// Wait for the second result event.
	for resultCount < 2 {
		select {
		case line, ok := <-lineCh:
			if !ok {
				t.Fatalf("stdout closed before second result event\nlines: %v\nstderr: %s",
					allLines, stderr.String())
			}
			allLines = append(allLines, line)
			if strings.Contains(line, `"type":"result"`) {
				resultCount++
			}
		case <-timeout:
			cmd.Process.Kill()
			t.Fatalf("second turn timed out\nlines: %v\nstderr: %s", allLines, stderr.String())
		}
	}

	// Close stdin to signal EOF — wrapper should exit cleanly.
	stdinW.Close()

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("cursor-wrap exited with error after multi-turn: %v\nstderr: %s",
				err, stderr.String())
		}
	case <-time.After(10 * time.Second):
		cmd.Process.Kill()
		t.Fatal("cursor-wrap did not exit after stdin EOF")
	}

	// Verify two result events.
	if resultCount != 2 {
		t.Fatalf("expected 2 result events, got %d", resultCount)
	}

	// Verify both turns have system/init events (each turn is a fresh process).
	initCount := 0
	for _, line := range allLines {
		if strings.Contains(line, `"type":"system"`) {
			initCount++
		}
	}
	if initCount != 2 {
		t.Fatalf("expected 2 system/init events, got %d", initCount)
	}

	// Verify session_id is preserved across turns.
	var sessionIDs []string
	for _, line := range allLines {
		var ev struct {
			Type      string `json:"type"`
			SessionID string `json:"session_id"`
		}
		if json.Unmarshal([]byte(line), &ev) == nil && ev.Type == "system" && ev.SessionID != "" {
			sessionIDs = append(sessionIDs, ev.SessionID)
		}
	}

	if len(sessionIDs) != 2 {
		t.Fatalf("expected 2 session_ids, got %d: %v", len(sessionIDs), sessionIDs)
	}
	if sessionIDs[0] != sessionIDs[1] {
		t.Errorf("session_id not preserved across turns: %q != %q", sessionIDs[0], sessionIDs[1])
	}

	// Session_id preservation across turns proves --resume was used:
	// cursor-agent only returns the same session_id on a resumed session.
	// The wrapper passes --resume <session_id> on subsequent turns, which
	// is how the session_id remains consistent.

	t.Logf("E2E multi-turn: session_id=%s, %d events total", sessionIDs[0], len(allLines))
}

// --- E2E test: Log file creation ---

func TestE2E_LogFileCreated(t *testing.T) {
	skipIfNoAgent(t)

	logDir := t.TempDir()

	cmd := exec.Command(wrapperBin,
		"-p",
		"--idle-timeout", "60s",
		"--tool-grace", "30s",
		"--tick-interval", "5s",
		"--log-dir", logDir,
		"--output-format", "stream-json",
		"say hi",
	)

	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard

	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start cursor-wrap: %v", err)
	}
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("cursor-wrap exited with error: %v", err)
		}
	case <-time.After(e2eTimeout):
		cmd.Process.Kill()
		t.Fatal("cursor-wrap timed out")
	}

	// Find the log file.
	entries, err := os.ReadDir(logDir)
	if err != nil {
		t.Fatalf("reading log dir: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no log files created")
	}

	logPath := filepath.Join(logDir, entries[0].Name())
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("reading log file: %v", err)
	}

	logContent := string(logData)
	logLines := nonEmptyLines(logContent)
	if len(logLines) == 0 {
		t.Fatal("log file is empty")
	}

	// Verify log file contains raw event records with recv_ts.
	var rawEventCount int
	for _, line := range logLines {
		var record map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			continue // skip non-JSON lines
		}

		var msg string
		if msgRaw, ok := record["msg"]; ok {
			json.Unmarshal(msgRaw, &msg)
		}

		if msg == "raw_event" {
			rawEventCount++
			if _, ok := record["recv_ts"]; !ok {
				t.Error("raw_event record missing recv_ts")
			}
			rawField, ok := record["raw"]
			if !ok {
				t.Error("raw_event record missing raw field")
			}
			if !json.Valid(rawField) {
				t.Errorf("raw field is not valid JSON: %s", rawField)
			}
		}
	}

	if rawEventCount == 0 {
		t.Error("no raw_event records found in log file")
	}

	// Verify log file contains wrapper decision records.
	var hasWrapperDecision bool
	for _, line := range logLines {
		// Look for any non-raw_event log entry (wrapper decisions like
		// session_started, cursor-agent exited, etc.)
		var record map[string]json.RawMessage
		if json.Unmarshal([]byte(line), &record) != nil {
			continue
		}
		var msg string
		if msgRaw, ok := record["msg"]; ok {
			json.Unmarshal(msgRaw, &msg)
		}
		if msg != "raw_event" && msg != "" {
			hasWrapperDecision = true
			break
		}
	}
	if !hasWrapperDecision {
		t.Error("no wrapper decision records found in log file")
	}

	// Verify log file was renamed with session_id.
	logFileName := entries[0].Name()
	if strings.Contains(logFileName, "unknown") {
		t.Errorf("log file still has 'unknown' placeholder: %s", logFileName)
	}

	t.Logf("E2E log file: %s (%d lines, %d raw events)", logFileName, len(logLines), rawEventCount)
}
