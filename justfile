# Atrium development tasks. Run `just` (or `just --list`) to see recipes.
#
# The Go toolchain is overridable so this works both on a host where `go` isn't
# on PATH (set GO to the absolute path) and inside containers where it is:
#   GO=/path/to/go just test
go := env_var_or_default("GO", "go")

# Make a recipe's arguments available as "$@" so quoting survives into the shell.
# Recipes that keep using {{args}} are unaffected — the setting only changes what
# "$@" means, and nothing else here refers to it. It is a setting rather than the
# per-recipe [positional-arguments] attribute because the setting shipped in just
# 0.9.1 (2021) and the attribute in 1.29.0 (2024); like the assignments above, an
# unsupported construct here is a parse error that breaks EVERY recipe, not one.
set positional-arguments

# Local builds stamp the version from git so `atrium version` tells the truth.
# Release builds get the tag via GoReleaser instead (see .goreleaser.yaml).
version := `git describe --tags --always --dirty 2>/dev/null | sed 's/^v//' || echo dev`
ldflags := "-s -w -X main.version=" + version

# golangci-lint's cache is global — one directory per machine — and Atrium's own
# workflow means many worktrees of this repo exist at once, so a stale entry makes
# `run` report findings against files in a *sibling* worktree (#486). Key the cache
# on this worktree's directory name instead. It stays outside the tree, so there is
# nothing to gitignore. A basename is only a good-enough key: two checkouts sharing
# one would share a cache and could still hit #486. Atrium's own worktrees carry a
# unique suffix, so only hand-made directories can collide.
#
# The cache root is derived from XDG by hand rather than via just's `cache_directory()`
# so that an older `just` still works: these assignments are evaluated at parse time,
# so one function the local `just` lacks breaks *every* recipe, not just `lint`. An
# empty XDG_CACHE_HOME counts as unset, as the spec says it should.
xdg_cache_home := env_var_or_default("XDG_CACHE_HOME", "")
cache_root := if xdg_cache_home == "" { env_var("HOME") / ".cache" } else { xdg_cache_home }
golangci_cache := cache_root / "golangci-lint" / file_name(justfile_directory())

# Show available recipes.
default:
    @just --list

# Build the atrium binary into ./bin/atrium.
build:
    {{go}} build -trimpath -ldflags "{{ldflags}}" -o bin/atrium .

# Build, then run atrium (pass args: `just run -- version`).
run *args: build
    ./bin/atrium {{args}}

# Run the full test suite. Tests sandbox HOME *and* the tmux socket dir, so this
# touches neither the real data dir nor a running Atrium's sessions.
test:
    {{go}} test ./...

# Run the test suite with the race detector.
test-race:
    {{go}} test -race ./...

# NOT part of `test`/`ci`: `go test` skips benchmarks unless -bench is passed, so
# they cost the gate nothing and cannot flake it. They exist to attribute Atrium's
# idle cost — the frame build, the pane classifier (#546) — so a change claiming to
# make idling cheaper has a baseline to beat. Narrow with
# `just bench BenchmarkView ./app/...`. Keep the summary on the last comment line —
# that is the only one `just --list` shows.
# Run the benchmarks (opt-in; never part of the gate).
bench pattern='.' *pkgs='./...':
    {{go}} test -run '^$' -bench '{{pattern}}' -benchmem {{pkgs}}

# End-to-end TUI smoke test (issue #148 spike): drives the real binary through
# vhs to prove the live create→attach→detach layer renders deterministically.
# Opt-in only — NOT part of `test`/`ci`: needs non-Go deps (vhs, ttyd, ffmpeg,
# tmux, jq) and drives a real tmux server. UPDATE=1 refreshes the golden.
smoke:
    GO={{go}} bash test/smoke/run.sh

# Drive a real agent CLI at a width ladder and capture its panes, for re-verifying
# session/agent/ heuristics (#647). Re-driving is the ONLY thing that detects
# heuristic rot — VerifiedVersion is a record, not a tripwire (registry.go's header).
# Opt-in only — NOT part of `test`/`ci`: it starts a real tmux server, drives a real
# authenticated CLI and spends real API turns. Start with `just drive-agent help`.
# Run `just drive-agent reap-all` if an interrupted ladder stranded a server.
#
# ${@+"$@"} rather than {{args}}, and that is load-bearing rather than style. just
# splices {{args}} into the recipe line as raw text, so a quoted argument is re-split
# by the shell: `just drive-agent send 'run: rm -f x'` would reach the script as five
# arguments and type only "run:". Every verb here takes prose — a prompt to send, a
# regex to wait for — so the whole surface is affected, and the failure is silent
# (a truncated prompt is still a prompt). `set positional-arguments` at the top of
# this file is what makes "$@" the recipe's arguments.
#
# The ${@+…} wrapper is for the no-argument case. just runs recipes under `sh -cu`,
# and in bash 3.2 — which is what /bin/sh is on macOS — `"$@"` with no positional
# parameters is an "unbound variable" error under set -u (fixed in 4.4). So a bare
# `just drive-agent`, the form the comment above sends people to, would be the one
# invocation that dies there.
# Drive a real agent CLI at a width ladder and capture its panes (#647).
drive-agent *args:
    bash scripts/drive-agent.sh ${@+"$@"}

# Render the README demo GIFs from the committed tapes (docs/demos/*.tape).
# Opt-in only — NOT part of `test`/`ci`: needs the same non-Go deps as `smoke`
# (vhs, ttyd, ffmpeg, tmux). Writes *.gif next to the tapes.
gifs:
    GO={{go}} bash docs/demos/render.sh

# Run tests with coverage and print the total.
cover:
    {{go}} test -coverprofile=coverage.out ./...
    {{go}} tool cover -func=coverage.out | tail -1

# Always lint through this recipe rather than a bare `golangci-lint run`, which
# shares one global cache across every worktree (#486). To scope a run, pass the
# packages here: `just lint ./ui/...`. Keep the summary on the last comment line —
# that is the only one `just --list` shows.
# Lint with golangci-lint, cached per worktree (see https://golangci-lint.run).
lint *args:
    GOLANGCI_LINT_CACHE="{{golangci_cache}}" golangci-lint run {{args}}

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
