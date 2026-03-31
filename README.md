# skills-engine

A standalone Go library for building **natural language-driven AI agents**.

Users define agents in plain English — a name, intent, and desired output format. The engine uses an LLM to generate the agent's instruction set automatically, then executes it against any document or content source. No prompt engineering. No hardcoded logic. Just intent.

## How It Works

Skills Engine follows a two-stage agent pattern:

**1. Generate** — The user describes what they want the agent to do. The engine calls the LLM to produce a structured `SkillDefinition` (stored as markdown instructions). This becomes the reusable agent.

**2. Execute** — The agent is run against one or more source documents. The engine feeds the skill's instructions and document content to the LLM and streams back the result. The engine automatically selects single-pass or two-pass processing based on source size.

```
User describes intent → Engine generates skill instructions (via LLM)
                                      ↓
         User picks agent + document → Engine executes skill → Streams markdown output
```

## Features

- **Natural language agent creation** — Define agents with a name, intent, and output format. The LLM writes the instructions.
- **Adaptive pipeline** — Automatically switches between single-pass and two-pass execution based on document token count (configurable threshold, default 4,000 tokens).
- **Streaming output** — `Engine.Run` returns a channel of `StepEvent` values so the caller can stream progress and results in real time.
- **Bring your own everything** — The engine has no opinion on LLMs, databases, or HTTP. Wire in your stack by implementing three small interfaces: `LLMProvider`, `SkillStore`, and `RunStore`.
- **Per-agent config overrides** — Each `SkillDefinition` can override the global `AdaptiveConfig` (force single-pass, two-pass, or auto).

## Project Status

Early-stage library. Interfaces and behavior may change before `v1.0.0`.

## Repository Layout

```
skillengine/          Go module (github.com/xorType/skillsengine)
  engine.go           Engine struct and public API
  generator.go        LLM-based skill generation
  pipeline.go         Adaptive single/two-pass pipeline
  interfaces.go       LLMProvider, SkillStore, RunStore interfaces
  types.go            SkillDefinition, RunRequest, events, and constants
  examples/
    minimal/          CLI example — generate and run a skill from the command line
    webapp/           Full web demo — browser UI for creating and running agents
```

## Requirements

- Go 1.22+

## Quick Start

```bash
git clone https://github.com/xorType/skillsengine.git
cd skillsengine/skillengine
go test ./...
```

## Usage

### 1. Implement the three interfaces

```go
// LLMProvider wraps your LLM of choice (OpenAI, Ollama, Anthropic, etc.)
type LLMProvider interface {
    Chat(ctx context.Context, systemPrompt, userPrompt string, opts ChatOpts) (<-chan Chunk, error)
}

// SkillStore persists agent definitions (Postgres, SQLite, files — your choice)
type SkillStore interface {
    Save(ctx context.Context, skill *SkillDefinition) error
    Get(ctx context.Context, id string) (*SkillDefinition, error)
    GetByUser(ctx context.Context, userID string) ([]SkillDefinition, error)
    Update(ctx context.Context, skill *SkillDefinition) error
    Delete(ctx context.Context, id string) error
}

// RunStore persists execution records and output artifacts
type RunStore interface {
    CreateRun(ctx context.Context, run *RunRecord) error
    UpdateRunStatus(ctx context.Context, runID string, status RunStatus) error
    CompleteRun(ctx context.Context, runID string) error
    FailRun(ctx context.Context, runID, errMsg string) error
    AddArtifact(ctx context.Context, artifact *ArtifactRecord) error
    GetRun(ctx context.Context, runID string) (*RunRecord, error)
    ListRuns(ctx context.Context) ([]RunRecord, error)
    GetRunArtifact(ctx context.Context, runID string) (*ArtifactRecord, error)
    ListArtifacts(ctx context.Context) ([]ArtifactRecord, error)
}
```

### 2. Create the engine

```go
cfg := skillengine.DefaultAdaptiveConfig() // SinglePassMaxTokens: 4000, ForceMode: auto
engine := skillengine.NewEngine(myLLM, mySkillStore, myRunStore, cfg)
```

### 3. Generate an agent from natural language

```go
skill, err := engine.GenerateSkill(ctx,
    "Sales Call Analyzer",
    "Analyses recorded sales call transcripts",
    "Extract key objections, outcomes, and follow-up actions",
    "## Objections\n## Outcome\n## Follow-up Actions",
    "user-123",
)
// skill.SkillMarkdown now contains LLM-generated instructions
// skill.ID is persisted via SkillStore
```

### 4. Run the agent against a document

```go
events, err := engine.Run(ctx, skillengine.RunRequest{
    SkillID: skill.ID,
    UserID:  "user-123",
    Sources: []skillengine.SourceInput{
        {ID: "doc-1", Name: "call-transcript.txt", Content: transcriptText},
    },
})
if err != nil {
    log.Fatal(err)
}

for ev := range events {
    fmt.Printf("[%s] %s — %s\n", ev.Step, ev.Status, ev.Message)
    if ev.Status == skillengine.EventStatusDone && ev.Step == skillengine.StepPersist {
        if payload, ok := ev.Payload.(map[string]any); ok {
            fmt.Println(payload["markdown"])
        }
    }
}
```

### Pipeline steps

Each `StepEvent` emitted by `Run` has a `Step` field:

| Step | Description |
|------|-------------|
| `assess` | Estimates token count and selects single-pass or two-pass mode |
| `execute` | Runs the skill instructions against the source content |
| `extract` | (Two-pass only) Extracts structured data from initial pass |
| `format` | Formats the final output according to the skill's output format |
| `persist` | Saves the artifact; payload contains the final `markdown` |

## Web Demo

The `examples/webapp` directory contains a full browser-based demo showing the natural language agent workflow end-to-end:

- A UI for defining new agents by describing intent in plain English
- A document picker (four built-in sample documents)
- Real-time streaming of pipeline steps via Server-Sent Events
- Rendered markdown output in the browser

It uses [Ollama](https://ollama.com) as the local LLM backend. To run it:

```bash
# Start Ollama with a supported model
ollama run qwen3.5:397b-cloud

# Start the web demo
cd skillengine/examples/webapp
go run .
# Open http://localhost:8080
```

## Configuration

`AdaptiveConfig` controls pipeline mode selection:

```go
type AdaptiveConfig struct {
    // Token count threshold for switching to two-pass mode. Default: 4000.
    SinglePassMaxTokens int

    // Force a specific mode: "single", "two-pass", or "" (auto).
    ForceMode string
}
```

Per-agent overrides can be set on `SkillDefinition.AdaptiveConfig`.

## Development

```bash
cd skillengine
gofmt -w .
go vet ./...
go test ./...
```

## Release

See [RELEASE.md](RELEASE.md) for the `v0.1.0` publish checklist.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md).

## Security

See [SECURITY.md](SECURITY.md).

## License

MIT — see [LICENSE](LICENSE).
