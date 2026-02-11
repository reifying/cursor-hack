package format

import (
	"fmt"
	"io"

	"cursor-wrap/internal/events"
	"cursor-wrap/internal/monitor"
)

// streamJSON is a transparent passthrough formatter — writes the raw JSON
// line plus a newline. With this formatter, cursor-agent events on the
// wrapper's stdout are byte-identical to cursor-agent's stdout.
type streamJSON struct {
	w io.Writer
}

func (f *streamJSON) WriteEvent(ev events.AnnotatedEvent) error {
	if _, err := f.w.Write(ev.Raw); err != nil {
		return err
	}
	_, err := f.w.Write([]byte("\n"))
	return err
}

func (f *streamJSON) WriteHangIndicator(reason monitor.Reason) error {
	msg := fmt.Sprintf(`{"type":"wrapper","subtype":"hang_detected","message":%q}`+"\n",
		reason.String())
	_, err := io.WriteString(f.w, msg)
	return err
}

func (f *streamJSON) Flush() error { return nil }
