# skills-engine

A standalone Go package for skill-based document execution with an adaptive LLM pipeline.

The package lets a host application:
- Define reusable skills (name, description, intent, output format)
- Auto-generate markdown skill instructions
- Execute skills against source documents
- Adapt between single-pass and two-pass processing based on source size
- Persist runs and output artifacts via host-provided interfaces

## Project Status

Early-stage library. Interfaces and behavior may change before `v1.0.0`.

## Repository Layout

- `skillengine/`: Go module (`github.com/xorType/skillsengine`)

## Requirements

- Go 1.25+

If you publish under a different GitHub org/repo path, update `skillengine/go.mod` module path before release.

## Quick Start

```bash
git clone https://github.com/<your-org>/skills-engine.git
cd skills-engine/skillengine
go test ./...
```

## Runnable Example

```bash
cd skillengine
go run ./examples/minimal --recipient you@example.com
```

Example defaults:
- `--subject` defaults to `Example Campaign Brief`
- `--recipient` is required
- Use `--skip-email` to test generation without MCP/Gmail delivery

## Minimal Usage

```go
cfg := skillengine.DefaultAdaptiveConfig()
engine := skillengine.NewEngine(llmProvider, skillStore, runStore, cfg)

skill, err := engine.GenerateSkill(ctx,
    "Status Report",
    "Weekly status report",
    "Summarize progress, blockers, and next steps",
    "## Summary\n## Risks\n## Next Steps",
    "user-123",
)
if err != nil {
    // handle error
}

events, err := engine.Run(ctx, skillengine.RunRequest{
    SkillID: skill.ID,
    UserID:  "user-123",
    Sources: []skillengine.SourceInput{
        {ID: "doc-1", Name: "notes.md", Content: "..."},
    },
})
if err != nil {
    // handle error
}

for ev := range events {
    _ = ev
}
```

## Development

```bash
cd skillengine
gofmt -w .
go vet ./...
go test ./...
```

## Release

See [RELEASE.md](RELEASE.md) for a `v0.1.0` publish checklist.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md).

## Security

See [SECURITY.md](SECURITY.md).

## License

MIT, see [LICENSE](LICENSE).
