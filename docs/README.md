# Documentation

All project documentation lives in `docs/`. Each document covers a single topic.

## Structure

```
docs/
├── README.md              # This file — index of all docs
├── cursor-agent-cli.md    # CLI interface, flags, invocation patterns
├── stream-json-events.md  # Event types and schemas for stream-json output
└── hang-detection.md      # Analysis of hang vs. long-running-tool signals

experiments/               # Raw experiment JSONL logs (one JSON object per line)
├── direct-sleep.jsonl     # Sequential: sleep 5 then sleep 3
├── subagent-sleep.jsonl   # Single: sleep 8 (attempted subagent, ran direct)
├── subagent-sleep-v2.jsonl # Single: sleep 10 (attempted subagent, ran direct)
└── parallel-sleep.jsonl   # Parallel: sleep 6 + sleep 4 simultaneously
```

## Conventions

- One topic per file, named with lowercase-kebab-case
- Keep docs factual and concise — no filler
- Link to experiment logs when claims are based on observed behavior
- Update docs as understanding evolves; delete stale content rather than leaving caveats
