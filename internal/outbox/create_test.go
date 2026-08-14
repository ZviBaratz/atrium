package outbox

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func req(title, path string) Request {
	return Request{Title: title, Path: path}
}

// TestWriteCreateThenListRoundTrip is the basic contract: a written request comes
// back out of ListCreates with every field intact and no decode error.
func TestWriteCreateThenListRoundTrip(t *testing.T) {
	sandbox(t)
	name, err := WriteCreate(Request{
		Title:   "fix-auth",
		Path:    "/repo/web",
		Program: "codex",
		Branch:  "release/2.0",
		Prompt:  "start on the parser",
		Force:   true,
	})
	require.NoError(t, err)
	assert.FileExists(t, name)

	entries, err := ListCreates()
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.NoError(t, entries[0].Err)

	got := entries[0].Request
	assert.Equal(t, "fix-auth", got.Title)
	assert.Equal(t, "/repo/web", got.Path)
	assert.Equal(t, "codex", got.Program)
	assert.Equal(t, "release/2.0", got.Branch)
	assert.Equal(t, "start on the parser", got.Prompt)
	assert.True(t, got.Force)
	assert.Equal(t, name, entries[0].Path)
}

// TestWriteCreateStampsVersionAndCreatedAt: both are WriteCreate's to set, never
// the caller's. A request written with neither must still decode and still be
// datable, or the version gate and the TTL horizon have nothing to read.
func TestWriteCreateStampsVersionAndCreatedAt(t *testing.T) {
	sandbox(t)
	before := time.Now()
	_, err := WriteCreate(req("t", "/repo"))
	require.NoError(t, err)

	entries, err := ListCreates()
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, createVersion, entries[0].Request.Version)
	assert.False(t, entries[0].Request.CreatedAt.Before(before))
}

// TestWriteCreatePreservesExplicitCreatedAt pins the seam the ordering test below
// relies on: a caller-supplied timestamp is kept, not overwritten.
func TestWriteCreatePreservesExplicitCreatedAt(t *testing.T) {
	sandbox(t)
	want := time.Unix(0, 1_500_000_000_000_000_000)
	r := req("t", "/repo")
	r.CreatedAt = want
	_, err := WriteCreate(r)
	require.NoError(t, err)

	entries, err := ListCreates()
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.True(t, want.Equal(entries[0].Request.CreatedAt))
}

// TestWriteCreateRejectsIncompleteRequest: a request with no title or no path
// cannot be executed by any drain, so it must never reach the spool — the same
// up-front refusal Write applies.
func TestWriteCreateRejectsIncompleteRequest(t *testing.T) {
	cases := map[string]Request{
		"no title": {Path: "/repo"},
		"no path":  {Title: "t"},
		"neither":  {},
	}
	for name, r := range cases {
		t.Run(name, func(t *testing.T) {
			sandbox(t)
			_, err := WriteCreate(r)
			require.Error(t, err)

			entries, err := ListCreates()
			require.NoError(t, err)
			assert.Empty(t, entries)
		})
	}
}

// TestListCreatesOrdersChronologicallyAcrossDigitWidths guards the zero padding
// writeRecord applies. Straddling 10^18 nanoseconds is where an unpadded name
// would sort the older request last, and a drain taking one request per tick
// would then execute them out of order.
func TestListCreatesOrdersChronologicallyAcrossDigitWidths(t *testing.T) {
	sandbox(t)
	older := req("older", "/repo")
	older.CreatedAt = time.Unix(0, 999_999_999_999_999_999) // 18 digits
	newer := req("newer", "/repo")
	newer.CreatedAt = time.Unix(0, 1_000_000_000_000_000_001) // 19 digits

	// Written newest-first, so passing cannot be an artifact of write order.
	_, err := WriteCreate(newer)
	require.NoError(t, err)
	_, err = WriteCreate(older)
	require.NoError(t, err)

	entries, err := ListCreates()
	require.NoError(t, err)
	require.Len(t, entries, 2)
	assert.Equal(t, "older", entries[0].Request.Title)
	assert.Equal(t, "newer", entries[1].Request.Title)
}

// TestListCreatesReportsUnsupportedVersion: a request from a future atrium is
// surfaced as an entry carrying Err — with its Path set, so the drain can discard
// it — rather than decoded on a guess or silently skipped.
func TestListCreatesReportsUnsupportedVersion(t *testing.T) {
	sandbox(t)
	writeRawCreate(t, "0000000000000000001-abcdabcd.json",
		`{"version":99,"title":"t","path":"/repo"}`)

	entries, err := ListCreates()
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Error(t, entries[0].Err)
	assert.Contains(t, entries[0].Err.Error(), "version 99")
	assert.NotEmpty(t, entries[0].Path)
}

// TestListCreatesReportsUndecodableFile: same contract for a file that is not
// JSON at all. Skipping it would leave a file nobody can decode and nobody
// deletes, re-read on every drain tick forever.
func TestListCreatesReportsUndecodableFile(t *testing.T) {
	sandbox(t)
	writeRawCreate(t, "0000000000000000002-abcdabcd.json", `{not json`)

	entries, err := ListCreates()
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Error(t, entries[0].Err)
	assert.NotEmpty(t, entries[0].Path)
}

// TestListCreatesOnMissingDirIsEmpty: never having run `atrium new` is the steady
// state, not an error.
func TestListCreatesOnMissingDirIsEmpty(t *testing.T) {
	sandbox(t)
	entries, err := ListCreates()
	require.NoError(t, err)
	assert.Empty(t, entries)
}

// TestListIgnoresTheCreateSubdirectory is the guard behind the whole two-spool
// design. A prompt drain that could read a create request would decode it as a
// Message and — because a request's Title and Path are shaped exactly like a
// message's target — type the first prompt into whatever session already holds
// that title.
//
// It asserts the property, not a mechanism, because two independent guards in
// listFiles produce it (IsDir, and a name the record format does not match) and
// removing either one on its own leaves this test passing. An atrium too old to
// know the create spool exists inherits the same property: it runs this loop.
func TestListIgnoresTheCreateSubdirectory(t *testing.T) {
	sandbox(t)
	_, err := WriteCreate(Request{Title: "fix-auth", Path: "/repo/web", Prompt: "do the thing"})
	require.NoError(t, err)

	entries, err := List()
	require.NoError(t, err)
	assert.Empty(t, entries, "a create request must be invisible to the prompt spool")
}

// TestListCreatesIgnoresPromptMessages is the other direction: a message spooled
// by `atrium send` sits in the parent directory and must never be read as a
// request to create a session.
func TestListCreatesIgnoresPromptMessages(t *testing.T) {
	sandbox(t)
	_, err := Write(msg("fix-auth", "/repo/web", "hello"))
	require.NoError(t, err)

	entries, err := ListCreates()
	require.NoError(t, err)
	assert.Empty(t, entries, "a prompt must be invisible to the create spool")
}

// TestListCreatesIgnoresForeignFiles: the name filter is what lets a drain delete
// an undecodable entry. Anything not written by this package must not be listed,
// or the drain would remove someone else's file — including WriteFileAtomic's
// in-flight temp for a concurrent `atrium new`, which lives in this directory.
func TestListCreatesIgnoresForeignFiles(t *testing.T) {
	sandbox(t)
	writeRawCreate(t, ".0000000000000000001-abcdabcd.json.tmp-9999", `{"version":1}`)
	writeRawCreate(t, "notes.json", `{"version":1}`)
	writeRawCreate(t, "0000000000000000001-abcdabcd.json.rejected", "a reason")

	entries, err := ListCreates()
	require.NoError(t, err)
	assert.Empty(t, entries)
}

// TestRequestExpiredBoundary: strictly greater-than, so a request exactly at the
// horizon still creates. Matches Message.Expired, which it now shares code with.
func TestRequestExpiredBoundary(t *testing.T) {
	now := time.Now()
	at := Request{CreatedAt: now.Add(-TTL)}
	assert.False(t, at.Expired(now))
	past := Request{CreatedAt: now.Add(-TTL - time.Nanosecond)}
	assert.True(t, past.Expired(now))
}

// TestRequestExpiredIgnoresZeroCreatedAt: a request with no timestamp is never
// expired, so a future version that stopped writing the field would not have
// every request silently discarded as infinitely old.
func TestRequestExpiredIgnoresZeroCreatedAt(t *testing.T) {
	assert.False(t, Request{}.Expired(time.Now()))
}

// TestSweepRejectionsClearsBothSpools: `atrium new --wait` reads its failures
// through the same receipts `atrium send --wait` does, so a sweep that only
// covered the prompt spool would leak one file per refused create request for the
// life of the data dir.
func TestSweepRejectionsClearsBothSpools(t *testing.T) {
	sandbox(t)

	msgPath, err := Write(msg("t", "/repo", "x"))
	require.NoError(t, err)
	require.NoError(t, Reject(msgPath, "gone"))

	reqPath, err := WriteCreate(req("t2", "/repo"))
	require.NoError(t, err)
	require.NoError(t, Reject(reqPath, "refused"))

	// Both receipts exist now, and both survive a sweep at the present moment.
	SweepRejections(time.Now())
	assert.FileExists(t, msgPath+rejectedSuffix)
	assert.FileExists(t, reqPath+rejectedSuffix)

	// Past the horizon, both go — measured from the receipts' own mtimes, so the
	// sweep clock has to be pushed forward rather than the files back-dated.
	SweepRejections(time.Now().Add(TTL + time.Minute))
	assert.NoFileExists(t, msgPath+rejectedSuffix)
	assert.NoFileExists(t, reqPath+rejectedSuffix)
}

// TestWriteCreateLeavesNoTempFile: a completed write cleans up after itself, so
// the spool does not accumulate orphans.
func TestWriteCreateLeavesNoTempFile(t *testing.T) {
	sandbox(t)
	_, err := WriteCreate(req("t", "/repo"))
	require.NoError(t, err)

	dir, err := CreateDir()
	require.NoError(t, err)
	left, err := filepath.Glob(filepath.Join(dir, ".*.tmp-*"))
	require.NoError(t, err)
	assert.Empty(t, left)
}

// TestCreateDirIsUnderTheOutbox pins the on-disk layout the two-spool argument
// rests on: nested, so listFiles' IsDir skip is what separates them.
func TestCreateDirIsUnderTheOutbox(t *testing.T) {
	sandbox(t)
	outboxDir, err := Dir()
	require.NoError(t, err)
	createDir, err := CreateDir()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(outboxDir, createDirName), createDir)
}

func writeRawCreate(t *testing.T, name, content string) {
	t.Helper()
	dir, err := CreateDir()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
}
