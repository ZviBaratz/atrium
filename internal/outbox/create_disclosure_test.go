package outbox

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// leftovers is a filled-in Disclosure, so a test that cares about one field says so by
// overriding it rather than by being the only one that sets anything. A pointer because
// Disclose stamps Version and CreatedAt on what it is given.
func leftovers() *Disclosure {
	return &Disclosure{
		Title:    "fix-auth",
		Repo:     "/repo/web",
		Branch:   "zvi/fix-auth",
		Worktree: "/data/worktrees/web/fix-auth-1",
		TmuxName: "atrium-web-fix-auth",
		Reason:   "the session was created but atrium could not record it",
	}
}

// TestDisclosureOutlivesTheRecordAndTheClaim is the property the whole kind exists for.
// Every path that gives up on a request destroys both of the files it can be on disk as;
// the disclosure has to survive that, or the branch and worktree it names are never
// mentioned again.
func TestDisclosureOutlivesTheRecordAndTheClaim(t *testing.T) {
	sandbox(t)
	record := claimed(t, req("fix-auth", "/repo/web"), meta())

	require.NoError(t, Disclose(record, leftovers()))
	require.NoError(t, Reject(record, "could not record it"))
	require.NoError(t, DiscardCreate(record))

	assert.NoFileExists(t, record)
	assert.NoFileExists(t, ClaimPath(record))
	d, state := DisclosureFor(record)
	require.Equal(t, HasDisclosure, state, "the disclosure is the only thing left that names the leftovers")
	assert.Equal(t, "zvi/fix-auth", d.Branch)
	assert.Equal(t, "/data/worktrees/web/fix-auth-1", d.Worktree)
	assert.Equal(t, "atrium-web-fix-auth", d.TmuxName)
	assert.Equal(t, disclosureVersion, d.Version)
	assert.False(t, d.CreatedAt.IsZero(),
		"and it has to be datable, or the report cannot say when this happened")
}

// TestADisclosureIsInvisibleToEveryWalkThatCreates is the safety argument, asserted as a
// property rather than through either mechanism that delivers it (the name format anchored
// at both ends, and the suffix each of the other walks cuts).
//
// #732 proposed a claim surviving in a terminal form, and this is why it is a separate kind
// instead: a claim that stays a claim is one forgotten check away from being rebuilt by a
// later edit of the reconcile, for a caller that was told the create failed and has long
// since exited non-zero. A disclosure cannot be decoded as a Request by anything.
func TestADisclosureIsInvisibleToEveryWalkThatCreates(t *testing.T) {
	sandbox(t)
	record, err := WriteCreate(req("fix-auth", "/repo/web"))
	require.NoError(t, err)
	require.NoError(t, Disclose(record, leftovers()))
	require.NoError(t, Reject(record, "gone"))

	creates, err := ListCreates()
	require.NoError(t, err)
	assert.Empty(t, creates, "the drain must not see it")
	claims, err := ListClaims()
	require.NoError(t, err)
	assert.Empty(t, claims, "and neither must the startup reconcile")

	ds, err := ListDisclosures()
	require.NoError(t, err)
	require.Len(t, ds, 1, "the one walk that does see it")
	assert.Equal(t, record, ds[0].Path, "keyed on the RECORD path, as ListClaims is")
	assert.Equal(t, "fix-auth", ds[0].Disclosure.Title)
}

// TestListDisclosuresIgnoresWhatItDidNotWrite: the reader unlinks what it shows, so the
// walk that finds a disclosure has to be as narrow as the walks that find records —
// otherwise an editor's swap file or a WriteFileAtomic temp becomes something this deletes.
func TestListDisclosuresIgnoresWhatItDidNotWrite(t *testing.T) {
	sandbox(t)
	dir, err := CreateDir()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	for _, name := range []string{
		"notes.txt.disclosure",              // not our record format under the suffix
		"1770000000000000000-zz.disclosure", // nonce is not hex
		"1770000000000000000-aabbccdd.json", // a record, not a disclosure
	} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("{}"), 0o644))
	}
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "1770000000000000000-deadbeef.json.disclosure"), 0o755))

	ds, err := ListDisclosures()
	require.NoError(t, err)
	assert.Empty(t, ds)
}

// TestListDisclosuresSurfacesAnUndecodableOne. Its caller's only move is to log and
// unlink — nothing downstream can act on a disclosure, so unlike a spool record there is
// no producer owed a receipt — but that decision belongs to the caller, so the entry has
// to arrive rather than be skipped.
func TestListDisclosuresSurfacesAnUndecodableOne(t *testing.T) {
	sandbox(t)
	record, err := WriteCreate(req("fix-auth", "/repo/web"))
	require.NoError(t, err)
	require.NoError(t, Disclose(record, leftovers()))
	require.NoError(t, os.WriteFile(disclosurePath(record), []byte("{not json"), 0o644))

	ds, err := ListDisclosures()
	require.NoError(t, err)
	require.Len(t, ds, 1)
	require.Error(t, ds[0].Err)
	assert.Equal(t, record, ds[0].Path, "and still keyed on the record, so the caller can clear it")
}

// TestDisclosureForSeparatesAbsentFromUnreadable: classifyCreateClaim asks this question
// to decide whether an earlier launch already answered a request's caller, and "there is
// no disclosure" and "there is one I cannot decode" must not collapse. Read as absent, an
// unreadable disclosure lets the claim be judged on live evidence again — the rebuild the
// kind exists to foreclose.
func TestDisclosureForSeparatesAbsentFromUnreadable(t *testing.T) {
	sandbox(t)
	record, err := WriteCreate(req("fix-auth", "/repo/web"))
	require.NoError(t, err)

	_, state := DisclosureFor(record)
	assert.Equal(t, NoDisclosure, state, "nothing has given up on this request")

	require.NoError(t, os.WriteFile(disclosurePath(record), []byte("{not json"), 0o644))
	_, state = DisclosureFor(record)
	assert.Equal(t, HasDisclosure, state, "a disclosure that cannot be read still says the caller was answered")
}

// TestClearDisclosureIsIdempotent: the reader clears what it showed, and a second flush
// (or a sweep that got there first) must not turn that into an error the user sees.
func TestClearDisclosureIsIdempotent(t *testing.T) {
	sandbox(t)
	record := claimed(t, req("fix-auth", "/repo/web"), meta())
	require.NoError(t, Disclose(record, leftovers()))
	require.NoError(t, Reject(record, "could not record it"))
	require.NoError(t, DiscardCreate(record))

	removed, err := ClearDisclosure(record)
	require.NoError(t, err)
	assert.True(t, removed, "and it says so, which is how a retrying caller stops")
	_, state := DisclosureFor(record)
	assert.Equal(t, NoDisclosure, state)
	removed, err = ClearDisclosure(record)
	assert.NoError(t, err, "already gone is not an error")
	assert.True(t, removed, "and reports the file gone, because it is")
}

// TestClearDisclosureKeepsAMarkOverAnExecutableFile is the second half of what a disclosure
// is for, and the half a reader cannot be trusted with.
//
// Showing the report finishes its job as a report. It does not finish its job as the mark
// that stops the file beside it from being built — and those two ends are exactly what
// Disclose being ordered before DiscardCreate buys. Cleared on the launch that showed it, a
// refusal whose unlink failed hands the next launch a bare claim to re-judge against live
// git, into a verdict that creates the session its caller was told it would not get.
//
// Both files, because both are executable: the record by the drain and the claim by the
// startup reconcile.
func TestClearDisclosureKeepsAMarkOverAnExecutableFile(t *testing.T) {
	for _, tc := range []struct {
		name  string
		leave func(t *testing.T, record string)
	}{
		{"a claim whose discard failed", func(t *testing.T, record string) {
			require.NoError(t, Reject(record, "could not record it"))
		}},
		{"a record whose unlink failed", func(t *testing.T, record string) {
			require.NoError(t, Requeue(record, ""))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sandbox(t)
			record := claimed(t, req("fix-auth", "/repo/web"), meta())
			require.NoError(t, Disclose(record, leftovers()))
			tc.leave(t, record)

			removed, err := ClearDisclosure(record)
			require.NoError(t, err, "and it is not an error to try")
			assert.False(t, removed, "and it says the file is still there")

			_, state := DisclosureFor(record)
			assert.Equal(t, HasDisclosure, state, "the mark outlives the report while anything can still act on it")

			SweepDisclosures(time.Now().Add(TTL + time.Hour))
			_, state = DisclosureFor(record)
			assert.Equal(t, HasDisclosure, state, "and the horizon is not a second way round it")
		})
	}
}

// TestClearCarriesADisclosureAcrossUntouched pins the `atrium reset` decision: a disclosure
// already on disk is kept, and kept whole.
//
// Kept, because the branch it names is what reset leaves standing — `branch -D` and
// `worktree prune` are the repo-scoped halves of git.CleanupWorktrees, and an orphan has no
// row to be enumerated through. Whole, because an earlier draft trimmed out the worktree
// directory and the agent on the grounds that reset destroys both, and reset does not always
// get that far: Clear runs before tmux.CleanupSessions and git.CleanupWorktrees, either of
// which aborts the reset on failure with the directory and the agent still standing. A
// disclosure that had been trimmed by then names neither, and its Reason — which reset does
// not edit — still tells the reader to kill a session nothing lists any more.
func TestClearCarriesADisclosureAcrossUntouched(t *testing.T) {
	sandbox(t)
	record, err := WriteCreate(req("fix-auth", "/repo/web"))
	require.NoError(t, err)
	want := leftovers()
	require.NoError(t, Disclose(record, want))
	require.NoError(t, Reject(record, "could not record it"))

	removed, err := Clear()
	require.NoError(t, err)
	assert.Zero(t, removed, "there was no queued record left to discard")
	d, state := DisclosureFor(record)
	require.Equal(t, HasDisclosure, state, "and the orphan still has something naming it")
	// Field by field rather than the whole struct: CreatedAt survives the round trip as an
	// equal instant in a different time.Location, which reflect.DeepEqual reads as a
	// difference. The three inventory fields are what the trim used to take.
	assert.Equal(t, want.Branch, d.Branch, "the branch reset leaves standing")
	assert.Equal(t, want.Worktree, d.Worktree, "and the directory reset usually removes")
	assert.Equal(t, want.TmuxName, d.TmuxName, "and the agent reset usually kills")
	assert.Equal(t, want.Reason, d.Reason, "and the reason that names all three")
	assert.True(t, want.CreatedAt.Equal(d.CreatedAt), "and the date the report needs")
}

// TestClearDisclosesTheBranchOfTheClaimItDestroys: reset is the one give-up path the
// Disclose-then-Reject-then-discard ordering does not reach on its own, and destroying a
// claim destroys the only link to the branch that claim's interrupted build made. Without
// this, `atrium reset` is the pre-#716 orphan with reset's name on it — state wiped, branch
// standing, nothing that mentions it, and every later `atrium new` under that title refused
// for a branch nobody can find.
func TestClearDisclosesTheBranchOfTheClaimItDestroys(t *testing.T) {
	sandbox(t)
	record := claimed(t, req("fix-auth", "/repo/web"), meta())

	removed, err := Clear()
	require.NoError(t, err)
	assert.Equal(t, 1, removed)
	assert.NoFileExists(t, ClaimPath(record), "precondition: the claim is gone")

	d, state := DisclosureFor(record)
	require.Equal(t, HasDisclosure, state, "so the branch it made has to be named somewhere")
	assert.Equal(t, meta().SessionBranch, d.Branch)
	assert.Empty(t, d.Worktree, "and not the directory reset removes on its way past")
	assert.Contains(t, d.Reason, clearReason)
}

// TestClearKeepsTheFullerAccountOfTwo: a claim and a disclosure together is a refusal whose
// unlink failed, and that disclosure was written with the whole inventory in hand. Reset
// destroys the claim, and the single-field one it would write for it must not overwrite the
// account already there.
func TestClearKeepsTheFullerAccountOfTwo(t *testing.T) {
	sandbox(t)
	record := claimed(t, req("fix-auth", "/repo/web"), meta())
	require.NoError(t, Disclose(record, leftovers()))

	_, err := Clear()
	require.NoError(t, err)

	d, state := DisclosureFor(record)
	require.Equal(t, HasDisclosure, state)
	assert.Equal(t, leftovers().Reason, d.Reason, "the refusal's own wording, not reset's")
	assert.Equal(t, leftovers().TmuxName, d.TmuxName, "and its whole inventory with it")
}

// TestSweepDisclosuresAgesByItsOwnMtime is the backstop, and the arithmetic that matters is
// which timestamp it uses. A disclosure's filename carries the REQUEST's CreatedAt, which
// for one that sat in the spool before failing is already old — keyed off that, the sweep
// would delete the report before any TUI could show it.
func TestSweepDisclosuresAgesByItsOwnMtime(t *testing.T) {
	sandbox(t)
	record, err := WriteCreate(Request{Title: "fix-auth", Path: "/repo/web",
		CreatedAt: time.Now().Add(-2 * TTL)})
	require.NoError(t, err)
	require.NoError(t, Disclose(record, leftovers()))
	// Nothing executable beside it, or the sweep defers to the mark rather than the
	// horizon (TestClearDisclosureKeepsAMarkOverAnExecutableFile).
	require.NoError(t, Reject(record, "could not record it"))

	SweepDisclosures(time.Now())
	_, state := DisclosureFor(record)
	assert.Equal(t, HasDisclosure, state, "a just-written disclosure survives even for a TTL-old request")

	SweepDisclosures(time.Now().Add(TTL + time.Hour))
	_, state = DisclosureFor(record)
	assert.Equal(t, NoDisclosure, state, "past the horizon there is no reader left")
}

// TestSweepRejectionsLeavesDisclosures is the separation the two sweeps exist to keep: they
// answer to different readers, so a receipt reaching its horizon must not take a disclosure
// with it.
func TestSweepRejectionsLeavesDisclosures(t *testing.T) {
	sandbox(t)
	record, err := WriteCreate(req("fix-auth", "/repo/web"))
	require.NoError(t, err)
	require.NoError(t, Disclose(record, leftovers()))
	require.NoError(t, Reject(record, "could not record it"))

	SweepRejections(time.Now().Add(TTL + time.Hour))
	_, ok := Rejection(record)
	assert.False(t, ok, "precondition: the receipt was swept")
	_, state := DisclosureFor(record)
	assert.Equal(t, HasDisclosure, state)
}

// TestCreateWritesRefuseADerivedPath closes the class #731 named. Every function here
// derives a second path by concatenation, so a caller handing one of those back in as a
// record would mint "….json.claimed.claimed" or "….json.claimed.disclosure" — a file no
// walk in this package matches, and therefore one nothing lists, sweeps, clears, or
// `atrium reset` can ever remove. No caller does it today; none can now.
func TestCreateWritesRefuseADerivedPath(t *testing.T) {
	sandbox(t)
	record := claimed(t, req("fix-auth", "/repo/web"), meta())
	derived := ClaimPath(record)

	// ErrorContains, not Error, and that is the point of the test rather than a
	// tightening. Requeue on a derived path errors either way — with the guard gone it
	// computes ClaimPath(derived), finds no such file, and reports that — so an assertion
	// on "there was an error" is satisfied by a failure that has nothing to do with the
	// guard, and passes with the guard deleted. Every leg is spelled the same way so none
	// of them can be the one that stops testing anything.
	const refused = "is not a spool record"
	assert.ErrorContains(t, Claim(derived, meta()), refused)
	assert.ErrorContains(t, Requeue(derived, "deadbeef"), refused)
	assert.ErrorContains(t, DiscardCreate(derived), refused)
	assert.ErrorContains(t, Disclose(derived, leftovers()), refused)
	assert.ErrorContains(t, Reject(derived, "no"), refused)
	assert.NoFileExists(t, derived+rejectedSuffix, "and no invisible receipt either")

	assert.NoFileExists(t, ClaimPath(derived), "and nothing invisible was minted")
	assert.NoFileExists(t, disclosurePath(derived))
	assert.FileExists(t, derived, "while the real claim is untouched")
}

// TestLeftoversSeparatesAMarkFromAReport: every refusal writes a disclosure, because the
// mark is what stops a claim being re-judged, and most refusals happen before anything
// durable was built. The reader shows only the ones with something to name, so this
// predicate is what keeps "a request failed" out of a modal the receipt already answered.
func TestLeftoversSeparatesAMarkFromAReport(t *testing.T) {
	assert.False(t, Disclosure{Title: "fix-auth", Reason: "the title is taken"}.Leftovers())
	assert.True(t, Disclosure{Branch: "zvi/fix-auth"}.Leftovers())
	assert.True(t, Disclosure{Worktree: "/data/worktrees/web/fix-auth-1"}.Leftovers())
	assert.True(t, Disclosure{TmuxName: "atrium-web-fix-auth"}.Leftovers())
}

// TestClearDisclosesTheBranchOfTheRecordItDestroys is the claim test's record-shaped twin,
// and it is a different hole rather than the same one twice. A request the startup reconcile
// re-queued to adopt its own orphan is back at the RECORD name, carrying the claim's evidence
// block — and applyCreateClaim released that branch's worktree registration on its way past.
// Reset it in the window before the drain picks it up and the branch has no row, no claim, no
// request and no registration: invisible to `atrium ls`, to `atrium reap` and to `git worktree
// list`, while every later `atrium new` under that title is refused for it.
func TestClearDisclosesTheBranchOfTheRecordItDestroys(t *testing.T) {
	sandbox(t)
	record := claimed(t, req("fix-auth", "/repo/web"), meta())
	require.NoError(t, Requeue(record, "deadbeef"))
	require.FileExists(t, record, "precondition: it is a queued record again, not a claim")

	removed, err := Clear()
	require.NoError(t, err)
	assert.Equal(t, 1, removed)

	d, state := DisclosureFor(record)
	require.Equal(t, HasDisclosure, state, "so the released branch is named somewhere")
	assert.Equal(t, meta().SessionBranch, d.Branch)
	assert.Equal(t, "fix-auth", d.Title, "and the request it belonged to")
	assert.Contains(t, d.Reason, "git worktree prune",
		"because reset leaves git's registration of a directory it removed")
}

// TestClearMarksAnOrdinaryRecordWithNothingToName is the record-shaped twin of the guard
// below, and it used to be the exception: an ordinary queued request has no evidence block, so
// it named no branch and got no mark, on the reasoning that reset removes the file and leaves
// nothing to guard.
//
// The reasoning skipped the step that makes hole 3 a hole. Reject writes the receipt and THEN
// unlinks, and the unlink can fail — a sticky-bit spool directory, an immutable file, an NFS
// EPERM — which reset itself prints a warning for. The record then outlives a reset that told
// its operator "discarded", state.json is empty so no title collides with it, and the next TUI
// takes it through the gates and builds the session for a caller that already exited non-zero.
// The mark is the only thing that stops that, and it is not a report: Leftovers() is false, so
// the reader drops it unshown and it costs the user nothing.
func TestClearMarksAnOrdinaryRecordWithNothingToName(t *testing.T) {
	sandbox(t)
	record, err := WriteCreate(req("fix-auth", "/repo/web"))
	require.NoError(t, err)

	removed, err := Clear()
	require.NoError(t, err)
	assert.Equal(t, 1, removed)

	d, state := DisclosureFor(record)
	require.Equal(t, HasDisclosure, state, "or a record that survives its Reject is executed")
	assert.Equal(t, "fix-auth", d.Title)
	assert.Empty(t, d.Branch, "it built nothing, so there is nothing to name")
	assert.False(t, d.Leftovers(), "which is what keeps it out of the reader's report")
}

// TestClearMarksAClaimWithNothingToName is the guard half of the same rule, and the case it
// covers is the one with no inventory at all: a direct session's claim, whose SessionBranch is
// empty by construction. Clear's removeClaim can fail — a full or read-only spool is #731 hole
// 3's own premise — and a claim that outlives it with no mark beside it is re-judged against
// live git on the next launch, into a verdict that builds the session whose caller reset told
// had been cleared. So the mark is written whether or not there is anything to show.
func TestClearMarksAClaimWithNothingToName(t *testing.T) {
	sandbox(t)
	record := claimed(t, req("fix-auth", "/repo/web"), ClaimMeta{At: time.Now()})

	removed, err := Clear()
	require.NoError(t, err)
	assert.Equal(t, 1, removed)

	d, state := DisclosureFor(record)
	require.Equal(t, HasDisclosure, state, "the mark is the guard, not the report")
	assert.False(t, d.Leftovers(), "and it has nothing for a report to show")
	assert.Contains(t, d.Reason, clearReason)
}

// TestDisclosureForSeparatesPresentFromUnknown: presence is answered by os.Stat and content by
// a read, because the two failures are not the same failure. os.ReadFile opens a descriptor,
// so under fd exhaustion it answers EMFILE for a path that does not exist — and a predicate
// reading every non-ENOENT error as "there is a mark" reports every queued request terminal,
// which is a receipt and an unlink apiece for requests nothing was wrong with. A file that IS
// there and cannot be read is still a mark.
func TestDisclosureForSeparatesPresentFromUnknown(t *testing.T) {
	sandbox(t)
	record, err := WriteCreate(req("fix-auth", "/repo/web"))
	require.NoError(t, err)

	// A directory at the disclosure path: stat succeeds, the read fails with EISDIR. That is
	// "present but unreadable for a reason that is not a decode", which the JSON case above
	// cannot reach — it proves the two syscalls are separate rather than one wrapped answer.
	require.NoError(t, os.Mkdir(disclosurePath(record), 0o755))
	_, state := DisclosureFor(record)
	assert.Equal(t, HasDisclosure, state, "a mark nobody can read still answers the question")
}

// TestWithdrawAdoptionEditsTheRecordTheClaimIsBuiltFrom: Claim re-reads the record, so a drain
// that withdraws the adoption licence in its own copy writes a claim carrying a licence its
// build never used — and the next launch reads that licence to tell "this session's own
// half-built branch" from "somebody else's branch is in the way".
func TestWithdrawAdoptionEditsTheRecordTheClaimIsBuiltFrom(t *testing.T) {
	sandbox(t)
	record := claimed(t, req("fix-auth", "/repo/web"), meta())
	require.NoError(t, Requeue(record, "deadbeef"))

	require.NoError(t, WithdrawAdoption(record))
	require.NoError(t, Claim(record, meta()))

	claims, err := ListClaims()
	require.NoError(t, err)
	require.Len(t, claims, 1)
	assert.False(t, claims[0].Request.Adopt, "the claim records what was executed")
	assert.Empty(t, claims[0].Request.AdoptTip)
	assert.False(t, claims[0].Request.CreatedAt.IsZero(),
		"and the record's own fields survive the edit, or nothing can judge its age")
}

// TestWithdrawAdoptionIsANoOpForAnOrdinaryRequest keeps the drain's common path off the disk.
// Every accepted `atrium new` would otherwise pay a rewrite of its own record for a licence it
// never carried.
func TestWithdrawAdoptionIsANoOpForAnOrdinaryRequest(t *testing.T) {
	sandbox(t)
	record, err := WriteCreate(req("fix-auth", "/repo/web"))
	require.NoError(t, err)
	before, err := os.Stat(record)
	require.NoError(t, err)

	require.NoError(t, WithdrawAdoption(record))

	after, err := os.Stat(record)
	require.NoError(t, err)
	assert.Equal(t, before.ModTime(), after.ModTime(), "nothing was rewritten")
}

// TestClearDisclosureRefusesADerivedPath: every entry point that writes or removes derives a
// second path by concatenation, so one handed a path already carrying a suffix mints a name no
// walk in this package matches. That is the class validRecord closes, and these two readers of
// the derived suffix were outside it.
//
// For this one the miss is worse than an unreachable file. os.Remove on a name that can never
// exist returns ENOENT, which ClearDisclosure's already-gone rule reports as removed=true — and
// app.withdrawUnrecordedCreates reads that as done and drops the only handle it had on a
// disclosure naming a live session's branch, worktree and tmux session, with no error logged.
// app.applyCreateClaim handles a record path and its claim path within a few lines, so passing
// the latter is a live hazard rather than a hypothetical one.
func TestClearDisclosureRefusesADerivedPath(t *testing.T) {
	sandbox(t)
	record := claimed(t, req("fix-auth", "/repo/web"), meta())
	d := Disclosure{Title: "fix-auth", Branch: "zvi/fix-auth", Reason: "the row could not be written"}
	require.NoError(t, Disclose(record, &d))

	removed, err := ClearDisclosure(ClaimPath(record))

	require.Error(t, err, "a derived path is not a record")
	assert.False(t, removed, "and above all it did not report a withdrawal that never happened")
	assert.Equal(t, HasDisclosure, DisclosureMark(record),
		"the real mark is untouched, so the caller that keeps its handle still has one")
}

// TestDisclosureForReadsAVanishedMarkAsAbsent is the window between the stat and the read, and
// the direction it must fall.
//
// Presence is a stat and content is a read (see DisclosureFor), which is what keeps fd
// exhaustion from reporting every request terminal. The cost of the split is that another
// atrium's reader can clear a delivered mark in between. Reported as HasDisclosure with a zero
// Disclosure, that answers a healthy request with "a previous atrium gave up on this request
// and could not record why" and unlinks it — so the vanished case is absence, not an unreadable
// mark.
func TestDisclosureForReadsAVanishedMarkAsAbsent(t *testing.T) {
	sandbox(t)
	record, err := WriteCreate(req("fix-auth", "/repo/web"))
	require.NoError(t, err)
	d := Disclosure{Title: "fix-auth", Reason: "gave up"}
	require.NoError(t, Disclose(record, &d))
	require.Equal(t, HasDisclosure, DisclosureMark(record), "precondition: the stat sees it")

	// What the reader's unlink leaves behind, between one call and the next.
	require.NoError(t, os.Remove(record+disclosureSuffix))

	_, state := DisclosureFor(record)
	assert.Equal(t, NoDisclosure, state,
		"a mark that is gone is no mark, not a mark nobody could read")
}
