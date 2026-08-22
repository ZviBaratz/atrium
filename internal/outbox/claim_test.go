package outbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// meta is a filled-in ClaimMeta, so a test that cares about one field says so by
// overriding it rather than by being the only one that sets anything.
func meta() ClaimMeta {
	return ClaimMeta{At: time.Now(), SessionBranch: "zvi/fix-auth"}
}

// claimed writes a request, claims it, and returns the record path.
func claimed(t *testing.T, r Request, m ClaimMeta) string {
	t.Helper()
	record, err := WriteCreate(r)
	require.NoError(t, err)
	require.NoError(t, Claim(record, m))
	return record
}

// TestClaimTakesTheRecordOutOfTheDrainsSight is the property the whole claim step
// exists for. A claimed request must not be visible to ListCreates, because every arm
// of the drain loop walks that listing: seen there, an in-flight request is re-executed
// (a second session on one branch), or expired out from under its own running Start.
func TestClaimTakesTheRecordOutOfTheDrainsSight(t *testing.T) {
	sandbox(t)
	record := claimed(t, req("fix-auth", "/repo/web"), meta())

	entries, err := ListCreates()
	require.NoError(t, err)
	assert.Empty(t, entries, "a claimed request is not a queued one")

	assert.NoFileExists(t, record)
	assert.FileExists(t, ClaimPath(record))
}

// TestClaimRecordsTheEvidenceRecoveryNeeds: the claim is read back by a different
// process, so what it carries is the whole of what that process can know.
func TestClaimRecordsTheEvidenceRecoveryNeeds(t *testing.T) {
	sandbox(t)
	at := time.Now().Truncate(time.Second)
	record := claimed(t, req("fix-auth", "/repo/web"),
		ClaimMeta{At: at, SessionBranch: "zvi/fix-auth", BranchExisted: true})

	claims, err := ListClaims()
	require.NoError(t, err)
	require.Len(t, claims, 1)
	require.NotNil(t, claims[0].Request.Claim)
	assert.Equal(t, "zvi/fix-auth", claims[0].Request.Claim.SessionBranch)
	assert.True(t, claims[0].Request.Claim.BranchExisted)
	assert.WithinDuration(t, at, claims[0].Request.Claim.At, time.Second)
	// The payload survives the claim: recovery re-queues this record for the drain to
	// execute, so losing a field here would silently create a different session.
	assert.Equal(t, "fix-auth", claims[0].Request.Title)
	assert.Equal(t, record, claims[0].Path, "entries are keyed on the RECORD path")
}

// TestListClaimsReportsTheRecordPath pins the addressing rule the receipt protocol
// depends on. Everything a producer watches or reads — the record it was handed by
// WriteCreate, the ".rejected" receipt beside it — hangs off the record path, so an
// entry that reported the claim file's own path would have callers write receipts to
// "….json.claimed.rejected", which no `--wait` ever looks for. The refusal would be
// invisible and the caller would time out instead.
func TestListClaimsReportsTheRecordPath(t *testing.T) {
	sandbox(t)
	record := claimed(t, req("fix-auth", "/repo/web"), meta())

	claims, err := ListClaims()
	require.NoError(t, err)
	require.Len(t, claims, 1)
	require.Equal(t, record, claims[0].Path)

	require.NoError(t, Reject(claims[0].Path, "no"))
	reason, ok := Rejection(record)
	assert.True(t, ok, "a receipt written against the reported path must be readable at the record path")
	assert.Equal(t, "no", reason)
}

// TestClaimSurvivesACrashBetweenItsTwoSteps is why Claim commits the enriched record
// before it renames, rather than writing a claim file and then unlinking the record.
//
// Both orderings have a window; only this one's intermediate state is a state the rest
// of the system already understands. Here it is an ordinary queued request that happens
// to carry claim fields, so the next drain executes and re-claims it. The alternative
// leaves the record AND a claim on disk at once, which is a third state every walk in
// the package would have to recognise.
//
// Staged by writing the enriched record without renaming — exactly what a process
// killed between Claim's two calls leaves behind.
func TestClaimSurvivesACrashBetweenItsTwoSteps(t *testing.T) {
	sandbox(t)
	record, err := WriteCreate(req("fix-auth", "/repo/web"))
	require.NoError(t, err)

	r := readCreate(record).Request
	m := meta()
	r.Claim = &m
	require.NoError(t, writeRequestInPlace(record, r))

	entries, err := ListCreates()
	require.NoError(t, err)
	require.Len(t, entries, 1, "a half-claimed request is still a queued one")
	assert.Equal(t, "fix-auth", entries[0].Request.Title)
	assert.NoError(t, entries[0].Err, "and is decodable, not a poisoned file")

	claims, err := ListClaims()
	require.NoError(t, err)
	assert.Empty(t, claims, "nothing is claimed until the rename lands")
}

// TestRequeueReturnsTheRequestToTheSpool: recovery's re-queue has to produce a record
// the ordinary drain executes, not a special one.
func TestRequeueReturnsTheRequestToTheSpool(t *testing.T) {
	sandbox(t)
	record := claimed(t, req("fix-auth", "/repo/web"), meta())

	require.NoError(t, Requeue(record, ""))

	entries, err := ListCreates()
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "fix-auth", entries[0].Request.Title)
	assert.False(t, entries[0].Request.Adopt, "a plain re-queue must not license an adoption")
	assert.NoFileExists(t, ClaimPath(record))
}

// TestRequeueWithAdoptMarksTheRequest is the licence half. Adopt is what lets the drain
// skip its branch check for this one request, so it has to survive the round trip to
// disk — a flag lost here silently degrades recovery to the refusal it replaces. So does
// the tip beside it, which is what the drain re-checks before taking the skip. app's
// recheckAdoption is what fails closed on a pin lost in transit — it reads "no pin" as "not
// the branch we vetted" and withdraws the licence. createConflictIn only reads the licence,
// and would take the skip on a bare Adopt.
func TestRequeueWithAdoptMarksTheRequest(t *testing.T) {
	sandbox(t)
	record := claimed(t, req("fix-auth", "/repo/web"), meta())

	require.NoError(t, Requeue(record, "deadbeef"))

	entries, err := ListCreates()
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.True(t, entries[0].Request.Adopt)
	assert.Equal(t, "deadbeef", entries[0].Request.AdoptTip)
	require.NotNil(t, entries[0].Request.Claim,
		"and the evidence stays, so a second interruption is judged on the same facts")
	assert.Equal(t, "zvi/fix-auth", entries[0].Request.Claim.SessionBranch)
}

// TestDiscardCreateDropsBothForms. holdCreateRequest builds the session even when the
// claim could not be written (a full or read-only data dir), so "in flight" does not
// imply "claimed" — and a settle that removed only the claim would leave the record for
// the next launch to execute a second time: one session becomes two, on one branch,
// with nothing in the protocol noticing.
func TestDiscardCreateDropsBothForms(t *testing.T) {
	sandbox(t)
	t.Run("claimed", func(t *testing.T) {
		record := claimed(t, req("fix-auth", "/repo/web"), meta())
		require.NoError(t, DiscardCreate(record))
		assert.NoFileExists(t, ClaimPath(record))
		assert.NoFileExists(t, record)
	})
	t.Run("never claimed", func(t *testing.T) {
		record, err := WriteCreate(req("other", "/repo/web"))
		require.NoError(t, err)
		require.NoError(t, DiscardCreate(record))
		assert.NoFileExists(t, record, "an unclaimed request must go too, or it is executed again")
		entries, err := ListCreates()
		require.NoError(t, err)
		assert.Empty(t, entries)
	})
}

// TestClearDiscardsAClaimedRequest: `atrium reset` wipes state.json, and a claim left
// behind is exactly what the next launch's reconcile reads as work to finish — so the
// reset would be followed by a session reappearing. Through Reject rather than a bare
// unlink, for Clear's own reason: a producer blocked in --wait cannot tell a discard
// from a delivery except by the receipt.
func TestClearDiscardsAClaimedRequest(t *testing.T) {
	sandbox(t)
	record := claimed(t, req("fix-auth", "/repo/web"), meta())

	n, err := Clear()
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.NoFileExists(t, ClaimPath(record))
	reason, ok := Rejection(record)
	require.True(t, ok, "a cleared claim owes its producer a receipt")
	assert.Contains(t, reason, "atrium reset")
}

// TestListClaimsIgnoresWhatItDidNotWrite. ListClaims' callers DELETE what it surfaces,
// so the screening has to be the record name format and not a ".claimed" suffix — an
// editor's swap file or a hand-dropped note would otherwise be reported as a stranded
// build and unlinked.
func TestListClaimsIgnoresWhatItDidNotWrite(t *testing.T) {
	sandbox(t)
	dir, err := CreateDir()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	for _, name := range []string{
		"notes.claimed",
		"notes.json.claimed",             // right suffix, wrong stem
		"123-abc.json.claimed",           // too few digits
		"0000000000000000001-zz.claimed", // no .json under the suffix
	} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("{}"), 0o600))
	}

	claims, err := ListClaims()
	require.NoError(t, err)
	assert.Empty(t, claims)
}

// TestListClaimsSurfacesAnUndecodableClaim is the other side of that screen: a file
// that IS ours and cannot be read must come back carrying Err rather than be skipped,
// because the reconcile is the only party that can discard it and one nobody discards
// is re-read on every launch forever.
func TestListClaimsSurfacesAnUndecodableClaim(t *testing.T) {
	sandbox(t)
	record := claimed(t, req("fix-auth", "/repo/web"), meta())
	require.NoError(t, config.WriteFileAtomic(ClaimPath(record), []byte("{not json"), 0o644))

	claims, err := ListClaims()
	require.NoError(t, err)
	require.Len(t, claims, 1)
	assert.Error(t, claims[0].Err)
	assert.Equal(t, record, claims[0].Path, "with the record path, so a receipt can still be written")
}

// TestUnclaimedRequestStaysByteIdenticalGuardsTheWireFormat. Request grew a Claim
// pointer and an Adopt flag at createVersion 1, on the argument that both are additive
// and omitted when unset — which is only true if they really are omitted. An inline
// time.Time would emit year 1 on every request (encoding/json omits empty values for
// basic types only), and an older atrium reading a field it does not know is not the
// hazard; a NEWER one is fine either way. The hazard is drift in what "unchanged"
// means, so it is asserted on the bytes.
func TestUnclaimedRequestStaysByteIdenticalGuardsTheWireFormat(t *testing.T) {
	sandbox(t)
	record, err := WriteCreate(req("fix-auth", "/repo/web"))
	require.NoError(t, err)

	raw, err := os.ReadFile(record)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(raw, &got))

	for _, k := range []string{"claim", "adopt"} {
		_, present := got[k]
		assert.False(t, present, "%q must be omitted from an unclaimed request", k)
	}
}
