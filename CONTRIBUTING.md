# Contributing

## Local Setup

1. Install Go 1.25+.
2. Run:

```bash
cd skillengine
go test ./...
```

## Development Workflow

1. Create a branch from `main`.
2. Keep changes scoped and focused.
3. Run before opening a PR:

```bash
cd skillengine
gofmt -w .
go vet ./...
go test ./...
```

## Pull Requests

- Explain behavior changes and tradeoffs.
- Add or update tests for behavior changes.
- Keep PRs reviewable (prefer small, incremental changes).

## Code Style

- Follow standard Go conventions.
- Avoid host-specific dependencies in the `skillengine` package.
- Preserve interface-driven architecture (`LLMProvider`, `SkillStore`, `RunStore`).
