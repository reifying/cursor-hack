# Hang Detection Analysis

## Problem

The cursor-agent can hang indefinitely during execution. We need to distinguish between:

1. **Actual hang** — the agent is stuck and will never produce output again
2. **Long-running tool** — a legitimate tool call (e.g. long build, test suite) is still executing
3. **Parallel tool execution** — multiple tools running concurrently, some finishing before others

## Available Signals

Every `stream-json` event includes `timestamp_ms` and `session_id`. Tool calls have a clear `started` → `completed` lifecycle with matching `call_id` values.

### Key fields for hang detection

| Field | Location | Use |
|-------|----------|-----|
| `timestamp_ms` | Most events | Wall-clock time of event emission |
| `tool_call.subtype` | `started` / `completed` | Track open tool calls |
| `call_id` | `tool_call` events | Match start to completion |
| `executionTime` | `tool_call/completed → result.success` | Actual tool runtime in ms |
| `model_call_id` | `assistant`, `tool_call` events | Groups tool calls from same model turn |
| `type: result` | Terminal event | Session complete signal |

## Observed Behavior (Experiments)

### Sequential tool calls (direct-sleep.jsonl)

Prompt: "Run `sleep 5` then `sleep 3`"

```
tool_call/started  sleep 5  ts=845357
tool_call/completed sleep 5 ts=850766  execTime=5399ms
tool_call/started  sleep 3  ts=853543   (2.8s gap: model thinking between calls)
tool_call/completed sleep 3 ts=856707  execTime=3159ms
result: duration_ms=21509
```

**Pattern**: Events arrive sequentially. Gap between first completion and second start is model inference time (~2.8s). No events during tool execution.

### Parallel tool calls (parallel-sleep.jsonl)

Prompt: "Run `sleep 6` and `sleep 4` in parallel"

```
tool_call/started  sleep 6  ts=976204  model_call_id=...-0-mwez
tool_call/started  sleep 4  ts=976330  model_call_id=...-0-mwez  (same model_call_id)
tool_call/completed sleep 4 ts=980492  execTime=4162ms
tool_call/completed sleep 6 ts=982401  execTime=6194ms
result: duration_ms=13039
```

**Pattern**: Both `started` events share the same `model_call_id`. Completions arrive out of order (shorter finishes first). Total wall time ≈ max(6s, 4s) + overhead, NOT sum.

### Single long tool (subagent-sleep.jsonl)

Prompt: "Run `sleep 8`"

```
tool_call/started  sleep 8  ts=867096
tool_call/completed sleep 8 ts=875274  execTime=8173ms
result: duration_ms=16460
```

**Pattern**: ~8s silence between started and completed. This is expected, not a hang.

## No Subagent Tool

Cursor agent does **not** have a subagent/Task tool. Its available tools are:

- Shell, Glob, Grep, LS, Read, Delete, EditNotebook, TodoWrite, SemanticSearch, WebFetch, ListMcpResources, FetchMcpResource, ApplyPatch, multi_tool_use.parallel

The `multi_tool_use.parallel` capability is how it runs multiple tools concurrently. There is no separate "subagent" concept — just parallel tool calls within a single agent turn.

## Hang Detection Strategy

### State to track

- **Open tool calls**: map of `call_id` → `{started_ts, command, timeout}`
- **Last event timestamp**: wall-clock time of most recent event
- **Session complete**: whether `type: result` has been received

### Detecting a hang vs. a long-running tool

A gap in events is normal **if and only if** there are open tool calls (started but not completed).

```
IF time_since_last_event > threshold:
  IF open_tool_calls is empty:
    → HANG (no tool running, but no events arriving)
  ELSE:
    FOR each open tool call:
      IF elapsed > tool's timeout field:
        → POSSIBLE HANG (tool exceeded its own timeout)
      ELSE:
        → WAITING (tool still within its declared timeout)
```

### Useful heuristics

1. **Tool timeout field**: Every `shellToolCall` includes an explicit `timeout` field in args (e.g. `10000`, `20000`, `30000` ms). This is the agent's declared max wait. If `executionTime` would exceed this, the agent backgrounds the command (per `timeoutBehavior: TIMEOUT_BEHAVIOR_BACKGROUND`).

2. **No-event silence with no open tools**: If >30s pass with no events and no tool calls are in-flight, this is almost certainly a hang.

3. **Model inference gaps**: Between tool completion and next action, there's a model inference delay (observed 2-3s typically). Gaps of >30s here without a new event suggest a hang.

4. **Parallel tool awareness**: When multiple tools share a `model_call_id`, expect silence until the longest one completes. Use `max(timeout)` of the group, not individual timeouts.

5. **Never-arriving result**: If all tool calls have completed, a final `assistant` + `result` event should follow within a few seconds. If it doesn't, the agent is hung.

## Edge Cases to Handle

- `call_id` values contain literal newlines — must parse carefully
- Stderr mirrors stdout in stream-json mode — deduplicate if reading both
- Free plan errors are emitted as raw text (`T: ...`), not JSON events
- The `timeout` field in tool args is a hint, not a guarantee
