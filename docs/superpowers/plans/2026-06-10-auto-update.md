# Auto-update Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Atrium checks GitHub Releases for newer versions at TUI startup (notify by default, opt-in background auto-install) and gains an explicit `atrium update` subcommand.

**Architecture:** A new `internal/update` package owns all update logic: a cached network check (24h TTL, cache file in the data dir), semver gating, and an atomic binary swap via the `creativeprojects/go-selfupdate` library (checksum-validated against GoReleaser's `checksums.txt`). `config.Config` gains an `auto_update` mode (`notify`/`auto`/`off`, default notify). The TUI fires one startup goroutine via a `tea.Cmd` and surfaces results as transient menu notices; `atrium update` is a thin Cobra wrapper over the same package. Spec: `docs/superpowers/specs/2026-06-10-auto-update-design.md`.

**Tech Stack:** Go, Bubble Tea, Cobra, `github.com/creativeprojects/go-selfupdate`, `github.com/Masterminds/semver/v3` (already a transitive dep of go-selfupdate).

---

## Conventions for every task

- `go` is NOT on the Bash PATH. Use `GOBIN=/home/zvi/.local/share/mise/installs/go/latest/bin` — invoke as `$GOBIN/go ...` and `GO=$GOBIN/go just ...`. Spelled out in each command below.
- Run tests with `env -u CLAUDE_CONFIG_DIR` (this branch may predate the PR #100 hermeticity fix for that env var).
- Tests must be hermetic: never touch the real `~/.atrium`. Any test that can reach the cache/config sets `t.Setenv("HOME", t.TempDir())`; the package `TestMain` sandboxes `HOME` as a safety net (same pattern as `app/app_test.go`).
- All commits: conventional, lowercase.

---

### Task 1: Add the go-selfupdate dependency

**Files:**
- Modify: `go.mod`, `go.sum`

- [ ] **Step 1: Fetch the dependency**

```bash
cd /home/zvi/.atrium/worktrees/zvi/auto-update_18b7c959292917ff
/home/zvi/.local/share/mise/installs/go/latest/bin/go get github.com/creativeprojects/go-selfupdate@latest
```

Expected: `go: added github.com/creativeprojects/go-selfupdate v1.x.x` (plus indirect deps such as `Masterminds/semver/v3`, `code.gitea.io/sdk`, `google/go-github`, `xanzy/go-gitlab`).

- [ ] **Step 2: Verify the module still builds and tests pass**

```bash
GO=/home/zvi/.local/share/mise/installs/go/latest/bin/go just build
env -u CLAUDE_CONFIG_DIR GO=/home/zvi/.local/share/mise/installs/go/latest/bin/go just test
```

Expected: build OK, all tests PASS (the dep is not imported yet; `go.mod` records it as indirect or with a `// indirect`-free require once imported in Task 3).

- [ ] **Step 3: Commit**

```bash
git add go.mod go.sum
git commit -m "chore: add go-selfupdate dependency"
```

---

### Task 2: config — `auto_update` field and mode accessor

**Files:**
- Modify: `config/config.go` (Config struct ~line 187, constants near top, accessor near the other `Get*` helpers)
- Test: `config/config_test.go`

- [ ] **Step 1: Write the failing test**

Append to `config/config_test.go`:

```go
// GetAutoUpdateMode must normalize every input to a valid mode. The default is
// notify; a typo must never silently disable update hints ("off") nor enable
// unattended binary swaps ("auto").
func TestGetAutoUpdateMode(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  string
	}{
		{"empty defaults to notify", "", AutoUpdateNotify},
		{"explicit notify", "notify", AutoUpdateNotify},
		{"auto", "auto", AutoUpdateAuto},
		{"off", "off", AutoUpdateOff},
		{"unknown falls back to notify", "yolo", AutoUpdateNotify},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.AutoUpdate = tc.value
			if got := cfg.GetAutoUpdateMode(); got != tc.want {
				t.Errorf("GetAutoUpdateMode(%q) = %q, want %q", tc.value, got, tc.want)
			}
		})
	}

	var nilCfg *Config
	if got := nilCfg.GetAutoUpdateMode(); got != AutoUpdateNotify {
		t.Errorf("nil config: got %q, want %q", got, AutoUpdateNotify)
	}
}
```

(Check the existing file's import/assert style first; it uses plain `testing` + testify in places — match whichever the file already uses.)

- [ ] **Step 2: Run the test to verify it fails**

```bash
env -u CLAUDE_CONFIG_DIR /home/zvi/.local/share/mise/installs/go/latest/bin/go test ./config/ -run TestGetAutoUpdateMode -v
```

Expected: FAIL — `undefined: AutoUpdateNotify`, `cfg.AutoUpdate undefined`.

- [ ] **Step 3: Implement**

In `config/config.go`, add constants after the `const` block at the top (~line 29):

```go
// AutoUpdate modes (Config.AutoUpdate). See GetAutoUpdateMode for normalization.
const (
	// AutoUpdateNotify checks for a newer release at TUI startup and shows a
	// hint pointing at `atrium update`. The default.
	AutoUpdateNotify = "notify"
	// AutoUpdateAuto downloads, verifies, and stages the new binary in the
	// background; it takes effect on the next launch (the running TUI, daemon,
	// and sessions are never disturbed).
	AutoUpdateAuto = "auto"
	// AutoUpdateOff disables the startup check entirely.
	AutoUpdateOff = "off"
)
```

Add the field at the end of the `Config` struct (after `ClaudeAccounts`, ~line 187):

```go
	// AutoUpdate selects the update behavior at TUI startup: "notify" (default
	// — check for a newer release and hint at `atrium update`), "auto"
	// (download + verify + stage in the background; applied on next launch), or
	// "off". Empty or unrecognized values behave as "notify". The explicit
	// `atrium update` command works regardless of this setting.
	AutoUpdate string `json:"auto_update,omitempty"`
```

Add the accessor near `GetPRCreateDraft` (~line 239):

```go
// GetAutoUpdateMode returns the normalized auto-update mode: AutoUpdateAuto,
// AutoUpdateOff, or AutoUpdateNotify for a nil Config, an empty value, or
// anything unrecognized — a typo must never silently disable update hints nor
// enable unattended binary swaps.
func (c *Config) GetAutoUpdateMode() string {
	if c == nil {
		return AutoUpdateNotify
	}
	switch c.AutoUpdate {
	case AutoUpdateAuto, AutoUpdateOff:
		return c.AutoUpdate
	default:
		return AutoUpdateNotify
	}
}
```

- [ ] **Step 4: Run the test to verify it passes**

```bash
env -u CLAUDE_CONFIG_DIR /home/zvi/.local/share/mise/installs/go/latest/bin/go test ./config/ -v -run TestGetAutoUpdateMode
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add config/config.go config/config_test.go
git commit -m "feat: add auto_update mode to config"
```

---

### Task 3: internal/update — version gating and semver comparison

**Files:**
- Create: `internal/update/version.go`
- Create: `internal/update/version_test.go`
- Create: `internal/update/main_test.go` (package TestMain, hermetic HOME)

- [ ] **Step 1: Write the package TestMain**

Create `internal/update/main_test.go`:

```go
package update

import (
	"os"
	"testing"

	"github.com/ZviBaratz/atrium/log"
)

// TestMain sandboxes HOME so no test can ever read or write the user's real
// data dir (the check cache lives under config.GetConfigDir()). Individual
// cache tests still t.Setenv their own temp HOME for isolation from each other.
func TestMain(m *testing.M) {
	tmpHome, err := os.MkdirTemp("", "atrium-update-test-home-")
	if err == nil {
		_ = os.Setenv("HOME", tmpHome)
	}
	log.Initialize(false)
	code := m.Run()
	log.Close()
	if tmpHome != "" {
		_ = os.RemoveAll(tmpHome)
	}
	os.Exit(code)
}
```

- [ ] **Step 2: Write the failing tests**

Create `internal/update/version_test.go`:

```go
package update

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// Only clean release versions self-update. "dev" (unstamped builds) and
// git-describe strings have no corresponding release asset, and a dev build
// usually outpaces the latest tag.
func TestIsUpdatableVersion(t *testing.T) {
	cases := []struct {
		v    string
		want bool
	}{
		{"0.6.0", true},
		{"1.2.3", true},
		{"dev", false},
		{"", false},
		{"0.6.0-5-gabc123", false},
		{"0.6.0-rc.1", false},
		{"0.6.0-dirty", false},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, IsUpdatableVersion(tc.v), "IsUpdatableVersion(%q)", tc.v)
	}
}

// isNewer drives the cache short-circuit; unparseable input on either side is
// never "newer" (no update prompt on bad data).
func TestIsNewer(t *testing.T) {
	cases := []struct {
		candidate, current string
		want               bool
	}{
		{"0.7.0", "0.6.0", true},
		{"0.6.1", "0.6.0", true},
		{"0.6.0", "0.6.0", false},
		{"0.5.9", "0.6.0", false},
		{"v0.7.0", "0.6.0", true},
		{"0.7.0", "v0.6.0", true},
		{"garbage", "0.6.0", false},
		{"0.7.0", "garbage", false},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, isNewer(tc.candidate, tc.current), "isNewer(%q, %q)", tc.candidate, tc.current)
	}
}
```

- [ ] **Step 3: Run the tests to verify they fail**

```bash
env -u CLAUDE_CONFIG_DIR /home/zvi/.local/share/mise/installs/go/latest/bin/go test ./internal/update/ -v
```

Expected: FAIL to build — `undefined: IsUpdatableVersion`, `undefined: isNewer`.

- [ ] **Step 4: Implement**

Create `internal/update/version.go`:

```go
// Package update implements Atrium's self-update: a cached check of GitHub
// Releases plus a checksum-validated atomic binary swap (via go-selfupdate).
// The swap never disturbs running processes — they hold the old inode — so an
// installed update takes effect on the next launch.
package update

import (
	"strings"

	"github.com/Masterminds/semver/v3"
)

// IsUpdatableVersion reports whether the running build can self-update: only a
// clean release version (e.g. "0.6.0") qualifies. "dev" (unstamped builds) and
// git-describe strings ("0.6.0-5-gabc123") are inert — they have no
// corresponding release asset, and a dev build usually outpaces the latest tag.
func IsUpdatableVersion(v string) bool {
	return v != "" && v != "dev" && !strings.Contains(v, "-")
}

// isNewer reports whether candidate is a strictly newer semver than current.
// Unparseable versions are never newer, so bad data can't trigger an update
// prompt or an auto-install.
func isNewer(candidate, current string) bool {
	cand, err := semver.NewVersion(strings.TrimPrefix(candidate, "v"))
	if err != nil {
		return false
	}
	cur, err := semver.NewVersion(strings.TrimPrefix(current, "v"))
	if err != nil {
		return false
	}
	return cand.GreaterThan(cur)
}
```

Then tidy so `Masterminds/semver/v3` becomes a direct require:

```bash
/home/zvi/.local/share/mise/installs/go/latest/bin/go mod tidy
```

- [ ] **Step 5: Run the tests to verify they pass**

```bash
env -u CLAUDE_CONFIG_DIR /home/zvi/.local/share/mise/installs/go/latest/bin/go test ./internal/update/ -v
```

Expected: PASS (TestIsUpdatableVersion, TestIsNewer).

- [ ] **Step 6: Commit**

```bash
git add internal/update/ go.mod go.sum
git commit -m "feat: add update version gating and semver comparison"
```

---

### Task 4: internal/update — 24h check cache

**Files:**
- Create: `internal/update/cache.go`
- Create: `internal/update/cache_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/update/cache_test.go`:

```go
package update

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCache_RoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	now := time.Now()

	require.NoError(t, saveCache("0.7.0", now))

	e, ok := loadCache(now.Add(time.Hour))
	require.True(t, ok, "an hour-old entry is fresh")
	assert.Equal(t, "0.7.0", e.Latest)
}

func TestCache_MissingFileIsNotFresh(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, ok := loadCache(time.Now())
	assert.False(t, ok)
}

func TestCache_StaleEntryIsNotFresh(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	now := time.Now()
	require.NoError(t, saveCache("0.7.0", now))

	_, ok := loadCache(now.Add(cacheTTL + time.Minute))
	assert.False(t, ok, "an entry past the TTL must force a fresh network check")
}

func TestCache_FutureTimestampIsNotFresh(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	now := time.Now()
	require.NoError(t, saveCache("0.7.0", now.Add(48*time.Hour)))

	_, ok := loadCache(now)
	assert.False(t, ok, "a clock-skewed future entry must not pin the cache forever")
}

func TestCache_CorruptFileIsNotFresh(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir, err := config.GetConfigDir()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, cacheFileName), []byte("{not json"), 0o644))

	_, ok := loadCache(time.Now())
	assert.False(t, ok)
}
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
env -u CLAUDE_CONFIG_DIR /home/zvi/.local/share/mise/installs/go/latest/bin/go test ./internal/update/ -run TestCache -v
```

Expected: FAIL to build — `undefined: saveCache`, `loadCache`, `cacheTTL`, `cacheFileName`.

- [ ] **Step 3: Implement**

Create `internal/update/cache.go`:

```go
package update

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/ZviBaratz/atrium/config"
)

const (
	// cacheFileName lives directly in the data dir, next to config.json.
	cacheFileName = "update-check.json"
	// cacheTTL bounds how often startups hit the network in the common
	// up-to-date case. It also respects GitHub's 60 req/h unauthenticated API
	// rate limit.
	cacheTTL = 24 * time.Hour
)

// cacheEntry records the last completed network check. Latest is the newest
// release version seen (== the current version when up to date).
type cacheEntry struct {
	CheckedAt time.Time `json:"checked_at"`
	Latest    string    `json:"latest"`
}

// cachePath derives the cache location from the data dir — never a hardcoded
// ~/.atrium, because legacy ~/.claude-squad installs keep their directory.
func cachePath() (string, error) {
	dir, err := config.GetConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, cacheFileName), nil
}

// loadCache returns the cached entry and whether it is fresh: present,
// parseable, younger than cacheTTL, and not from the future (clock skew must
// not pin the cache forever). Any failure reads as "no cache".
func loadCache(now time.Time) (cacheEntry, bool) {
	path, err := cachePath()
	if err != nil {
		return cacheEntry{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return cacheEntry{}, false
	}
	var e cacheEntry
	if err := json.Unmarshal(data, &e); err != nil {
		return cacheEntry{}, false
	}
	if e.CheckedAt.After(now) || now.Sub(e.CheckedAt) >= cacheTTL {
		return cacheEntry{}, false
	}
	return e, true
}

// saveCache records a completed check. Best-effort consumers may ignore the
// error: a failed write only means the next startup re-checks the network.
func saveCache(latest string, now time.Time) error {
	path, err := cachePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(cacheEntry{CheckedAt: now, Latest: latest})
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
env -u CLAUDE_CONFIG_DIR /home/zvi/.local/share/mise/installs/go/latest/bin/go test ./internal/update/ -run TestCache -v
```

Expected: PASS (all five).

- [ ] **Step 5: Commit**

```bash
git add internal/update/cache.go internal/update/cache_test.go
git commit -m "feat: add update-check cache with 24h ttl"
```

---

### Task 5: internal/update — Check, CheckCached, and Release.Apply

**Files:**
- Create: `internal/update/update.go`
- Create: `internal/update/update_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/update/update_test.go`. The network is faked by swapping the `checkRemote` package var (same pattern as `app.copyToClipboard`):

```go
package update

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// swapRemote installs a fake network check, restores the real one on cleanup,
// and returns a pointer to the call counter.
func swapRemote(t *testing.T, fake func(context.Context, string) (*Release, error)) *int {
	t.Helper()
	calls := 0
	orig := checkRemote
	checkRemote = func(ctx context.Context, current string) (*Release, error) {
		calls++
		return fake(ctx, current)
	}
	t.Cleanup(func() { checkRemote = orig })
	return &calls
}

// The common case: a fresh cache that says we're current must not touch the
// network at all — that is the cache's entire job.
func TestCheckCached_FreshUpToDateCacheSkipsNetwork(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	require.NoError(t, saveCache("0.6.0", time.Now()))
	calls := swapRemote(t, func(context.Context, string) (*Release, error) {
		return nil, nil
	})

	rel, err := CheckCached(context.Background(), "0.6.0")

	require.NoError(t, err)
	assert.Nil(t, rel)
	assert.Zero(t, *calls, "a fresh up-to-date cache must skip the network")
}

func TestCheckCached_NoCacheQueriesNetworkAndSavesResult(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	calls := swapRemote(t, func(context.Context, string) (*Release, error) {
		return &Release{Version: "0.7.0"}, nil
	})

	rel, err := CheckCached(context.Background(), "0.6.0")

	require.NoError(t, err)
	require.NotNil(t, rel)
	assert.Equal(t, "0.7.0", rel.Version)
	assert.Equal(t, 1, *calls)
	e, ok := loadCache(time.Now())
	require.True(t, ok, "a completed check must refresh the cache")
	assert.Equal(t, "0.7.0", e.Latest)
}

func TestCheckCached_UpToDateResultCachesCurrentVersion(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	swapRemote(t, func(context.Context, string) (*Release, error) {
		return nil, nil // up to date
	})

	rel, err := CheckCached(context.Background(), "0.6.0")

	require.NoError(t, err)
	assert.Nil(t, rel)
	e, ok := loadCache(time.Now())
	require.True(t, ok)
	assert.Equal(t, "0.6.0", e.Latest, "up to date caches the current version")
}

// A fresh cache that already knows about a newer release still consults the
// network: Apply needs the resolved release handle, and this path only recurs
// while an available update stays uninstalled.
func TestCheckCached_PendingNewerReleaseStillQueries(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	require.NoError(t, saveCache("0.7.0", time.Now()))
	calls := swapRemote(t, func(context.Context, string) (*Release, error) {
		return &Release{Version: "0.7.0"}, nil
	})

	rel, err := CheckCached(context.Background(), "0.6.0")

	require.NoError(t, err)
	require.NotNil(t, rel)
	assert.Equal(t, 1, *calls)
}

func TestCheckCached_NetworkErrorPropagates(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	swapRemote(t, func(context.Context, string) (*Release, error) {
		return nil, errors.New("rate limited")
	})

	_, err := CheckCached(context.Background(), "0.6.0")

	require.Error(t, err)
	_, ok := loadCache(time.Now())
	assert.False(t, ok, "a failed check must not refresh the cache")
}

// A Release constructed without going through the network (e.g. by a test or a
// future cached path) cannot be applied; the guard must say so, not panic.
func TestApply_UnresolvedReleaseErrors(t *testing.T) {
	rel := &Release{Version: "9.9.9"}
	err := rel.Apply(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not resolved")
}
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
env -u CLAUDE_CONFIG_DIR /home/zvi/.local/share/mise/installs/go/latest/bin/go test ./internal/update/ -run 'TestCheckCached|TestApply' -v
```

Expected: FAIL to build — `undefined: Release`, `checkRemote`, `CheckCached`.

- [ ] **Step 3: Implement**

Create `internal/update/update.go`:

```go
package update

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ZviBaratz/atrium/log"

	selfupdate "github.com/creativeprojects/go-selfupdate"
)

// repoSlug is the canonical release source. Updates always come from this
// repository regardless of any fork the binary was built from.
const repoSlug = "ZviBaratz/atrium"

// Release is a newer-than-current release resolved from the network. The
// embedded library handles are what Apply needs to download and swap.
type Release struct {
	// Version is the release's clean semver, without a leading "v".
	Version string

	updater *selfupdate.Updater
	release *selfupdate.Release
}

// checkRemote queries the release source. It is a package var so tests can
// fake the network (same pattern as app.copyToClipboard).
var checkRemote = realCheck

// realCheck asks GitHub for the latest release and returns it only when it is
// strictly newer than current; nil means up to date. The checksum validator
// pins every later download to GoReleaser's checksums.txt.
func realCheck(ctx context.Context, current string) (*Release, error) {
	updater, err := selfupdate.NewUpdater(selfupdate.Config{
		Validator: &selfupdate.ChecksumValidator{UniqueFilename: "checksums.txt"},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to initialize updater: %w", err)
	}
	latest, found, err := updater.DetectLatest(ctx, selfupdate.ParseSlug(repoSlug))
	if err != nil {
		return nil, fmt.Errorf("failed to query the latest release: %w", err)
	}
	if !found || latest.LessOrEqual(current) {
		return nil, nil
	}
	return &Release{Version: latest.Version(), updater: updater, release: latest}, nil
}

// Check always queries the network — the `atrium update` path. It returns nil
// when the running version is already the latest.
func Check(ctx context.Context, current string) (*Release, error) {
	return checkRemote(ctx, current)
}

// CheckCached is the TUI-startup check: it consults the 24h cache first so the
// common up-to-date startup never touches the network. A fresh cache that
// already knows about a newer release still queries — Apply needs the resolved
// release handle — but that path only recurs while an available update stays
// uninstalled. The caller is responsible for gating on IsUpdatableVersion.
func CheckCached(ctx context.Context, current string) (*Release, error) {
	now := time.Now()
	if e, ok := loadCache(now); ok && !isNewer(e.Latest, current) {
		return nil, nil
	}
	rel, err := checkRemote(ctx, current)
	if err != nil {
		return nil, err
	}
	latest := current
	if rel != nil {
		latest = rel.Version
	}
	if err := saveCache(latest, now); err != nil {
		log.WarningLog.Printf("failed to save update-check cache: %v", err)
	}
	return rel, nil
}

// Apply downloads the release archive, validates its checksum, and atomically
// replaces the running executable. Running processes keep the old inode; the
// new version takes effect on the next launch. On any failure the old binary
// stays in place (the library rolls back a partial swap).
func (r *Release) Apply(ctx context.Context) error {
	if r.release == nil || r.updater == nil {
		return errors.New("release was not resolved from the network")
	}
	exe, err := selfupdate.ExecutablePath()
	if err != nil {
		return fmt.Errorf("could not locate the running executable: %w", err)
	}
	if err := canReplaceExecutable(exe); err != nil {
		return fmt.Errorf("cannot replace %s: %w", exe, err)
	}
	if err := r.updater.UpdateTo(ctx, r.release, exe); err != nil {
		return fmt.Errorf("update to v%s failed: %w", r.Version, err)
	}
	return nil
}

// canReplaceExecutable verifies the swap can succeed before any download: the
// atomic replace writes a temp file next to exe and renames it into place, so
// write permission on the directory is the real requirement (e.g. a
// package-manager-owned path would fail here, before wasting a download).
func canReplaceExecutable(exe string) error {
	f, err := os.CreateTemp(filepath.Dir(exe), ".atrium-update-probe-*")
	if err != nil {
		return err
	}
	name := f.Name()
	_ = f.Close()
	return os.Remove(name)
}
```

- [ ] **Step 4: Run the whole package, race on**

```bash
env -u CLAUDE_CONFIG_DIR /home/zvi/.local/share/mise/installs/go/latest/bin/go test ./internal/update/ -race -v
```

Expected: PASS (version, cache, check, apply tests).

- [ ] **Step 5: Tidy and commit**

```bash
/home/zvi/.local/share/mise/installs/go/latest/bin/go mod tidy
git add internal/update/ go.mod go.sum
git commit -m "feat: add release check and atomic self-update core"
```

---

### Task 6: `atrium update` subcommand

**Files:**
- Modify: `main.go` (new command var after `versionCmd` ~line 217, flag + registration in `init()` ~line 228)

- [ ] **Step 1: Add the command**

In `main.go`, add to the `var (...)` block after `versionCmd` (~line 217):

```go
	updateCheckOnly bool

	updateCmd = &cobra.Command{
		Use:   "update",
		Short: "Update atrium to the latest release",
		Long: "Checks GitHub releases for a newer version, downloads the matching archive,\n" +
			"verifies its checksum, and atomically replaces the current binary. Running\n" +
			"sessions are not disturbed; the new version takes effect on the next launch.",
		RunE: func(cmd *cobra.Command, args []string) error {
			log.Initialize(false)
			defer log.Close()

			if !update.IsUpdatableVersion(version) {
				return fmt.Errorf("this is a dev build (version %q); self-update only works on release builds — see install.sh", version)
			}
			// Same signal-driven lifecycle as the root command: Ctrl+C aborts a
			// download cleanly instead of leaving the HTTP transfer orphaned.
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			rel, err := update.Check(ctx, version)
			if err != nil {
				return fmt.Errorf("update check failed: %w", err)
			}
			if rel == nil {
				fmt.Printf("%s v%s is the latest version\n", binName, version)
				return nil
			}
			if updateCheckOnly {
				fmt.Printf("v%s is available (current: v%s) — run `%s update` to install\n", rel.Version, version, binName)
				return nil
			}
			fmt.Printf("updating v%s → v%s ...\n", version, rel.Version)
			if err := rel.Apply(ctx); err != nil {
				return fmt.Errorf("update failed: %w", err)
			}
			fmt.Printf("✓ updated to v%s — restart %s to apply\n", rel.Version, binName)
			return nil
		},
	}
```

Add the import `"github.com/ZviBaratz/atrium/internal/update"` to the import block. In `init()` (~line 242):

```go
	updateCmd.Flags().BoolVar(&updateCheckOnly, "check", false,
		"Only check whether a newer release exists; do not install it")
	rootCmd.AddCommand(updateCmd)
```

- [ ] **Step 2: Build and verify the dev-build guard manually**

```bash
GO=/home/zvi/.local/share/mise/installs/go/latest/bin/go just build
./bin/atrium update --check
```

Expected: exits non-zero with `Error: this is a dev build (version "..."); self-update only works on release builds — see install.sh` (the justfile stamps a git-describe version containing `-`).

- [ ] **Step 3: Verify the real check path with a stamped build**

```bash
/home/zvi/.local/share/mise/installs/go/latest/bin/go build -ldflags "-X main.version=0.0.1" -o /tmp/atrium-update-test .
/tmp/atrium-update-test update --check
```

Expected: `v<latest> is available (current: v0.0.1) — run `atrium-update-test update` to install` — proving DetectLatest finds the release and selects an asset for this OS/arch. (Do NOT run the install against a real binary here; `--check` is the verification. If this errors with "no matching asset", the GoReleaser sbom/signature assets are confusing detection — fix by adding `Filters: []string{`^atrium_.*\.(tar\.gz|zip)$`}` to the `selfupdate.Config` in `realCheck` and re-verify.)

```bash
rm /tmp/atrium-update-test
```

- [ ] **Step 4: Run the full suite**

```bash
env -u CLAUDE_CONFIG_DIR GO=/home/zvi/.local/share/mise/installs/go/latest/bin/go just test
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add main.go
git commit -m "feat: add atrium update subcommand"
```

---

### Task 7: TUI startup check (notify / auto modes)

**Files:**
- Create: `app/app_updatecheck.go`
- Create: `app/updatecheck_test.go`
- Modify: `app/app.go` (Run ~line 36, home struct ~line 121, newHome ~line 234, Init ~line 308)
- Modify: `app/app_update.go` (new msg case in Update ~line 36)
- Modify: `main.go` (Run call ~line 98)

- [ ] **Step 1: Write the failing tests**

Create `app/updatecheck_test.go`:

```go
package app

import (
	"context"
	"errors"
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/internal/update"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// swapUpdateFakes replaces the package-level network/swap hooks for one test.
func swapUpdateFakes(t *testing.T,
	check func(context.Context, string) (*update.Release, error),
	apply func(context.Context, *update.Release) error) {
	t.Helper()
	origCheck, origApply := checkForUpdate, applyUpdate
	checkForUpdate = check
	applyUpdate = apply
	t.Cleanup(func() { checkForUpdate, applyUpdate = origCheck, origApply })
}

// newUpdateHome builds a home on a release version with the given mode.
func newUpdateHome(t *testing.T, mode string) *home {
	t.Helper()
	h := newCreateFormHome(t)
	h.version = "0.6.0"
	h.appConfig.AutoUpdate = mode
	return h
}

// Dev builds have no release asset to update to; the command must be inert.
func TestUpdateCheckCmd_DevBuildIsInert(t *testing.T) {
	h := newCreateFormHome(t) // zero-value version ("")
	assert.Nil(t, h.updateCheckCmd())
	h.version = "dev"
	assert.Nil(t, h.updateCheckCmd())
}

func TestUpdateCheckCmd_OffModeIsInert(t *testing.T) {
	h := newUpdateHome(t, config.AutoUpdateOff)
	assert.Nil(t, h.updateCheckCmd())
}

// Notify mode: a newer release produces a hint naming the version and the
// update command; nothing is downloaded.
func TestUpdateCheckCmd_NotifyShowsHint(t *testing.T) {
	h := newUpdateHome(t, config.AutoUpdateNotify)
	applied := false
	swapUpdateFakes(t,
		func(context.Context, string) (*update.Release, error) {
			return &update.Release{Version: "9.9.9"}, nil
		},
		func(context.Context, *update.Release) error { applied = true; return nil },
	)

	cmd := h.updateCheckCmd()
	require.NotNil(t, cmd)
	msg := cmd()
	require.IsType(t, updateCheckDoneMsg{}, msg)

	h.Update(msg)
	assert.False(t, applied, "notify mode must never download")
	require.True(t, h.menu.HasNotice())
	assert.Contains(t, h.menu.String(), "9.9.9")
	assert.Contains(t, h.menu.String(), "atrium update")
}

// Auto mode: the binary is swapped in the background and the notice asks for a
// restart — the running TUI is never disturbed.
func TestUpdateCheckCmd_AutoInstallsAndAsksRestart(t *testing.T) {
	h := newUpdateHome(t, config.AutoUpdateAuto)
	applied := false
	swapUpdateFakes(t,
		func(context.Context, string) (*update.Release, error) {
			return &update.Release{Version: "9.9.9"}, nil
		},
		func(context.Context, *update.Release) error { applied = true; return nil },
	)

	msg := h.updateCheckCmd()()
	done, ok := msg.(updateCheckDoneMsg)
	require.True(t, ok)
	assert.True(t, applied)
	assert.True(t, done.installed)

	h.Update(msg)
	require.True(t, h.menu.HasNotice())
	assert.Contains(t, h.menu.String(), "restart")
}

// A failed auto-install (e.g. unwritable binary) degrades to the notify hint
// instead of surfacing an error: updater problems are log-only in the TUI.
func TestUpdateCheckCmd_AutoApplyFailureDegradesToNotify(t *testing.T) {
	h := newUpdateHome(t, config.AutoUpdateAuto)
	swapUpdateFakes(t,
		func(context.Context, string) (*update.Release, error) {
			return &update.Release{Version: "9.9.9"}, nil
		},
		func(context.Context, *update.Release) error { return errors.New("read-only bin dir") },
	)

	msg := h.updateCheckCmd()()
	done, ok := msg.(updateCheckDoneMsg)
	require.True(t, ok)
	assert.False(t, done.installed)

	h.Update(msg)
	require.True(t, h.menu.HasNotice())
	assert.Contains(t, h.menu.String(), "atrium update")
	assert.False(t, h.errBox.HasError(), "updater failures are never errors in the TUI")
}

// Up to date or check failure: the command resolves to a nil message and the
// UI shows nothing at all.
func TestUpdateCheckCmd_UpToDateAndErrorsAreSilent(t *testing.T) {
	h := newUpdateHome(t, config.AutoUpdateNotify)

	swapUpdateFakes(t,
		func(context.Context, string) (*update.Release, error) { return nil, nil },
		func(context.Context, *update.Release) error { return nil },
	)
	assert.Nil(t, h.updateCheckCmd()(), "up to date yields no message")

	swapUpdateFakes(t,
		func(context.Context, string) (*update.Release, error) { return nil, errors.New("offline") },
		func(context.Context, *update.Release) error { return nil },
	)
	assert.Nil(t, h.updateCheckCmd()(), "a failed check yields no message")
	assert.False(t, h.menu.HasNotice())
}
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
env -u CLAUDE_CONFIG_DIR /home/zvi/.local/share/mise/installs/go/latest/bin/go test ./app/ -run TestUpdateCheckCmd -v
```

Expected: FAIL to build — `h.version undefined`, `undefined: checkForUpdate`, `updateCheckDoneMsg`.

- [ ] **Step 3: Implement the check command**

Create `app/app_updatecheck.go`:

```go
package app

// Startup update check, per config.auto_update: notify shows a hint when a
// newer release exists; auto additionally downloads, verifies, and stages the
// new binary (applied on the next launch — the running TUI, daemon, and
// sessions are never disturbed). Every failure is log-only: the TUI never
// blocks on the network and never surfaces updater errors.

import (
	"context"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/internal/update"
	"github.com/ZviBaratz/atrium/log"

	tea "github.com/charmbracelet/bubbletea"
)

// checkForUpdate / applyUpdate are package vars so tests can fake the network
// and the binary swap (same pattern as copyToClipboard).
var (
	checkForUpdate = update.CheckCached
	applyUpdate    = func(ctx context.Context, r *update.Release) error { return r.Apply(ctx) }
)

// updateCheckDoneMsg reports a startup check that found a newer release.
// installed means auto mode already swapped the binary on disk, so the notice
// asks for a restart instead of pointing at `atrium update`. Up-to-date and
// failed checks never produce this message.
type updateCheckDoneMsg struct {
	version   string
	installed bool
}

// updateCheckCmd returns the one-shot startup update command, or nil when the
// updater is inert (dev/unstamped build, or auto_update=off).
func (m *home) updateCheckCmd() tea.Cmd {
	mode := m.appConfig.GetAutoUpdateMode()
	if mode == config.AutoUpdateOff || !update.IsUpdatableVersion(m.version) {
		return nil
	}
	ctx, current := m.ctx, m.version
	return func() tea.Msg {
		rel, err := checkForUpdate(ctx, current)
		if err != nil {
			log.WarningLog.Printf("update check failed: %v", err)
			return nil
		}
		if rel == nil {
			return nil
		}
		if mode == config.AutoUpdateAuto {
			if err := applyUpdate(ctx, rel); err != nil {
				// Covers the unwritable-binary case: degrade to the notify hint.
				log.WarningLog.Printf("auto-update to v%s failed: %v", rel.Version, err)
				return updateCheckDoneMsg{version: rel.Version}
			}
			return updateCheckDoneMsg{version: rel.Version, installed: true}
		}
		return updateCheckDoneMsg{version: rel.Version}
	}
}
```

- [ ] **Step 4: Plumb the version and wire Init + Update**

In `app/app.go`:

1. `Run` (~line 36) gains a version param; pass it through:

```go
// Run is the main entrypoint into the application. version is the build-time
// stamped version (main.version), which gates the startup update check.
func Run(ctx context.Context, program string, autoYes bool, version string) error {
```

and inside, `newHome(ctx, program, autoYes, version)`.

2. `home` struct (~line 127, next to `program`/`autoYes`):

```go
	// version is the build-stamped binary version ("dev" when unstamped); it
	// gates the startup update check and names the current release in hints.
	version string
```

3. `newHome` (~line 234) signature `func newHome(ctx context.Context, program string, autoYes bool, version string) *home`, and set `version: version,` in the `&home{...}` literal (after `autoYes`).

4. `Init` (~line 308): add the check to the batch:

```go
	return tea.Batch(
		m.spinner.Tick,
		func() tea.Msg {
			time.Sleep(100 * time.Millisecond)
			return previewTickMsg{}
		},
		tickUpdateMetadataCmd(m.snapshotActiveInstances(), m.list.GetSelectedInstance()),
		m.updateCheckCmd(), // nil (inert) is fine: tea.Batch skips nil cmds
	)
```

In `app/app_update.go`, add a case to the `switch` in `Update` (after the `hideErrMsg` case, ~line 43):

```go
	case updateCheckDoneMsg:
		if msg.installed {
			return m, m.handleInfoNotice(fmt.Sprintf("updated to v%s — restart atrium to apply", msg.version))
		}
		return m, m.handleInfoNotice(fmt.Sprintf("v%s available — run `atrium update`", msg.version))
```

(`fmt` is already imported there.)

In `main.go` (~line 98): `return app.Run(ctx, program, autoYes, version)`.

- [ ] **Step 5: Run the tests to verify they pass**

```bash
env -u CLAUDE_CONFIG_DIR /home/zvi/.local/share/mise/installs/go/latest/bin/go test ./app/ -run TestUpdateCheckCmd -v
```

Expected: PASS (all six).

- [ ] **Step 6: Full suite + race (the new goroutine path must be clean)**

```bash
env -u CLAUDE_CONFIG_DIR GO=/home/zvi/.local/share/mise/installs/go/latest/bin/go just test
env -u CLAUDE_CONFIG_DIR /home/zvi/.local/share/mise/installs/go/latest/bin/go test ./app/ -race
```

Expected: PASS. (The pre-existing `newHome()` shadow helper inside `app/app_test.go` is a local closure, unaffected by the package function's new signature — if the build says otherwise, the signature change missed a call site.)

- [ ] **Step 7: Commit**

```bash
git add app/app.go app/app_update.go app/app_updatecheck.go app/updatecheck_test.go main.go
git commit -m "feat: check for updates at startup with notify and auto modes"
```

---

### Task 8: Documentation and final verification

**Files:**
- Modify: `README.md` (after the `#### go install` block ~line 31, and under `### Configuration` ~line 108)

- [ ] **Step 1: README — Updating section**

After the `#### go install` subsection (before `### Prerequisites`), add:

```markdown
#### Updating

```bash
atrium update          # download, verify, and install the latest release
atrium update --check  # just see whether one exists
```

Atrium also checks for new releases when it starts (at most once a day) and
shows a hint when one is available. The running app and your sessions are
never touched — an installed update takes effect the next time you start
`atrium`. Set `"auto_update": "auto"` in `config.json` to install updates
automatically in the background, or `"off"` to disable the startup check.
Builds from source (`go install`, `just build`) report a dev version and never
self-update.
```

- [ ] **Step 2: README — Configuration mention**

Under `### Configuration`, add a subsection (after `#### Auto-attach`, matching its style):

```markdown
#### Auto-update

`auto_update` controls the startup release check: `"notify"` (default) shows a
hint when a newer release exists, `"auto"` downloads and installs it in the
background (applied on the next launch), and `"off"` disables the check. The
explicit `atrium update` command works regardless of this setting.
```

- [ ] **Step 3: Full verification sweep**

```bash
GO=/home/zvi/.local/share/mise/installs/go/latest/bin/go just fmt-check
GO=/home/zvi/.local/share/mise/installs/go/latest/bin/go just vet
GO=/home/zvi/.local/share/mise/installs/go/latest/bin/go just lint
GO=/home/zvi/.local/share/mise/installs/go/latest/bin/go just build
env -u CLAUDE_CONFIG_DIR GO=/home/zvi/.local/share/mise/installs/go/latest/bin/go just test
```

Expected: all pass. (If `just lint` can't find golangci-lint, add `~/go/bin` to PATH — see the pre-push hook note in the project memory.)

- [ ] **Step 4: Commit**

```bash
git add README.md
git commit -m "docs: document atrium update and auto_update config"
```
