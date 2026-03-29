# Release Playbook (`v0.1.0`)

## One-time repository setup

1. Create the GitHub repository (empty) named `skills-engine`.
2. Update placeholders:
- `README.md` clone URL
- `.github/CODEOWNERS`
- `SECURITY.md` contact email
- `CODE_OF_CONDUCT.md` contact email

## Pre-release checks

Run from repo root:

```bash
cd skillengine
gofmt -w .
go vet ./...
go test ./...
```

## Create first release commit

```bash
git add .
git commit -m "Prepare open-source release v0.1.0"
```

## Push to GitHub

```bash
git branch -M main
git remote add origin https://github.com/xorType/skillsengine/skills-engine.git
git push -u origin main
```

## Tag and publish `v0.1.0`

```bash
git tag v0.1.0
git push origin v0.1.0
```

Then create a GitHub Release from tag `v0.1.0` and use the `CHANGELOG.md` `Unreleased` notes as the initial release notes.
