# Logging Requirements

## Goal

Post go-live, when the wrapper encounters unexpected behavior, the logs alone should be sufficient to diagnose the issue without needing to reproduce it. Every decision the wrapper makes (especially around hang detection) must be traceable back to the raw event data that triggered it.

## What to Log

### Raw event stream (always)

- Every JSON event received from cursor-agent, verbatim, with a wall-clock receive timestamp
- This is the ground truth. If the wrapper makes a wrong decision, this log lets us replay exactly what it saw.
- Stored as JSONL — one wrapper-annotated event per line:
  ```json
  {"recv_ts": 1770823845357, "raw": {"type": "tool_call", "subtype": "started", ...}}
  ```

### Wrapper state transitions

Log every internal state change with the event that caused it:

- Tool call tracking: opened, completed, timed out
- Hang detection: timer started, timer reset, threshold crossed, action taken
- Session lifecycle: init received, result received, process exited, process killed
- Decision points: "no events for 45s, 2 open tool calls with max timeout 30s → declaring hang"

Format: standard `slog` structured JSON records. Distinguished from raw event capture records by the presence of `level`/`msg` fields (and absence of `raw`):
```json
{"level":"INFO","msg":"tool_call_opened","ts":1770823845400,"call_id":"call_xxx","command":"sleep 5","timeout_ms":10000}
{"level":"ERROR","msg":"hang_detected","ts":1770823900000,"idle_silence_ms":45000,"open_call_count":2,"last_event_type":"tool_call"}
```

### Process-level events

- cursor-agent process spawn (pid, args, env)
- Process exit (exit code, signal, expected vs unexpected)
- Process kill (reason, signal sent)
- Stdin close / pipe events

## Log Levels

| Level | What | When to use |
|-------|------|-------------|
| `debug` | Raw events, timer ticks, state snapshots | Always written to file, never to console by default |
| `info` | State transitions, lifecycle events, decisions | Console + file |
| `warn` | Unexpected but handled situations (e.g. tool exceeded timeout but eventually completed) | Console + file |
| `error` | Unhandled situations, process crashes, unparseable events | Console + file |

## Log Destinations

- **File**: rotated JSONL files in a configurable directory. One file per wrapper session. Filename includes session start timestamp and cursor-agent session_id once known.
- **Console**: human-readable summary at `info` level and above. Configurable verbosity.

## Retention

- Keep raw session logs for at least 7 days by default (configurable)
- On error/hang, automatically preserve the full session log and flag it for review

## Non-negotiables

1. **Never drop raw events.** Even if the wrapper crashes, the raw event log should be flushed/synced. Use append-mode writes, not buffered-then-flush.
2. **Timestamps on everything.** Both the cursor-agent's `timestamp_ms` and our own wall-clock receive time. Clock skew between the two is diagnostic data.
3. **Structured, not printf.** Every log entry is valid JSON. Grep and jq are the analysis tools, not eyeballing freeform text.
4. **Log the decision, not just the outcome.** "Killed process" is useless. "Killed process because no events for 60s with 0 open tool calls after last tool_call/completed at ts=X" is actionable.
