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

// TestListRetiresIgnoresWhatIsNotARecord: the spool directory holds more than records.
// Reject writes a receipt beside each one, WriteFileAtomic leaves an in-flight temp file
// while a concurrent kill commits, and an editor or a future version may leave a
// subdirectory. listFiles screens all three by name and by type, and it matters most here
// because this is the walk whose entries get acted on: a receipt decoded as a record would
// be a retirement nobody asked for.
func TestListRetiresIgnoresWhatIsNotARecord(t *testing.T) {
	sandbox(t)
	record := writeValidRetire(t)
	dir, err := RetireDir()
	require.NoError(t, err)
	require.NoError(t, Reject(record, "it has uncommitted changes"))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("scratch"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".1787-abcd.json.tmp"), []byte("{}"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "nested"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "nested", "1787729910438636469-b526c7c4.json"),
		[]byte("{}"), 0o644))
	// Precondition, so a listing that came back empty because the directory was empty
	// could not pass this.
	des, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Greater(t, len(des), 1, "the spool holds more than the one record")

	entries, err := ListRetires()

	require.NoError(t, err)
	assert.Empty(t, entries, "the record was receipted; nothing else in the directory is one")
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

// writeValidRetire spools one well-formed retirement and returns its path, for the tests
// that then break exactly one field of it through rewriteRetire.
func writeValidRetire(t *testing.T) string {
	t.Helper()
	record, err := WriteRetire(retirement("fix-auth", "/repo/web", ModeKill))
	require.NoError(t, err)
	return record
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

// TestListRetiresRejectsAFutureTimestamp closes the other half of the guard the
// zero-time screen exists for, and it has the same consequence for the same reason.
//
// expired() is `now.Sub(createdAt) > TTL`, which is negative — so never expired — for
// any CreatedAt ahead of the clock, and the record's filename is its UnixNano, so it
// also sorts to the tail of the spool and is drained last. A record written while the
// machine's clock was fast, or before an NTP correction stepped it backwards, would
// therefore be both immortal AND executable: it survives every sweep and every
// reset-era horizon check, and is acted on by whichever TUI starts next.
func TestListRetiresRejectsAFutureTimestamp(t *testing.T) {
	sandbox(t)
	record := writeValidRetire(t)
	rewriteRetire(t, record, func(m map[string]any) {
		m["created_at"] = time.Now().Add(2 * time.Hour).Format(time.RFC3339Nano)
	})

	entries, err := ListRetires()
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Error(t, entries[0].Err)
	assert.Contains(t, entries[0].Err.Error(), "the future")
}

// TestListRetiresToleratesASmallClockSkew: the guard is for a record nothing can ever
// expire, not for the sub-second disagreement between two processes stamping and reading
// a time. Refusing that would make the spool unreliable on any machine whose clock is
// being nudged by NTP while Atrium runs.
func TestListRetiresToleratesASmallClockSkew(t *testing.T) {
	sandbox(t)
	record := writeValidRetire(t)
	rewriteRetire(t, record, func(m map[string]any) {
		m["created_at"] = time.Now().Add(time.Second).Format(time.RFC3339Nano)
	})

	entries, err := ListRetires()
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.NoError(t, entries[0].Err)
}

// TestListRetiresRejectsAControlCharacterInTheTitle applies readCreate's screen to the
// other record type that carries an argv-shaped title.
//
// The reason is the same one FirstControlRune documents: a title is stored verbatim and
// rendered as one row, so an embedded newline splits that row and shifts every mouse
// zone below it, and an escape lets the title write its own ANSI. `atrium kill "$(gh
// issue view N --json title -q .title)"` is exactly how one arrives. The title here also
// has to MATCH a stored one to do anything, so a control character is doubly certain to
// be a mistake — but it reaches the drain's log line and its rejection receipt either way.
func TestListRetiresRejectsAControlCharacterInTheTitle(t *testing.T) {
	sandbox(t)
	record := writeValidRetire(t)
	rewriteRetire(t, record, func(m map[string]any) { m["title"] = "fix\nauth" })

	entries, err := ListRetires()
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Error(t, entries[0].Err)
	assert.Contains(t, entries[0].Err.Error(), "control character")
}

// TestWriteRetireRefusesAControlCharacterInTheTitle is the producer side of the same
// screen: the command is the last place with somebody to tell.
func TestWriteRetireRefusesAControlCharacterInTheTitle(t *testing.T) {
	sandbox(t)

	_, err := WriteRetire(Retire{Title: "fix\x1b[31mauth", Path: "/repo/web", Mode: ModeKill})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "control character")
}

// TestWriteRetireRefusesAFutureTimestamp: WriteRetire takes a caller-supplied CreatedAt
// verbatim, which is what lets a test stage an aged record — so it is also what would let
// a caller stage an immortal one.
func TestWriteRetireRefusesAFutureTimestamp(t *testing.T) {
	sandbox(t)

	_, err := WriteRetire(Retire{
		Title: "fix-auth", Path: "/repo/web", Mode: ModeKill,
		CreatedAt: time.Now().Add(2 * time.Hour),
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "the future")
}

// TestGerundNamesOnlyTheModesItKnows: the mapping used to return "killing" for anything
// that was not pause, so a mode this build does not recognise would be announced to the
// user as a kill — the most destructive of the two — while retireVerb refuses to act on
// it. A progress row and a warning that disagree with what will happen is worse than a
// vague one.
func TestGerundNamesOnlyTheModesItKnows(t *testing.T) {
	assert.Equal(t, "killing", ModeKill.Gerund())
	assert.Equal(t, "pausing", ModePause.Gerund())
	assert.Equal(t, "retiring", Mode("vaporize").Gerund(),
		"an unrecognised mode must not borrow the destructive verb's name")
	assert.Equal(t, "retiring", Mode("").Gerund())
}
