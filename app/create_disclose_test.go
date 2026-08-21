package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	xansi "github.com/charmbracelet/x/ansi"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/internal/outbox"
)

// discloseTo writes a disclosure for a fresh spool record and returns the record path, so
// a reader test does not have to reach a failure to get one on disk.
func discloseTo(t *testing.T, d outbox.Disclosure) string {
	t.Helper()
	record, err := outbox.WriteCreate(outbox.Request{Title: d.Title, Path: "/repo/web"})
	require.NoError(t, err)
	require.NoError(t, outbox.Disclose(record, &d))
	require.NoError(t, outbox.Reject(record, d.Reason))
	return record
}

func orphanDisclosure(title string) outbox.Disclosure {
	return outbox.Disclosure{
		Title:    title,
		Repo:     "/repo/web",
		Branch:   "zvi/" + title,
		Worktree: "/data/worktrees/web/" + title + "-1",
		TmuxName: "atrium-web-" + title,
		Reason:   "the session was created but atrium could not record it: disk full",
	}
}

// TestFlushCreateDisclosuresOpensTheModalAndClearsTheFile is the reader's contract. The
// modal is the surface because the report names a branch, a directory and a tmux session and
// the remedy is for the user to go and remove them; a truncating notice row would name the
// branch and lose the rest.
//
// The file is unlinked at the flush rather than at the read, so a quit inside the window
// before the first preview tick leaves the explanation on disk for the next launch instead of
// erasing it — flushDeferredRecovery's rule, and the whole failure this path exists for.
func TestFlushCreateDisclosuresOpensTheModalAndClearsTheFile(t *testing.T) {
	h := drainHome(t)
	d := orphanDisclosure("fix-auth")
	record := discloseTo(t, d)
	h.pendingCreateDisclosures = loadCreateDisclosures()
	require.Len(t, h.pendingCreateDisclosures, 1, "precondition: the startup read found it")

	h.flushCreateDisclosures()

	require.Equal(t, stateInfo, h.state, "an orphan the user has to clean up is not a toast")
	shown := xansi.Strip(h.textOverlay.Render())
	assert.Contains(t, shown, d.Branch)
	assert.Contains(t, shown, d.Worktree)
	assert.Contains(t, shown, d.TmuxName)
	assert.Empty(t, h.pendingCreateDisclosures, "or the preview tick reopens it forever")
	_, ok := outbox.DisclosureFor(record)
	assert.False(t, ok, "and a shown report must not come back on the next launch")
}

// TestFlushCreateDisclosuresWaitsForTheScreen mirrors flushCustomCommandProblems: a modal
// opened while an overlay owns the screen would clobber it and discard in-progress input.
func TestFlushCreateDisclosuresWaitsForTheScreen(t *testing.T) {
	h := drainHome(t)
	record := discloseTo(t, orphanDisclosure("fix-auth"))
	h.pendingCreateDisclosures = loadCreateDisclosures()

	h.state = stateHelp
	// The returned command is not the assertion: showInfo returns nil unconditionally, so
	// a nil return is as true of a report that fired as of one that waited. The state and
	// the buffer are what separate them.
	h.flushCreateDisclosures()
	assert.Equal(t, stateHelp, h.state, "it must wait while an overlay owns the screen")
	assert.NotEmpty(t, h.pendingCreateDisclosures, "and stay buffered")
	_, ok := outbox.DisclosureFor(record)
	assert.True(t, ok, "with the file still there, so a quit now leaves it for the next launch")

	h.state = stateDefault
	h.flushCreateDisclosures()
	assert.Equal(t, stateInfo, h.state)

	h.state = stateDefault
	h.flushCreateDisclosures()
	assert.Equal(t, stateDefault, h.state, "a second tick must find nothing to reopen")
}

// TestFlushCreateDisclosuresSaysNothingAboutAMarkAlone: every refusal writes a disclosure,
// because the mark is what stops a claim being re-judged (applyCreateClaim), and most
// refusals happen before anything durable was built. The file still has to be cleared, or
// the sweep is the only thing that ever removes it.
func TestFlushCreateDisclosuresSaysNothingAboutAMarkAlone(t *testing.T) {
	h := drainHome(t)
	record := discloseTo(t, outbox.Disclosure{
		Title: "fix-auth", Repo: "/repo/web", Reason: "the title is already used"})
	h.pendingCreateDisclosures = loadCreateDisclosures()
	require.Len(t, h.pendingCreateDisclosures, 1)

	h.flushCreateDisclosures()
	assert.Equal(t, stateDefault, h.state, "the receipt already answered this one")
	_, ok := outbox.DisclosureFor(record)
	assert.False(t, ok, "and it is cleared regardless")
}

// TestLoadCreateDisclosuresDropsWhatItCannotRead: nothing downstream can act on a
// disclosure, so one nobody can decode has no reader to preserve it for — unlike a spool
// record, where the same file means a caller is owed a receipt. Left in place it would be
// re-read on every launch until the horizon.
func TestLoadCreateDisclosuresDropsWhatItCannotRead(t *testing.T) {
	sandboxSpool(t)
	record := discloseTo(t, outbox.Disclosure{Title: "fix-auth", Repo: "/repo/web"})
	// The suffix spelled out from outside the package, which also pins the on-disk name a
	// disclosure is found under: it is part of the wire format, not an implementation
	// detail, since one atrium writes the file and another reads it.
	require.NoError(t, os.WriteFile(record+".disclosure", []byte("{not json"), 0o644))

	assert.Empty(t, loadCreateDisclosures())
	_, ok := outbox.DisclosureFor(record)
	assert.False(t, ok, "and it is not left to be re-read forever")
}

// TestLoadCreateDisclosuresKeepsAnUnreadableMarkOverASurvivingRecord is the other half, and
// it is the one with teeth. The startup read runs before any frame, so this unlink is the
// fastest route back into the hole the whole kind exists to close: a disclosure is also the
// mark that stops the file beside it from being executed, and "I cannot decode it" is not
// the same statement as "it has nothing left to guard".
//
// A version from the future is the case that makes it concrete. A newer atrium wrote the
// mark; this one cannot read it; deleting it hands the drain a record it will build.
func TestLoadCreateDisclosuresKeepsAnUnreadableMarkOverASurvivingRecord(t *testing.T) {
	sandboxSpool(t)
	record, err := outbox.WriteCreate(outbox.Request{Title: "fix-auth", Path: "/repo/web"})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(record+".disclosure",
		[]byte(`{"version":99,"title":"fix-auth"}`), 0o644))

	assert.Empty(t, loadCreateDisclosures(), "there is nothing in it to report")

	_, ok := outbox.DisclosureFor(record)
	assert.True(t, ok, "but the record beside it is still executable, so the mark stays")
}

// TestCreateDisclosureReportNamesTheKillCommandForTheRightSocket: the tmux session is the
// leftover a person can least easily connect back to a title, so the report names the socket
// it is on.
//
// Asserted against a LEGACY data dir, and that is the whole test. A hardcoded "atrium" is
// right on every fresh install, so a sandbox HOME cannot tell it from config.RuntimeName() —
// the two agree, and the assertion would pass while the one install that needs it got a
// command that finds nothing (CLAUDE.md).
func TestCreateDisclosureReportNamesTheKillCommandForTheRightSocket(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".claude-squad"), 0o755))
	require.Equal(t, "claudesquad", config.RuntimeName(), "precondition: a legacy install")

	report := createDisclosureReport([]outbox.Disclosure{orphanDisclosure("fix-auth")}, time.Now())

	assert.Contains(t, report, "tmux -L claudesquad kill-session -t <name>")
	assert.Contains(t, report, "atrium-web-fix-auth", "and the session it applies to")
	assert.Contains(t, report, "an interrupted `atrium new` left artifacts behind:")
	assert.NotContains(t, report, "… and")
}

// TestCreateDisclosureReportOmitsTheSocketWithNoSession: the socket line is the only one
// naming a machine-wide fact rather than this request's leftovers, so it has no business on
// a report about a branch and a worktree.
func TestCreateDisclosureReportOmitsTheSocketWithNoSession(t *testing.T) {
	d := orphanDisclosure("fix-auth")
	d.TmuxName = ""

	report := createDisclosureReport([]outbox.Disclosure{d}, time.Now())

	assert.NotContains(t, report, "kill-session")
	assert.Contains(t, report, d.Branch, "while the artifacts it does have are still named")
}

// TestCreateDisclosureReportKeepsTheEndOfTheReason: a reason's useful half is its tail —
// a persist failure's cause ("…: no space left on device"), or the remedy a refusal ends
// with — while its head is boilerplate every reason here repeats. So the row clips from the
// left, and this pins the direction rather than the budget.
//
// The fixture is deliberately over-long: a reason that fits is not clipped at all, and a
// test whose input fits passes whichever way the helper truncates. The precondition below
// is on being longer than the budget, not merely longer than a path.
func TestCreateDisclosureReportKeepsTheEndOfTheReason(t *testing.T) {
	d := orphanDisclosure("fix-auth")
	d.Reason = "a previous atrium was interrupted while creating this session and " +
		strings.Repeat("the state file could not be written; ", 4) +
		"write /home/zvi/.atrium/state.json: no space left on device"
	require.Greater(t, len([]rune(d.Reason)), reportLineBudget,
		"precondition: long enough that the row must clip")

	report := createDisclosureReport([]outbox.Disclosure{d}, time.Now())

	assert.Contains(t, report, "no space left on device", "the cause survives")
	assert.NotContains(t, report, "a previous atrium was interrupted",
		"and the boilerplate head is what goes")
}

// TestCreateDisclosureReportDatesEachEntry: CreatedAt's only reader. Two orphans from
// different days are otherwise indistinguishable in the report, and the age is the first
// thing a person needs in order to guess which build left what.
func TestCreateDisclosureReportDatesEachEntry(t *testing.T) {
	now := time.Now()
	old := orphanDisclosure("stale")
	old.CreatedAt = now.Add(-49 * time.Hour)
	fresh := orphanDisclosure("recent")
	fresh.CreatedAt = now.Add(-3 * time.Minute)
	// A disclosure written by an atrium that predates the field, or one whose Disclose
	// failed before stamping. Dating it "0s ago" would make the oldest look newest.
	undated := orphanDisclosure("undated")

	report := createDisclosureReport([]outbox.Disclosure{old, fresh, undated}, now)

	assert.Contains(t, report, "(given up on 2d ago)")
	assert.Contains(t, report, "(given up on 3m ago)")
	assert.Contains(t, report, `"undated" in `)
	assert.NotContains(t, report, `"undated" in /repo/web (given`,
		"and an entry with no timestamp is not dated to today")
}

// TestCreateDisclosureReportBoundsWhatItEnumerates, for repoScriptProblemsReport's reason:
// the count has no ceiling this side of the spool, and three fields in each entry are paths
// a user chose.
func TestCreateDisclosureReportBoundsWhatItEnumerates(t *testing.T) {
	var ds []outbox.Disclosure
	for _, title := range []string{"a", "b", "c", "d", "e", "f", "g"} {
		ds = append(ds, orphanDisclosure(title))
	}
	report := createDisclosureReport(ds, time.Now())

	assert.Contains(t, report, "7 interrupted `atrium new` requests left artifacts behind:")
	assert.Contains(t, report, "… and 2 more")
	assert.NotContains(t, report, "zvi/g", "the tail is counted, not printed")
}

// TestFlushCreateDisclosuresKeepsTheFileGuardingAClaim is #731's third hole approached from
// the reader, which is the direction the writer's ordering cannot defend against.
//
// Disclose runs before DiscardCreate so that a claim outliving a failed unlink has a mark
// beside it, and classifyCreateClaim reads that mark before any evidence. The reader
// unlinking the mark on the very launch that showed it gives all of that back one launch
// later: the next reconcile finds a bare claim, judges it against live git — a branch since
// freed, a session since killed — and re-queues it to build the session whose caller exited
// non-zero long ago.
//
// So the report firing and the mark being spent are two different events, and only the first
// happens here.
func TestFlushCreateDisclosuresKeepsTheFileGuardingAClaim(t *testing.T) {
	h := drainHome(t)
	record, err := outbox.WriteCreate(outbox.Request{Title: "fix-auth", Path: "/repo/web"})
	require.NoError(t, err)
	require.NoError(t, outbox.Claim(record, outbox.ClaimMeta{
		At: time.Now(), SessionBranch: "zvi/fix-auth"}))
	d := orphanDisclosure("fix-auth")
	require.NoError(t, outbox.Disclose(record, &d))
	h.pendingCreateDisclosures = loadCreateDisclosures()
	require.Len(t, h.pendingCreateDisclosures, 1)

	h.flushCreateDisclosures()

	require.Equal(t, stateInfo, h.state, "the report still fires — the orphan is still there")
	_, ok := outbox.DisclosureFor(record)
	assert.True(t, ok, "but the mark outlives it while the claim beside it is still judgeable")
}
