package git

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestDiff_NoBaseCommitSHA: without a resolved base commit, Diff/DiffNumstat return
// the recognizable errBaseCommitNotSet and never shell out to git (the zero-value
// worktree has no repo). The message is a cross-package contract — Instance.UpdateDiffStats
// and the poll handler substring-match "base commit SHA not set" — so it must not drift.
func TestDiff_NoBaseCommitSHA(t *testing.T) {
	wt := &Worktree{} // baseCommitSHA == ""

	for _, tc := range []struct {
		name string
		got  *DiffStats
	}{
		{"Diff", wt.Diff()},
		{"DiffNumstat", wt.DiffNumstat()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !errors.Is(tc.got.Error, errBaseCommitNotSet) {
				t.Fatalf("%s().Error = %v, want errBaseCommitNotSet", tc.name, tc.got.Error)
			}
			if !strings.Contains(tc.got.Error.Error(), "base commit SHA not set") {
				t.Errorf("%s() error %q must contain the cross-package contract string", tc.name, tc.got.Error)
			}
			if !tc.got.IsEmpty() {
				t.Errorf("%s() should return empty stats, got %+v", tc.name, tc.got)
			}
		})
	}
}

func TestParseNumstat(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		wantAdded   int
		wantRemoved int
		wantFiles   int
	}{
		{
			name:        "empty output",
			input:       "",
			wantAdded:   0,
			wantRemoved: 0,
			wantFiles:   0,
		},
		{
			name:        "single file",
			input:       "3\t1\tfoo.go\n",
			wantAdded:   3,
			wantRemoved: 1,
			wantFiles:   1,
		},
		{
			name:        "multiple files sum correctly",
			input:       "3\t1\tfoo.go\n10\t2\tbar/baz.go\n",
			wantAdded:   13,
			wantRemoved: 3,
			wantFiles:   2,
		},
		{
			name:        "binary files count but skip line totals",
			input:       "5\t0\tfoo.go\n-\t-\timage.png\n2\t2\tbar.go\n",
			wantAdded:   7,
			wantRemoved: 2,
			wantFiles:   3,
		},
		{
			name:        "path with tabs is preserved via SplitN",
			input:       "4\t4\tpath\twith\ttabs.go\n",
			wantAdded:   4,
			wantRemoved: 4,
			wantFiles:   1,
		},
		{
			name:        "trailing newlines do not add garbage",
			input:       "1\t0\ta.go\n\n\n",
			wantAdded:   1,
			wantRemoved: 0,
			wantFiles:   1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotAdded, gotRemoved, gotFiles := parseNumstat(tt.input)
			if gotAdded != tt.wantAdded || gotRemoved != tt.wantRemoved || gotFiles != tt.wantFiles {
				t.Errorf("parseNumstat(%q) = (%d, %d, %d), want (%d, %d, %d)",
					tt.input, gotAdded, gotRemoved, gotFiles, tt.wantAdded, tt.wantRemoved, tt.wantFiles)
			}
		})
	}
}

func TestParseLeftRightCount(t *testing.T) {
	// `git rev-list --left-right --count <baseRef>...HEAD` prints "<behind>\t<ahead>":
	// left side = commits in baseRef not in HEAD (base moved on), right side = commits
	// in HEAD not in baseRef (session progress).
	tests := []struct {
		name       string
		input      string
		wantBehind int
		wantAhead  int
		wantOK     bool
	}{
		{name: "ahead and behind", input: "3\t2\n", wantBehind: 3, wantAhead: 2, wantOK: true},
		{name: "no divergence", input: "0\t0\n", wantBehind: 0, wantAhead: 0, wantOK: true},
		{name: "ahead only", input: "0\t5\n", wantBehind: 0, wantAhead: 5, wantOK: true},
		{name: "no trailing newline", input: "1\t4", wantBehind: 1, wantAhead: 4, wantOK: true},
		{name: "empty output is not ok", input: "", wantOK: false},
		{name: "malformed output is not ok", input: "garbage\n", wantOK: false},
		{name: "single field is not ok", input: "3\n", wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			behind, ahead, ok := parseLeftRightCount(tt.input)
			if ok != tt.wantOK || (ok && (behind != tt.wantBehind || ahead != tt.wantAhead)) {
				t.Errorf("parseLeftRightCount(%q) = (%d, %d, %v), want (%d, %d, %v)",
					tt.input, behind, ahead, ok, tt.wantBehind, tt.wantAhead, tt.wantOK)
			}
		})
	}
}

func TestCountDiffFiles(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  int
	}{
		{name: "empty diff", input: "", want: 0},
		{
			name:  "single file",
			input: "diff --git a/foo.go b/foo.go\n@@ -1 +1 @@\n-old\n+new\n",
			want:  1,
		},
		{
			name: "two files",
			input: "diff --git a/foo.go b/foo.go\n+x\n" +
				"diff --git a/bar.go b/bar.go\n+y\n",
			want: 2,
		},
		{
			name:  "added-line content starting with diff word is not miscounted",
			input: "diff --git a/foo.go b/foo.go\n+diff --git this is code\n",
			want:  1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := countDiffFiles(tt.input); got != tt.want {
				t.Errorf("countDiffFiles(%q) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

// TestComputeRepoStats_RevListCacheHit verifies that a fresh cache entry causes
// computeRepoStats to populate Commits/Behind straight from the cache instead of
// recomputing. git status still runs regardless of cache state; with an empty path
// it fails silently, leaving Dirty at its zero value — the test only asserts the
// cached fields.
func TestComputeRepoStats_RevListCacheHit(t *testing.T) {
	wt := &Worktree{}
	wt.statsCacheMu.Lock()
	wt.statsCache = repoStatsEntry{
		commits:    3,
		behind:     1,
		computedAt: time.Now(),
	}
	wt.statsCacheMu.Unlock()

	stats := &DiffStats{}
	wt.computeRepoStats(stats, "")

	if stats.Commits != 3 {
		t.Errorf("rev-list cache hit: Commits = %d, want 3", stats.Commits)
	}
	if stats.Behind != 1 {
		t.Errorf("rev-list cache hit: Behind = %d, want 1", stats.Behind)
	}
}

// TestComputeRepoStats_RevListCacheMiss verifies that an expired cache entry is not
// propagated. The bare Worktree has no baseRef or baseCommitSHA, so revListCounts
// short-circuits to a zero result rather than running a subprocess — the stale 99
// values must be overwritten with that fresh zero.
func TestComputeRepoStats_RevListCacheMiss(t *testing.T) {
	wt := &Worktree{}
	wt.statsCacheMu.Lock()
	wt.statsCache = repoStatsEntry{
		commits:    99,
		behind:     99,
		computedAt: time.Now().Add(-(revListCacheTTL + time.Second)),
	}
	wt.statsCacheMu.Unlock()

	stats := &DiffStats{}
	wt.computeRepoStats(stats, "")

	if stats.Commits == 99 || stats.Behind == 99 {
		t.Errorf("stale rev-list cache propagated: Commits=%d Behind=%d, want != 99", stats.Commits, stats.Behind)
	}
}

// TestComputeRepoStats_RevListErrorDoesNotCache verifies that a failed rev-list is
// not written to the cache. With a base ref set but a non-repo worktree path, the
// subprocess errors; the cache must stay empty so the next tick retries immediately
// instead of serving a zero for the whole TTL.
func TestComputeRepoStats_RevListErrorDoesNotCache(t *testing.T) {
	wt := &Worktree{baseRef: "main"}

	stats := &DiffStats{}
	// t.TempDir() is not a git repo, so `git -C <dir> rev-list` fails.
	wt.computeRepoStats(stats, t.TempDir())

	wt.statsCacheMu.Lock()
	defer wt.statsCacheMu.Unlock()
	if !wt.statsCache.computedAt.IsZero() {
		t.Errorf("failed rev-list poisoned the cache: computedAt = %v, want zero", wt.statsCache.computedAt)
	}
}

// TestComputeRepoStats_DirtyCacheHit verifies that a fresh dirtyComputedAt entry
// causes computeRepoStats to serve Dirty from the cache without re-running git
// status. The rev-list cache is stale here, so this also guards against a rev-list
// refresh clobbering the dirty fields when it stores its result.
func TestComputeRepoStats_DirtyCacheHit(t *testing.T) {
	wt := &Worktree{}
	wt.statsCacheMu.Lock()
	wt.statsCache = repoStatsEntry{
		dirty:           true,
		dirtyComputedAt: time.Now(),
	}
	wt.statsCacheMu.Unlock()

	stats := &DiffStats{}
	// A nonexistent dir makes any unexpected git invocation fail loudly ("" would
	// make git -C a no-op and silently inherit the test process's cwd).
	wt.computeRepoStats(stats, filepath.Join(t.TempDir(), "missing"))
	if !stats.Dirty {
		t.Error("dirty cache hit: Dirty = false, want true")
	}
}

// TestComputeRepoStats_DirtyCacheMiss verifies that a stale dirtyComputedAt entry
// is not propagated. t.TempDir() is not a git repo, so git status fails silently,
// leaving Dirty at its zero value — the stale true must not be carried forward.
func TestComputeRepoStats_DirtyCacheMiss(t *testing.T) {
	wt := &Worktree{}
	wt.statsCacheMu.Lock()
	wt.statsCache = repoStatsEntry{
		dirty:           true,
		dirtyComputedAt: time.Now().Add(-(dirtyCacheTTL + time.Second)),
	}
	wt.statsCacheMu.Unlock()

	stats := &DiffStats{}
	wt.computeRepoStats(stats, t.TempDir()) // non-git dir → git status fails silently → Dirty=false
	if stats.Dirty {
		t.Error("stale dirty cache propagated: Dirty = true, want false")
	}
}
