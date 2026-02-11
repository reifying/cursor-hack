# cursor-wrap

A Go CLI wrapper around [Cursor's](https://cursor.com) headless `cursor-agent` that adds hang detection, automatic recovery, and structured logging.

## Problem

`cursor-agent --print --output-format stream-json` can hang indefinitely during execution -- the model stalls, a tool call never completes, or the agent simply stops emitting events. There's no built-in timeout or hang detection at the session level.

## What cursor-wrap does

- **Transparent proxy**: forwards `stream-json` events byte-identically to stdout, or renders them as human-readable text
- **Hang detection**: monitors event flow in real time using a state machine that tracks open tool calls and idle silence
- **Automatic recovery**: kills the agent process on confirmed hangs; in interactive mode, prompts for the next input instead of exiting
- **Structured logging**: dual-sink JSONL file logs (for forensic replay) and human-readable console output
- **Multi-turn sessions**: supports `--resume` for interactive conversations across turns

## Install

Requires Go 1.25+ and `cursor-agent` on your PATH (installed with [Cursor](https://cursor.com)).

```bash
go build -o cursor-wrap ./cmd/cursor-wrap
```

## Usage

### Single-shot (piped or positional prompt)

```bash
# Pipe a prompt
echo "Explain this codebase" | cursor-wrap -p

# Positional argument
cursor-wrap -p "Fix the failing tests"

# Stream-json output (default for -p)
cursor-wrap -p "Refactor auth module" --output-format stream-json
```

### Interactive multi-turn

```bash
cursor-wrap
> What files handle authentication?
# ... agent responds ...
> Now refactor the login function to use JWT
# ... agent responds, using --resume to maintain context ...
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-p` / `--print` | false | Non-interactive mode: single prompt, then exit |
| `--output-format` | `text` (interactive) / `stream-json` (`-p`) | Output format |
| `--idle-timeout` | 60s | Max silence with no open tool calls before hang |
| `--tool-grace` | 30s | Extra time beyond a tool's declared timeout |
| `--tick-interval` | 5s | How often to check for hangs |
| `--log-dir` | `~/.cursor-wrap/logs` | Session log directory |
| `--log-level` | `warn` (interactive) / `info` (`-p`) | Console log level |
| `--agent-bin` | auto-detected | Path to `cursor-agent` binary |
| `--model` | (none) | Model to pass to cursor-agent |
| `--workspace` | (none) | Working directory for cursor-agent |
| `--force` | true | Auto-approve tool calls |

Everything after `--` is passed through to `cursor-agent` as extra flags.

### Exit codes

| Code | Meaning |
|------|---------|
| 0 | Normal completion |
| 1 | Error (spawn failure, abnormal exit, etc.) |
| 2 | Hang detected |

## How hang detection works

The monitor tracks two conditions:

1. **Idle hang**: no events received and no tool calls are in-flight for longer than `--idle-timeout`. This catches the case where the agent simply stops responding between actions.

2. **Tool-timeout hang**: every open tool call has exceeded its declared timeout plus `--tool-grace`. This catches tools that never complete. The monitor only declares a hang when *all* open tools have expired, avoiding false positives during parallel tool execution.

Each tool call in cursor-agent's stream-json output includes a `timeout` field. The monitor uses this per-tool deadline rather than a single global timeout, so a legitimately long-running tool (compilation, test suite) won't trigger a false positive.

## Project structure

```
cmd/cursor-wrap/        CLI entry point, flag parsing, orchestrator
internal/events/        JSON event types and streaming parser
internal/format/        Output formatters (stream-json passthrough, text)
internal/monitor/       Hang detection state machine
internal/process/       Child process lifecycle (spawn, kill, wait)
internal/logger/        Dual-sink structured logger (JSONL file + console)
docs/                   Design docs, event schemas, analysis
experiments/            Raw JSONL captures from cursor-agent sessions
```

## Testing

```bash
# Unit and integration tests
go test ./...

# End-to-end tests (requires authenticated cursor-agent)
CURSOR_E2E=1 go test -v -run TestE2E ./cmd/cursor-wrap/
```

## License

[MIT](LICENSE)
