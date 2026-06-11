# Auto-update

**Date:** 2026-06-10
**Status:** Approved

## Motivation

Atrium ships as a single binary via GoReleaser to GitHub Releases, installed by
`install.sh` into `~/.local/bin`. Today there is no update path other than
re-running the install script by hand, which nobody does until something
breaks. The release pipeline already provides everything an updater needs:
predictable archive names (`atrium_<version>_<os>_<arch>.tar.gz`/`.zip`), a
`checksums.txt`, and a version stamped into the binary at build time
(`main.version`, `dev` for local builds).

The model follows Claude Code: updates are fetched and installed **in the
background**, and take effect on the **next launch**. The running TUI, the
daemon, and live agent sessions are never disturbed — on Unix the swap is an
atomic rename and running processes keep the old inode; the new binary only
matters at the next exec.

## Scope

**In scope (v1):**
- Startup update check (cached, non-blocking) with an in-TUI hint.
- Explicit `atrium update` subcommand (with `--check` dry-run).
- Opt-in fully automatic mode: background download + verify + swap, then a
  "restart to apply" notice.
- Config field `auto_update` with modes `notify` (default) / `auto` / `off`.

**Out of scope (deliberately):**
- Auto-restarting the TUI after an update.
- In-binary cosign/sigstore verification. Checksum verification over HTTPS
  from GitHub is the v1 trust baseline — identical to what `install.sh`
  provides today. Cosign artifacts remain available for manual verification.
- Update checks or updates initiated by the daemon.
- Channel/prerelease selection, downgrade support, delta updates.

## Approach

Use **`creativeprojects/go-selfupdate`** for the mechanics. It is purpose-built
for this stack: GitHub Releases source, OS/arch asset detection matching
GoReleaser naming, tar.gz/zip extraction, `checksums.txt` validation, and
atomic executable replacement (wrapping `minio/selfupdate`, including the
Windows rename-the-running-exe-aside quirk).

Alternatives considered:
- *Hand-rolled stdlib + `minio/selfupdate`*: more code to own and test (asset
  matching across six platform combos is the fiddly part) for no added value.
- *Shell out to `install.sh`*: no Windows, no checksum verification, fragile,
  and cannot report structured progress to the TUI.

## Architecture

```
internal/update/        new package — all update logic lives here
  check.go              Check(currentVersion) → (*Release, error)
  apply.go              Apply(release) error — download, verify, atomic swap
  cache.go              24h check-cache (update-check.json in the data dir)
config/config.go        + AutoUpdate string `json:"auto_update,omitempty"`
                          ("notify" / "auto" / "off"; empty or unknown
                          values are treated as "notify")
main.go                 + `atrium update` Cobra subcommand (--check flag)
app/                    startup goroutine → tea.Msg → hint-bar / toast notice
```

### Check

- `GET https://api.github.com/repos/ZviBaratz/atrium/releases/latest` — this
  endpoint already excludes drafts and prereleases, matching the release
  pipeline's `prerelease: auto, draft: true` flow.
- Semver comparison against `main.version`. A `dev` version (or any version
  containing a prerelease suffix) makes the updater fully inert.
- Results cached in `update-check.json` in the data dir (path derived from
  `config.GetConfigDir()`, never hardcoded) with a 24-hour TTL. While the
  cache is fresh the network is never touched: a known-newer release is served
  as a version-only (unresolved) `Release`, which is all the notify hint
  needs; the resolved install handle is fetched only when the cache expires.
  Failed checks are themselves recorded and retried after a one-hour backoff,
  so an offline or rate-limited machine doesn't re-query on every launch.
  Together these respect GitHub's 60 requests/hour unauthenticated rate
  limit. `atrium update` bypasses the cache. A cross-process flock serializes
  concurrent appliers (e.g. a TUI auto-install racing `atrium update`).

### Apply

- Pre-flight: resolve `os.Executable()` through symlinks; verify the target
  is writable. If not (e.g. a future package-manager install), fail with
  guidance in the CLI, and silently degrade `auto` to `notify` in the TUI.
- Download the matching archive, validate against `checksums.txt` (mandatory:
  no match → abort, old binary untouched), extract, atomic rename into place.
- The swap either fully happens or doesn't; there is no partial state.

### Entry points

1. **TUI startup** (`app/`): if version is a clean release and mode ≠ `off`,
   fire exactly one goroutine; its result arrives as a `tea.Msg`.
   - `notify`: quiet hint ("v0.X.Y available — run `atrium update`").
   - `auto`: a network-resolved release stages the download as a second
     command (so an "updating…" notice renders during the transfer); on
     success show "✓ updated to v0.X.Y — restart atrium to apply". The
     notices are distinct: *available* vs *updating* vs *installed, restart
     needed*. A notice that arrives while a modal overlay owns the screen is
     buffered and re-delivered once the hint bar is back.
   - Every failure (network, rate limit, checksum, permissions) is log-only.
     The TUI never blocks on the network and never surfaces updater errors.
2. **`atrium update`**: explicit and verbose — prints current/latest versions,
   download progress, and errors. Exits non-zero on failure. `--check` reports
   without applying. Works regardless of the `auto_update` mode (only a `dev`
   build refuses).
3. **Daemon**: no participation. The TUI already owns the daemon lifecycle;
   keeping a single updater process avoids two processes racing to rename the
   same binary.

### Accepted version skew

After a background swap, a still-running TUI that restarts the daemon spawns
the *new* binary under an *old* TUI. The TUI↔daemon contract today is "poll
stored instances and tap Enter on prompts", which is insensitive to this skew.
If that contract ever grows a real protocol, revisit this assumption.

Concurrent TUIs could both attempt an auto-download; the rename-based swap
makes the race harmless (last writer wins, both write identical bytes). No
locking in v1.

## Error handling

| Failure | CLI (`atrium update`) | TUI (startup check) |
|---|---|---|
| Network / rate limit | error, exit 1 | log only |
| No matching asset | error, exit 1 | log only |
| Checksum mismatch | error, exit 1, binary untouched | log only |
| Binary not writable | error + guidance | `auto` degrades to `notify` |
| `dev` build | refuses with message | check skipped entirely |

## Testing

- `httptest` fake of the GitHub releases API for Check; unit tests for semver
  comparison (older / equal / newer / prerelease / dev) and cache TTL
  (fresh / stale / missing / corrupt).
- Apply tested against temp-dir fake binaries and archives (success, checksum
  mismatch leaves the original intact, unwritable target).
- All tests hermetic: temp `HOME`, no real data dir, no real network (per the
  project rule in CLAUDE.md).
- `app/` integration: the startup check goroutine is behind an interface so
  app tests can fake it; verify the right notice for notify vs auto modes.
