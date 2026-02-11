package format

import (
	"io"

	"cursor-wrap/internal/events"
	"cursor-wrap/internal/monitor"
)

// Formatter renders cursor-agent events to the wrapper's stdout.
type Formatter interface {
	// WriteEvent renders a single event. Called for every event in the
	// stream, in order. The formatter decides what to display.
	WriteEvent(ev events.AnnotatedEvent) error

	// WriteHangIndicator renders a hang detection message inline.
	// Called by the session loop when a hang is detected in interactive mode.
	WriteHangIndicator(reason monitor.Reason) error

	// Flush is called after each turn completes (result event received
	// or stream ended). The formatter can write separators or finalize
	// buffered output.
	Flush() error
}

// New creates a formatter for the given format name.
// Supported formats: "stream-json", "text".
// Panics on unknown format name (caller validates before calling).
func New(format string, w io.Writer) Formatter {
	switch format {
	case "stream-json":
		return &streamJSON{w: w}
	case "text":
		return &text{w: w}
	default:
		panic("unknown format: " + format)
	}
}
