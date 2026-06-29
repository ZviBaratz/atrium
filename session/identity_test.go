package session

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/session/git"
	"github.com/ZviBaratz/atrium/session/tmux"
	"github.com/stretchr/testify/require"
)

// The tmux session name is persisted state: it must round-trip through
// InstanceData so a restored session is found by exactly the name it was
// created under, regardless of how new names are derived.
func TestTmuxNameRoundTripsThroughInstanceData(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	name := tmux.QualifiedSessionName("myrepo", "my title")
	data := InstanceData{
		Title:    "my title",
		Path:     "/nonexistent/myrepo",
		Status:   Paused, // Paused: rehydrates without touching a tmux server
		Program:  "claude",
		TmuxName: name,
		Worktree: GitWorktreeData{
			RepoPath:     "/nonexistent/myrepo",
			WorktreePath: "/nonexistent/wt",
			SessionName:  "my title",
			BranchName:   "zvi/my-title",
		},
	}

	inst, err := FromInstanceData(context.Background(), data, "zvi/")
	require.NoError(t, err)
	require.Equal(t, name, inst.TmuxSessionName())
	require.Equal(t, name, inst.ToInstanceData().TmuxName)
}

// A state.json written before tmux names were persisted has no tmux_name field.
// Such a session must keep its legacy derived name — that is the name its live
// tmux session still has on the socket — and record it for the next save.
func TestFromInstanceDataLegacyTmuxNameFallback(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	data := InstanceData{
		Title:   "legacy title",
		Path:    "/nonexistent/repo",
		Status:  Paused,
		Program: "claude",
		Worktree: GitWorktreeData{
			RepoPath:     "/nonexistent/repo",
			WorktreePath: "/nonexistent/wt",
			SessionName:  "legacy title",
			BranchName:   "zvi/legacy-title",
		},
	}

	inst, err := FromInstanceData(context.Background(), data, "zvi/")
	require.NoError(t, err)
	legacy := tmux.Prefix() + tmux.SanitizeNameSegment("legacy title")
	require.Equal(t, legacy, inst.TmuxSessionName())
	require.Equal(t, legacy, inst.ToInstanceData().TmuxName, "legacy name must persist on next save")
}

// GroupKey must report the repo-root basename even before Start — a Loading
// instance created from a repo subdirectory has to land in (and be duplicate-
// checked against) the same group it will join once started.
func TestGroupKeyUnstartedResolvesRepoRoot(t *testing.T) {
	repoPath := renameTestRepo(t)
	sub := filepath.Join(repoPath, "nested")
	require.NoError(t, os.MkdirAll(sub, 0o755))

	inst, err := NewInstance(InstanceOptions{Title: "x", Path: sub, Program: "claude"})
	require.NoError(t, err)
	require.Equal(t, filepath.Base(repoPath), inst.GroupKey())
}

// Outside a git repo the group is the directory's own basename, matching how
// direct sessions are grouped in the list.
func TestGroupKeyNonGitFallsBackToBasename(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	inst, err := NewInstance(InstanceOptions{Title: "x", Path: dir, Program: "claude", Direct: true})
	require.NoError(t, err)
	require.Equal(t, filepath.Base(dir), inst.GroupKey())
}

// SetPath re-points a not-yet-started instance, so a group key cached from the
// old path (e.g. by a list render between creation and re-pointing) must not
// survive — the instance would be grouped and duplicate-checked against the
// directory it no longer targets.
func TestSetPathInvalidatesGroupKeyCache(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	oldDir, newDir := t.TempDir(), t.TempDir()
	inst, err := NewInstance(InstanceOptions{Title: "x", Path: oldDir, Program: "claude", Direct: true})
	require.NoError(t, err)
	require.Equal(t, filepath.Base(oldDir), inst.GroupKey())

	require.NoError(t, inst.SetPath(newDir))
	require.Equal(t, filepath.Base(newDir), inst.GroupKey())
}

// GroupKey's cold path shells out to git; concurrent callers racing an empty
// cache (a list render and the background Start goroutine both hitting a Loading
// instance) must run that subprocess at most once, not once per caller. The
// leaf compute-mutex + post-lock re-check collapses them to a single run.
func TestGroupKeyDedupsColdComputation(t *testing.T) {
	var calls atomic.Int64
	orig := repoGroupKey
	t.Cleanup(func() { repoGroupKey = orig })
	repoGroupKey = func(context.Context, string) string {
		calls.Add(1)
		time.Sleep(time.Millisecond) // widen the window so an un-deduped race would recompute
		return "deduped-key"
	}

	// A non-direct, not-yet-started instance: no worktree, not direct, so
	// GroupKey takes the cold (subprocess) branch.
	inst := &Instance{Title: "x", Path: t.TempDir()}

	const n = 32
	var wg sync.WaitGroup
	wg.Add(n)
	results := make([]string, n)
	for k := 0; k < n; k++ {
		go func(idx int) {
			defer wg.Done()
			results[idx] = inst.GroupKey()
		}(k)
	}
	wg.Wait()

	require.Equal(t, int64(1), calls.Load(), "cold computation should run exactly once")
	for _, got := range results {
		require.Equal(t, "deduped-key", got)
	}
}

// A deep rename re-mints the tmux session name in qualified form — this is the
// migration point where a legacy-named session adopts a repo-qualified name.
func TestInstanceRenameMintsQualifiedTmuxName(t *testing.T) {
	repoPath := renameTestRepo(t)
	wt, _, err := git.NewWorktree(context.Background(), repoPath, "old-name")
	require.NoError(t, err)
	require.NoError(t, wt.Setup())

	inst := &Instance{
		Title:       "old-name",
		Path:        repoPath,
		status:      Running,
		started:     true,
		gitWorktree: wt,
		tmuxSession: liveTmux(t, "old-name"),
		Branch:      wt.GetBranchName(),
	}

	require.NoError(t, inst.Rename("new-name"))
	want := tmux.QualifiedSessionName(filepath.Base(repoPath), "new-name")
	require.Equal(t, want, inst.TmuxSessionName())
}

// Storage matching must be composite (Title, Path): with same-titled sessions
// legal across repos, a Title-only match would delete or update the wrong one.
func TestStorageCompositeMatching(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	a := InstanceData{Title: "same", Path: "/repo/a", Status: Paused, Program: "claude"}
	b := InstanceData{Title: "same", Path: "/repo/b", Status: Paused, Program: "claude"}
	seeded, err := json.Marshal([]InstanceData{a, b})
	require.NoError(t, err)

	state := config.DefaultState()
	state.InstancesData = seeded
	storage, err := NewStorage(state)
	require.NoError(t, err)

	require.NoError(t, storage.DeleteInstance("same", "/repo/a"))
	var got []InstanceData
	require.NoError(t, json.Unmarshal(state.GetInstances(), &got))
	require.Len(t, got, 1)
	require.Equal(t, "/repo/b", got[0].Path, "only the matching entry may be deleted")

	require.Error(t, storage.DeleteInstance("same", "/repo/a"), "already gone: must error")
}
