# Go Coding Standards

Coding standards for the cursor-agent CLI wrapper. This project reads streamed JSON from a child process, tracks event-driven state, detects hangs, and manages process lifecycle. These standards reflect that context.

## Project Layout

Follow standard Go project layout:

```
cmd/
└── cursor-wrap/
    └── main.go            # Entry point only — parse flags, wire dependencies, run

internal/
├── events/                # JSON event types and parser for stream-json output
├── format/                # Output formatting — stream-json passthrough or text rendering
├── monitor/               # Hang detection state machine
├── process/               # Child process spawn, kill, wait
└── logger/                # Structured logging setup
```

No `pkg/` directory. Everything is internal. The `cmd/` main package is thin: it parses flags, constructs dependencies, and calls into `internal/` packages. Business logic never lives in `main.go`.

## Error Handling

Wrap errors with context at every call site:

```go
stream, err := startProcess(ctx, args)
if err != nil {
    return fmt.Errorf("starting cursor-agent: %w", err)
}
```

Never silently swallow errors. If an error is intentionally ignored, it must have a comment explaining why:

```go
// Best-effort cleanup; the process may already be dead.
_ = cmd.Process.Kill()
```

Use sentinel errors for expected conditions that callers need to distinguish:

```go
var (
    ErrHangDetected = errors.New("hang detected")
    ErrProcessExited = errors.New("child process exited")
)
```

Check sentinel errors with `errors.Is`, not string comparison.

## Logging

All operational logging goes through a structured JSON logger. Use `slog` from the standard library:

```go
slog.Info("event received",
    "type", ev.Type,
    "tool_call_id", ev.ToolCallID,
)
```

Prohibited for operational output:
- `fmt.Println`, `fmt.Printf`
- `log.Println`, `log.Printf`

These write unstructured text to stderr and are invisible to log aggregation. The one exception is `fmt.Printf`-style output in tests, where structured logging adds noise.

Log levels:
- **Debug**: individual event details, state transitions
- **Info**: process start/stop, hang detection triggers, recovery actions
- **Warn**: unexpected event types, malformed JSON lines that are skipped
- **Error**: failures that affect correctness (process spawn failure, unrecoverable parse errors)

## JSON Parsing

Define concrete Go types for every known event schema. Types live in `internal/events/`:

```go
// RawEvent is the first-pass parse — the two-field discriminator.
type RawEvent struct {
    Type    string `json:"type"`
    Subtype string `json:"subtype,omitempty"`
    Line    []byte `json:"-"` // original JSON bytes
}

// ToolCallStarted is parsed from type="tool_call", subtype="started".
type ToolCallStarted struct {
    CallID      string          `json:"call_id"`
    ModelCallID string          `json:"model_call_id"`
    TimestampMS int64           `json:"timestamp_ms"`
    ToolCall    json.RawMessage `json:"tool_call"`
}
```

Do not use `map[string]interface{}` for known schemas. It loses type safety and pushes errors to runtime.

Handle unknown event types gracefully. The stream will evolve and may contain types this tool does not yet understand:

```go
switch ev.Type {
case "tool_call":
    switch ev.Subtype {
    case "started":
        // ...
    case "completed":
        // ...
    }
case "system", "user", "thinking", "assistant", "result":
    // ...
default:
    slog.Debug("unknown event type, skipping", "type", ev.Type)
}
```

Unknown types are logged at debug level and skipped. They must never cause a crash or stop processing.

## Concurrency

This project has inherently concurrent work: reading stdout from the child process, managing timers for hang detection, handling OS signals. Use goroutines and channels.

Every goroutine must have a clean shutdown path via context cancellation:

```go
func readStream(ctx context.Context, r io.Reader, events chan<- Event) error {
    scanner := bufio.NewScanner(r)
    for scanner.Scan() {
        select {
        case <-ctx.Done():
            return ctx.Err()
        default:
        }
        // parse and send...
    }
    return scanner.Err()
}
```

No naked goroutines. Every `go func()` must be accounted for — either joined via `sync.WaitGroup`, drained via channel close, or cancelled via context. If a goroutine is launched, the caller must be able to wait for it to finish.

Use `context.WithCancel` or `context.WithTimeout` to propagate shutdown. When the parent context is cancelled, all child goroutines must exit promptly.

Channel direction should be specified in function signatures (`chan<- Event`, `<-chan Event`) to make data flow explicit.

## Testing

Use table-driven tests:

```go
func TestParseEvent(t *testing.T) {
    tests := []struct {
        name    string
        input   string
        want    RawEvent
        wantErr bool
    }{
        {
            name:  "tool call started",
            input: `{"type":"tool_call","subtype":"started","call_id":"abc"}`,
            want:  RawEvent{Type: "tool_call", Subtype: "started"},
        },
        {
            name:    "malformed json",
            input:   `{not json`,
            wantErr: true,
        },
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            // ...
        })
    }
}
```

Test the event parser against real JSONL from `experiments/`. These files capture actual cursor-agent output and are the ground truth for what the parser must handle. Use `testdata/` directories within each package for fixture files that are specific to that package.

Test the hang detector state machine with synthetic event sequences. Feed it a sequence of events and assert on the resulting state transitions and timeout decisions.

Tests should not depend on timing. Use fake clocks or injected time functions for anything involving timeouts or durations.

## Dependencies

Prefer the standard library. This project's core needs are well-served by stdlib:

| Need | Package |
|---|---|
| Structured logging | `log/slog` |
| JSON parsing | `encoding/json` |
| Process management | `os/exec` |
| Signal handling | `os/signal` |
| Context and cancellation | `context` |
| Streaming line reading | `bufio` |

Only add a third-party dependency when stdlib is genuinely insufficient for the task. If a dependency is added, it must be justified in the PR description.

## Code Style

`gofmt` is non-negotiable. All code must be formatted with `gofmt`. No exceptions, no arguments.

Keep functions short. A function that does one thing is easier to name, test, and reason about. If a function needs a comment explaining its sections, it should be split.

Name things clearly:
- `handleToolCallStarted` not `handle1`
- `ErrHangDetected` not `ErrType3`
- `parseEventLine` not `process`

Comments explain **why**, not what. The code already says what it does:

```go
// Bad: increment the counter
count++

// Good: count includes both direct and subagent tool calls,
// since hang detection applies to both equally.
count++
```

Avoid `init()` functions. They make testing harder and hide dependency ordering. Pass dependencies explicitly.
