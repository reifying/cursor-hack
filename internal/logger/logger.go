package logger

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// LogConfig holds configuration for the dual-sink logger.
type LogConfig struct {
	Dir          string     // directory for log files
	ConsoleLevel slog.Level // minimum level for console output
	FileLevel    slog.Level // minimum level for file output (typically debug)
}

// LogSession wraps *slog.Logger and holds a reference to the file sink,
// enabling the log file to be renamed once the session_id is known.
type LogSession struct {
	*slog.Logger
	filePath   string
	sessionSet bool
	mu         sync.Mutex // protects filePath and sessionSet
}

// Setup initializes the dual-sink logger and returns a LogSession.
// The teardown function flushes and closes the file sink.
// If setup fails to create the log directory or file, it falls back
// to console-only logging and logs a warning.
func Setup(cfg LogConfig) (*LogSession, func() error) {
	dir := cfg.Dir
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = "."
		}
		dir = filepath.Join(home, ".cursor-wrap", "logs")
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		// Fall back to console-only if we can't create the directory.
		slog.Warn("failed to create log directory, using console only", "dir", dir, "error", err)
		ls := &LogSession{
			Logger: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
				Level: cfg.ConsoleLevel,
			})),
		}
		return ls, func() error { return nil }
	}

	startTS := time.Now().UnixMilli()
	filename := fmt.Sprintf("cursor-wrap-%d-unknown.jsonl", startTS)
	filePath := filepath.Join(dir, filename)

	f, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND|os.O_SYNC, 0o644)
	if err != nil {
		slog.Warn("failed to open log file, using console only", "path", filePath, "error", err)
		ls := &LogSession{
			Logger: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
				Level: cfg.ConsoleLevel,
			})),
		}
		return ls, func() error { return nil }
	}

	fileHandler := slog.NewJSONHandler(f, &slog.HandlerOptions{
		Level:       cfg.FileLevel,
		ReplaceAttr: replaceTimeAttr,
	})

	consoleHandler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: cfg.ConsoleLevel,
	})

	multi := &multiHandler{
		handlers: []slog.Handler{fileHandler, consoleHandler},
	}

	ls := &LogSession{
		Logger:   slog.New(multi),
		filePath: filePath,
	}

	teardown := func() error {
		return f.Close()
	}

	return ls, teardown
}

// SetSessionID renames the log file to incorporate the session_id.
// Called once after the first system/init event is received.
// No-op if session_id was already set or if the rename fails (logged at warn).
func (ls *LogSession) SetSessionID(id string) {
	ls.mu.Lock()
	defer ls.mu.Unlock()

	if ls.sessionSet || ls.filePath == "" {
		return
	}

	dir := filepath.Dir(ls.filePath)
	base := filepath.Base(ls.filePath)

	// Replace "unknown" with the session_id in the filename.
	newBase := strings.Replace(base, "-unknown.jsonl", "-"+id+".jsonl", 1)
	if newBase == base {
		// Replacement didn't happen — unexpected filename format.
		return
	}

	newPath := filepath.Join(dir, newBase)
	if err := os.Rename(ls.filePath, newPath); err != nil {
		ls.Logger.Warn("failed to rename log file", "old", ls.filePath, "new", newPath, "error", err)
		return
	}

	ls.filePath = newPath
	ls.sessionSet = true
}

// FilePath returns the current path of the log file.
// Returns an empty string if no file sink is configured.
func (ls *LogSession) FilePath() string {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	return ls.filePath
}

// replaceTimeAttr serializes the time field as Unix milliseconds
// to match cursor-agent's timestamp_ms convention.
func replaceTimeAttr(groups []string, a slog.Attr) slog.Attr {
	if a.Key == slog.TimeKey {
		if t, ok := a.Value.Any().(time.Time); ok {
			a.Value = slog.Int64Value(t.UnixMilli())
		}
	}
	return a
}

// multiHandler fans out log records to multiple slog.Handlers.
type multiHandler struct {
	handlers []slog.Handler
}

func (h *multiHandler) Enabled(_ context.Context, level slog.Level) bool {
	for _, handler := range h.handlers {
		if handler.Enabled(context.Background(), level) {
			return true
		}
	}
	return false
}

func (h *multiHandler) Handle(ctx context.Context, r slog.Record) error {
	for _, handler := range h.handlers {
		if handler.Enabled(ctx, r.Level) {
			if err := handler.Handle(ctx, r); err != nil {
				return err
			}
		}
	}
	return nil
}

func (h *multiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	handlers := make([]slog.Handler, len(h.handlers))
	for i, handler := range h.handlers {
		handlers[i] = handler.WithAttrs(attrs)
	}
	return &multiHandler{handlers: handlers}
}

func (h *multiHandler) WithGroup(name string) slog.Handler {
	handlers := make([]slog.Handler, len(h.handlers))
	for i, handler := range h.handlers {
		handlers[i] = handler.WithGroup(name)
	}
	return &multiHandler{handlers: handlers}
}
