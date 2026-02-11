package events

import (
	"encoding/json"
	"time"
)

// RawEvent is the first-pass parse of every JSON line. Only the
// discriminator fields are decoded; the full line is retained verbatim.
type RawEvent struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype,omitempty"`
	Line    []byte `json:"-"` // original JSON bytes, unparsed
}

// AnnotatedEvent wraps a parsed event with the wrapper's receive timestamp.
type AnnotatedEvent struct {
	RecvTime time.Time
	Raw      []byte   // verbatim JSON line
	Parsed   RawEvent // first-pass parse (type + subtype)
}

// SystemInit is the "system"/"init" event.
type SystemInit struct {
	SessionID      string `json:"session_id"`
	Model          string `json:"model"`
	CWD            string `json:"cwd"`
	PermissionMode string `json:"permissionMode"`
}

// ToolCallStarted is emitted when a tool begins execution.
type ToolCallStarted struct {
	CallID      string          `json:"call_id"`
	ModelCallID string          `json:"model_call_id"`
	TimestampMS int64           `json:"timestamp_ms"`
	ToolCall    json.RawMessage `json:"tool_call"`
}

// ShellToolArgs holds the fields we need from shellToolCall.args.
type ShellToolArgs struct {
	Command      string `json:"command"`
	Timeout      int64  `json:"timeout"` // ms
	IsBackground bool   `json:"isBackground"`
}

// ToolCallCompleted is emitted when a tool finishes.
type ToolCallCompleted struct {
	CallID      string          `json:"call_id"`
	ModelCallID string          `json:"model_call_id"`
	TimestampMS int64           `json:"timestamp_ms"`
	ToolCall    json.RawMessage `json:"tool_call"`
}

// Result is the terminal event.
type Result struct {
	Subtype    string `json:"subtype"`
	DurationMS int64  `json:"duration_ms"`
	IsError    bool   `json:"is_error"`
	SessionID  string `json:"session_id"`
	RequestID  string `json:"request_id"`
}
