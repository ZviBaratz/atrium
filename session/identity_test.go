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

// The hook directory a live agent writes to is frozen at ITS launch, so after a deep rename
// it no longer matches the session's tmux name. That divergence has to round-trip through
// InstanceData: a TUI restart rebuilds the Session from the post-rename tmux name while the
// agent that outlived the restart keeps writing to the pre-rename directory, and reattach
// restores the pane without re-running the bake that would re-key it. Drop the field and the
// #492 fix holds only until the user quits atrium (#492).
func TestHookNameRoundTripsThroughInstanceData(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	launched := tmux.QualifiedSessionName("myrepo", "before rename")
	current := tmux.QualifiedSessionName("myrepo", "after rename")
	require.NotEqual(t, launched, current, "the fixture must actually exercise the divergence")

	data := InstanceData{
		Title:    "after rename",
		Path:     "/nonexistent/myrepo",
		Status:   Paused, // Paused: rehydrates without touching a tmux server
		Program:  "claude",
		TmuxName: current,
		HookName: launched,
		Worktree: GitWorktreeData{
			RepoPath:     "/nonexistent/myrepo",
			WorktreePath: "/nonexistent/wt",
			SessionName:  "after rename",
			BranchName:   "zvi/after-rename",
		},
	}

	inst, err := FromInstanceData(context.Background(), data, "zvi/")
	require.NoError(t, err)
	require.Equal(t, current, inst.TmuxSessionName(), "the session keeps its post-rename name")
	require.Equal(t, launched, inst.ToInstanceData().HookName, "and re-persists the launched one")

	// The point of persisting it: the restored session reads where the surviving agent writes.
	statePath, err := inst.tmux().HookStateFile()
	require.NoError(t, err)
	require.Contains(t, statePath, string(filepath.Separator)+launched+string(filepath.Separator),
		"the rehydrated session reads the LAUNCHED session's hook directory")
	require.NotContains(t, statePath, current,
		"not the post-rename directory, which nothing has ever written to")
}

// A state.json written before the hook name was persisted has no hook_name field, and neither
// does a session Atrium has never launched. Both resolve to the tmux name — the pre-#492
// answer, and the right one when no live agent is writing anywhere else — but they resolve to
// it by PINNING it at rehydration, not by re-reading the session's name on every access. The
// difference only shows up under a later rename, which is the whole point: see
// tmux.TestRestoredLegacyHookNameSurvivesRename.
func TestFromInstanceDataLegacyHookNameFallback(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	name := tmux.QualifiedSessionName("myrepo", "legacy")
	data := InstanceData{
		Title:    "legacy",
		Path:     "/nonexistent/myrepo",
		Status:   Paused,
		Program:  "claude",
		TmuxName: name,
		Worktree: GitWorktreeData{
			RepoPath:     "/nonexistent/myrepo",
			WorktreePath: "/nonexistent/wt",
			SessionName:  "legacy",
			BranchName:   "zvi/legacy",
		},
	}

	inst, err := FromInstanceData(context.Background(), data, "zvi/")
	require.NoError(t, err)
	require.Equal(t, name, inst.ToInstanceData().HookName,
		"an absent hook_name is pinned to the tmux name, and persists so the next load is not legacy")

	statePath, err := inst.tmux().HookStateFile()
	require.NoError(t, err)
	require.Contains(t, statePath, string(filepath.Separator)+name+string(filepath.Separator),
		"an unfrozen session keys off its tmux name, exactly as before the field existed")
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
	inst := &Instance{ident: identity{title: "x"}, Path: t.TempDir()}

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
		ident:       identity{title: "old-name", branch: wt.GetBranchName()},
		Path:        repoPath,
		status:      Running,
		started:     true,
		gitWorktree: wt,
		tmuxSession: liveTmux(t, "old-name"),
	}

	require.NoError(t, renameAndAdopt(inst, "new-name"))
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
