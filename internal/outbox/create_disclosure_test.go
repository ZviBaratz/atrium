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
// overriding it rather than by being the only one that sets anything.
func leftovers() Disclosure {
	return Disclosure{
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
	assert.False(t, d.CreatedAt.IsZero(), "and it has to be datable, or the sweep cannot age it")
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
	record, err := WriteCreate(req("fix-auth", "/repo/web"))
	require.NoError(t, err)
	require.NoError(t, Disclose(record, leftovers()))

	require.NoError(t, ClearDisclosure(record))
	_, ok := DisclosureFor(record)
	assert.False(t, ok)
	assert.NoError(t, ClearDisclosure(record), "already gone is not an error")
}

// TestClearLeavesADisclosure pins the `atrium reset` decision. Reset discards every queued
// record so the next launch cannot rebuild deleted state — but a disclosure is not queued
// work, it is the only record of a branch and a worktree belonging to nothing, and reset
// does not remove those: CleanupWorktrees enumerates the repos of the rows it just deleted,
// and an orphan has no row to be enumerated through. Discarding it would make the reset the
// reason nobody is ever told about the leftovers it did not clean up.
func TestClearLeavesADisclosure(t *testing.T) {
	sandbox(t)
	record, err := WriteCreate(req("fix-auth", "/repo/web"))
	require.NoError(t, err)
	require.NoError(t, Disclose(record, leftovers()))
	require.NoError(t, Reject(record, "could not record it"))

	removed, err := Clear()
	require.NoError(t, err)
	assert.Zero(t, removed, "there was no queued record left to discard")
	_, ok := DisclosureFor(record)
	assert.True(t, ok, "and the orphan still has something naming it")
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

	assert.Error(t, Claim(derived, meta()))
	assert.Error(t, Requeue(derived, "deadbeef"))
	assert.Error(t, DiscardCreate(derived))
	assert.Error(t, Disclose(derived, leftovers()))

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
