package monitor

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"cursor-wrap/internal/events"
)

// Verdict represents the hang detection outcome.
type Verdict int

const (
	VerdictOK      Verdict = iota // Session completed or no anomaly
	VerdictWaiting                // Tools running, within deadlines
	VerdictHang                   // Hang detected
)

func (v Verdict) String() string {
	switch v {
	case VerdictOK:
		return "OK"
	case VerdictWaiting:
		return "Waiting"
	case VerdictHang:
		return "Hang"
	default:
		return fmt.Sprintf("Verdict(%d)", int(v))
	}
}

// OpenToolCall tracks an in-flight tool invocation.
type OpenToolCall struct {
	CallID      string
	ModelCallID string
	StartedAt   time.Time
	TimeoutMS   int64  // from tool args; 0 if unknown
	Command     string // shell command, empty for non-shell tools
}

// OpenCallDetail is a snapshot of an open tool call for diagnostic output.
type OpenCallDetail struct {
	CallID    string
	Command   string
	ElapsedMS int64
	TimeoutMS int64
}

// Reason provides diagnostic context for a verdict.
type Reason struct {
	IdleSilenceMS int64
	OpenCallCount int
	LastEventType string
	OpenCalls     []OpenCallDetail
}

// String formats a one-line human-readable summary.
func (r Reason) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "idle %dms, %d open calls, last event: %s", r.IdleSilenceMS, r.OpenCallCount, r.LastEventType)
	for _, oc := range r.OpenCalls {
		cmd := oc.Command
		if cmd == "" {
			cmd = "(non-shell)"
		}
		fmt.Fprintf(&b, " [%s %s elapsed=%dms timeout=%dms]", oc.CallID, cmd, oc.ElapsedMS, oc.TimeoutMS)
	}
	return b.String()
}

// Clock abstracts time for testing.
type Clock interface {
	Now() time.Time
}

// realClock uses the system clock.
type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// Option configures a Monitor.
type Option func(*Monitor)

// WithClock injects a custom clock for testing.
func WithClock(c Clock) Option {
	return func(m *Monitor) {
		m.clock = c
	}
}

// State is the hang monitor's internal state.
type State struct {
	OpenCalls   map[string]*OpenToolCall // keyed by call_id
	LastEventAt time.Time               // wall-clock time of last event received
	LastEvType  string                  // "type" or "type/subtype"
	SessionDone bool                    // true after result event
	SessionID   string                  // from system/init
}

// Monitor is the hang detection state machine. It consumes annotated events,
// tracks open tool calls, and produces verdicts on timer ticks.
type Monitor struct {
	clock       Clock
	idleTimeout time.Duration
	toolGrace   time.Duration
	state       State
}

// NewMonitor creates a Monitor with the given thresholds.
func NewMonitor(idleTimeout, toolGrace time.Duration, opts ...Option) *Monitor {
	m := &Monitor{
		clock:       realClock{},
		idleTimeout: idleTimeout,
		toolGrace:   toolGrace,
		state: State{
			OpenCalls: make(map[string]*OpenToolCall),
		},
	}
	for _, o := range opts {
		o(m)
	}
	m.state.LastEventAt = m.clock.Now()
	return m
}

// ProcessEvent updates state based on an incoming event.
// Returns VerdictOK or VerdictWaiting. Never returns VerdictHang
// synchronously — hangs are detected by CheckTimeout.
func (m *Monitor) ProcessEvent(ev events.AnnotatedEvent) Verdict {
	m.state.LastEventAt = ev.RecvTime

	evType := ev.Parsed.Type
	if ev.Parsed.Subtype != "" {
		evType = ev.Parsed.Type + "/" + ev.Parsed.Subtype
	}
	m.state.LastEvType = evType

	switch ev.Parsed.Type {
	case "system":
		if ev.Parsed.Subtype == "init" {
			var init events.SystemInit
			if err := json.Unmarshal(ev.Raw, &init); err == nil {
				m.state.SessionID = init.SessionID
			}
		}
	case "tool_call":
		switch ev.Parsed.Subtype {
		case "started":
			var started events.ToolCallStarted
			if err := json.Unmarshal(ev.Raw, &started); err == nil {
				oc := &OpenToolCall{
					CallID:      started.CallID,
					ModelCallID: started.ModelCallID,
					StartedAt:   ev.RecvTime,
				}
				// Try to extract shell tool args for timeout and command.
				info, err := events.ParseToolCallInfo(started.ToolCall)
				if err == nil && info.ToolType == "shellToolCall" {
					oc.TimeoutMS = info.TimeoutMS
					oc.Command = info.Command
				}
				m.state.OpenCalls[started.CallID] = oc
			}
		case "completed":
			var completed events.ToolCallCompleted
			if err := json.Unmarshal(ev.Raw, &completed); err == nil {
				delete(m.state.OpenCalls, completed.CallID)
			}
		}
	case "result":
		m.state.SessionDone = true
	}

	if len(m.state.OpenCalls) > 0 {
		return VerdictWaiting
	}
	return VerdictOK
}

// CheckTimeout evaluates the current state and returns a verdict with reason.
// Called periodically by the orchestrator on a timer tick.
func (m *Monitor) CheckTimeout(now time.Time) (Verdict, Reason) {
	idleElapsed := now.Sub(m.state.LastEventAt)
	idleMS := idleElapsed.Milliseconds()

	reason := Reason{
		IdleSilenceMS: idleMS,
		OpenCallCount: len(m.state.OpenCalls),
		LastEventType: m.state.LastEvType,
	}

	if m.state.SessionDone {
		return VerdictOK, reason
	}

	if len(m.state.OpenCalls) == 0 {
		if idleElapsed > m.idleTimeout {
			return VerdictHang, reason
		}
		return VerdictOK, reason
	}

	// Tools running — check each against its own deadline.
	allExpired := true
	for _, tool := range m.state.OpenCalls {
		toolElapsed := now.Sub(tool.StartedAt)
		toolDeadline := time.Duration(tool.TimeoutMS)*time.Millisecond + m.toolGrace
		if tool.TimeoutMS == 0 {
			toolDeadline = m.idleTimeout
		}
		detail := OpenCallDetail{
			CallID:    tool.CallID,
			Command:   tool.Command,
			ElapsedMS: toolElapsed.Milliseconds(),
			TimeoutMS: tool.TimeoutMS,
		}
		reason.OpenCalls = append(reason.OpenCalls, detail)

		if toolElapsed <= toolDeadline {
			allExpired = false
		}
	}

	if allExpired {
		return VerdictHang, reason
	}
	return VerdictWaiting, reason
}

// Now returns the current time from the monitor's clock.
func (m *Monitor) Now() time.Time {
	return m.clock.Now()
}

// SessionDone reports whether a result event has been received.
func (m *Monitor) SessionDone() bool {
	return m.state.SessionDone
}

// SessionID returns the session_id captured from the system/init event.
func (m *Monitor) SessionID() string {
	return m.state.SessionID
}
