package process

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeScript creates an executable shell script in the given directory and
// returns its path. The script ignores all arguments (as cursor-agent would
// handle unknown wrapper-injected flags gracefully).
func writeScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	content := "#!/bin/sh\n" + body + "\n"
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("writing test script: %v", err)
	}
	return path
}

func TestBuildArgs_Basic(t *testing.T) {
	cfg := Config{
		AgentBin: "cursor-agent",
		Prompt:   "hello",
	}
	args := buildArgs(cfg)
	want := []string{"--print", "--output-format", "stream-json"}
	if len(args) != len(want) {
		t.Fatalf("got %v, want %v", args, want)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Errorf("args[%d] = %q, want %q", i, args[i], want[i])
		}
	}
}

func TestBuildArgs_WithResume(t *testing.T) {
	cfg := Config{
		AgentBin:  "cursor-agent",
		SessionID: "sess-abc-123",
	}
	args := buildArgs(cfg)
	found := false
	for i, a := range args {
		if a == "--resume" {
			if i+1 >= len(args) {
				t.Fatal("--resume is last arg, missing session ID")
			}
			if args[i+1] != "sess-abc-123" {
				t.Errorf("--resume value = %q, want %q", args[i+1], "sess-abc-123")
			}
			found = true
			break
		}
	}
	if !found {
		t.Errorf("--resume not found in args: %v", args)
	}
}

func TestBuildArgs_WithoutResume(t *testing.T) {
	cfg := Config{
		AgentBin: "cursor-agent",
	}
	args := buildArgs(cfg)
	for _, a := range args {
		if a == "--resume" {
			t.Errorf("--resume found in args without SessionID: %v", args)
		}
	}
}

func TestBuildArgs_AllFlags(t *testing.T) {
	cfg := Config{
		AgentBin:   "cursor-agent",
		SessionID:  "sess-123",
		Force:      true,
		Model:      "gpt-4",
		Workspace:  "/tmp/ws",
		ExtraFlags: []string{"--extra1", "--extra2"},
	}
	args := buildArgs(cfg)

	if args[0] != "--print" || args[1] != "--output-format" || args[2] != "stream-json" {
		t.Fatalf("base flags wrong: %v", args[:3])
	}

	joined := strings.Join(args, " ")
	for _, expected := range []string{
		"--resume sess-123",
		"--force",
		"--model gpt-4",
		"--workspace /tmp/ws",
		"--extra1",
		"--extra2",
	} {
		if !strings.Contains(joined, expected) {
			t.Errorf("missing %q in args: %s", expected, joined)
		}
	}
}

func TestBuildArgs_Order(t *testing.T) {
	cfg := Config{
		AgentBin:   "cursor-agent",
		SessionID:  "s1",
		Force:      true,
		Model:      "m1",
		Workspace:  "/ws",
		ExtraFlags: []string{"--x1"},
	}
	args := buildArgs(cfg)

	positions := make(map[string]int)
	for i, a := range args {
		switch a {
		case "--print", "--resume", "--force", "--model", "--workspace", "--x1":
			positions[a] = i
		}
	}

	order := []string{"--print", "--resume", "--force", "--model", "--workspace", "--x1"}
	for i := 1; i < len(order); i++ {
		prev, ok1 := positions[order[i-1]]
		curr, ok2 := positions[order[i]]
		if !ok1 || !ok2 {
			t.Fatalf("missing flag positions: %v", positions)
		}
		if prev >= curr {
			t.Errorf("%s (pos %d) should come before %s (pos %d)", order[i-1], prev, order[i], curr)
		}
	}
}

// Lifecycle tests below use helper scripts that ignore all arguments,
// simulating cursor-agent which accepts the flags buildArgs produces.

func TestStart_SpawnsProcessAndCapturesStdout(t *testing.T) {
	dir := t.TempDir()
	bin := writeScript(t, dir, "agent.sh", `echo hello_world`)

	sess, err := Start(context.Background(), Config{AgentBin: bin, Prompt: ""})
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	output, _ := io.ReadAll(sess.Stdout)
	if got := strings.TrimSpace(string(output)); got != "hello_world" {
		t.Errorf("stdout = %q, want %q", got, "hello_world")
	}

	ps, err := sess.Wait()
	if err != nil {
		t.Fatalf("Wait failed: %v", err)
	}
	if ps.ExitCode() != 0 {
		t.Errorf("exit code = %d, want 0", ps.ExitCode())
	}
}

func TestStart_WritesPromptToStdin(t *testing.T) {
	dir := t.TempDir()
	bin := writeScript(t, dir, "agent.sh", `cat`)

	sess, err := Start(context.Background(), Config{AgentBin: bin, Prompt: "hello from test"})
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	output, _ := io.ReadAll(sess.Stdout)
	if got := strings.TrimSpace(string(output)); got != "hello from test" {
		t.Errorf("cat output = %q, want %q", got, "hello from test")
	}

	_, err = sess.Wait()
	if err != nil {
		t.Fatalf("Wait failed: %v", err)
	}
}

func TestStart_ClosesStdinAfterWrite(t *testing.T) {
	dir := t.TempDir()
	// cat will exit once it reads EOF from stdin. If stdin is not closed,
	// this test would hang indefinitely.
	bin := writeScript(t, dir, "agent.sh", `cat`)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sess, err := Start(ctx, Config{AgentBin: bin, Prompt: "test"})
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	io.ReadAll(sess.Stdout)

	ps, err := sess.Wait()
	if err != nil {
		t.Fatalf("Wait failed: %v", err)
	}
	if ps.ExitCode() != 0 {
		t.Errorf("exit code = %d, want 0 (cat should have exited on stdin EOF)", ps.ExitCode())
	}
}

func TestStart_ErrorForNonExistentBinary(t *testing.T) {
	_, err := Start(context.Background(), Config{
		AgentBin: "/nonexistent/binary/that/does/not/exist",
		Prompt:   "test",
	})
	if err == nil {
		t.Fatal("expected error for non-existent binary, got nil")
	}
	if !strings.Contains(err.Error(), "starting cursor-agent") {
		t.Errorf("error = %q, expected it to mention 'starting cursor-agent'", err.Error())
	}
}

func TestWait_ReturnsCorrectExitCode(t *testing.T) {
	tests := []struct {
		name     string
		script   string
		wantCode int
	}{
		{name: "success", script: "exit 0", wantCode: 0},
		{name: "failure", script: "exit 42", wantCode: 42},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			bin := writeScript(t, dir, "agent.sh", tt.script)

			sess, err := Start(context.Background(), Config{AgentBin: bin, Prompt: ""})
			if err != nil {
				t.Fatalf("Start failed: %v", err)
			}

			ps, _ := sess.Wait()
			if ps.ExitCode() != tt.wantCode {
				t.Errorf("exit code = %d, want %d", ps.ExitCode(), tt.wantCode)
			}
		})
	}
}

func TestKill_SendsSIGTERM(t *testing.T) {
	dir := t.TempDir()
	bin := writeScript(t, dir, "agent.sh", `sleep 60`)

	sess, err := Start(context.Background(), Config{AgentBin: bin, Prompt: ""})
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	if err := sess.Kill("test"); err != nil {
		t.Fatalf("Kill failed: %v", err)
	}

	ps, _ := sess.Wait()
	if ps == nil {
		t.Fatal("ProcessState is nil after Kill + Wait")
	}
}

func TestKill_EscalatesToSIGKILL(t *testing.T) {
	dir := t.TempDir()
	// Script that traps SIGTERM and ignores it — requires SIGKILL.
	bin := writeScript(t, dir, "agent.sh", `
trap '' TERM
sleep 60
`)

	sess, err := Start(context.Background(), Config{AgentBin: bin, Prompt: ""})
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	done := make(chan error, 1)
	go func() {
		done <- sess.Kill("test escalation")
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Kill failed: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Kill did not return within 15s")
	}

	ps, _ := sess.Wait()
	if ps == nil {
		t.Fatal("ProcessState is nil after escalated Kill + Wait")
	}
}

func TestKill_AlreadyDeadProcess(t *testing.T) {
	dir := t.TempDir()
	bin := writeScript(t, dir, "agent.sh", `exit 0`)

	sess, err := Start(context.Background(), Config{AgentBin: bin, Prompt: ""})
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	sess.Wait()

	// Kill on an already-dead process should not error.
	if err := sess.Kill("already dead"); err != nil {
		t.Errorf("Kill on dead process returned error: %v", err)
	}
}

func TestStart_StderrCapture(t *testing.T) {
	dir := t.TempDir()
	bin := writeScript(t, dir, "agent.sh", `echo error_output >&2`)

	sess, err := Start(context.Background(), Config{AgentBin: bin, Prompt: ""})
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	output, _ := io.ReadAll(sess.Stderr)
	if got := strings.TrimSpace(string(output)); got != "error_output" {
		t.Errorf("stderr = %q, want %q", got, "error_output")
	}

	sess.Wait()
}

func TestStart_ContextCancellation(t *testing.T) {
	dir := t.TempDir()
	bin := writeScript(t, dir, "agent.sh", `sleep 60`)

	ctx, cancel := context.WithCancel(context.Background())

	sess, err := Start(ctx, Config{AgentBin: bin, Prompt: ""})
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	cancel()

	_, err = sess.Wait()
	if err == nil {
		t.Error("expected error from Wait after context cancellation, got nil")
	}
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
