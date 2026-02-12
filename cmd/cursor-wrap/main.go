package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"cursor-wrap/internal/events"
	"cursor-wrap/internal/format"
	"cursor-wrap/internal/logger"
	"cursor-wrap/internal/monitor"
	"cursor-wrap/internal/process"
)

var (
	ErrHangDetected = errors.New("hang detected")
	ErrAbnormalExit = errors.New("abnormal exit")
)

// TurnResult is returned by runTurn to communicate outcome to the session loop.
type TurnResult struct {
	SessionID string         // from system/init event
	Err       error          // nil on normal completion
	Reason    monitor.Reason // populated when Err is ErrHangDetected
}

// isTerminal reports whether the given file descriptor is connected to a terminal.
// This is a variable so tests can override it.
var isTerminal = func(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg := parseFlags(os.Args[1:])
	if err := run(ctx, cfg); err != nil {
		slog.Error("fatal", "error", err)
		if errors.Is(err, ErrHangDetected) {
			os.Exit(2)
		}
		os.Exit(1)
	}
}

func run(ctx context.Context, cfg Config) error {
	log, teardown := logger.Setup(cfg.Log)
	defer func() {
		if err := teardown(); err != nil {
			slog.Warn("log teardown failed", "error", err)
		}
	}()

	fmtr := format.New(cfg.OutputFormat, os.Stdout)

	prompt, err := firstPrompt(cfg)
	if err != nil {
		return fmt.Errorf("reading prompt: %w", err)
	}

	if cfg.Print && cfg.PromptAfterHang != "" {
		log.Warn("--prompt-after-hang has no effect in -p (print) mode")
	}

	sessionID := cfg.Process.SessionID // pre-seeded if --resume was passed
	hangRetries := 0
	const maxHangRetries = 3
	for {
		// Value copy of process.Config. Safe because the loop only sets
		// Prompt and SessionID (both strings). ExtraFlags is a shared
		// slice but is never mutated after parseFlags returns.
		procCfg := cfg.Process
		procCfg.Prompt = prompt
		procCfg.SessionID = sessionID // empty on first turn

		result := runTurn(ctx, procCfg, fmtr, log, cfg)

		if result.SessionID != "" && sessionID == "" {
			sessionID = result.SessionID
			log.Info("session started", "session_id", sessionID)
			log.SetSessionID(sessionID)
		}

		if result.Err != nil {
			if cfg.Print {
				// Non-interactive: exit on any error.
				return result.Err
			}
			// Interactive: only hangs are recoverable.
			if errors.Is(result.Err, ErrHangDetected) {
				fmtr.WriteHangIndicator(result.Reason)
				if cfg.PromptAfterHang != "" {
					hangRetries++
					if hangRetries > maxHangRetries {
						log.Error("max hang retries exceeded", "retries", hangRetries)
						return result.Err
					}
					prompt = cfg.PromptAfterHang
					log.Info("using prompt-after-hang", "prompt", prompt, "retry", hangRetries)
					continue
				}
				log.Warn("hang detected, awaiting next prompt")
			} else {
				return result.Err // non-recoverable errors exit even in interactive mode
			}
		}

		if cfg.Print {
			break // single turn in non-interactive mode
		}

		prompt, err = readPrompt(cfg.PromptReader)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil // clean exit on stdin EOF / Ctrl+D
			}
			return fmt.Errorf("reading prompt: %w", err)
		}
	}
	return nil
}

func runTurn(ctx context.Context, procCfg process.Config, fmtr format.Formatter, log *logger.LogSession, cfg Config) TurnResult {
	sess, err := process.Start(ctx, procCfg)
	if err != nil {
		return TurnResult{Err: err}
	}

	eventCh := make(chan events.AnnotatedEvent, 64)
	readerErrCh := make(chan error, 1)
	mon := monitor.NewMonitor(cfg.IdleTimeout, cfg.ToolGrace)

	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		events.Reader(ctx, sess.Stdout, eventCh, readerErrCh)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		drainStderr(ctx, sess.Stderr, log)
	}()

	ticker := time.NewTicker(cfg.TickInterval)
	defer ticker.Stop()

	var runErr error
	streamDone := false
	for runErr == nil && !streamDone {
		select {
		case ev, ok := <-eventCh:
			if !ok {
				runErr = handleStreamEnd(sess, mon, log)
				streamDone = true
			} else {
				logRawEvent(log, ev)
				if err := fmtr.WriteEvent(ev); err != nil {
					log.Warn("formatter write error", "error", err)
				}
				verdict := mon.ProcessEvent(ev)
				logVerdict(log, verdict, ev)
			}

		case err := <-readerErrCh:
			log.Error("event reader failed", "error", err)
			_ = sess.Kill("reader error")
			runErr = fmt.Errorf("event reader: %w", err)

		case <-ticker.C:
			verdict, reason := mon.CheckTimeout(mon.Now())
			if verdict == monitor.VerdictHang {
				log.Error("hang detected", reasonAttrs(reason)...)
				_ = sess.Kill(reason.String())
				wg.Wait()
				fmtr.Flush()
				return TurnResult{SessionID: mon.SessionID(), Err: ErrHangDetected, Reason: reason}
			}

		case <-ctx.Done():
			_ = sess.Kill("context cancelled")
			runErr = ctx.Err()
		}
	}

	wg.Wait()
	fmtr.Flush()
	return TurnResult{SessionID: mon.SessionID(), Err: runErr}
}

// firstPrompt resolves the initial prompt from the available sources.
// Precedence: positional arg > stdin.
// In -p mode with no positional arg, stdin is read to EOF (pipe mode).
// In interactive mode with no positional arg, the first stdin line is used.
func firstPrompt(cfg Config) (string, error) {
	if cfg.PositionalPrompt != "" {
		return cfg.PositionalPrompt, nil
	}
	if cfg.Print {
		// Non-interactive with no positional arg: require piped stdin.
		if isTerminal(os.Stdin) {
			return "", fmt.Errorf("no prompt provided (use a positional arg or pipe stdin)")
		}
		// Read all of stdin as a single prompt.
		data, err := io.ReadAll(cfg.PromptReader)
		if err != nil {
			return "", fmt.Errorf("reading stdin: %w", err)
		}
		prompt := strings.TrimSpace(string(data))
		if prompt == "" {
			return "", fmt.Errorf("no prompt provided")
		}
		return prompt, nil
	}
	// Interactive: read first line from stdin.
	return readPrompt(cfg.PromptReader)
}

// readPrompt reads the next non-empty prompt from the given reader.
// In interactive mode with a TTY, writes a prompt indicator to stderr first.
// Returns io.EOF when the input is exhausted. Skips blank lines.
func readPrompt(r *bufio.Reader) (string, error) {
	for {
		if isTerminal(os.Stdin) {
			fmt.Fprint(os.Stderr, "> ")
		}
		line, err := r.ReadString('\n')
		if err != nil && err != io.EOF {
			return "", err
		}
		prompt := strings.TrimSpace(line)
		if prompt != "" {
			return prompt, nil
		}
		if err == io.EOF {
			return "", io.EOF
		}
		// Empty line: skip and read again.
	}
}

// handleStreamEnd is called when the event channel closes (stdout EOF).
// This means cursor-agent's stdout pipe is closed — the process is exiting
// or has exited.
func handleStreamEnd(sess *process.Session, mon *monitor.Monitor, log *logger.LogSession) error {
	ps, err := sess.Wait()
	if err != nil {
		log.Error("process wait failed", "error", err)
		// ps may be nil on wait failure — log what we can and treat as abnormal.
		return fmt.Errorf("waiting for cursor-agent: %w", err)
	}
	exitCode := ps.ExitCode()
	log.Info("cursor-agent exited", "exit_code", exitCode, "session_done", mon.SessionDone())

	if mon.SessionDone() {
		return nil
	}
	return fmt.Errorf("cursor-agent exited (code %d) without emitting a result event: %w",
		exitCode, ErrAbnormalExit)
}

// drainStderr reads and discards stderr, logging each line at debug level.
// This prevents the child process from blocking on a full stderr pipe buffer.
// The context check inside the loop ensures prompt exit on cancellation,
// even if the stderr pipe hasn't closed yet (belt-and-suspenders with
// sess.Kill closing the pipe).
func drainStderr(ctx context.Context, r io.Reader, log *logger.LogSession) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return
		default:
		}
		log.Debug("stderr", "line", scanner.Text())
	}
	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		log.Warn("stderr read error", "error", err)
	}
}

// logRawEvent writes a raw event capture record to the file sink.
// This is the forensic replay record — it writes synchronously to the
// O_SYNC file before any further processing, ensuring the event is
// persisted even if the wrapper crashes immediately after.
func logRawEvent(log *logger.LogSession, ev events.AnnotatedEvent) {
	log.Debug("raw_event",
		"recv_ts", ev.RecvTime.UnixMilli(),
		slog.Any("raw", json.RawMessage(ev.Raw)),
	)
}

// logVerdict logs the monitor's verdict for non-OK results.
// VerdictWaiting is logged at debug level (expected during tool execution).
// VerdictOK is not logged (too noisy for every event).
func logVerdict(log *logger.LogSession, v monitor.Verdict, ev events.AnnotatedEvent) {
	if v == monitor.VerdictWaiting {
		log.Debug("verdict_waiting", "event_type", ev.Parsed.Type)
	}
}

// reasonAttrs converts a Reason into slog key-value pairs for structured logging.
func reasonAttrs(r monitor.Reason) []any {
	attrs := []any{
		"idle_silence_ms", r.IdleSilenceMS,
		"open_call_count", r.OpenCallCount,
		"last_event_type", r.LastEventType,
	}
	for i, c := range r.OpenCalls {
		prefix := fmt.Sprintf("open_call_%d", i)
		attrs = append(attrs,
			prefix+"_id", c.CallID,
			prefix+"_command", c.Command,
			prefix+"_elapsed_ms", c.ElapsedMS,
			prefix+"_timeout_ms", c.TimeoutMS,
		)
	}
	return attrs
}
