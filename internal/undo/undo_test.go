package undo

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sandbox points HOME at a fresh temp dir so each test gets its own data dir
// (and so no test can ever touch the user's real ~/.atrium, per CLAUDE.md).
func sandbox(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
}

func entry(title string) Entry {
	return Entry{
		Title:    title,
		Path:     "/repo/web",
		RepoPath: "/repo/web",
		Branch:   "zvi/" + title,
		SHA:      "0123456789abcdef0123456789abcdef01234567",
		TmuxName: "atrium_web_" + title,
		Snapshot: json.RawMessage(`{"title":"` + title + `"}`),
	}
}

// TestWriteThenLoadRoundTrip is the basic contract: a written entry comes back
// out of Load with every field intact, and Write stamps the fields the caller
// left blank.
func TestWriteThenLoadRoundTrip(t *testing.T) {
	sandbox(t)

	e, err := Write(entry("fix-auth"))
	require.NoError(t, err)
	assert.NotEmpty(t, e.ID, "Write must mint an ID")
	assert.False(t, e.At.IsZero(), "Write must stamp At")
	assert.Equal(t, currentVersion, e.Version)

	got, err := Load()
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, e.ID, got[0].ID)
	assert.Equal(t, "fix-auth", got[0].Title)
	assert.Equal(t, "zvi/fix-auth", got[0].Branch)
	assert.Equal(t, "atrium_web_fix-auth", got[0].TmuxName)
	assert.JSONEq(t, `{"title":"fix-auth"}`, string(got[0].Snapshot))
}

// TestLoadIsOldestFirst pins the ordering the filename format buys: entries come
// back chronologically, so Latest can take the tail without sorting.
func TestLoadIsOldestFirst(t *testing.T) {
	sandbox(t)

	base := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	for i, name := range []string{"first", "second", "third"} {
		e := entry(name)
		e.At = base.Add(time.Duration(i) * time.Minute)
		_, err := Write(e)
		require.NoError(t, err)
	}

	got, err := Load()
	require.NoError(t, err)
	require.Len(t, got, 3)
	assert.Equal(t, []string{"first", "second", "third"},
		[]string{got[0].Title, got[1].Title, got[2].Title})
}

// TestIDsSortChronologicallyAcrossDigitWidths. The filename is the sort key, so
// the nanosecond count is zero-padded: without it a shorter timestamp sorts after
// a longer one and Latest offers the wrong session. Realistic clocks all produce
// 19 digits, which is exactly why an unpadded format would look correct forever
// and then not be.
func TestIDsSortChronologicallyAcrossDigitWidths(t *testing.T) {
	sandbox(t)

	early := entry("early")
	early.At = time.Unix(0, 12345)
	_, err := Write(early)
	require.NoError(t, err)

	late := entry("late")
	late.At = time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	_, err = Write(late)
	require.NoError(t, err)

	got, err := Load()
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, []string{"early", "late"}, []string{got[0].Title, got[1].Title})
}

// TestLoadOnAVirginDataDir: never having killed anything is the steady state, so
// a missing directory is no entries and no error.
func TestLoadOnAVirginDataDir(t *testing.T) {
	sandbox(t)

	got, err := Load()
	require.NoError(t, err)
	assert.Empty(t, got)
}

// TestZeroTimestampNeverExpires. Treating a zero At as infinitely old would
// silently discard an entry written by a future version that omits the field —
// and for an undo record, discarding means deleting the ref that holds the only
// copy of the user's work.
func TestZeroTimestampNeverExpires(t *testing.T) {
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	assert.False(t, Entry{}.Expired(now))
	assert.False(t, Entry{At: now.Add(-TTL / 2)}.Expired(now))
	assert.True(t, Entry{At: now.Add(-TTL - time.Second)}.Expired(now))
}

// TestForeignVersionIsNotDecodedOnAGuess. An entry from a different atrium is
// skipped rather than half-understood: a wrong snapshot shape would restore a
// session that is not the one that was killed.
func TestForeignVersionIsNotDecodedOnAGuess(t *testing.T) {
	sandbox(t)

	e, err := Write(entry("fix-auth"))
	require.NoError(t, err)
	dir, err := Dir()
	require.NoError(t, err)
	raw, err := os.ReadFile(filepath.Join(dir, e.ID+".json"))
	require.NoError(t, err)
	bumped := strings.Replace(string(raw), `"version":1`, `"version":99`, 1)
	require.NotEqual(t, string(raw), bumped, "the version field must be present to bump")
	require.NoError(t, os.WriteFile(filepath.Join(dir, e.ID+".json"), []byte(bumped), 0o644))

	got, err := Load()
	require.NoError(t, err)
	assert.Empty(t, got, "a foreign-version entry must not be offered for undo")
}

// TestCorruptFileDoesNotBreakLoad: one unreadable file must not cost the user
// the undo records that are still good.
func TestCorruptFileDoesNotBreakLoad(t *testing.T) {
	sandbox(t)

	_, err := Write(entry("good"))
	require.NoError(t, err)
	dir, err := Dir()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "0000000000000000001-deadbeef.json"), []byte("{not json"), 0o644))

	got, err := Load()
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "good", got[0].Title)
}

// TestLatestSkipsExpiredAndSuperseded. Latest is what the undo key reads, so a
// record that must not be acted on has to be invisible here — checking the
// horizon on read means an unswept entry is never offered.
func TestLatestSkipsExpiredAndSuperseded(t *testing.T) {
	sandbox(t)
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)

	old := entry("stale")
	old.At = now.Add(-TTL - time.Hour)
	_, err := Write(old)
	require.NoError(t, err)

	sup := entry("superseded")
	sup.At = now.Add(-time.Hour)
	sup.Superseded = true
	_, err = Write(sup)
	require.NoError(t, err)

	live := entry("live")
	live.At = now.Add(-2 * time.Hour)
	_, err = Write(live)
	require.NoError(t, err)

	got, ok := Latest(now)
	require.True(t, ok)
	assert.Equal(t, "live", got.Title)
}

// TestLatestRefusesAJournalThatIsOnlyStale. The horizon has to be checked on read
// and not only by Sweep: a session killed a week before the TUI was last opened
// must never be offered, however long it is until the next sweep runs. The
// sibling test above happens to reach a live entry first, so this is the case
// that actually exercises the horizon.
func TestLatestRefusesAJournalThatIsOnlyStale(t *testing.T) {
	sandbox(t)
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)

	old := entry("stale")
	old.At = now.Add(-TTL - time.Hour)
	_, err := Write(old)
	require.NoError(t, err)

	_, ok := Latest(now)
	assert.False(t, ok, "an entry past the horizon is not an undo target")

	_, ok = LatestBatch(now)
	assert.False(t, ok)
}

// TestAnEntryWithNoSnapshotIsNotOffered. The entry is written before the git work
// so a crash cannot leak a ref nothing names, which means a record can exist whose
// snapshot never landed. There is nothing to rehydrate from one, so offering it
// would promise a restore that fails after the branch has already been recreated.
func TestAnEntryWithNoSnapshotIsNotOffered(t *testing.T) {
	sandbox(t)
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)

	e := entry("half-written")
	e.At = now.Add(-time.Hour)
	e.Snapshot = nil
	_, err := Write(e)
	require.NoError(t, err)

	got, err := Load()
	require.NoError(t, err)
	require.Len(t, got, 1, "the record is still readable, so the ref can still be swept")

	_, ok := Latest(now)
	assert.False(t, ok, "an entry with no snapshot is not an undo target")
}

// TestAnEntryWhoseBranchWasNeverPinnedIsNotOffered. When retention fails — the
// repository moved, the ref could not be written — the kill still proceeds and the
// record is still kept so it expires normally. But there is nothing to restore
// from, and offering it would fail after the confirmation, with the session
// already gone.
func TestAnEntryWhoseBranchWasNeverPinnedIsNotOffered(t *testing.T) {
	sandbox(t)
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)

	e := entry("unpinned")
	e.At = now.Add(-time.Hour)
	e.SHA = ""
	_, err := Write(e)
	require.NoError(t, err)

	got, err := Load()
	require.NoError(t, err)
	require.Len(t, got, 1, "the record is kept so the sweep can expire it")

	_, ok := Latest(now)
	assert.False(t, ok, "a kill whose commits were never pinned is not an undo target")
}

// TestADirectEntryNeedsNoSHA — the sibling of the test above. A direct session has
// no repository, so the absence of a SHA is its normal state, not a failure.
func TestADirectEntryNeedsNoSHA(t *testing.T) {
	sandbox(t)
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)

	e := entry("scratch")
	e.At = now.Add(-time.Hour)
	e.Direct = true
	e.SHA = ""
	e.RepoPath = ""
	e.Branch = ""
	_, err := Write(e)
	require.NoError(t, err)

	got, ok := Latest(now)
	require.True(t, ok)
	assert.Equal(t, "scratch", got.Title)
}

// TestLatestOnAnEmptyJournal reports absence rather than a zero Entry the caller
// could mistake for a record.
func TestLatestOnAnEmptyJournal(t *testing.T) {
	sandbox(t)

	_, ok := Latest(time.Now())
	assert.False(t, ok)
}

// TestLatestBatchReturnsTheWholeGroup. A visual-mode kill of five sessions is one
// user action; undoing it one press at a time would be drift between what the
// user did and what undo reverses.
func TestLatestBatchReturnsTheWholeGroup(t *testing.T) {
	sandbox(t)
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)

	solo := entry("earlier-solo")
	solo.At = now.Add(-time.Hour)
	_, err := Write(solo)
	require.NoError(t, err)

	for i, name := range []string{"a", "b", "c"} {
		e := entry(name)
		e.BatchID = "batch-1"
		e.At = now.Add(time.Duration(i) * time.Second)
		_, err := Write(e)
		require.NoError(t, err)
	}

	group, ok := LatestBatch(now)
	require.True(t, ok)
	require.Len(t, group, 3)
	assert.Equal(t, []string{"a", "b", "c"},
		[]string{group[0].Title, group[1].Title, group[2].Title})
}

// TestLatestBatchOnASoloKillReturnsJustIt — a batch of one is the common case and
// must not accidentally sweep up unrelated entries with an empty BatchID.
func TestLatestBatchOnASoloKillReturnsJustIt(t *testing.T) {
	sandbox(t)
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)

	for i, name := range []string{"first", "second"} {
		e := entry(name)
		e.At = now.Add(time.Duration(i) * time.Second)
		_, err := Write(e)
		require.NoError(t, err)
	}

	group, ok := LatestBatch(now)
	require.True(t, ok)
	require.Len(t, group, 1)
	assert.Equal(t, "second", group[0].Title)
}

// TestSweepDropsTheRefBeforeTheEntry. The ref is what keeps the commits alive; if
// the entry went first, a crash in between would leak the ref with nothing left
// that names it.
func TestSweepDropsTheRefBeforeTheEntry(t *testing.T) {
	sandbox(t)
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)

	old := entry("stale")
	old.At = now.Add(-TTL - time.Hour)
	written, err := Write(old)
	require.NoError(t, err)

	fresh := entry("fresh")
	fresh.At = now.Add(-time.Hour)
	_, err = Write(fresh)
	require.NoError(t, err)

	dir, err := Dir()
	require.NoError(t, err)

	var dropped []string
	Sweep(now, func(e Entry) {
		dropped = append(dropped, e.Title)
		// The ordering is the point: at the moment the ref is dropped the entry
		// naming it must still be on disk, or a crash here leaks a ref that
		// nothing left can identify.
		assert.FileExists(t, filepath.Join(dir, e.ID+".json"),
			"the entry must outlive the ref it names")
	})

	assert.Equal(t, []string{"stale"}, dropped, "exactly the expired entry's ref is dropped")

	got, err := Load()
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "fresh", got[0].Title)
	assert.NoFileExists(t, filepath.Join(dir, written.ID+".json"))
}

// TestSweepPreservesAForeignVersionEntry. A file this binary cannot read still
// names a ref, and that ref holds the only copy of someone's work. Deleting the
// pointer is worse than leaving a file a newer atrium will understand — so the
// foreign-version case must not fall through to the corrupt-file deletion.
func TestSweepPreservesAForeignVersionEntry(t *testing.T) {
	sandbox(t)

	e, err := Write(entry("from-the-future"))
	require.NoError(t, err)
	dir, err := Dir()
	require.NoError(t, err)
	path := filepath.Join(dir, e.ID+".json")
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path,
		[]byte(strings.Replace(string(raw), `"version":1`, `"version":99`, 1)), 0o644))

	Sweep(time.Now().Add(100*TTL), func(Entry) { t.Fatal("an unreadable entry has no ref we can drop") })
	assert.FileExists(t, path)
}

// TestSweepDropsAnUndecodableFileOnlyOnceItIsStale. A file nobody can decode and
// nobody deletes would be re-read forever; deleting it immediately would race a
// half-written entry from this very moment.
func TestSweepDropsAnUndecodableFileOnlyOnceItIsStale(t *testing.T) {
	sandbox(t)
	dir, err := Dir()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	junk := filepath.Join(dir, "0000000000000000001-deadbeef.json")
	require.NoError(t, os.WriteFile(junk, []byte("{not json"), 0o644))

	Sweep(time.Now(), func(Entry) { t.Fatal("an undecodable file has no ref to drop") })
	assert.FileExists(t, junk, "a fresh unreadable file is left alone")

	Sweep(time.Now().Add(2*TTL), func(Entry) { t.Fatal("an undecodable file has no ref to drop") })
	assert.NoFileExists(t, junk)
}

// TestSweepIgnoresForeignFiles. The sweep deletes things, so it must only ever be
// able to delete files this package wrote — never an editor swap file, and never
// WriteFileAtomic's in-flight temp for a concurrent write in this same directory.
func TestSweepIgnoresForeignFiles(t *testing.T) {
	sandbox(t)
	dir, err := Dir()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	foreign := filepath.Join(dir, ".undo.json.tmp-123")
	require.NoError(t, os.WriteFile(foreign, []byte("{not json"), 0o644))

	Sweep(time.Now().Add(100*TTL), func(Entry) {})
	assert.FileExists(t, foreign)
}

// TestRemoveIsIdempotent — undo removes the entry it consumed, and a retry after a
// partial failure must not turn "already gone" into an error.
func TestRemoveIsIdempotent(t *testing.T) {
	sandbox(t)

	e, err := Write(entry("fix-auth"))
	require.NoError(t, err)
	require.NoError(t, Remove(e.ID))
	require.NoError(t, Remove(e.ID))

	got, err := Load()
	require.NoError(t, err)
	assert.Empty(t, got)
}

// TestMarkSupersededHidesTheEntryWithoutDroppingTheRef. Creation wins over a stale
// undo record, but the commits stay retained — the refusal copy can still point at
// the ref, so "no" means "not automatically", not "gone".
func TestMarkSupersededHidesTheEntryWithoutDroppingTheRef(t *testing.T) {
	sandbox(t)
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)

	e := entry("fix-auth")
	e.At = now.Add(-time.Hour)
	written, err := Write(e)
	require.NoError(t, err)

	require.NoError(t, MarkSuperseded(func(c Entry) bool { return c.Branch == "zvi/fix-auth" }))

	_, ok := Latest(now)
	assert.False(t, ok, "a superseded entry is never offered")

	got, err := Load()
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.True(t, got[0].Superseded)
	assert.Equal(t, written.Ref, got[0].Ref, "the ref is retained, not dropped")
}

// TestClearDropsEveryRefAndEntry — `atrium reset` must leave nothing behind, or the
// retained objects survive forever: CleanupWorktrees enumerates branches from
// `git worktree list`, which cannot see a ref outside refs/heads.
func TestClearDropsEveryRefAndEntry(t *testing.T) {
	sandbox(t)

	for _, name := range []string{"a", "b"} {
		_, err := Write(entry(name))
		require.NoError(t, err)
	}

	var dropped []string
	require.NoError(t, Clear(func(e Entry) { dropped = append(dropped, e.Title) }))
	assert.ElementsMatch(t, []string{"a", "b"}, dropped)

	got, err := Load()
	require.NoError(t, err)
	assert.Empty(t, got)
}

// TestRefIsNamespacedByInstall. Two data dirs can retain into the same project
// repo, and refs/atrium/ already carries refs this program did not write
// (refs/atrium/pr-NNN, from a human review workflow). Anything that sweeps refs
// keys off this prefix, so a shared prefix would let one install delete another's
// only copy of a killed branch.
func TestRefIsNamespacedByInstall(t *testing.T) {
	homeA := t.TempDir()
	homeB := t.TempDir()

	t.Setenv("HOME", homeA)
	a, err := Write(entry("fix-auth"))
	require.NoError(t, err)
	prefixA, err := RefPrefix()
	require.NoError(t, err)

	t.Setenv("HOME", homeB)
	b, err := Write(entry("fix-auth"))
	require.NoError(t, err)
	prefixB, err := RefPrefix()
	require.NoError(t, err)

	assert.NotEqual(t, prefixA, prefixB, "two data dirs must not share a ref subtree")
	assert.True(t, strings.HasPrefix(a.Ref, "refs/atrium/undo/"), "ref: %s", a.Ref)
	assert.True(t, strings.HasPrefix(a.Ref, prefixA), "ref %s must sit under %s", a.Ref, prefixA)
	assert.True(t, strings.HasPrefix(b.Ref, prefixB), "ref %s must sit under %s", b.Ref, prefixB)
	assert.NotEqual(t, a.Ref, b.Ref)

	// Same data dir, same prefix: the ID varies, the subtree does not.
	t.Setenv("HOME", homeA)
	again, err := RefPrefix()
	require.NoError(t, err)
	assert.Equal(t, prefixA, again)
}

// TestADirectSessionGetsNoRef. A direct session runs in the user's own directory
// with no worktree and no branch, so there is nothing to retain. Stamping a
// refname anyway would make a refusal print a ref that was never created, telling
// the user to run a `git branch` that cannot work.
func TestADirectSessionGetsNoRef(t *testing.T) {
	sandbox(t)

	e := entry("scratch")
	e.Direct = true
	e.RepoPath = ""
	e.Branch = ""
	written, err := Write(e)
	require.NoError(t, err)
	assert.Empty(t, written.Ref)

	got, err := Load()
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Empty(t, got[0].Ref)
	assert.True(t, got[0].Restorable(time.Now()), "a direct session is still restorable")
}

// TestEntriesAreOwnerReadableOnly. An entry embeds a whole session snapshot —
// project paths, per-account config directories, the names of token environment
// variables. state.json next door is still 0644 for back-compat reasons; a file
// introduced today carries no such debt, so it starts tight.
func TestEntriesAreOwnerReadableOnly(t *testing.T) {
	sandbox(t)

	e, err := Write(entry("fix-auth"))
	require.NoError(t, err)
	dir, err := Dir()
	require.NoError(t, err)
	info, err := os.Stat(filepath.Join(dir, e.ID+".json"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

// TestWriteKeepsACallerSuppliedID — a caller that already minted an ID (to name
// the ref before the entry exists) must get that same ID back.
func TestWriteKeepsACallerSuppliedID(t *testing.T) {
	sandbox(t)

	e := entry("fix-auth")
	e.ID = "0000000000000000042-cafebabe"
	e.Ref = "refs/atrium/undo/deadbeef/0000000000000000042-cafebabe"
	written, err := Write(e)
	require.NoError(t, err)
	assert.Equal(t, "0000000000000000042-cafebabe", written.ID)
	assert.Equal(t, "refs/atrium/undo/deadbeef/0000000000000000042-cafebabe", written.Ref)
}
