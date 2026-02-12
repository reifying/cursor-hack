# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project

**cursor-wrap** is a Go CLI wrapper around Cursor's headless `cursor-agent` that adds hang detection, automatic recovery, and structured logging. It transparently proxies stream-json events while monitoring for hangs via a state machine that tracks open tool calls and idle silence.

## Build & Test

```bash
# Build
go build -o cursor-wrap ./cmd/cursor-wrap

# All unit + integration tests
go test ./...

# Single package
go test ./internal/monitor -v

# Single test function
go test ./cmd/cursor-wrap -v -run TestParseFlags_DefaultsPrintMode

# E2E tests (requires real cursor-agent on PATH, authenticated)
go test -tags=e2e -v ./cmd/cursor-wrap/ -run TestE2E -timeout 300s
```

E2E tests use the `e2e` build tag and are excluded from `go test ./...`. Integration tests in `cmd/cursor-wrap/integration_test.go` use a fake agent binary built via TestMain.

No third-party dependencies — stdlib only (`slog`, `encoding/json`, `os/exec`, `bufio`). Go 1.25+ required.

## Architecture

Five internal packages, each with a single responsibility:

- **`internal/events`** — Streaming line reader that parses cursor-agent's JSON output into typed events. Two-pass parsing: first extracts type/subtype discriminator, then dispatches to concrete Go types. Unknown event types are logged at debug and skipped (never crash).

- **`internal/monitor`** — Hang detection state machine. Tracks open tool calls and detects two conditions: (1) idle hang — no events and no in-flight tools for longer than idle timeout; (2) tool-timeout hang — all open tool calls exceeded their declared timeout + grace period. Each tool measured from its own `StartedAt` time to avoid false positives during parallel execution.

- **`internal/process`** — Spawns cursor-agent with correct flags, manages stdin/stdout/stderr pipes. Supports `--resume` for multi-turn sessions.

- **`internal/format`** — Pluggable formatter interface: `StreamJSON` (byte-identical passthrough) and `Text` (human-readable rendering).

- **`internal/logger`** — Dual-sink structured logger using `slog`: JSONL file sink (DEBUG level) and console text sink (configurable level).

**Orchestrator** (`cmd/cursor-wrap/main.go`): Two-layer event loop — outer session loop for multi-turn interactive mode, inner per-turn loop that spawns the agent, reads events, checks for hangs on a ticker, and forwards to the formatter.

**Config** (`cmd/cursor-wrap/config.go`): Centralizes all CLI flags with mode-dependent defaults (`-p` mode vs interactive mode).

## Coding Conventions

- **Error handling**: Wrap with context at every call site (`fmt.Errorf("doing X: %w", err)`). Sentinel errors with `errors.New()`, check with `errors.Is()`. Intentionally ignored errors: `_ = cmd.Process.Kill()` with a comment.
- **Logging**: All operational output through `slog`. No `fmt.Println`/`log.Printf` for operational output. Log levels: DEBUG (event details, state transitions), INFO (lifecycle), WARN (unexpected types, malformed JSON), ERROR (spawn failures).
- **JSON parsing**: Concrete Go types for known schemas, never `map[string]interface{}`. Preserve raw bytes. Handle unknown types gracefully.
- **Concurrency**: Context propagation for all goroutines. No naked goroutines — track with WaitGroup or channel close. Specify channel direction in signatures (`chan<-`, `<-chan`).
- **Testing**: Table-driven tests. Fake clocks for timing (no `time.Sleep` in tests). Real JSONL fixtures from `experiments/` for parser tests.
- **Style**: `gofmt` required. Short functions. Comments explain "why" not "what". No `init()` functions.

## Key Files by Task

| Task | Files |
|------|-------|
| Hang detection logic | `internal/monitor/monitor.go` |
| CLI flags / config | `cmd/cursor-wrap/config.go` |
| Event parsing | `internal/events/reader.go`, `internal/events/types.go`, `internal/events/content.go` |
| Output formatting | `internal/format/text.go`, `internal/format/streamjson.go` |
| Process management | `internal/process/process.go` |
| Orchestrator loop | `cmd/cursor-wrap/main.go` |
| Integration tests (fake agent) | `cmd/cursor-wrap/integration_test.go`, `cmd/cursor-wrap/testdata/fakeagent/main.go` |

## Documentation

`docs/design.md` is the comprehensive design document (~55KB) covering data model, algorithms, and acceptance criteria. Other docs: `docs/coding-standards.md`, `docs/logging.md`, `docs/hang-detection.md`, `docs/stream-json-events.md`, `docs/cursor-agent-cli.md`.
