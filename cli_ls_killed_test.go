package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/internal/undo"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// journalEntry commits one killed-session record and returns it as stored.
//
// Snapshot is non-empty because Entry.Restorable requires it — an entry with no
// snapshot records a kill nothing can rebuild, so offering it would promise a restore
// that fails. A test that forgot it would assert about an empty listing and pass for
// the wrong reason.
func journalEntry(t *testing.T, e undo.Entry) undo.Entry {
	t.Helper()
	if len(e.Snapshot) == 0 {
		e.Snapshot = json.RawMessage(`{"title":"x"}`)
	}
	if e.SHA == "" && !e.Direct {
		e.SHA = "0123456789abcdef0123456789abcdef01234567"
	}
	written, err := undo.Write(e)
	require.NoError(t, err)
	return written
}

func lsKilled(t *testing.T, jsonOut bool) string {
	t.Helper()
	var out bytes.Buffer
	require.NoError(t, runLs(&out, jsonOut, true))
	return out.String()
}

func killedJSONOut(t *testing.T) []map[string]any {
	t.Helper()
	var got []map[string]any
	require.NoError(t, json.Unmarshal([]byte(lsKilled(t, true)), &got))
	return got
}

// TestLsKilledPublishesTheJournalEntry is the contract: everything a human needs to
// find a killed session again — which entry it is, what it was, and what branch its
// retained ref holds.
func TestLsKilledPublishesTheJournalEntry(t *testing.T) {
	sandboxDataDir(t)
	e := journalEntry(t, undo.Entry{
		Title: "fix-auth", Display: "auth fix", Path: "/repo/web", Branch: "zvi/fix-auth",
	})

	got := killedJSONOut(t)
	require.Len(t, got, 1)
	assert.Equal(t, e.ID, got[0]["id"])
	assert.Equal(t, "fix-auth", got[0]["title"])
	assert.Equal(t, "auth fix", got[0]["display_name"])
	assert.Equal(t, "/repo/web", got[0]["path"])
	assert.Equal(t, "zvi/fix-auth", got[0]["branch"])
	assert.NotEmpty(t, got[0]["killed_at"])
}

// TestLsKilledShowsEveryEntryNotJustTheNewest is the whole reason this exists. The
// TUI's undo key offers only the newest restorable batch (undo.LatestBatch), so a
// human returning to several agent-initiated kills can undo one and has no surface
// that even names the rest.
func TestLsKilledShowsEveryEntryNotJustTheNewest(t *testing.T) {
	sandboxDataDir(t)
	now := time.Now()
	journalEntry(t, undo.Entry{Title: "first", Path: "/repo/web", At: now.Add(-3 * time.Hour)})
	journalEntry(t, undo.Entry{Title: "second", Path: "/repo/web", At: now.Add(-2 * time.Hour)})
	journalEntry(t, undo.Entry{Title: "third", Path: "/repo/web", At: now.Add(-time.Hour)})

	got := killedJSONOut(t)
	require.Len(t, got, 3)
	// Newest first: a human wants the kill that just happened, not the oldest one
	// still inside the horizon.
	assert.Equal(t, "third", got[0]["title"])
	assert.Equal(t, "second", got[1]["title"])
	assert.Equal(t, "first", got[2]["title"])
}

// TestLsKilledOmitsWhatCannotBeRestored: offering an entry that cannot come back
// promises a recovery that fails. Restorable is the single predicate for that, so
// this asserts the property through each of its arms rather than re-deriving it.
func TestLsKilledOmitsWhatCannotBeRestored(t *testing.T) {
	for _, tc := range []struct {
		name  string
		entry undo.Entry
	}{
		{"past the horizon", undo.Entry{Title: "old", Path: "/r", At: time.Now().Add(-30 * 24 * time.Hour)}},
		{"superseded by a new session of the same name", undo.Entry{Title: "reused", Path: "/r", Superseded: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sandboxDataDir(t)
			journalEntry(t, tc.entry)
			assert.Empty(t, killedJSONOut(t))
		})
	}
}

// TestLsKilledOmitsAnEntryWithNoPinnedCommits covers Restorable's other arm, which
// needs its own case because journalEntry fills SHA in by default: an entry whose
// commits could not be pinned is a record of a kill, not an offer to undo one.
func TestLsKilledOmitsAnEntryWithNoPinnedCommits(t *testing.T) {
	sandboxDataDir(t)
	journalEntry(t, undo.Entry{Title: "unpinned", Path: "/r", SHA: "-"})
	require.Len(t, killedJSONOut(t), 1, "precondition: a pinned entry is listed")

	sandboxDataDir(t)
	_, err := undo.Write(undo.Entry{
		Title: "unpinned", Path: "/r", Snapshot: json.RawMessage(`{"title":"x"}`),
	})
	require.NoError(t, err)
	assert.Empty(t, killedJSONOut(t), "no SHA means no restore to offer")
}

// TestLsKilledNamesWorkTheRestoreCannotBringBack is the honesty half. Dirty without
// Committed is the one shape where a restore comes back incomplete — the teardown
// could not fold the uncommitted changes into the retained commits, so
// `git worktree remove -f` destroyed them. A listing that did not say so would
// present that entry as an intact undo.
func TestLsKilledNamesWorkTheRestoreCannotBringBack(t *testing.T) {
	sandboxDataDir(t)
	journalEntry(t, undo.Entry{Title: "lost", Path: "/r", Dirty: true})
	journalEntry(t, undo.Entry{Title: "saved", Path: "/r", Dirty: true, Committed: true})

	got := killedJSONOut(t)
	require.Len(t, got, 2)
	byTitle := map[string]map[string]any{}
	for _, row := range got {
		byTitle[row["title"].(string)] = row
	}
	assert.Equal(t, true, byTitle["lost"]["uncommitted_work_lost"])
	assert.Equal(t, false, byTitle["saved"]["uncommitted_work_lost"],
		"a teardown that committed the dirty work loses nothing")
}

// TestLsKilledJSONIsAnArrayWhenEmpty: a consumer iterating the result should not
// have to special-case a fleet nobody has killed anything in.
func TestLsKilledJSONIsAnArrayWhenEmpty(t *testing.T) {
	sandboxDataDir(t)
	assert.Equal(t, "[]", trimmed(lsKilled(t, true)))
}

// TestLsKilledTableNamesTheSessionAndItsRecoverability: the human-facing form has to
// carry the same two facts as the JSON — which session, and whether undoing it brings
// everything back.
func TestLsKilledTableNamesTheSessionAndItsRecoverability(t *testing.T) {
	sandboxDataDir(t)
	journalEntry(t, undo.Entry{Title: "fix-auth", Path: "/repo/web", Branch: "zvi/fix-auth", Dirty: true})

	out := lsKilled(t, false)
	assert.Contains(t, out, "fix-auth")
	assert.Contains(t, out, "zvi/fix-auth")
	assert.Contains(t, out, "uncommitted")
}

// TestLsKilledSaysSoWhenTheJournalIsEmpty: the table form must not print a bare
// header and leave the reader wondering whether it failed.
func TestLsKilledSaysSoWhenTheJournalIsEmpty(t *testing.T) {
	sandboxDataDir(t)
	assert.Contains(t, lsKilled(t, false), "no killed sessions")
}

// TestLsKilledNeitherSweepsNorWrites is the safety property, and it is the reason
// this reads the journal rather than going anywhere near undo.Sweep. A sweep runs
// `git update-ref -d` inside the user's repositories — internal/undo's own package
// doc says a headless process must never do that — and an expired entry omitted from
// this listing must still be omitted by being filtered, not by being deleted.
func TestLsKilledNeitherSweepsNorWrites(t *testing.T) {
	sandboxDataDir(t)
	journalEntry(t, undo.Entry{Title: "old", Path: "/r", At: time.Now().Add(-30 * 24 * time.Hour)})
	dir, err := undo.Dir()
	require.NoError(t, err)
	before := dirEntryNames(t, dir)
	require.NotEmpty(t, before, "precondition: the expired entry is on disk")

	require.Empty(t, killedJSONOut(t), "and is filtered out of the listing")

	assert.Equal(t, before, dirEntryNames(t, dir), "but is still on disk: nothing here deletes")
}

func dirEntryNames(t *testing.T, dir string) []string {
	t.Helper()
	des, err := os.ReadDir(dir)
	require.NoError(t, err)
	names := make([]string, 0, len(des))
	for _, de := range des {
		names = append(names, filepath.Join(dir, de.Name()))
	}
	return names
}

func trimmed(s string) string {
	return string(bytes.TrimSpace([]byte(s)))
}

// TestLsKilledPublishesBranchAsAnEmptyStringForADirectSession is the schema half of the
// honesty this listing is for.
//
// `branch,omitempty` drops the key entirely for a direct session, so `jq -r '.[].branch'`
// prints "null" and `select(.branch == "")` matches nothing — which the README's field
// table says is how you find one. sessionJSON, the sibling schema this mirrors, keeps
// Branch plain for the same reason: `ls --json` promises fields are added and never
// repurposed, and a key present for some rows and absent for others breaks that for
// anyone iterating.
func TestLsKilledPublishesBranchAsAnEmptyStringForADirectSession(t *testing.T) {
	sandboxDataDir(t)
	journalEntry(t, undo.Entry{Title: "notes", Path: "/repo/web", Direct: true})

	got := killedJSONOut(t)
	require.Len(t, got, 1)
	branch, present := got[0]["branch"]
	assert.True(t, present, "the key must be there for every row, or a consumer cannot test it")
	assert.Equal(t, "", branch)
	assert.Equal(t, true, got[0]["direct"])
}

// TestLsKilledTableNamesADirectSessionWithoutABranch: the human form has to say
// something in the branch column for a session that never had one, rather than leave it
// looking truncated.
func TestLsKilledTableNamesADirectSessionWithoutABranch(t *testing.T) {
	sandboxDataDir(t)
	journalEntry(t, undo.Entry{Title: "notes", Path: "/repo/web", Direct: true})

	out := lsKilled(t, false)
	assert.Contains(t, out, "notes")
	assert.Contains(t, out, "—", "a direct session's missing branch is stated, not blank")
}

// TestLsKilledGroupsTheEntriesOneUndoWouldRestoreTogether is why batch_id is published.
//
// The TUI's undo key restores undo.LatestBatch, which is a BATCH and not one entry — a
// visual-mode kill of four sessions is undone as four. So "the newest entry" is not what
// `U` offers, and a listing that dropped the grouping would leave a reader unable to tell
// which rows come back together from which are separate kills needing recoverByHand.
func TestLsKilledGroupsTheEntriesOneUndoWouldRestoreTogether(t *testing.T) {
	sandboxDataDir(t)
	now := time.Now()
	journalEntry(t, undo.Entry{Title: "solo", Path: "/r", At: now.Add(-time.Hour)})
	journalEntry(t, undo.Entry{Title: "a", Path: "/r", BatchID: "batch-1", At: now.Add(-2 * time.Minute)})
	journalEntry(t, undo.Entry{Title: "b", Path: "/r", BatchID: "batch-1", At: now.Add(-time.Minute)})

	got := killedJSONOut(t)
	require.Len(t, got, 3)
	byTitle := map[string]map[string]any{}
	for _, row := range got {
		byTitle[row["title"].(string)] = row
	}
	assert.Equal(t, "batch-1", byTitle["a"]["batch_id"])
	assert.Equal(t, "batch-1", byTitle["b"]["batch_id"])
	assert.NotContains(t, byTitle["solo"], "batch_id",
		"a session killed on its own belongs to no batch, which is different from an empty one")
}
