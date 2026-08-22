package app

import (
	"fmt"
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
	_, state := outbox.DisclosureFor(record)
	assert.Equal(t, outbox.NoDisclosure, state, "and a shown report must not come back on the next launch")
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
	_, state := outbox.DisclosureFor(record)
	assert.Equal(t, outbox.HasDisclosure, state, "with the file still there, so a quit now leaves it for the next launch")

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
	_, state := outbox.DisclosureFor(record)
	assert.Equal(t, outbox.NoDisclosure, state, "and it is cleared regardless")
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
	_, state := outbox.DisclosureFor(record)
	assert.Equal(t, outbox.NoDisclosure, state, "and it is not left to be re-read forever")
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

	_, state := outbox.DisclosureFor(record)
	assert.Equal(t, outbox.HasDisclosure, state, "but the record beside it is still executable, so the mark stays")
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

	report := createDisclosureReport([]outbox.Disclosure{orphanDisclosure("fix-auth")}, nil, time.Now())

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

	report := createDisclosureReport([]outbox.Disclosure{d}, nil, time.Now())

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

	report := createDisclosureReport([]outbox.Disclosure{d}, nil, time.Now())

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

	report := createDisclosureReport([]outbox.Disclosure{old, fresh, undated}, nil, now)

	assert.Contains(t, report, "(given up on 2d ago)")
	assert.Contains(t, report, "(given up on 3m ago)")
	assert.Contains(t, report, `"undated" in `)
	assert.NotContains(t, report, `"undated" in /repo/web (given`,
		"and an entry with no timestamp is not dated to today")
}

// TestCreateDisclosureReportBoundsWhatItEnumerates, for repoScriptProblemsReport's reason:
// the count has no ceiling this side of the spool, and three fields in each entry are paths
// a user chose. The cap is applied by the caller, so the held entries arrive as a slice and
// the report's job is to count them without naming them.
func TestCreateDisclosureReportBoundsWhatItEnumerates(t *testing.T) {
	var ds []outbox.Disclosure
	for _, title := range []string{"a", "b", "c", "d", "e", "f", "g"} {
		ds = append(ds, orphanDisclosure(title))
	}
	report := createDisclosureReport(ds[:5], ds[5:], time.Now())

	assert.Contains(t, report, "7 interrupted `atrium new` requests left artifacts behind:",
		"the headline counts every entry, not the five on screen")
	assert.Contains(t, report, "… and 2 more, kept for the next launch")
	assert.NotContains(t, report, "zvi/g", "the tail is counted, not printed")
}

// TestFlushCreateDisclosuresKeepsWhatItCouldNotEnumerate is the other half of that cap, and
// the half the report cannot enforce. An earlier draft unlinked every buffered file and then
// truncated the list, so entries past the fifth were destroyed having never been on screen —
// and since the spool is walked oldest-first those are the NEWEST orphans, left with a count
// as their only remedy. Unlike customCommandProblemsReport, whose analogous cap truncates a
// report over a config file that survives, the source of truth here is what the same call
// deletes.
func TestFlushCreateDisclosuresKeepsWhatItCouldNotEnumerate(t *testing.T) {
	h := drainHome(t)
	titles := []string{"a", "b", "c", "d", "e", "f", "g"}
	records := make(map[string]string, len(titles))
	for _, title := range titles {
		record, err := outbox.WriteCreate(outbox.Request{Title: title, Path: "/repo/web"})
		require.NoError(t, err)
		// The record itself out of the way, so the only thing that could keep a file is the
		// cap rather than recordStillSpooled's veto.
		require.NoError(t, outbox.DiscardCreate(record))
		d := orphanDisclosure(title)
		require.NoError(t, outbox.Disclose(record, &d))
		records[title] = record
	}
	h.pendingCreateDisclosures = loadCreateDisclosures()
	require.Len(t, h.pendingCreateDisclosures, len(titles))

	h.flushCreateDisclosures()
	require.Equal(t, stateInfo, h.state, "precondition: the report fired")

	for _, title := range titles[:createDisclosuresShown] {
		_, state := outbox.DisclosureFor(records[title])
		assert.Equal(t, outbox.NoDisclosure, state, "%s was named, so its file is spent", title)
	}
	for _, title := range titles[createDisclosuresShown:] {
		_, state := outbox.DisclosureFor(records[title])
		assert.Equal(t, outbox.HasDisclosure, state,
			"%s was only counted, so the next launch has to be able to name it", title)
	}
}

// TestFlushCreateDisclosuresDropsAMarkWithNothingToShow is the opposite direction: every
// giving-up writes a disclosure whether or not it has an inventory, because the mark is what
// keeps a claim from being re-judged and a record from being re-drained. One with nothing to
// name has no report to give, so its report job is finished by there being nothing to report
// — and leaving it would put a file in the spool for every refused `atrium new` until the TTL.
func TestFlushCreateDisclosuresDropsAMarkWithNothingToShow(t *testing.T) {
	h := drainHome(t)
	record, err := outbox.WriteCreate(outbox.Request{Title: "fix-auth", Path: "/repo/web"})
	require.NoError(t, err)
	require.NoError(t, outbox.DiscardCreate(record))
	require.NoError(t, outbox.Disclose(record, &outbox.Disclosure{
		Title: "fix-auth", Repo: "/repo/web", Reason: "the title is already used"}))
	h.pendingCreateDisclosures = loadCreateDisclosures()
	require.Len(t, h.pendingCreateDisclosures, 1)

	h.flushCreateDisclosures()
	assert.Equal(t, stateDefault, h.state, "a mark is not a modal")
	_, state := outbox.DisclosureFor(record)
	assert.Equal(t, outbox.NoDisclosure, state, "and it is not left to be re-read forever")
}

// TestCreateDisclosureReportFitsANarrowTerminal is the wrap guard, and it exists because the
// Go suite cannot see a wrap: TextOverlay renders the string, so a line over the box's inner
// width arrives split with the modal's border between the halves. That is tolerable for a
// path — a wrapped value is still a value — and not for the trailer's kill-session command,
// which is pasteable in neither half. See reportNarrowWidth for where 64 comes from.
//
// Both socket names, because the line carries config.RuntimeName twice and a legacy install's
// "claudesquad" is ten runes longer than "atrium" — the form that fitted for one did not for
// the other.
func TestCreateDisclosureReportFitsANarrowTerminal(t *testing.T) {
	report := createDisclosureReport([]outbox.Disclosure{orphanDisclosure("fix-auth")}, nil, time.Now())

	for _, line := range strings.Split(report, "\n") {
		if strings.HasPrefix(line, "    ") {
			continue // a labelled row, whose value is allowed to wrap
		}
		assert.LessOrEqual(t, len([]rune(line)), reportNarrowWidth,
			"%q must arrive unwrapped on a 72-column terminal", line)
	}
	// The command row is the one indented line that must also fit, and it is the whole point
	// of the guard. Measured for the longest socket name any install can have.
	for _, socket := range []string{"atrium", "claudesquad"} {
		cmd := fmt.Sprintf("    tmux -L %s kill-session -t <name>", socket)
		assert.LessOrEqual(t, len([]rune(cmd)), reportNarrowWidth, cmd)
	}
	require.Contains(t, report, "kill-session -t <name>",
		"precondition: the fixture reaches the trailer this guards")
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
	_, state := outbox.DisclosureFor(record)
	assert.Equal(t, outbox.HasDisclosure, state, "but the mark outlives it while the claim beside it is still judgeable")
}

// TestCreateDisclosureReportKeepsTheDateBehindALongPath: the entry's line was composed and
// then clipped whole, so an ordinary monorepo path pushed `(given up on N ago)` off the end —
// and CreatedAt has no other reader, so two orphans from different days rendered identically.
// The values are clipped and the age is appended after, which is the same rule every labelled
// row already follows: clip the VALUE, keep what tells you what you are looking at.
func TestCreateDisclosureReportKeepsTheDateBehindALongPath(t *testing.T) {
	d := orphanDisclosure("payments-retry")
	d.Repo = "/Users/dev/Development/clients/acme/backend/services/payments-gateway-internal"
	d.CreatedAt = time.Now().Add(-50 * time.Hour)
	// The composed line, which is what the old clip was applied to: two quotes, " in ", the
	// path, and the age suffix. Under the budget none of this is reachable.
	composed := fmt.Sprintf("%q in %s%s", d.Title, d.Repo, gaveUp(d.CreatedAt, time.Now()))
	require.Greater(t, len([]rune(composed)), reportLineBudget,
		"precondition: composed and clipped whole, this line loses its tail")

	report := createDisclosureReport([]outbox.Disclosure{d}, nil, time.Now())

	assert.Contains(t, report, "(given up on 2d ago)", "the age survives a long path")
	assert.Contains(t, report, "payments-retry", "and so does the title")
}

// TestCreateDisclosureReportNamesTheSocketForAHeldEntry: the trailer supplies the socket and
// nothing else does — on a legacy install it is "claudesquad" and unguessable. Computed over
// the shown entries alone, a report whose five oldest are branch-only refusals drops the line
// while the two it held back are the live agents whose only remedy is that command. Since
// ListDisclosures is oldest-first, the held ones are the newest — the likeliest to still be
// running.
func TestCreateDisclosureReportNamesTheSocketForAHeldEntry(t *testing.T) {
	shown := outbox.Disclosure{Title: "old", Repo: "/repo/web", Branch: "zvi/old",
		Reason: "the title is already used"}
	held := orphanDisclosure("new") // this one has a tmux session

	report := createDisclosureReport([]outbox.Disclosure{shown}, []outbox.Disclosure{held}, time.Now())

	assert.NotContains(t, report, "atrium-web-new", "precondition: the tail is counted, not named")
	assert.Contains(t, report, "kill-session -t <name>",
		"but the socket it needs is still on the page")
}
