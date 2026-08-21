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
	d, ok := DisclosureFor(record)
	require.True(t, ok, "the disclosure is the only thing left that names the leftovers")
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

	_, ok := DisclosureFor(record)
	assert.False(t, ok, "nothing has given up on this request")

	require.NoError(t, os.WriteFile(disclosurePath(record), []byte("{not json"), 0o644))
	_, ok = DisclosureFor(record)
	assert.True(t, ok, "a disclosure that cannot be read still says the caller was answered")
}

// TestClearDisclosureIsIdempotent: the reader clears what it showed, and a second flush
// (or a sweep that got there first) must not turn that into an error the user sees.
func TestClearDisclosureIsIdempotent(t *testing.T) {
	sandbox(t)
	record := claimed(t, req("fix-auth", "/repo/web"), meta())
	require.NoError(t, Disclose(record, leftovers()))
	require.NoError(t, Reject(record, "could not record it"))
	require.NoError(t, DiscardCreate(record))

	require.NoError(t, ClearDisclosure(record))
	_, ok := DisclosureFor(record)
	assert.False(t, ok)
	assert.NoError(t, ClearDisclosure(record), "already gone is not an error")
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

			require.NoError(t, ClearDisclosure(record), "and it is not an error to try")

			_, ok := DisclosureFor(record)
			assert.True(t, ok, "the mark outlives the report while anything can still act on it")

			SweepDisclosures(time.Now().Add(TTL + time.Hour))
			_, ok = DisclosureFor(record)
			assert.True(t, ok, "and the horizon is not a second way round it")
		})
	}
}

// TestClearKeepsADisclosureAndTrimsIt pins the `atrium reset` decision, which is neither
// "keep it" nor "drop it": reset removes SOME of what a disclosure names.
//
// Kept, because the branch it names is what reset leaves standing — `branch -D` and
// `worktree prune` are the repo-scoped halves of git.CleanupWorktrees, and an orphan has no
// row to be enumerated through. Trimmed, because the worktree DIRECTORY and the agent are
// the unscoped halves: CleanupWorktrees removes every top-level entry under the data dir's
// worktrees/ tree and tmux.CleanupSessions kills every session matching the prefix. Carried
// across unedited, the report would send the next launch's reader after two things reset
// had already destroyed, under a header saying nothing here will clean them up.
func TestClearKeepsADisclosureAndTrimsIt(t *testing.T) {
	sandbox(t)
	record, err := WriteCreate(req("fix-auth", "/repo/web"))
	require.NoError(t, err)
	require.NoError(t, Disclose(record, leftovers()))
	require.NoError(t, Reject(record, "could not record it"))

	removed, err := Clear()
	require.NoError(t, err)
	assert.Zero(t, removed, "there was no queued record left to discard")
	d, ok := DisclosureFor(record)
	require.True(t, ok, "and the orphan still has something naming it")
	assert.Equal(t, "zvi/fix-auth", d.Branch, "the branch reset leaves standing")
	assert.Empty(t, d.Worktree, "the directory reset removed")
	assert.Empty(t, d.TmuxName, "the agent reset killed")
	assert.NotEmpty(t, d.Reason, "and it still says why the create failed")
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

	d, ok := DisclosureFor(record)
	require.True(t, ok, "so the branch it made has to be named somewhere")
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

	d, ok := DisclosureFor(record)
	require.True(t, ok)
	assert.Equal(t, leftovers().Reason, d.Reason, "the refusal's own wording, not reset's")
	assert.Empty(t, d.TmuxName, "trimmed to what reset leaves, as any other carried one is")
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
	_, ok := DisclosureFor(record)
	assert.True(t, ok, "a just-written disclosure survives even for a TTL-old request")

	SweepDisclosures(time.Now().Add(TTL + time.Hour))
	_, ok = DisclosureFor(record)
	assert.False(t, ok, "past the horizon there is no reader left")
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
	_, ok = DisclosureFor(record)
	assert.True(t, ok)
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
