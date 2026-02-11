# Design: cursor-agent Hang Detection Wrapper

## 1. Overview

### Problem statement

The cursor-agent CLI hangs indefinitely under certain conditions. When run headlessly (via `--print --output-format stream-json`), the process stops emitting events but never exits. The operator has no way to distinguish a hang from a long-running tool call without monitoring the event stream and applying domain-specific heuristics.

We need a wrapper that:
- Transparently proxies cursor-agent's stream-json output
- Detects hangs by analyzing event flow patterns in real time
- Takes corrective action (kill + optional restart) when a hang is confirmed
- Produces logs sufficient to diagnose any post-go-live surprise

### Goals

1. Detect and recover from cursor-agent hangs automatically
2. Never misidentify a legitimate long-running tool call as a hang
3. Log every raw event and every wrapper decision for post-mortem analysis
4. Be a drop-in replacement — callers pipe a prompt in and consume stream-json out

### Non-goals

- Modifying cursor-agent's behavior or source code
- Interpreting or acting on the semantic content of agent responses
- Supporting interactive (non-`--print`) mode
- Multi-session management (one wrapper instance = one agent session)

## 2. Background & Context

### Current state

cursor-agent is invoked headlessly and emits newline-delimited JSON to stdout. Events follow a lifecycle documented in @stream-json-events.md. The agent has no built-in hang detection or timeout at the session level.

### Why now

The hang bug is blocking reliable automated use of cursor-agent. Manual intervention (watching for stalls, killing the process) does not scale.

### Related work

- @cursor-agent-cli.md — CLI interface and invocation patterns
- @stream-json-events.md — event type reference and schemas
- @hang-detection.md — analysis of hang signals from experiments
- @logging.md — logging requirements
- @coding-standards.md — Go project structure and conventions

## 3. Detailed Design

### Architecture

```
┌─────────────────────────────────────────────────┐
│                  cursor-wrap                     │
│                                                  │
│  ┌──────────┐   ┌──────────┐   ┌─────────────┐  │
│  │ Process  │──▶│  Event   │──▶│   Hang      │  │
│  │ Manager  │   │  Reader  │   │   Monitor   │  │
│  │          │   │          │   │             │  │
│  │ spawn    │   │ parse    │   │ track calls │  │
│  │ kill     │   │ annotate │   │ run timers  │  │
│  │ wait     │   │ log raw  │   │ decide      │  │
│  └────┬─────┘   └────┬─────┘   └──────┬──────┘  │
│       │              │                 │         │
│       │              ▼                 │         │
│       │         ┌──────────┐           │         │
│       │         │  Logger  │◀──────────┘         │
│       │         │          │                     │
│       │         │ file     │                     │
│       │         │ console  │                     │
│       │         └──────────┘                     │
│       │              ▲                           │
│       └──────────────┘                           │
│                                                  │
│  stdin ──▶ cursor-agent ──▶ stdout ──▶ caller    │
└─────────────────────────────────────────────────┘
```

Four internal components, each a package under `internal/`:

| Package | Responsibility |
|---------|---------------|
| `process` | Spawn cursor-agent, wire stdin/stdout/stderr, kill, wait for exit |
| `events` | Read stdout line-by-line, parse JSON into typed events, annotate with receive timestamp |
| `monitor` | Consume parsed events, track open tool calls, run silence timers, emit hang/timeout verdicts |
| `logger` | Structured JSONL logging to file + human-readable console output via `slog` |

### Data Model

#### Event types (`internal/events/`)

A base envelope parsed first, then dispatched by type/subtype:

```go
// RawEvent is the first-pass parse of every JSON line. Only the
// discriminator fields are decoded; the full line is retained verbatim.
type RawEvent struct {
    Type    string `json:"type"`
    Subtype string `json:"subtype,omitempty"`
    Line    []byte `json:"-"` // original JSON bytes, unparsed
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
    ToolCall    json.RawMessage `json:"tool_call"` // varies by tool type
}

// ShellToolArgs holds the fields we need from shellToolCall.args.
type ShellToolArgs struct {
    Command   string `json:"command"`
    Timeout   int64  `json:"timeout"` // ms
    IsBackground bool `json:"isBackground"`
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
```

#### Monitor state (`internal/monitor/`)

```go
// OpenToolCall tracks a tool that has started but not completed.
type OpenToolCall struct {
    CallID      string
    ModelCallID string
    StartedAt   time.Time // wrapper wall-clock time at started event
    TimeoutMS   int64     // from tool args, 0 if unknown/not a shell call
    Command     string    // shell command string, empty for non-shell tools
}

// State is the hang monitor's internal state.
type State struct {
    OpenCalls     map[string]*OpenToolCall // keyed by call_id
    LastEventAt   time.Time               // wall-clock time of last event received
    SessionDone   bool                    // true after result event
    SessionID     string                  // from system/init
}
```

### Process Manager (`internal/process/`)

Responsible for spawning cursor-agent with the correct flags and managing its lifecycle.

```go
// Config holds the arguments needed to start cursor-agent.
type Config struct {
    AgentBin   string   // path to cursor-agent binary
    Prompt     string   // the user prompt
    Model      string   // model flag value
    Workspace  string   // --workspace path
    ExtraFlags []string // any additional flags to pass through
    Force      bool     // --force flag
}

// Start spawns cursor-agent and returns handles to its I/O and process.
func Start(ctx context.Context, cfg Config) (*Session, error)

// Session represents a running cursor-agent process.
type Session struct {
    Stdin  io.WriteCloser
    Stdout io.ReadCloser
    Stderr io.ReadCloser
    Cmd    *exec.Cmd
}

// Kill sends SIGTERM, waits briefly, then SIGKILL if needed.
func (s *Session) Kill(reason string) error

// Wait blocks until the process exits and returns its status.
func (s *Session) Wait() (*os.ProcessState, error)
```

The wrapper always adds `--print --output-format stream-json` to the cursor-agent invocation. The caller's prompt is piped to stdin, then stdin is closed to signal end of input.

### Event Reader (`internal/events/`)

Reads stdout line-by-line, parses each line, annotates it, and forwards it.

```go
// AnnotatedEvent wraps a parsed event with the wrapper's receive timestamp.
type AnnotatedEvent struct {
    RecvTime time.Time
    Raw      []byte   // verbatim JSON line
    Parsed   RawEvent // first-pass parse (type + subtype)
}

// Reader reads from an io.Reader and emits AnnotatedEvents on a channel.
// It closes the channel when the reader hits EOF or the context is cancelled.
func Reader(ctx context.Context, r io.Reader, out chan<- AnnotatedEvent) error
```

Key behaviors:
- Lines that fail JSON parsing are logged at `warn` level and skipped (handles the `T: ...` free-plan error lines)
- The raw bytes are always preserved, even for parse failures
- Channel is closed on EOF or context cancellation, signaling downstream that the stream is done

### Hang Monitor (`internal/monitor/`)

A state machine that consumes events and produces verdicts.

```go
// Verdict represents the monitor's assessment after processing an event
// or after a timer fires.
type Verdict int

const (
    VerdictOK       Verdict = iota // everything looks normal
    VerdictWaiting                 // tools running, silence is expected
    VerdictHang                    // hang detected — take action
)

// Monitor tracks event flow and detects hangs.
type Monitor struct {
    state       State
    clock       func() time.Time   // injectable for testing
    idleTimeout time.Duration      // max silence with no open tools
    toolGrace   time.Duration      // extra grace beyond a tool's declared timeout
}

// NewMonitor creates a monitor with the given thresholds.
func NewMonitor(idleTimeout, toolGrace time.Duration, opts ...Option) *Monitor

// ProcessEvent updates state based on an incoming event.
// Returns VerdictOK or VerdictWaiting. Never returns VerdictHang
// synchronously — hangs are detected by CheckTimeout.
func (m *Monitor) ProcessEvent(ev AnnotatedEvent) Verdict

// CheckTimeout evaluates whether the current silence duration
// constitutes a hang given the current state.
// Called periodically by the orchestrator on a timer tick.
func (m *Monitor) CheckTimeout(now time.Time) (Verdict, Reason)

// Reason provides structured context for a verdict.
type Reason struct {
    SilenceMS       int64
    OpenCallCount   int
    MaxToolTimeout  int64    // ms, from the longest-running open tool
    LastEventType   string
    OpenCommands    []string // shell commands currently running
}
```

#### Decision logic in `CheckTimeout`

```
elapsed = now - state.LastEventAt

if state.SessionDone:
    return VerdictOK  // session completed normally

if len(state.OpenCalls) == 0:
    if elapsed > idleTimeout:
        return VerdictHang, reason  // nothing running, silence too long
    return VerdictOK

// Tools are running. Find the one with the longest allowed runtime.
maxAllowed = max(tool.TimeoutMS for tool in state.OpenCalls) + toolGrace
if elapsed > maxAllowed:
    return VerdictHang, reason  // all tools have exceeded their timeout + grace
return VerdictWaiting
```

#### Default thresholds

| Parameter | Default | Rationale |
|-----------|---------|-----------|
| `idleTimeout` | 60s | Model inference is typically 2-3s. 60s is extremely generous. |
| `toolGrace` | 30s | Buffer beyond a tool's declared timeout for process scheduling jitter. |
| Timer tick interval | 5s | Frequent enough to catch hangs promptly, infrequent enough to be cheap. |

All thresholds are configurable via CLI flags.

### Logger (`internal/logger/`)

Two log sinks, both fed by `slog`:

```go
// Setup initializes the dual-sink logger.
// Returns a teardown function that flushes and closes the file sink.
func Setup(cfg LogConfig) (*slog.Logger, func() error)

type LogConfig struct {
    Dir          string    // directory for log files
    SessionID    string    // incorporated into filename once known
    ConsoleLevel slog.Level // minimum level for console output
    FileLevel    slog.Level // minimum level for file output (typically debug)
}
```

The file sink writes JSONL. The console sink writes human-readable text. Both are `slog.Handler` implementations.

The file is opened in append mode with `O_SYNC` to minimize data loss on crash. Filename format: `cursor-wrap-{start_ts}-{session_id}.jsonl`. If session_id is not yet known (before system/init), the file starts with a placeholder and is renamed once the session_id arrives.

### Orchestrator (in `cmd/cursor-wrap/main.go`)

The main function wires the components together:

```go
func run(ctx context.Context, cfg Config) error {
    log, teardown := logger.Setup(cfg.Log)
    defer teardown()

    sess, err := process.Start(ctx, cfg.Process)
    if err != nil { return err }

    events := make(chan events.AnnotatedEvent, 64)
    mon := monitor.NewMonitor(cfg.IdleTimeout, cfg.ToolGrace)

    // Goroutine: read stdout → parse → channel
    go events.Reader(ctx, sess.Stdout, events)

    // Goroutine: periodic timeout check
    ticker := time.NewTicker(cfg.TickInterval)
    defer ticker.Stop()

    for {
        select {
        case ev, ok := <-events:
            if !ok {
                // Stream closed (process exited or EOF)
                return handleStreamEnd(sess, mon, log)
            }
            logRawEvent(log, ev)
            forwardToStdout(ev.Raw)
            verdict := mon.ProcessEvent(ev)
            logVerdict(log, verdict, ev)

        case <-ticker.C:
            verdict, reason := mon.CheckTimeout(time.Now())
            if verdict == monitor.VerdictHang {
                log.Error("hang detected", reasonAttrs(reason)...)
                return sess.Kill(reason.String())
            }

        case <-ctx.Done():
            return sess.Kill("context cancelled")
        }
    }
}
```

Key behaviors:
- Every raw event is logged to file before any processing
- Every raw event is forwarded to the wrapper's own stdout so callers see the same stream
- On hang: kill the process, log the full reason, exit with a non-zero status
- On normal completion (result event received, then EOF): exit 0
- On context cancellation (e.g. SIGINT to the wrapper): kill the child, exit

### CLI Flags (`cmd/cursor-wrap/`)

```
cursor-wrap [flags] [-- cursor-agent-flags...] [prompt]

Flags:
  --idle-timeout duration    Max silence with no open tool calls (default 60s)
  --tool-grace duration      Extra time beyond a tool's declared timeout (default 30s)
  --tick-interval duration   How often to check for hangs (default 5s)
  --log-dir string           Directory for session log files (default ~/.cursor-wrap/logs)
  --log-level string         Console log level: debug|info|warn|error (default info)
  --agent-bin string         Path to cursor-agent binary (default: from $PATH)
  --model string             Model to pass to cursor-agent (default auto)
  --workspace string         Workspace directory for cursor-agent
  --force                    Pass --force to cursor-agent (default true)

Everything after -- is passed directly to cursor-agent.
```

The wrapper consumes its own flags, constructs the cursor-agent command with `--print --output-format stream-json` always present, and passes the prompt via stdin.

## 4. Verification Strategy

### Unit tests

**Event parser** (`internal/events/`):
- Parse each known event type from real JSONL lines (fixtures from `experiments/`)
- Handle malformed JSON (skip gracefully, no panic)
- Handle non-JSON lines (the `T: ...` free-plan error case)
- Handle `call_id` values containing literal newlines
- Handle unknown event types (parse base envelope, skip gracefully)

**Hang monitor** (`internal/monitor/`):
- Sequential tool call: started → silence → completed → no hang
- Parallel tool calls: two started → one completed → still waiting → second completed → no hang
- Idle hang: thinking/completed → long silence with no open tools → hang
- Tool timeout hang: tool started → silence exceeds tool timeout + grace → hang
- Normal completion: result event received → no hang regardless of subsequent silence
- Clock injection: all tests use a fake clock, no real `time.Sleep`

```go
func TestMonitor_IdleHang(t *testing.T) {
    clock := &fakeClock{now: time.Now()}
    m := NewMonitor(60*time.Second, 30*time.Second, WithClock(clock.Now))

    m.ProcessEvent(thinkingCompletedEvent(clock.Now()))

    // Advance clock past idle timeout
    clock.Advance(61 * time.Second)

    verdict, reason := m.CheckTimeout(clock.Now())
    if verdict != VerdictHang {
        t.Fatalf("expected hang, got %v", verdict)
    }
    if reason.OpenCallCount != 0 {
        t.Fatalf("expected 0 open calls, got %d", reason.OpenCallCount)
    }
}

func TestMonitor_ToolRunning_NoHang(t *testing.T) {
    clock := &fakeClock{now: time.Now()}
    m := NewMonitor(60*time.Second, 30*time.Second, WithClock(clock.Now))

    m.ProcessEvent(toolCallStartedEvent(clock.Now(), "call_1", 120000)) // 120s timeout

    // 90s silence — within tool timeout + grace
    clock.Advance(90 * time.Second)

    verdict, _ := m.CheckTimeout(clock.Now())
    if verdict != VerdictWaiting {
        t.Fatalf("expected waiting, got %v", verdict)
    }
}
```

**Process manager** (`internal/process/`):
- Spawns a process and captures stdout
- Kill sends SIGTERM then SIGKILL after deadline
- Wait returns exit status correctly

### Integration tests

- Full pipeline: spawn a real `sleep` command (as a stand-in for cursor-agent), feed synthetic JSONL to verify the wrapper parses, monitors, and forwards correctly
- Hang scenario: spawn a process that emits some events then goes silent, verify the wrapper detects the hang and kills it within the expected window
- Normal completion: feed a complete event sequence ending with a `result` event, verify clean exit

### End-to-end tests

- Run the wrapper against a real cursor-agent invocation (e.g. `echo "say hi" | cursor-wrap`) and verify stdout contains the expected event stream and the process exits cleanly
- These are slower and depend on network/API access; run manually or in CI with appropriate credentials

### Acceptance criteria

1. Wrapper exits 0 when cursor-agent completes normally (result event received)
2. Wrapper detects an idle hang (no events, no open tools) within `idleTimeout + tickInterval` and exits non-zero
3. Wrapper detects a tool-timeout hang (tool exceeds declared timeout + grace) within `toolGrace + tickInterval` and exits non-zero
4. Wrapper does NOT false-positive on a long-running tool call that is within its declared timeout
5. Wrapper does NOT false-positive on parallel tool calls where one finishes before others
6. Every raw cursor-agent event appears in the log file with a `recv_ts`
7. Every hang detection decision appears in the log file with the full `Reason` struct
8. Wrapper's stdout is byte-identical to cursor-agent's stdout (transparent proxy)
9. On SIGINT/SIGTERM to the wrapper, the child process is killed cleanly
10. All thresholds are configurable via CLI flags

## 5. Alternatives Considered

### Poll-based external watchdog

A separate process that monitors cursor-agent's stdout using `lsof` or `/proc` and kills it if output stalls.

**Rejected**: loses all semantic awareness. Cannot distinguish tool execution from a hang. Would need very conservative timeouts, causing unnecessary kills during legitimate long operations.

### Modify cursor-agent source

Patch the bundled Node.js to add its own hang detection.

**Rejected**: the source is minified/bundled, fragile to patch, and would break on updates. Out of scope per non-goals.

### Timeout-only wrapper (no event parsing)

Simple wrapper: if no stdout bytes for N seconds, kill.

**Rejected**: during a `sleep 30` tool call, there are legitimately 30+ seconds of silence. A byte-level timeout would either be too aggressive (killing legitimate operations) or too lenient (taking minutes to detect a hang during model inference). Event-aware monitoring solves this.

## 6. Risks & Mitigations

| Risk | Impact | Mitigation |
|------|--------|------------|
| Unknown event types in future cursor-agent versions | Monitor can't track new tool types → potential false hang detection | Unknown events still reset `LastEventAt` (they prove the stream is alive). Only `tool_call/started` and `tool_call/completed` affect the open call map. New tool types are safe-by-default. |
| Tool with no `timeout` field (non-shell tools) | Can't compute per-tool deadline | Fall back to `idleTimeout` for tools without a declared timeout. Log a warning so we notice and add support. |
| `call_id` matching issues (newlines, encoding) | Orphaned open calls → eventual false hang | Use raw bytes for call_id matching, not decoded strings. Log unmatched completions at warn level. |
| cursor-agent changes stream-json format | Parser breaks | Base envelope parse (`type`/`subtype`) is resilient — unknown types are skipped. Structural changes to the envelope itself would break us, but this is a stable API surface. |
| Wrapper adds latency to event forwarding | Caller sees delayed events | Forwarding happens synchronously in the event loop before any processing. The overhead is a `json.Unmarshal` + channel send per line — sub-millisecond. |
| Log file fills disk | Resource exhaustion on long sessions | File-per-session naming. Future: add configurable max file size and rotation. For now, sessions are short-lived enough that this is not a concern. |
