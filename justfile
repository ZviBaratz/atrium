# Atrium development tasks. Run `just` (or `just --list`) to see recipes.
#
# The Go toolchain is overridable so this works both on a host where `go` isn't
# on PATH (set GO to the absolute path) and inside containers where it is:
#   GO=/path/to/go just test
go := env_var_or_default("GO", "go")

# Local builds stamp the version from git so `atrium version` tells the truth.
# Release builds get the tag via GoReleaser instead (see .goreleaser.yaml).
version := `git describe --tags --always --dirty 2>/dev/null | sed 's/^v//' || echo dev`
ldflags := "-s -w -X main.version=" + version

# Show available recipes.
default:
    @just --list

# Build the atrium binary into ./bin/atrium.
build:
    {{go}} build -trimpath -ldflags "{{ldflags}}" -o bin/atrium .

# Build, then run atrium (pass args: `just run -- version`).
run *args: build
    ./bin/atrium {{args}}

# Run the full test suite. Tests sandbox HOME, so this never touches real state.
test:
    {{go}} test ./...

# Run the test suite with the race detector.
test-race:
    {{go}} test -race ./...

# End-to-end TUI smoke test (issue #148 spike): drives the real binary through
# vhs to prove the live create→attach→detach layer renders deterministically.
# Opt-in only — NOT part of `test`/`ci`: needs non-Go deps (vhs, ttyd, ffmpeg,
# tmux, jq) and drives a real tmux server. UPDATE=1 refreshes the golden.
smoke:
    GO={{go}} bash test/smoke/run.sh

# Run tests with coverage and print the total.
cover:
    {{go}} test -coverprofile=coverage.out ./...
    {{go}} tool cover -func=coverage.out | tail -1

# Lint with golangci-lint (see https://golangci-lint.run for install).
lint:
    golangci-lint run

# Format all Go code.
fmt:
    {{go}} fmt ./...

# Report formatting issues without rewriting (what CI checks).
fmt-check:
    @test -z "$(gofmt -l . | grep -v '^web/')" || { echo "gofmt issues:"; gofmt -l . | grep -v '^web/'; exit 1; }

# Vet for suspicious constructs.
vet:
    {{go}} vet ./...

# Scan for known vulnerabilities (govulncheck). Allowlists the single documented
# advisory GO-2026-5932; fails on anything else (see scripts/govulncheck.sh).
# Needs network for `go run ...@latest`, so — like smoke/snapshot — it is not in `ci`.
vuln:
    GO={{go}} bash scripts/govulncheck.sh

# Tidy go.mod / go.sum.
tidy:
    {{go}} mod tidy

# Install atrium into the Go bin directory.
install:
    {{go}} install -trimpath -ldflags "{{ldflags}}" .

# Build a local snapshot with GoReleaser (no publish). Requires goreleaser.
snapshot:
    goreleaser build --snapshot --clean

# Tag and push a release. Usage: `just release 0.1.0` (run as the ZviBaratz account).
release tag:
    git tag -a "v{{tag}}" -m "Release v{{tag}}"
    git push origin "v{{tag}}"

# Install git hooks (pre-commit + pre-push) via pre-commit.
hooks:
    pre-commit install --install-hooks
    pre-commit install --hook-type pre-push

# Run the local gate sequence mirroring CI (CI also runs race + a macOS job).
ci: build vet fmt-check lint test cover

# Remove build artifacts.
clean:
    rm -rf bin dist coverage.out
