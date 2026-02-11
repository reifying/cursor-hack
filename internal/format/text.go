package format

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"

	"cursor-wrap/internal/events"
	"cursor-wrap/internal/monitor"
)

// text renders a human-readable view of the agent's activity.
// This is the default format for interactive mode.
type text struct {
	w io.Writer
}

func (f *text) WriteEvent(ev events.AnnotatedEvent) error {
	switch ev.Parsed.Type {
	case "assistant":
		return f.writeAssistant(ev)
	case "tool_call":
		switch ev.Parsed.Subtype {
		case "started":
			return f.writeToolCallStarted(ev)
		case "completed":
			return f.writeToolCallCompleted(ev)
		}
	}
	// Silent: system/init, user, thinking/delta, thinking/completed,
	// result, and unknown event types.
	return nil
}

func (f *text) writeAssistant(ev events.AnnotatedEvent) error {
	msg, err := events.ParseAssistantMessage(ev.Raw)
	if err != nil {
		slog.Debug("text formatter: skipping assistant event", "error", err)
		return nil
	}
	_, err = fmt.Fprintf(f.w, "%s\n", msg.Text)
	return err
}

func (f *text) writeToolCallStarted(ev events.AnnotatedEvent) error {
	var started events.ToolCallStarted
	if err := json.Unmarshal(ev.Raw, &started); err != nil {
		slog.Debug("text formatter: skipping tool_call/started event", "error", err)
		return nil
	}

	info, err := events.ParseToolCallInfo(started.ToolCall)
	if err != nil {
		slog.Debug("text formatter: skipping tool_call/started event", "error", err)
		return nil
	}

	if info.ToolType == "shellToolCall" {
		_, err = fmt.Fprintf(f.w, "⏳ `%s`\n", info.Command)
	} else if args := toolCallArgs(info); args != "" {
		_, err = fmt.Fprintf(f.w, "⏳ %s: %s\n", info.ToolType, args)
	} else {
		_, err = fmt.Fprintf(f.w, "⏳ %s\n", info.ToolType)
	}
	return err
}

func (f *text) writeToolCallCompleted(ev events.AnnotatedEvent) error {
	var completed events.ToolCallCompleted
	if err := json.Unmarshal(ev.Raw, &completed); err != nil {
		slog.Debug("text formatter: skipping tool_call/completed event", "error", err)
		return nil
	}

	info, err := events.ParseToolCallInfo(completed.ToolCall)
	if err != nil {
		slog.Debug("text formatter: skipping tool_call/completed event", "error", err)
		return nil
	}

	if info.ToolType == "shellToolCall" {
		result, err := events.ParseShellToolResult(completed.ToolCall)
		if err != nil {
			slog.Debug("text formatter: skipping shell result rendering", "error", err)
			return nil
		}
		seconds := float64(result.ExecutionTime) / 1000.0
		if result.ExitCode == 0 {
			_, err = fmt.Fprintf(f.w, "✓ `%s` (%.1fs, exit 0)\n", info.Command, seconds)
		} else {
			_, err = fmt.Fprintf(f.w, "✗ `%s` (%.1fs, exit %d)\n", info.Command, seconds, result.ExitCode)
		}
		return err
	}

	_, err = fmt.Fprintf(f.w, "✓ %s\n", info.ToolType)
	return err
}

// toolCallArgs returns a display-friendly summary of non-shell tool args.
func toolCallArgs(info events.ToolCallInfo) string {
	switch info.ToolType {
	case "lsToolCall":
		return info.Path
	default:
		return ""
	}
}

func (f *text) WriteHangIndicator(reason monitor.Reason) error {
	_, err := fmt.Fprintf(f.w, "⚠ Hang detected — killed cursor-agent (%s)\n", reason.String())
	return err
}

func (f *text) Flush() error {
	// Write a blank line to visually separate turns in interactive mode.
	_, err := f.w.Write([]byte("\n"))
	return err
}
