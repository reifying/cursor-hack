# Design: cursor-agent Hang Detection Wrapper

## 1. Overview

### Problem statement

The cursor-agent CLI hangs indefinitely under certain conditions. When run headlessly (via `--print --output-format stream-json`), the process stops emitting events but never exits. The operator has no way to distinguish a hang from a long-running tool call without monitoring the event stream and applying domain-specific heuristics.

We need a wrapper that:
- Transparently proxies cursor-agent's stream-json output
- Detects hangs by analyzing event flow patterns in real time
- Takes corrective action (kill + optional restart) when a hang is confirmed
- Produces logs sufficient to diagnose any post-go-live surprise
- Supports both single-shot and interactive multi-turn usage

### Goals

1. Detect and recover from cursor-agent hangs automatically
2. Never misidentify a legitimate long-running tool call as a hang
3. Log every raw event and every wrapper decision for post-mortem analysis
4. In non-interactive mode (`-p`), be a drop-in replacement — callers pipe a prompt in and consume stream-json out
5. In interactive mode (default), support multi-turn conversations with human-readable output, resuming the same cursor-agent session across turns

### Non-goals

- Modifying cursor-agent's behavior or source code
- Interpreting or acting on the semantic content of agent responses
- Multi-session management (one wrapper invocation = one cursor-agent session, possibly with multiple turns)
- Multi-line prompt input in interactive mode (one line = one prompt)
- Log retention and rotation (logging.md specifies 7-day default; deferred until session volume warrants it)
- Automatic flagging of error/hang logs for review (logging.md requirement; deferred — log files are preserved per-session and inspectable manually)

## 2. Background & Context

### Current state

cursor-agent is invoked headlessly and emits newline-delimited JSON to stdout. Events follow a lifecycle documented in @stream-json-events.md. The agent has no built-in hang detection or timeout at the session level.

cursor-agent supports resuming a previous session via `--resume <chatId>`. When resumed with `--print --output-format stream-json`, the event stream format is identical to a fresh session (system/init → user → thinking → assistant → result), and the `session_id` remains the same across turns. Each resumed turn is a separate process invocation. The new prompt is delivered via stdin, and stdin must be closed after writing (cursor-agent reads to EOF). This was verified experimentally — see @cursor-agent-cli.md.

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
┌────────────────────────────────────────────────────────────┐
│                      cursor-wrap                            │
│                                                             │
│  ┌──────────┐   ┌──────────┐   ┌─────────────┐            │
│  │ Process  │──▶│  Event   │──▶│   Hang      │            │
│  │ Manager  │   │  Reader  │   │   Monitor   │            │
│  │          │   │          │   │             │            │
│  │ spawn    │   │ parse    │   │ track calls │            │
│  │ resume   │   │ annotate │   │ run timers  │            │
│  │ kill     │   │          │   │ decide      │            │
│  └────┬─────┘   └────┬─────┘   └──────┬──────┘            │
│       │              │                 │                   │
│       │              ├─────────────┐   │                   │
│       │              ▼             ▼   │                   │
│       │         ┌──────────┐  ┌────────────┐              │
│       │         │  Logger  │  │  Output    │              │
│       │         │          │  │  Formatter │              │
│       │         │ file     │  │            │              │
│       │         │ console  │  │ stream-json│              │
│       │         └──────────┘  │ text       │              │
│       │              ▲        └─────┬──────┘              │
│       └──────────────┘              │                      │
│                                     ▼                      │
│  prompt ──▶ cursor-agent ──▶ formatter ──▶ stdout          │
│    ▲                                                        │
│    │  (interactive mode: read next prompt from stdin)        │
│    └────────────────────────────────────────────────────────│
└────────────────────────────────────────────────────────────┘
```

Five internal components, each a package under `internal/`:

| Package | Responsibility |
|---------|---------------|
| `process` | Spawn cursor-agent, wire stdin/stdout/stderr, kill, wait for exit, support `--resume` for multi-turn |
| `events` | Read stdout line-by-line, parse JSON into typed events, annotate with receive timestamp |
| `monitor` | Consume parsed events, track open tool calls, run silence timers, emit hang/timeout verdicts |
| `logger` | Structured JSONL logging to file + human-readable console output via `slog` |
| `format` | Output formatting — stream-json passthrough or human-readable text rendering |

### Operating Modes

The wrapper has two operating modes controlled by the `-p`/`--print` flag:

| | Non-interactive (`-p`) | Interactive (default) |
|---|---|---|
| Turns | Single | Multi-turn loop |
| Prompt source | Positional arg or stdin (read to EOF) | First: positional arg or first stdin line. Subsequent: one line per prompt from stdin |
| Session resume | N/A | Automatic via `--resume <session_id>` |
| Default output format | `stream-json` | `text` |
| Default console log level | `info` | `warn` |
| On hang | Exit with code 2 | Log error, prompt for next input |
| On non-hang error | Exit with code 1 | Exit with code 1 (non-recoverable: process spawn failure, reader error, abnormal exit) |
| On EOF / Ctrl+D | N/A | Clean exit |

In both modes, cursor-agent is always invoked with `--print --output-format stream-json`. The wrapper is the interactive layer; cursor-agent always runs headlessly.

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

#### Content event types (for output formatter)

The text formatter needs deeper parsing than the monitor. These types extract content fields that the monitor ignores:

```go
// AssistantMessage extracts the text content from an "assistant" event.
type AssistantMessage struct {
    Text        string `json:"-"` // extracted from message.content[0].text
    ModelCallID string `json:"model_call_id,omitempty"`
    IsFinal     bool   `json:"-"` // true when model_call_id is absent (final response)
}

// ThinkingDelta extracts the token text from a "thinking"/"delta" event.
type ThinkingDelta struct {
    Text string `json:"text"`
}

// ToolCallInfo extracts tool type and key arguments for display.
// Parsed from the tool_call field of started/completed events.
type ToolCallInfo struct {
    ToolType string // key name: "shellToolCall", "lsToolCall", etc.
    // Shell-specific fields (populated when ToolType == "shellToolCall"):
    Command   string
    TimeoutMS int64
    // LS-specific fields (populated when ToolType == "lsToolCall"):
    Path string
}

// ShellToolResult extracts result fields from a completed shellToolCall.
type ShellToolResult struct {
    ExitCode      int    `json:"exitCode"`
    Stdout        string `json:"stdout"`
    Stderr        string `json:"stderr"`
    ExecutionTime int64  `json:"executionTime"` // ms
}

// ParseAssistantMessage extracts text from an assistant event's raw JSON.
func ParseAssistantMessage(raw []byte) (AssistantMessage, error)

// ParseToolCallInfo extracts tool type and display-relevant args from
// the tool_call field of a started or completed event.
func ParseToolCallInfo(toolCallJSON json.RawMessage) (ToolCallInfo, error)

// ParseShellToolResult extracts the result from a completed shellToolCall.
func ParseShellToolResult(toolCallJSON json.RawMessage) (ShellToolResult, error)
```

These content types are used exclusively by the text formatter. The monitor and logger do not depend on them, maintaining clean separation.

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
    SessionID  string   // non-empty to resume a previous session via --resume
}

// Start spawns cursor-agent and returns handles to its I/O and process.
func Start(ctx context.Context, cfg Config) (*Session, error)

// Session represents a running cursor-agent process.
// Stdin is not exposed — it is written and closed during Start().
type Session struct {
    Stdout io.ReadCloser
    Stderr io.ReadCloser
    Cmd    *exec.Cmd
}

// Kill sends SIGTERM, waits briefly, then SIGKILL if needed.
func (s *Session) Kill(reason string) error

// Wait blocks until the process exits and returns its status.
func (s *Session) Wait() (*os.ProcessState, error)
```

The wrapper always adds `--print --output-format stream-json` to the cursor-agent invocation. When `Config.SessionID` is non-empty, `--resume <session_id>` is also added.

#### Prompt delivery

`Start()` writes the prompt to cursor-agent's stdin and immediately closes it:

```go
func Start(ctx context.Context, cfg Config) (*Session, error) {
    cmd := exec.CommandContext(ctx, cfg.AgentBin, buildArgs(cfg)...)
    stdin, err := cmd.StdinPipe()
    if err != nil { return nil, fmt.Errorf("stdin pipe: %w", err) }

    stdout, err := cmd.StdoutPipe()
    if err != nil { return nil, fmt.Errorf("stdout pipe: %w", err) }

    stderr, err := cmd.StderrPipe()
    if err != nil { return nil, fmt.Errorf("stderr pipe: %w", err) }

    if err := cmd.Start(); err != nil {
        return nil, fmt.Errorf("starting cursor-agent: %w", err)
    }

    // Write prompt and close stdin. cursor-agent reads stdin to EOF
    // to capture the prompt. If stdin is not closed, the agent hangs
    // waiting for more input — which would look like an agent hang
    // to the monitor.
    if _, err := io.WriteString(stdin, cfg.Prompt); err != nil {
        // Best-effort kill; process may not have read anything yet.
        _ = cmd.Process.Kill()
        return nil, fmt.Errorf("writing prompt to stdin: %w", err)
    }
    if err := stdin.Close(); err != nil {
        _ = cmd.Process.Kill()
        return nil, fmt.Errorf("closing stdin: %w", err)
    }

    return &Session{Stdout: stdout, Stderr: stderr, Cmd: cmd}, nil
}
```

Note: `Session` does not expose `Stdin` since it is always closed during `Start()`. The caller never needs to write to it after the prompt is delivered.

#### Argument construction

```go
func buildArgs(cfg Config) []string {
    args := []string{"--print", "--output-format", "stream-json"}
    if cfg.SessionID != "" {
        args = append(args, "--resume", cfg.SessionID)
    }
    if cfg.Force {
        args = append(args, "--force")
    }
    if cfg.Model != "" {
        args = append(args, "--model", cfg.Model)
    }
    if cfg.Workspace != "" {
        args = append(args, "--workspace", cfg.Workspace)
    }
    args = append(args, cfg.ExtraFlags...)
    return args
}
```

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
// It closes the out channel when the reader hits EOF or the context is
// cancelled, signaling downstream that the stream is done. Any fatal
// read error (not EOF, not context cancellation) is sent on errCh
// before closing out.
func Reader(ctx context.Context, r io.Reader, out chan<- AnnotatedEvent, errCh chan<- error)
```

**Key behaviors** (Event Reader):
- Lines that fail JSON parsing are logged at `warn` level and skipped (handles the `T: ...` free-plan error lines)
- The raw bytes are always preserved, even for parse failures
- Fatal read errors (e.g. broken pipe) are sent on `errCh` so the orchestrator can act on them
- Channel `out` is closed on EOF or context cancellation, signaling downstream that the stream is done

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
func (m *Monitor) ProcessEvent(ev events.AnnotatedEvent) Verdict

// Now returns the current time from the monitor's clock.
// Callers should use this instead of time.Now() to keep production
// and test code on the same path.
func (m *Monitor) Now() time.Time

// SessionDone reports whether the result event has been received.
func (m *Monitor) SessionDone() bool

// SessionID returns the session_id captured from the system/init event.
func (m *Monitor) SessionID() string

// CheckTimeout evaluates whether the current silence duration
// constitutes a hang given the current state.
// Called periodically by the orchestrator on a timer tick.
func (m *Monitor) CheckTimeout(now time.Time) (Verdict, Reason)

// Reason provides structured context for a verdict.
type Reason struct {
    IdleSilenceMS   int64            // ms since last event of any kind
    OpenCallCount   int
    LastEventType   string
    OpenCalls       []OpenCallDetail // details for each open tool call
}

// OpenCallDetail captures per-tool timing for diagnostic logging.
type OpenCallDetail struct {
    CallID    string
    Command   string // shell command, empty for non-shell tools
    ElapsedMS int64  // ms since this tool's started event
    TimeoutMS int64  // declared timeout (0 if unknown)
}

// String returns a human-readable summary for use in kill reasons and logs.
func (r Reason) String() string
```

`Reason.String()` formats a one-line summary like: `"idle 65000ms, 0 open calls, last event: thinking"` or `"2 open calls all expired, last event: tool_call"`. This is used as the kill reason passed to `sess.Kill()` and in the text formatter's hang indicator.

#### Decision logic in `CheckTimeout`

```
idleElapsed = now - state.LastEventAt

if state.SessionDone:
    return VerdictOK  // session completed normally

if len(state.OpenCalls) == 0:
    if idleElapsed > idleTimeout:
        return VerdictHang, reason  // nothing running, silence too long
    return VerdictOK

// Tools are running. Check each tool against its own start time.
// A hang is declared only when ALL open tools have exceeded their
// individual deadline (timeout + grace measured from StartedAt).
allExpired = true
for tool in state.OpenCalls:
    toolElapsed = now - tool.StartedAt
    toolDeadline = tool.TimeoutMS + toolGrace
    if tool.TimeoutMS == 0:
        // Non-shell tool with no declared timeout — use idleTimeout as fallback
        toolDeadline = idleTimeout
    if toolElapsed <= toolDeadline:
        allExpired = false
        break

if allExpired:
    return VerdictHang, reason
return VerdictWaiting
```

This per-tool measurement is critical for correctness. If tool A starts at T=0 with a 10s timeout, and tool B starts at T=8 with a 10s timeout, measuring from `LastEventAt` (T=8) would prematurely declare A as within bounds at T=18, or worse, reset A's clock entirely. By measuring each tool from its own `StartedAt`, we get accurate per-tool deadlines regardless of when other events arrive.

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
// LogSession wraps *slog.Logger and holds a reference to the file sink,
// enabling the log file to be renamed once the session_id is known.
type LogSession struct {
    *slog.Logger
    filePath string // current file path, updated by SetSessionID
}

// Setup initializes the dual-sink logger and returns a LogSession.
// The teardown function flushes and closes the file sink.
func Setup(cfg LogConfig) (*LogSession, func() error)

// SetSessionID renames the log file to incorporate the session_id.
// Called once after the first system/init event is received.
// No-op if session_id was already set or if the rename fails (logged at warn).
func (ls *LogSession) SetSessionID(id string)

type LogConfig struct {
    Dir          string     // directory for log files
    ConsoleLevel slog.Level // minimum level for console output
    FileLevel    slog.Level // minimum level for file output (typically debug)
}
```

`LogSession` embeds `*slog.Logger` so callers use standard `slog` methods (`log.Info(...)`, `log.Debug(...)`, etc.) without any API change. The `SetSessionID` method renames the underlying file from the placeholder name to the final `cursor-wrap-{start_ts}-{session_id}.jsonl` name.

The file sink writes JSONL. The console sink writes human-readable text. Both are `slog.Handler` implementations.

The file is opened in append mode with `O_SYNC` to minimize data loss on crash. Filename format: `cursor-wrap-{start_ts}-{session_id}.jsonl`. Before session_id is known, the file uses `cursor-wrap-{start_ts}-unknown.jsonl` and is renamed once `SetSessionID` is called.

In interactive mode (multi-turn), a single log file spans all turns of the wrapper invocation. The session_id is the same across turns (cursor-agent preserves it on `--resume`), so the filename does not change between turns. Turn boundaries are visible in the log via repeated `system/init` events.

#### Log record formats

The file sink contains two kinds of JSONL records. Both are emitted through `slog` and share the standard `time`/`level`/`msg` fields. They are distinguished by the presence of the `"raw"` key:

**Raw event capture** (`logRawEvent`): preserves the entire cursor-agent event verbatim inside a `"raw"` field, per @logging.md. This is the forensic replay record. Emitted at `DEBUG` level with msg `"raw_event"`.

```json
{"time":"2026-02-10T12:30:45.400Z","level":"DEBUG","msg":"raw_event","recv_ts":1770823845400,"raw":{"type":"tool_call","subtype":"started","call_id":"call_xxx","timestamp_ms":1770823845357}}
```

**Wrapper decision records**: state transitions, hang detection verdicts, and process lifecycle events. These do NOT contain a `"raw"` field.

```json
{"time":"2026-02-10T12:30:45.400Z","level":"INFO","msg":"tool_call_opened","ts":1770823845400,"call_id":"call_xxx","command":"sleep 5","timeout_ms":10000}
{"time":"2026-02-10T12:31:00.000Z","level":"ERROR","msg":"hang_detected","ts":1770823900000,"idle_silence_ms":55000,"open_call_count":0,"last_event_type":"thinking"}
```

#### Timestamp serialization

All timestamps use **Unix milliseconds (int64)**, matching cursor-agent's own `timestamp_ms` field. This makes it trivial to diff wrapper receive times against agent-reported times with simple arithmetic.

- Raw event records: `recv_ts` field (wrapper wall-clock at receive)
- Wrapper decision records: `ts` field (wrapper wall-clock at decision point)
- Agent timestamps: preserved as-is inside the `raw` object

The `AnnotatedEvent.RecvTime` field is `time.Time` internally for clean Go APIs, but serialized as epoch millis when written to the log file. Conversion: `ev.RecvTime.UnixMilli()`.

### Output Formatter (`internal/format/`)

Replaces the direct `forwardToStdout` function with a pluggable interface. The orchestrator calls the formatter for each event instead of writing raw bytes.

```go
// Formatter renders cursor-agent events to the wrapper's stdout.
type Formatter interface {
    // WriteEvent renders a single event. Called for every event in the
    // stream, in order. The formatter decides what to display.
    WriteEvent(ev events.AnnotatedEvent) error

    // WriteHangIndicator renders a hang detection message inline.
    // Called by the session loop when a hang is detected in interactive mode.
    // The stream-json formatter writes a synthetic JSON event; the text
    // formatter writes a human-readable warning line.
    WriteHangIndicator(reason monitor.Reason) error

    // Flush is called after each turn completes (result event received
    // or stream ended). The formatter can write separators or finalize
    // buffered output.
    Flush() error
}

// New creates a formatter for the given format name.
// Supported formats: "stream-json", "text".
func New(format string, w io.Writer) Formatter
```

#### StreamJSON formatter

Transparent passthrough — writes the raw JSON line plus a newline. This is the existing behavior, extracted into the interface:

```go
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
```

With `--output-format stream-json`, cursor-agent events on the wrapper's stdout are byte-identical to cursor-agent's stdout (AC 8). The only non-passthrough output is the synthetic `wrapper/hang_detected` event from `WriteHangIndicator`, emitted in interactive mode after a hang kill.

#### Text formatter

Renders a human-readable view of the agent's activity. This is the default format for interactive mode.

```go
type text struct {
    w io.Writer
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
```

The `WriteEvent` method dispatches on `ev.Parsed.Type` and `ev.Parsed.Subtype` per the rendering rules below. Silent events return nil immediately. Events that require content parsing (`assistant`, `tool_call`) call the corresponding `Parse*` function and skip on error.

**Rendering rules by event type:**

| Event | Rendering |
|-------|-----------|
| `system/init` | Silent (session info logged, not displayed) |
| `user` | Silent (user already knows what they typed) |
| `thinking/delta` | Silent (internal reasoning, not shown) |
| `thinking/completed` | Silent |
| `assistant` (mid-turn) | Print `message.content[0].text` followed by newline |
| `assistant` (final) | Print `message.content[0].text` followed by newline |
| `tool_call/started` (shell) | Print `⏳ \`command\`` followed by newline |
| `tool_call/started` (other) | Print `⏳ toolType: args` followed by newline |
| `tool_call/completed` (shell, exit 0) | Print `✓ \`command\` (Xs, exit 0)` followed by newline |
| `tool_call/completed` (shell, exit ≠ 0) | Print `✗ \`command\` (Xs, exit N)` followed by newline |
| `tool_call/completed` (other) | Print `✓ toolType` followed by newline |
| `result` | Silent (redundant with final assistant message) |
| Unknown | Silent (logged, not displayed) |

The text formatter needs the content event types (`AssistantMessage`, `ToolCallInfo`, `ShellToolResult`) defined in the data model section. Parse failures for display-only types are logged at debug level and the event is skipped (never crashes the formatter).

**Example text output** for a session with sequential tool calls:

```
I'll run `sleep 5` in bash, wait for it to finish, then run `sleep 3` in bash.

⏳ `sleep 5`
✓ `sleep 5` (5.4s, exit 0)

⏳ `sleep 3`
✓ `sleep 3` (3.2s, exit 0)

Both commands have finished running: first `sleep 5`, then `sleep 3`, with no other actions taken.
```

**Example text output** for a hang detection:

```
I'll help with that.

⏳ `npm install`
⚠ Hang detected — killed cursor-agent (idle 65s, 0 open tool calls)
```

### Orchestrator (in `cmd/cursor-wrap/main.go`)

The orchestrator has two layers: a **session loop** that manages multi-turn interactive sessions, and a **per-turn event loop** that processes one cursor-agent invocation.

#### Per-turn event loop

Handles a single cursor-agent process from spawn to exit. Returns the session_id captured from the `system/init` event (needed by the session loop for `--resume`).

```go
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
    for runErr == nil {
        select {
        case ev, ok := <-eventCh:
            if !ok {
                runErr = handleStreamEnd(sess, mon, log)
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
            runErr = sess.Kill("reader error")

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
```

Changes from the original single-turn `run()`:
- `forwardToStdout(ev.Raw)` replaced with `fmtr.WriteEvent(ev)`
- Returns `TurnResult` with session_id for the session loop
- A fresh `Monitor` is created per turn (no stale state between turns)
- `fmtr.Flush()` called after the event loop exits

#### Session loop

The outer loop manages prompt input and multi-turn session resumption:

```go
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

    var sessionID string
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
            log.SetSessionID(sessionID) // renames log file
        }

        if result.Err != nil {
            if cfg.Print {
                // Non-interactive: exit on any error.
                return result.Err
            }
            // Interactive: log the error and continue to next prompt.
            // The user can decide whether to continue or Ctrl+D.
            if errors.Is(result.Err, ErrHangDetected) {
                fmtr.WriteHangIndicator(result.Reason)
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
```

#### Prompt reading

```go
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
```

#### Orchestrator helpers

```go
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
// Format: {"recv_ts":<epoch_ms>,"raw":<verbatim event JSON>}
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
```

**Key behaviors** (Orchestrator):
- Every raw event is logged to file (as a `raw` record) before any processing
- Every raw event is passed to the formatter, which decides what to render to stdout
- Stderr is drained in a separate goroutine to prevent pipe buffer deadlock; lines are logged at debug level
- On hang: kill the process, log the full reason. In `-p` mode, return `ErrHangDetected` (exit 2). In interactive mode, display a warning and continue to the next prompt.
- On normal completion (result event received, then EOF): return nil
- On abnormal EOF (stream ends without result event): return `ErrAbnormalExit`
- On reader error: kill the process, log the error, exit
- On context cancellation (e.g. SIGINT to the wrapper): kill the child, return the context error
- The monitor's injectable clock (`mon.Now()`) is used for timeout checks, keeping production and test code on the same path
- Both background goroutines are tracked with `sync.WaitGroup` and joined before the turn returns, ensuring the log file stays open until all goroutines finish writing
- In interactive mode, a fresh `Monitor` is created per turn — no stale state carries over between turns

#### Signal handling

The wrapper must handle OS signals (SIGINT, SIGTERM) to ensure the child process is killed cleanly before the wrapper exits. This is done in `main()` using `signal.NotifyContext`, which cancels the context on signal receipt:

```go
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
```

When a signal arrives, `ctx` is cancelled, which triggers the `case <-ctx.Done()` branch in the event loop. This kills the child process and returns `ctx.Err()`. The exit code distinguishes hang detection (exit 2) from other failures (exit 1) and normal completion (exit 0).

### CLI Flags (`cmd/cursor-wrap/`)

```
cursor-wrap [flags] [-- cursor-agent-flags...] [prompt]

Flags:
  -p, --print                  Non-interactive mode: single prompt, exit after (default: false)
  --output-format string       Output format: stream-json | text (default: stream-json with -p, text without)
  --idle-timeout duration      Max silence with no open tool calls (default 60s)
  --tool-grace duration        Extra time beyond a tool's declared timeout (default 30s)
  --tick-interval duration     How often to check for hangs (default 5s)
  --log-dir string             Directory for session log files (default ~/.cursor-wrap/logs)
  --log-level string           Console log level: debug|info|warn|error (default: info with -p, warn without)
  --agent-bin string           Path to cursor-agent binary (default: from $PATH)
  --model string               Model to pass to cursor-agent (default auto)
  --workspace string           Workspace directory for cursor-agent
  --force                      Pass --force to cursor-agent (default true)

Everything after -- is passed directly to cursor-agent.
```

#### Prompt resolution

| Flag | Positional arg | Stdin | Behavior |
|------|---------------|-------|----------|
| `-p` | Given | Any | Use positional arg as prompt. Run one turn. Exit. |
| `-p` | None | Pipe | Read stdin to EOF as prompt. Run one turn. Exit. |
| `-p` | None | TTY | Error: "no prompt provided" |
| (none) | Given | TTY | First turn uses positional arg. Subsequent turns read lines from stdin. |
| (none) | Given | Pipe | First turn uses positional arg. Subsequent turns read lines from stdin until EOF. |
| (none) | None | TTY | Show `> ` prompt. Read lines from stdin for each turn. |
| (none) | None | Pipe | Read lines from stdin for each turn until EOF. |

#### Config struct

```go
type Config struct {
    // Mode
    Print        bool   // -p: non-interactive, single prompt
    OutputFormat string // "stream-json" or "text"

    // Hang detection
    IdleTimeout  time.Duration
    ToolGrace    time.Duration
    TickInterval time.Duration

    // Logging
    Log logger.LogConfig

    // Process
    Process process.Config

    // Prompt input
    PositionalPrompt string       // trailing arg, if any
    PromptReader     *bufio.Reader // wraps os.Stdin
}
```

#### Helper functions

```go
// parseFlags uses the stdlib flag package to parse CLI flags and trailing
// args into a Config. Everything after "--" is captured as ExtraFlags
// for pass-through to cursor-agent. The last non-flag argument (if any)
// is treated as the positional prompt. Mode-dependent defaults (output
// format, console log level) are applied after flag parsing based on
// whether -p was set.
func parseFlags(args []string) Config

// isTerminal reports whether the given file descriptor is connected to a
// terminal. Used to decide whether to show the "> " prompt indicator and
// to detect the -p+TTY+no-prompt error case. Implemented via
// golang.org/x/term.IsTerminal or an equivalent syscall check.
func isTerminal(f *os.File) bool
```

## 4. Verification Strategy

### Unit tests

**Event parser** (`internal/events/`):
- Parse each known event type from real JSONL lines (fixtures from `experiments/`)
- Handle malformed JSON (skip gracefully, no panic)
- Handle non-JSON lines (the `T: ...` free-plan error case)
- Handle `call_id` values containing literal newlines
- Handle unknown event types (parse base envelope, skip gracefully)
- Parse content event types: `AssistantMessage`, `ToolCallInfo`, `ShellToolResult`

**Hang monitor** (`internal/monitor/`):
- Sequential tool call: started → silence → completed → no hang
- Parallel tool calls: two started → one completed → still waiting → second completed → no hang
- Idle hang: thinking/completed → long silence with no open tools → hang
- Tool timeout hang: tool started → silence exceeds tool timeout + grace → hang
- Normal completion: result event received → no hang regardless of subsequent silence
- Clock injection: all tests use a fake clock, no real `time.Sleep`
- SessionID capture: system/init event populates SessionID()

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
- `buildArgs` includes `--resume` when SessionID is set
- `buildArgs` omits `--resume` when SessionID is empty

**Logger** (`internal/logger/`):
- Setup creates a log file in the configured directory with a placeholder filename
- SetSessionID renames the file to include the session_id
- Raw event records contain `recv_ts` and `raw` fields (validates AC 6)
- Hang detection records contain all `Reason` fields: `idle_silence_ms`, `open_call_count`, `last_event_type`, per-call details (validates AC 7)

**Output formatter** (`internal/format/`):
- StreamJSON: WriteEvent outputs raw bytes + newline, byte-identical to input
- Text: assistant events render message text
- Text: tool_call/started renders spinner + command
- Text: tool_call/completed renders checkmark + timing
- Text: thinking/delta events produce no output
- Text: parse failures in content types are handled gracefully (no panic, no output)
- Flush: text formatter writes newline separator between turns
- WriteHangIndicator: streamJSON writes synthetic JSON event; text writes human-readable warning

**Prompt reading** (`cmd/cursor-wrap/`):
- `firstPrompt`: positional arg takes precedence over stdin
- `firstPrompt`: `-p` with piped stdin reads to EOF as single prompt
- `firstPrompt`: `-p` with TTY stdin and no positional arg returns error
- `firstPrompt`: interactive mode with no positional arg delegates to `readPrompt`
- `readPrompt`: returns first non-empty line from reader
- `readPrompt`: skips blank lines
- `readPrompt`: returns `io.EOF` when input is exhausted

### Integration tests

- Full pipeline: spawn a real `sleep` command (as a stand-in for cursor-agent), feed synthetic JSONL to verify the wrapper parses, monitors, and forwards correctly
- Hang scenario: spawn a process that emits some events then goes silent, verify the wrapper detects the hang and kills it within the expected window
- Normal completion: feed a complete event sequence ending with a `result` event, verify clean exit
- Multi-turn: feed two complete event sequences (simulating two turns), verify the wrapper reads a second prompt and passes `--resume` with the correct session_id
- Output format: verify stream-json mode produces byte-identical output; verify text mode produces expected human-readable rendering
- Signal handling: send SIGINT to the wrapper process while cursor-agent is running, verify the child process is killed and the wrapper exits cleanly (validates AC 9)

### End-to-end tests

- Run the wrapper against a real cursor-agent invocation (e.g. `echo "say hi" | cursor-wrap -p`) and verify stdout contains the expected event stream and the process exits cleanly
- Run a two-turn interactive session: first turn with positional arg, second turn via stdin pipe, verify `--resume` is used on the second invocation
- These are slower and depend on network/API access; run manually or in CI with appropriate credentials

### Acceptance criteria

1. Wrapper exits 0 when cursor-agent completes normally (result event received)
2. Wrapper detects an idle hang (no events, no open tools) within `idleTimeout + tickInterval` and exits non-zero
3. Wrapper detects a tool-timeout hang (tool exceeds declared timeout + grace) within `toolGrace + tickInterval` and exits non-zero
4. Wrapper does NOT false-positive on a long-running tool call that is within its declared timeout
5. Wrapper does NOT false-positive on parallel tool calls where one finishes before others
6. Every raw cursor-agent event appears in the log file with a `recv_ts`
7. Every hang detection decision appears in the log file with the full `Reason` struct
8. With `--output-format stream-json`, cursor-agent events on wrapper's stdout are byte-identical to cursor-agent's stdout (transparent proxy). The only addition is the synthetic `wrapper/hang_detected` event emitted by `WriteHangIndicator` in interactive mode after a hang kill.
9. On SIGINT/SIGTERM to the wrapper, the child process is killed cleanly
10. All thresholds are configurable via CLI flags
11. In interactive mode, the wrapper reads subsequent prompts from stdin and resumes the cursor-agent session via `--resume <session_id>`
12. In interactive mode, hang detection kills the current turn's process and prompts for the next input (does not exit the wrapper)
13. With `--output-format text`, assistant text and tool call status are rendered in human-readable form
14. `-p` flag runs a single turn and exits; omitting `-p` enters the interactive multi-turn loop

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

### Formatter as middleware (io.Writer chain)

Instead of an interface called per-event, make the formatter an `io.Writer` that sits between the event reader and stdout, transforming bytes on the fly.

**Rejected**: the formatter needs parsed event structure (type, subtype, tool call fields) to make rendering decisions. A byte-level `io.Writer` would need to re-parse every line, duplicating the event reader's work. Calling the formatter with `AnnotatedEvent` gives it free access to both raw bytes and parsed structure.

## 6. Risks & Mitigations

| Risk | Impact | Mitigation |
|------|--------|------------|
| Unknown event types in future cursor-agent versions | Monitor can't track new tool types → potential false hang detection | Unknown events still reset `LastEventAt` (they prove the stream is alive). Only `tool_call/started` and `tool_call/completed` affect the open call map. New tool types are safe-by-default. |
| Tool with no `timeout` field (non-shell tools) | Can't compute per-tool deadline | Fall back to `idleTimeout` for tools without a declared timeout. Log a warning so we notice and add support. |
| `call_id` matching issues (newlines, encoding) | Orphaned open calls → eventual false hang | Use raw bytes for call_id matching, not decoded strings. Log unmatched completions at warn level. |
| cursor-agent changes stream-json format | Parser breaks | Base envelope parse (`type`/`subtype`) is resilient — unknown types are skipped. Structural changes to the envelope itself would break us, but this is a stable API surface. |
| Wrapper adds latency to event forwarding | Caller sees delayed events | Forwarding happens synchronously in the event loop before any processing. The overhead is a `json.Unmarshal` + channel send per line — sub-millisecond. |
| Log file fills disk | Resource exhaustion on long sessions | File-per-session naming. Future: add configurable max file size and rotation. For now, sessions are short-lived enough that this is not a concern. |
| Session corruption after hang in interactive mode | `--resume` fails on next turn | Log the error from cursor-agent. If the resumed process fails to start, report the error to the user. They can Ctrl+D to exit and start a fresh session. |
| Stdin contention between prompt reader and cursor-agent | Prompt bytes leak into agent's stdin or vice versa | No contention: cursor-agent's stdin is a pipe created by `StdinPipe()`, completely separate from the wrapper's `os.Stdin`. The wrapper reads prompts from `os.Stdin`; cursor-agent reads from its own pipe, which is closed after prompt delivery. |
| Text formatter parse failure on new event fields | Crash or garbled output | Content type parsing is best-effort. Parse failures are logged at debug level and the event is silently skipped by the formatter. The stream-json formatter is unaffected (raw passthrough). |
