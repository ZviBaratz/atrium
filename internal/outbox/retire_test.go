package outbox

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func retirement(title, path string, mode Mode) Retire {
	return Retire{Title: title, Path: path, Mode: mode}
}

// TestWriteRetireThenListRoundTrip is the basic contract: what a producer spools
// is what the drain reads, field for field. The drain acts on Mode and addresses
// the session by (Title, Path), so a field lost here retires the wrong session or
// retires it the wrong way.
func TestWriteRetireThenListRoundTrip(t *testing.T) {
	sandbox(t)
	record, err := WriteRetire(Retire{
		Title: "fix-auth", Path: "/repo/web", TmuxName: "atrium-fix-auth", Mode: ModeKill,
	})
	require.NoError(t, err)

	entries, err := ListRetires()
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.NoError(t, entries[0].Err)
	assert.Equal(t, record, entries[0].Path)
	assert.Equal(t, "fix-auth", entries[0].Retire.Title)
	assert.Equal(t, "/repo/web", entries[0].Retire.Path)
	assert.Equal(t, "atrium-fix-auth", entries[0].Retire.TmuxName)
	assert.Equal(t, ModeKill, entries[0].Retire.Mode)
	assert.False(t, entries[0].Retire.CreatedAt.IsZero(), "WriteRetire stamps the timestamp")
}

// TestRetireSpoolIsInvisibleToTheOtherTwo is the property the package's
// separate-directories argument exists for, extended to a third record type.
//
// A retire record and a prompt are opposites in what they authorize — one deletes a
// branch, the other types text — so a reader that mistook one for the other would
// act on a payload it never validated. The retire directory nests inside the prompt
// spool's directory exactly as create/ does, so the same two independent guards in
// listFiles keep it out: IsDir, and a name the record format does not match.
func TestRetireSpoolIsInvisibleToTheOtherTwo(t *testing.T) {
	sandbox(t)
	_, err := WriteRetire(retirement("fix-auth", "/repo/web", ModeKill))
	require.NoError(t, err)

	messages, err := List()
	require.NoError(t, err)
	assert.Empty(t, messages, "a retirement is not a prompt")

	creates, err := ListCreates()
	require.NoError(t, err)
	assert.Empty(t, creates, "and it is not a create request")
}

// TestListRetiresIgnoresTheNestedSpools is the same property from the other side:
// the retire walk must not pick up the sibling spools' records either.
func TestListRetiresIgnoresTheNestedSpools(t *testing.T) {
	sandbox(t)
	_, err := Write(msg("fix-auth", "/repo/web", "hello"))
	require.NoError(t, err)
	_, err = WriteCreate(Request{Title: "other", Path: "/repo/web"})
	require.NoError(t, err)

	entries, err := ListRetires()
	require.NoError(t, err)
	assert.Empty(t, entries)
}

// TestListRetiresToleratesAnAbsentSpool: never having run `atrium kill` is the
// steady state, not an error.
func TestListRetiresToleratesAnAbsentSpool(t *testing.T) {
	sandbox(t)
	entries, err := ListRetires()
	assert.NoError(t, err)
	assert.Empty(t, entries)
}

// TestWriteRetireRefusesAnUnaddressableTarget: (Title, Path) is how the drain finds
// the session, and a relative path resolves against the DRAINING TUI's working
// directory rather than the producer's — the same hazard readCreate screens for. A
// record that names nothing addressable is refused at the producer, where there is
// still somebody to tell.
func TestWriteRetireRefusesAnUnaddressableTarget(t *testing.T) {
	sandbox(t)
	for _, tc := range []struct {
		name string
		r    Retire
	}{
		{"no title", retirement("", "/repo/web", ModeKill)},
		{"blank title", retirement("   ", "/repo/web", ModeKill)},
		{"no path", retirement("fix-auth", "", ModeKill)},
		{"relative path", retirement("fix-auth", "repo/web", ModeKill)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := WriteRetire(tc.r)
			assert.Error(t, err)
		})
	}
}

// TestWriteRetireRefusesAnUnknownMode keeps the discriminator honest at the only
// point where refusing is cheap. Mode is what decides between deleting a branch and
// keeping it, so a record carrying anything else must not reach a drain that has to
// guess — and defaulting a blank mode to either verb is the guess.
func TestWriteRetireRefusesAnUnknownMode(t *testing.T) {
	sandbox(t)
	for _, mode := range []Mode{"", "Kill", "destroy", "kill "} {
		_, err := WriteRetire(retirement("fix-auth", "/repo/web", mode))
		assert.Error(t, err, "mode %q must be refused", mode)
	}
}

// TestListRetiresRejectsAForeignVersion: a record from a different atrium is
// surfaced as an error rather than decoded on a guess, because the guess would be
// acted on by a teardown.
func TestListRetiresRejectsAForeignVersion(t *testing.T) {
	sandbox(t)
	record, err := WriteRetire(retirement("fix-auth", "/repo/web", ModeKill))
	require.NoError(t, err)
	rewriteRetire(t, record, func(m map[string]any) { m["version"] = 99 })

	entries, err := ListRetires()
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Error(t, entries[0].Err)
	assert.Equal(t, record, entries[0].Path, "the caller still needs the path to discard it")
}

// TestListRetiresRejectsAMissingTimestamp is readCreate's argument applied here, and
// it has more teeth for this record type. expired() treats a zero time as never
// expired — deliberately — so a hand-written record with no created_at would be
// both immortal and executable: it would survive every sweep and be acted on by
// whichever TUI started next, killing a session weeks after anyone asked.
func TestListRetiresRejectsAMissingTimestamp(t *testing.T) {
	sandbox(t)
	record, err := WriteRetire(retirement("fix-auth", "/repo/web", ModeKill))
	require.NoError(t, err)
	rewriteRetire(t, record, func(m map[string]any) { delete(m, "created_at") })

	entries, err := ListRetires()
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Error(t, entries[0].Err)
}

// TestListRetiresRejectsAModeItCannotActOn: what WriteRetire refuses to write is
// refused on the way back in. Nothing this package produces can fail it; a
// hand-written file can, and a record that gets past here is executed.
func TestListRetiresRejectsAModeItCannotActOn(t *testing.T) {
	sandbox(t)
	record, err := WriteRetire(retirement("fix-auth", "/repo/web", ModeKill))
	require.NoError(t, err)
	rewriteRetire(t, record, func(m map[string]any) { m["mode"] = "destroy" })

	entries, err := ListRetires()
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Error(t, entries[0].Err)
}

func TestRetireExpiresPastTheHorizon(t *testing.T) {
	r := Retire{CreatedAt: time.Now().Add(-2 * TTL)}
	assert.True(t, r.Expired(time.Now()))
	assert.False(t, Retire{CreatedAt: time.Now()}.Expired(time.Now()))
}

// TestRetireRecordTakesTheReceiptProtocol: the retire spool must reuse the
// path-keyed receipt trio rather than grow its own, because `atrium kill --wait`
// blocks on exactly the same Rejection call `send` and `new` do.
func TestRetireRecordTakesTheReceiptProtocol(t *testing.T) {
	sandbox(t)
	record, err := WriteRetire(retirement("fix-auth", "/repo/web", ModeKill))
	require.NoError(t, err)

	require.NoError(t, Reject(record, "it has uncommitted changes"))
	reason, ok := Rejection(record)
	require.True(t, ok)
	assert.Equal(t, "it has uncommitted changes", reason)
	assert.NoFileExists(t, record, "a rejected record is not re-judged forever")

	require.NoError(t, ClearRejection(record))
	_, ok = Rejection(record)
	assert.False(t, ok)
}

// TestSweepRejectionsReachesTheRetireSpool: a receipt is read only by a producer
// still blocked in --wait, so one past the horizon has no reader left. A spool the
// sweep does not walk leaks one file per refusal for the life of the data dir.
func TestSweepRejectionsReachesTheRetireSpool(t *testing.T) {
	sandbox(t)
	record, err := WriteRetire(retirement("fix-auth", "/repo/web", ModeKill))
	require.NoError(t, err)
	require.NoError(t, Reject(record, "busy"))

	SweepRejections(time.Now())
	_, ok := Rejection(record)
	require.True(t, ok, "a fresh receipt may still have a reader")

	SweepRejections(time.Now().Add(TTL + time.Hour))
	_, ok = Rejection(record)
	assert.False(t, ok, "a receipt past the horizon must be swept")
}

// TestClearDiscardsQueuedRetiresWithAReceipt: `atrium reset` wipes the sessions a
// queued retirement names, so the record must go — but through Reject, not an
// unlink. A producer blocked in --wait reads the record vanishing as "done", so a
// silent removal would report reset to `atrium kill --wait` as a successful kill.
func TestClearDiscardsQueuedRetiresWithAReceipt(t *testing.T) {
	sandbox(t)
	record, err := WriteRetire(retirement("fix-auth", "/repo/web", ModeKill))
	require.NoError(t, err)

	removed, err := Clear()
	require.NoError(t, err)
	assert.Positive(t, removed, "the queued retirement is counted among what reset discarded")
	assert.NoFileExists(t, record)
	reason, ok := Rejection(record)
	require.True(t, ok, "a waiting producer must be able to tell a discard from a delivery")
	assert.Equal(t, clearReason, reason)
}

// rewriteRetire edits a spooled record's raw JSON in place, standing in for a
// record written by a different atrium or by hand. It goes through the map rather
// than the struct so a field can be removed outright, which is the case
// TestListRetiresRejectsAMissingTimestamp needs and no typed write can stage.
func rewriteRetire(t *testing.T, record string, edit func(map[string]any)) {
	t.Helper()
	data, err := os.ReadFile(record)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(data, &m))
	edit(m)
	out, err := json.Marshal(m)
	require.NoError(t, err)
	require.NoError(t, config.WriteFileAtomic(record, out, 0o644))
}
