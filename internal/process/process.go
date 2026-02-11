package process

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// Config holds the arguments needed to start cursor-agent.
type Config struct {
	AgentBin   string   // path to cursor-agent binary
	Prompt     string   // the user prompt
	Model      string   // model flag value
	Workspace  string   // --workspace path
	ExtraFlags []string // any additional flags to pass through
	Force      bool     // --force flag
	SessionID  string   // non-empty to resume a previous session via --resume
}

// Session represents a running cursor-agent process.
// Stdin is not exposed — it is written and closed during Start().
type Session struct {
	Stdout io.ReadCloser
	Stderr io.ReadCloser
	Cmd    *exec.Cmd
}

// Start spawns cursor-agent and returns handles to its I/O and process.
// The prompt is written to stdin and stdin is closed before returning.
func Start(ctx context.Context, cfg Config) (*Session, error) {
	cmd := exec.CommandContext(ctx, cfg.AgentBin, buildArgs(cfg)...)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting cursor-agent: %w", err)
	}

	// Write prompt and close stdin. cursor-agent reads stdin to EOF
	// to capture the prompt. If stdin is not closed, the agent hangs
	// waiting for more input — which would look like an agent hang
	// to the monitor.
	if _, err := io.WriteString(stdin, cfg.Prompt); err != nil {
		// Best-effort kill; process may not have read anything yet.
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("writing prompt to stdin: %w", err)
	}
	if err := stdin.Close(); err != nil {
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("closing stdin: %w", err)
	}

	return &Session{Stdout: stdout, Stderr: stderr, Cmd: cmd}, nil
}

// killGrace is the time to wait after SIGTERM before sending SIGKILL.
const killGrace = 5 * time.Second

// Kill sends SIGTERM to the process, waits briefly, then sends SIGKILL
// if the process has not exited. The reason is for logging only.
//
// Kill only sends signals — it does not wait for the process to exit.
// The caller must still call Wait() to collect the process state.
func (s *Session) Kill(reason string) error {
	if s.Cmd.Process == nil {
		return nil
	}

	// Send SIGTERM for graceful shutdown.
	if err := s.Cmd.Process.Signal(syscall.SIGTERM); err != nil {
		// Process may already be dead — not an error.
		return nil
	}

	// Poll briefly to see if SIGTERM was enough. We use a goroutine
	// with Process.Signal(0) to probe liveness, avoiding a race with
	// cmd.Wait() which the caller uses to collect the process state.
	done := make(chan struct{})
	go func() {
		deadline := time.After(killGrace)
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-deadline:
				close(done)
				return
			case <-ticker.C:
				// Signal(0) returns an error if the process has exited.
				if err := s.Cmd.Process.Signal(syscall.Signal(0)); err != nil {
					close(done)
					return
				}
			}
		}
	}()
	<-done

	// Check if process is still alive after the grace period.
	if err := s.Cmd.Process.Signal(syscall.Signal(0)); err != nil {
		// Process has exited — SIGTERM was sufficient.
		return nil
	}

	// Process did not exit after SIGTERM — escalate to SIGKILL.
	if err := s.Cmd.Process.Kill(); err != nil {
		// Process may have exited between the check and the kill.
		return nil
	}
	return nil
}

// Wait blocks until the process exits and returns its status.
func (s *Session) Wait() (*os.ProcessState, error) {
	err := s.Cmd.Wait()
	return s.Cmd.ProcessState, err
}

// buildArgs constructs the cursor-agent argument list from the config.
func buildArgs(cfg Config) []string {
	args := []string{"--print", "--output-format", "stream-json"}
	if cfg.SessionID != "" {
		args = append(args, "--resume", cfg.SessionID)
	}
	if cfg.Force {
		args = append(args, "--force")
	}
	if cfg.Model != "" {
		args = append(args, "--model", cfg.Model)
	}
	if cfg.Workspace != "" {
		args = append(args, "--workspace", cfg.Workspace)
	}
	args = append(args, cfg.ExtraFlags...)
	return args
}
