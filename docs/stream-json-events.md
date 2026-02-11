# Stream-JSON Event Reference

When invoked with `--print --output-format stream-json`, cursor-agent emits one JSON object per line to stdout.

## Event Lifecycle

```
system/init → user → [thinking/delta]* → thinking/completed →
  assistant (with model_call_id) →
  [tool_call/started → tool_call/completed]* →
  assistant (final, no model_call_id) →
  result/success
```

The agent may loop through multiple thinking → assistant → tool_call cycles before the final result.

## Event Types

### system (subtype: init)

First event. Emitted once per session.

```json
{
  "type": "system",
  "subtype": "init",
  "apiKeySource": "login",
  "cwd": "/path/to/workspace",
  "session_id": "uuid",
  "model": "Auto",
  "permissionMode": "default"
}
```

### user

Echo of the submitted prompt.

```json
{
  "type": "user",
  "message": {
    "role": "user",
    "content": [{"type": "text", "text": "the prompt"}]
  },
  "session_id": "uuid"
}
```

### thinking (subtype: delta)

Streaming thinking tokens. Many of these per thinking phase.

```json
{
  "type": "thinking",
  "subtype": "delta",
  "text": "token",
  "session_id": "uuid",
  "timestamp_ms": 1770823434041
}
```

### thinking (subtype: completed)

Marks end of a thinking phase.

```json
{
  "type": "thinking",
  "subtype": "completed",
  "session_id": "uuid",
  "timestamp_ms": 1770823435070
}
```

### assistant

Assistant text output. Two forms:

**Mid-turn** (precedes tool calls): includes `model_call_id` and `timestamp_ms`.

```json
{
  "type": "assistant",
  "message": {"role": "assistant", "content": [{"type": "text", "text": "..."}]},
  "session_id": "uuid",
  "model_call_id": "uuid-suffix",
  "timestamp_ms": 1770823449274
}
```

**Final** (after all tool calls resolved): no `model_call_id`, no `timestamp_ms`.

```json
{
  "type": "assistant",
  "message": {"role": "assistant", "content": [{"type": "text", "text": "..."}]},
  "session_id": "uuid"
}
```

**With `--stream-partial-output`**: individual token deltas are emitted as separate `assistant` events (each with a single token in `text`), followed by a consolidated final `assistant` event.

### tool_call (subtype: started)

Tool invocation begins.

```json
{
  "type": "tool_call",
  "subtype": "started",
  "call_id": "call_xxx",
  "tool_call": {
    "lsToolCall": {
      "args": {"path": "/some/path", "ignore": [], "toolCallId": "call_xxx"}
    }
  },
  "model_call_id": "uuid-suffix",
  "session_id": "uuid",
  "timestamp_ms": 1770823449274
}
```

**Note**: `call_id` values can contain literal newline characters (`\n`). Parsers must handle this.

### tool_call (subtype: completed)

Tool result returned.

```json
{
  "type": "tool_call",
  "subtype": "completed",
  "call_id": "call_xxx",
  "tool_call": {
    "lsToolCall": {
      "args": {"path": "/some/path", "ignore": [], "toolCallId": "call_xxx"},
      "result": {"success": {"directoryTreeRoot": {"...": "..."}}}
    }
  },
  "model_call_id": "uuid-suffix",
  "session_id": "uuid",
  "timestamp_ms": 1770823449565
}
```

### result (subtype: success)

Terminal event. Session is complete.

```json
{
  "type": "result",
  "subtype": "success",
  "duration_ms": 4986,
  "duration_api_ms": 4986,
  "is_error": false,
  "result": "concatenated assistant text",
  "session_id": "uuid",
  "request_id": "uuid"
}
```

## Known Tool Call Types

The tool call type is identified by the key name in the `tool_call` object:

- `lsToolCall` — directory listing (args: `path`, `ignore`)
- `shellToolCall` — shell command execution (args: `command`, `workingDirectory`, `timeout`, `simpleCommands`, `parsingResult`, `timeoutBehavior`, etc.)

### shellToolCall Detail

The `shellToolCall` is the most complex tool type. Key fields:

```
args.command          — the shell command string
args.timeout          — max wait in ms before backgrounding
args.simpleCommands   — parsed command names (e.g. ["sleep"])
args.isBackground     — whether running in background
args.timeoutBehavior  — "TIMEOUT_BEHAVIOR_BACKGROUND"
args.parsingResult    — structured parse of the command

result.success.exitCode       — process exit code
result.success.stdout         — captured stdout
result.success.stderr         — captured stderr
result.success.executionTime  — actual runtime in ms
result.isBackground           — whether it was backgrounded
```

## Parallel Tool Calls

When the agent invokes multiple tools simultaneously, all `tool_call/started` events share the same `model_call_id`. Completions arrive in order of actual completion time (shortest first), not invocation order.

## Available Agent Tools

Full list (from asking the agent):

Shell, Glob, Grep, LS, Read, Delete, EditNotebook, TodoWrite, SemanticSearch, WebFetch, ListMcpResources, FetchMcpResource, ApplyPatch, multi_tool_use.parallel

There is no subagent/Task tool. `multi_tool_use.parallel` is the mechanism for concurrent tool execution.
