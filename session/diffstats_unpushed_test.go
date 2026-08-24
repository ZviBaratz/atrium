package session

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/ZviBaratz/atrium/session/git"

	"github.com/stretchr/testify/require"
)

// unpushedTestData is a minimal git-backed session carrying the given raw diff_stats
// JSON. Status 3 is Paused — the shape that makes this field load-bearing, since the
// poll never recomputes stats for a paused session (see UpdateDiffStats), leaving
// state.json as the only source its kill dialog will ever read.
func unpushedTestData(t *testing.T, diffStats string) InstanceData {
	t.Helper()
	blob := `{
		"title": "my title",
		"path": "/nonexistent/myrepo",
		"status": 3,
		"program": "claude",
		"worktree": {
			"repo_path": "/nonexistent/myrepo",
			"worktree_path": "/nonexistent/wt",
			"session_name": "my title",
			"branch_name": "zvi/my-title"
		},
		"diff_stats": ` + diffStats + `
	}`
	var data InstanceData
	require.NoError(t, json.Unmarshal([]byte(blob), &data))
	return data
}

// TestFromInstanceData_LegacyStateAssumesNothingPushed is the back-compat guard for
// the at-risk count. A state.json written before `unpushed` existed carries only
// `commits`, and the poll skips paused sessions — so a paused-with-WIP session
// restored from such a file would never get its count recomputed. Decoding the
// missing field as a literal 0 would silently report "nothing at risk" and let a
// kill discard real work. Absent must mean unknown, and unknown must assume the
// worst: every commit ahead of base is at risk, which is the pre-fix behavior.
func TestFromInstanceData_LegacyStateAssumesNothingPushed(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	data := unpushedTestData(t, `{"added": 5, "removed": 0, "files_changed": 1, "commits": 2, "behind": 0, "dirty": false}`)
	require.Nil(t, data.DiffStats.Unpushed, "fixture must exercise the absent-key case")

	inst, err := FromInstanceData(context.Background(), data, "zvi/")
	require.NoError(t, err)

	stats := inst.GetDiffStats()
	require.NotNil(t, stats)
	require.Equal(t, 2, stats.Commits)
	require.Equal(t, 2, stats.Unpushed, "legacy state: assume nothing is pushed rather than nothing is at risk")
}

// TestFromInstanceData_PersistedZeroUnpushedIsHonored is the other half: once the
// field is written, a real 0 must survive as 0. This is the CORE-280 shape — commits
// ahead of base, all of them on origin — and it must not be confused with the legacy
// absent-key case above and re-inflated back to Commits.
func TestFromInstanceData_PersistedZeroUnpushedIsHonored(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	data := unpushedTestData(t, `{"added": 5, "removed": 0, "files_changed": 1, "commits": 2, "behind": 0, "unpushed": 0, "dirty": false}`)
	require.NotNil(t, data.DiffStats.Unpushed, "fixture must exercise the present-key case")

	inst, err := FromInstanceData(context.Background(), data, "zvi/")
	require.NoError(t, err)

	stats := inst.GetDiffStats()
	require.NotNil(t, stats)
	require.Equal(t, 2, stats.Commits)
	require.Equal(t, 0, stats.Unpushed, "a persisted 0 means genuinely nothing at risk")
}

// TestInstanceData_UnpushedRoundTrips verifies the count survives a full
// save/load cycle through real JSON, including that a new save always writes the key
// (so the next load is never mistaken for legacy state).
func TestInstanceData_UnpushedRoundTrips(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	data := unpushedTestData(t, `{"commits": 3, "unpushed": 1, "dirty": false}`)
	inst, err := FromInstanceData(context.Background(), data, "zvi/")
	require.NoError(t, err)

	blob, err := json.Marshal(inst.ToInstanceData())
	require.NoError(t, err)
	require.Contains(t, string(blob), `"unpushed":1`, "a save must write the key so the next load is not mistaken for legacy state")

	var decoded InstanceData
	require.NoError(t, json.Unmarshal(blob, &decoded))
	restored, err := FromInstanceData(context.Background(), decoded, "zvi/")
	require.NoError(t, err)
	require.Equal(t, 1, restored.GetDiffStats().Unpushed)
}

// TestToInstanceData_UnpushedZeroIsWritten guards the omitempty foot-gun: the field
// is a *int precisely so absent and 0 differ, so a save of a fully-pushed session
// must still emit "unpushed":0 rather than dropping the key and having the next load
// read it back as legacy-unknown and re-warn.
func TestToInstanceData_UnpushedZeroIsWritten(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	inst := &Instance{ident: identity{title: "t"}, status: Paused, started: true, Path: t.TempDir(), Program: "claude"}
	inst.SetDiffStats(&git.DiffStats{Commits: 2, Unpushed: 0})

	blob, err := json.Marshal(inst.ToInstanceData())
	require.NoError(t, err)
	require.Contains(t, string(blob), `"unpushed":0`)
}

// TestNoteAutoPauseCommit_CountsWIPAsUnpushed verifies pause's auto-WIP commit lands
// in the at-risk count as well as the ahead-of-base one. It is created locally and
// never pushed, so it is exactly the kind of commit a kill destroys — and since the
// poll skips paused sessions, this hand-maintained count is the only thing standing
// between the user and silently discarding it.
func TestNoteAutoPauseCommit_CountsWIPAsUnpushed(t *testing.T) {
	// Never polled: creates the stats and counts the one WIP commit as at-risk.
	fresh := &Instance{}
	fresh.noteAutoPauseCommit()
	require.Equal(t, 1, fresh.GetDiffStats().Commits)
	require.Equal(t, 1, fresh.GetDiffStats().Unpushed)

	// Already polled on a fully-pushed branch: the WIP commit is the only thing at
	// risk, even though the branch is 2 commits ahead of base.
	pushed := &Instance{}
	pushed.SetDiffStats(&git.DiffStats{Commits: 2, Unpushed: 0, Dirty: true})
	pushed.noteAutoPauseCommit()
	require.Equal(t, 3, pushed.GetDiffStats().Commits)
	require.Equal(t, 1, pushed.GetDiffStats().Unpushed, "only the new WIP commit is unpushed")
}

// TestNoteAutoPauseUnwind_DropsUnpushedWithCommits verifies resume's soft-reset of the
// auto-WIP commits reverses both counts symmetrically. Leaving Unpushed inflated would
// durably over-warn a session re-paused before the next poll.
func TestNoteAutoPauseUnwind_DropsUnpushedWithCommits(t *testing.T) {
	i := &Instance{}
	i.SetDiffStats(&git.DiffStats{Commits: 3, Unpushed: 1})
	i.noteAutoPauseUnwind(1)
	require.Equal(t, 2, i.GetDiffStats().Commits)
	require.Equal(t, 0, i.GetDiffStats().Unpushed)
	require.True(t, i.GetDiffStats().Dirty, "unwound commits become pending changes again")

	// Never underflows, even if the unwind count exceeds what was cached.
	j := &Instance{}
	j.SetDiffStats(&git.DiffStats{Commits: 1, Unpushed: 1})
	j.noteAutoPauseUnwind(5)
	require.Equal(t, 0, j.GetDiffStats().Commits)
	require.Equal(t, 0, j.GetDiffStats().Unpushed)
}
