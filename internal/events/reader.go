package events

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"time"
)

// Reader reads from an io.Reader and emits AnnotatedEvents on a channel.
// It closes the out channel when the reader hits EOF or the context is
// cancelled, signaling downstream that the stream is done. Any fatal
// read error (not EOF, not context cancellation) is sent on errCh
// before closing out.
func Reader(ctx context.Context, r io.Reader, out chan<- AnnotatedEvent, errCh chan<- error) {
	defer close(out)

	scanner := bufio.NewScanner(r)
	// Increase max line size to handle large JSON events (e.g. tool results).
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return
		default:
		}

		now := time.Now()

		// Copy the raw bytes — scanner reuses its buffer.
		line := make([]byte, len(scanner.Bytes()))
		copy(line, scanner.Bytes())

		var parsed RawEvent
		if err := json.Unmarshal(line, &parsed); err != nil {
			// Non-JSON line (e.g. "T: Named models unavailable") — skip gracefully.
			slog.Warn("skipping non-JSON line", "line", string(line), "error", err)
			continue
		}
		parsed.Line = line

		ev := AnnotatedEvent{
			RecvTime: now,
			Raw:      line,
			Parsed:   parsed,
		}

		select {
		case out <- ev:
		case <-ctx.Done():
			return
		}
	}

	if err := scanner.Err(); err != nil {
		// Fatal read error (e.g. broken pipe). Not EOF, not context cancellation.
		if ctx.Err() == nil {
			select {
			case errCh <- err:
			default:
			}
		}
	}
}
