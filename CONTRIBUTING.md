# Contributing

Thank you for considering contributing to our project! This document outlines the process for contributing.

## Development Setup

1. Fork the repository
2. Clone your fork: `git clone https://github.com/YOUR-USERNAME/atrium.git`
3. Add the upstream repository: `git remote add upstream https://github.com/ZviBaratz/atrium.git`
4. Install dependencies: `go mod download`

## Code Standards

Tasks are standardized through the [`justfile`](justfile) — run `just --list` to
see everything. If `go` isn't on your `PATH`, prefix with `GO=/path/to/go`.

### Format & lint

```bash
just fmt        # format all Go code
just fmt-check  # what CI checks (non-mutating)
just lint       # golangci-lint
```

### Testing

```bash
just test       # full suite (sandboxes HOME — safe to run anywhere)
just test-race  # with the race detector
just cover      # with coverage
```

Please include tests for new features or bug fixes. Tests must not read or write
the real Atrium data directory — see `internal/testutil.SandboxHomeMain`.

### Git hooks (recommended)

Install [pre-commit](https://pre-commit.com) hooks so the same checks CI runs
catch issues locally first:

```bash
just hooks      # pre-commit + pre-push hooks
just ci         # run the full local gate sequence on demand
```

Fast checks (gofmt, go vet, secret scan) run on commit; `golangci-lint` and the
`go mod tidy` drift check run on push.

## Commits

Use [Conventional Commits](https://www.conventionalcommits.org/), lowercase
(`feat: …`, `fix: …`). The release changelog is generated from commit prefixes
and published on the [Releases](https://github.com/ZviBaratz/atrium/releases)
page (see [CHANGELOG.md](CHANGELOG.md)).

## Questions?

Feel free to open an issue for any questions about contributing.
