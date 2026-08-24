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
	report := createDisclosureReport(ds[:5], heldEntries(ds[5:]...), time.Now())

	assert.Contains(t, report, "7 interrupted `atrium new` requests left artifacts behind:",
		"the headline counts every entry, not the five on screen")
	assert.Contains(t, report, "… and 2 more, shown after this one")
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
			"%s was only counted, so a later pass has to be able to name it", title)
	}
	// And still buffered, which is the half this fixture cannot see on its own: it discards the
	// record first, so no drain tick has anything to drop and the marks survive whatever the
	// buffer holds. TestFlushKeepsTheHeldTailInTheBufferThatGuardsIt is that case.
	assert.Len(t, h.pendingCreateDisclosures, len(titles)-createDisclosuresShown,
		"the tail is what the next pass reports, and the buffer is where it waits")
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

// TestCreateDisclosureTrailerFitsANarrowTerminal is the wrap guard, and it exists because the
// Go suite cannot see a wrap: TextOverlay renders the string, so a line over the box's inner
// width arrives split with the modal's border between the halves. That is tolerable for a
// path — a wrapped value is still a value — and not for the kill-session command, which is
// pasteable in neither half. See reportNarrowWidth for where 64 comes from.
//
// Measured on the trailer rather than by scanning the report, which is what this did and what
// made it vacuous. The scan skipped indented lines as "a labelled row, whose value is allowed
// to wrap" — but the entry HEADER is un-indented and clips its title and its repo at
// reportLineBudget apiece, so the code permits about 210 cells there while the assertion
// demanded 64. It passed only because the fixture's path was short:
// TestCreateDisclosureReportKeepsTheDateBehindALongPath's own fixture breaks it at 119. The
// bound belongs to the lines the code actually bounds, so those are built somewhere a test can
// enumerate them.
//
// Cells, not runes, because a cell is what TextOverlay wraps against: a CJK or emoji title is
// two cells per rune, so a 40-rune line passes a rune count at 40 and still arrives wrapped.
//
// Both socket names, because the trailer carries config.RuntimeName twice and a legacy
// install's "claudesquad" is five cells longer than "atrium" — the single-line form fitted for
// one and not the other.
func TestCreateDisclosureTrailerFitsANarrowTerminal(t *testing.T) {
	for _, socket := range []string{config.RuntimeName(), "atrium", "claudesquad"} {
		for _, line := range createDisclosureTrailer(socket, true) {
			assert.LessOrEqual(t, xansi.StringWidth(line), reportNarrowWidth,
				"%q must arrive unwrapped on a 72-column terminal", line)
		}
	}

	// Tied to the report the code builds, so the loop above cannot be measuring lines nothing
	// renders. A long repo path is in the fixture on purpose: it proves the entry header is
	// deliberately outside the bound rather than accidentally under it.
	d := orphanDisclosure("fix-auth")
	d.Repo = "/Users/dev/Development/clients/acme/backend/services/payments-gateway-internal"
	report := createDisclosureReport([]outbox.Disclosure{d}, nil, time.Now())
	for _, line := range createDisclosureTrailer(config.RuntimeName(), true) {
		require.Contains(t, report, line)
	}
	require.Greater(t, xansi.StringWidth(fmt.Sprintf("%q in %s", d.Title, d.Repo)), reportNarrowWidth,
		"precondition: the entry header is over the trailer's bound, and wraps rather than clips")
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

	report := createDisclosureReport([]outbox.Disclosure{shown}, heldEntries(held), time.Now())

	assert.NotContains(t, report, "atrium-web-new", "precondition: the tail is counted, not named")
	assert.Contains(t, report, "kill-session -t <name>",
		"but the socket it needs is still on the page")
}

// heldEntries wraps disclosures as the DisclosureEntry slice createDisclosureReport takes for
// its held tail. The tail carries record paths in production because flushCreateDisclosures
// re-buffers it — a Disclosure alone cannot be re-offered to ClearDisclosure — and the report
// only ever counts it, so the paths here are irrelevant to what is being asserted.
func heldEntries(ds ...outbox.Disclosure) []outbox.DisclosureEntry {
	entries := make([]outbox.DisclosureEntry, 0, len(ds))
	for i, d := range ds {
		entries = append(entries, outbox.DisclosureEntry{
			Path: filepath.Join("/spool", fmt.Sprintf("held-%d.json", i)), Disclosure: d})
	}
	return entries
}

// TestFlushKeepsTheHeldTailInTheBufferThatGuardsIt is the cap's other half, and the half no
// assertion on the report can see.
//
// m.pendingCreateDisclosures is the only thing clearMarkOverADroppedRecord consults before
// deleting a mark. Held entries that left the buffer were therefore unguarded from the instant
// the report fired: one drain tick later the same records took the terminal-mark arm, were
// answered and unlinked, and every held mark went with them — while the modal on screen said
// they were kept. They were also the NEWEST orphans, since ListDisclosures is oldest-first, so
// the ones destroyed unread were the likeliest to still have an agent running.
//
// The fixture is the state the mark exists FOR: records still in the spool, each with a mark
// beside it, which is what a failed unlink leaves. That is why the drain's disposal arm reaches
// them at all — a fixture that discarded the record first would leave the drain nothing to drop
// and the loss would not reproduce.
func TestFlushKeepsTheHeldTailInTheBufferThatGuardsIt(t *testing.T) {
	h := drainHome(t)
	const total = createDisclosuresShown + 2
	var records []string
	for i := range total {
		title := fmt.Sprintf("orphan-%d", i)
		record := spoolCreate(t, outbox.Request{Title: title, Path: gitRepoWithBranch(t, "")})
		d := orphanDisclosure(title)
		require.NoError(t, outbox.Disclose(record, &d))
		records = append(records, record)
	}
	h.pendingCreateDisclosures = loadCreateDisclosures()
	require.Len(t, h.pendingCreateDisclosures, total, "precondition: all of them are buffered")

	h.flushCreateDisclosures()
	require.Equal(t, stateInfo, h.state, "precondition: the report fired")
	require.Len(t, h.pendingCreateDisclosures, total-createDisclosuresShown,
		"the tail stays where the only thing that guards it can see it")

	// One tick of the drain: every record now has a mark beside it, so every one takes the
	// terminal-mark arm and is dropped. createDisposalBudget is 50, so all of them fit.
	h.state = stateDefault
	h.drainCreateRequests()

	surviving := 0
	for _, record := range records {
		if outbox.DisclosureMark(record) == outbox.HasDisclosure {
			surviving++
		}
	}
	assert.Equal(t, total-createDisclosuresShown, surviving,
		"the marks the report promised to show next are still there to show")
}

// TestFlushDropsAMarkALiveSessionOwns is the screen the file cannot do for itself.
//
// discloseLiveButUnrecorded writes a mark for a session that is running while its row is not,
// and withdraws it when the row lands. A session killed before it ever persisted is absent
// from every later save, so the withdrawal never comes and the file outlives the run —
// applyKillDone drops the row BEFORE it reports an incomplete teardown, which is what makes
// that reachable rather than theoretical. git.BranchNameForSession and
// tmux.QualifiedSessionName are pure functions of (repo, title), so the next session created
// under that title is described byte for byte by a file saying nothing in atrium's records
// points at it — under a report that hands the reader `tmux kill-session`. Following it
// destroys a running agent, which is the one thing discloseLiveButUnrecorded exists to avoid.
func TestFlushDropsAMarkALiveSessionOwns(t *testing.T) {
	h := drainHome(t)
	repo := gitRepoWithBranch(t, "")
	inst := addPersistedInstanceOnBranch(t, h, "fix-auth", repo, "zvi/fix-auth")
	require.True(t, inst.Started(), "precondition: an unstarted row owns nothing yet")

	record := spoolCreate(t, outbox.Request{Title: "fix-auth", Path: repo})
	live := outbox.Disclosure{Title: "fix-auth", Repo: repo, Branch: inst.Branch(),
		Reason: "the row could not be written"}
	require.NoError(t, outbox.Disclose(record, &live))
	stranded := orphanDisclosure("long-gone")
	strandedRecord := spoolCreate(t, outbox.Request{Title: "long-gone", Path: repo})
	require.NoError(t, outbox.Disclose(strandedRecord, &stranded))
	h.pendingCreateDisclosures = loadCreateDisclosures()
	require.Len(t, h.pendingCreateDisclosures, 2)

	h.flushCreateDisclosures()

	require.Equal(t, stateInfo, h.state, "the genuine orphan is still reported")
	report := h.textOverlay.Render()
	assert.NotContains(t, report, inst.Branch(),
		"a branch a live row holds is that row's, whatever a file written before it says")
	assert.Contains(t, report, stranded.Branch, "and the real orphan is not screened away with it")
}

// TestCreateDisclosureBacklogPutsWhatCannotBeReReadFirst: the reconcile's undisclosed entries
// exist nowhere but in that slice, because the write is what failed. flushCreateDisclosures
// caps what one report names and decides by position, so appended they sat behind every
// disk-backed entry and went to the held tail — and a crash before the second pass takes them
// with it, where a disk-backed entry is read again by the next launch.
func TestCreateDisclosureBacklogPutsWhatCannotBeReReadFirst(t *testing.T) {
	onDisk := []outbox.DisclosureEntry{
		{Path: "/spool/1.json", Disclosure: orphanDisclosure("on-disk")},
	}
	undisclosed := []outbox.DisclosureEntry{
		{Path: "/spool/2.json", Disclosure: orphanDisclosure("memory-only")},
	}

	got := createDisclosureBacklog(onDisk, undisclosed)

	require.Len(t, got, 2)
	assert.Equal(t, "memory-only", got[0].Disclosure.Title,
		"the entry with no file takes the slot the cap might not reach twice")
	assert.Equal(t, "on-disk", got[1].Disclosure.Title)
}
