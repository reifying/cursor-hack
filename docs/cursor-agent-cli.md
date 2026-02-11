# Cursor Agent CLI

## Binary

- **Path**: `/Users/travisbrown/.local/bin/cursor-agent`
- **Type**: Bash wrapper that execs a bundled Node.js app
- **Entry**: `$SCRIPT_DIR/index.js` (webpack-bundled, single-line minified)
- **Node**: Ships its own `node` binary alongside `index.js`
- **Version**: `2026.01.28-fd13201`
- **Source dir**: `~/.local/share/cursor-agent/versions/2026.01.28-fd13201/`

## Key Flags

| Flag | Description |
|------|-------------|
| `--print` | Headless/non-interactive mode. Required for scripting. |
| `--output-format <fmt>` | `text` (default), `json` (single result object), `stream-json` (newline-delimited JSON events) |
| `--stream-partial-output` | With `stream-json`, emit per-token assistant text deltas as individual events |
| `--model <model>` | Model selection. Free plan is limited to `auto`. |
| `--force` | Auto-approve all tool calls (no confirmation prompts) |
| `--workspace <path>` | Working directory (defaults to cwd) |
| `--approve-mcps` | Auto-approve MCP servers in headless mode |
| `--mode <mode>` | `plan` (read-only) or `ask` (Q&A, read-only) |
| `--resume [chatId]` | Resume a previous session |
| `--continue` | Resume the most recent session |

## Invocation Patterns

### Piped prompt (headless)
```bash
echo "Do something" | cursor-agent --print --output-format stream-json --model auto --force
```

### Positional prompt (headless)
```bash
cursor-agent --print --output-format stream-json --model auto --force "Do something"
```

## Account Constraints

- Free plan: only `auto` model works; named models (e.g. `gemini-3-flash`) return an error message instead of structured events
- The error is emitted as a bare `T: ...` text line, not a JSON event

## Stderr

In `stream-json` mode, stderr mirrors the same JSON events as stdout.
