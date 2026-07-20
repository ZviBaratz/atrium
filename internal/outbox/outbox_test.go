package outbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sandbox points HOME at a fresh temp dir so each test gets its own data dir
// (and so no test can ever touch the user's real ~/.atrium, per CLAUDE.md).
func sandbox(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
}

func msg(title, path, text string) Message {
	return Message{Title: title, Path: path, Text: text}
}

// TestWriteThenListRoundTrip is the basic contract: a written message comes back
// out of List with every field intact and no decode error.
func TestWriteThenListRoundTrip(t *testing.T) {
	sandbox(t)
	name, err := Write(Message{
		Title:    "fix-auth",
		Path:     "/repo/web",
		TmuxName: "atrium_web_fix-auth",
		Text:     "rebase on main",
	})
	require.NoError(t, err)
	assert.FileExists(t, name)

	entries, err := List()
	require.NoError(t, err)
	require.Len(t, entries, 1)

	got := entries[0]
	require.NoError(t, got.Err)
	assert.Equal(t, name, got.Path)
	assert.Equal(t, "fix-auth", got.Message.Title)
	assert.Equal(t, "/repo/web", got.Message.Path)
	assert.Equal(t, "atrium_web_fix-auth", got.Message.TmuxName)
	assert.Equal(t, "rebase on main", got.Message.Text)
}

// TestWriteStampsVersionAndCreatedAt covers Write filling in the two fields
// callers never set themselves, so there is a single source of truth for both.
func TestWriteStampsVersionAndCreatedAt(t *testing.T) {
	sandbox(t)
	before := time.Now()
	_, err := Write(msg("t", "/repo", "hello"))
	require.NoError(t, err)

	entries, err := List()
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.NoError(t, entries[0].Err)

	assert.Equal(t, currentVersion, entries[0].Message.Version)
	assert.False(t, entries[0].Message.CreatedAt.Before(before.Truncate(time.Second)),
		"CreatedAt should be stamped at write time, got %v", entries[0].Message.CreatedAt)
}

// TestWritePreservesExplicitCreatedAt pins the seam the ordering test relies on:
// a caller-supplied CreatedAt is kept rather than overwritten.
func TestWritePreservesExplicitCreatedAt(t *testing.T) {
	sandbox(t)
	want := time.Unix(0, 1_500_000_000_000_000_000).UTC()
	_, err := Write(Message{Title: "t", Path: "/repo", Text: "x", CreatedAt: want})
	require.NoError(t, err)

	entries, err := List()
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.NoError(t, entries[0].Err)
	assert.True(t, want.Equal(entries[0].Message.CreatedAt))
}

// TestListOrdersChronologicallyAcrossDigitWidths is the guard on the zero-padded
// filename timestamp. The two messages straddle the 10^18ns boundary, so their
// unpadded decimal forms (18 vs 19 digits) sort the wrong way lexicographically:
// dropping the padding in the filename format reverses this order.
func TestListOrdersChronologicallyAcrossDigitWidths(t *testing.T) {
	sandbox(t)
	earlier := time.Unix(0, 999_999_999_999_999_999).UTC() // 18 digits
	later := time.Unix(0, 1_000_000_000_000_000_000).UTC() // 19 digits

	// Written newest-first so a List that simply preserved write order would fail.
	_, err := Write(Message{Title: "later", Path: "/repo", Text: "second", CreatedAt: later})
	require.NoError(t, err)
	_, err = Write(Message{Title: "earlier", Path: "/repo", Text: "first", CreatedAt: earlier})
	require.NoError(t, err)

	entries, err := List()
	require.NoError(t, err)
	require.Len(t, entries, 2)
	assert.Equal(t, "earlier", entries[0].Message.Title, "older message must drain first")
	assert.Equal(t, "later", entries[1].Message.Title)
}

// TestListOrdersManyWrites checks ordering holds at volume with real clock
// timestamps, complementing the synthetic digit-width case above.
func TestListOrdersManyWrites(t *testing.T) {
	sandbox(t)
	const n = 50
	for i := range n {
		_, err := Write(Message{
			Title:     "s",
			Path:      "/repo",
			Text:      "m",
			CreatedAt: time.Unix(0, int64(1_700_000_000_000_000_000+i)),
		})
		require.NoError(t, err)
	}

	entries, err := List()
	require.NoError(t, err)
	require.Len(t, entries, n)
	for i := 1; i < len(entries); i++ {
		assert.False(t, entries[i].Message.CreatedAt.Before(entries[i-1].Message.CreatedAt),
			"entry %d is older than its predecessor", i)
	}
}

// TestWriteRejectsIncompleteMessage keeps unroutable messages out of the spool:
// without all three fields the drain could never match an instance.
func TestWriteRejectsIncompleteMessage(t *testing.T) {
	sandbox(t)
	for name, m := range map[string]Message{
		"no title": msg("", "/repo", "x"),
		"no path":  msg("t", "", "x"),
		"no text":  msg("t", "/repo", ""),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Write(m)
			require.Error(t, err)

			entries, listErr := List()
			require.NoError(t, listErr)
			assert.Empty(t, entries, "a rejected message must not reach the spool")
		})
	}
}

// TestListReportsUnsupportedVersion covers forward compatibility: a message from
// a newer atrium is surfaced as an error entry, not decoded on a guess.
func TestListReportsUnsupportedVersion(t *testing.T) {
	sandbox(t)
	writeRaw(t, "0000000000000000001-aaaaaaaa.json",
		`{"version":99,"title":"t","path":"/repo","text":"x"}`)

	entries, err := List()
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Error(t, entries[0].Err)
	assert.NotEmpty(t, entries[0].Path, "the caller needs the path to discard the file")
}

// TestListReportsUndecodableFile covers the corrupt-file case the same way: the
// entry carries an error and a path so the drain can delete it.
func TestListReportsUndecodableFile(t *testing.T) {
	sandbox(t)
	writeRaw(t, "0000000000000000001-aaaaaaaa.json", "{not json")

	entries, err := List()
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Error(t, entries[0].Err)
	assert.NotEmpty(t, entries[0].Path)
}

// TestListIgnoresForeignFiles pins the filename filter. List hands undecodable
// files to the caller to delete, so anything that slips through this filter and
// fails to parse gets removed — the filter is what keeps that blast radius to
// files this package wrote. Each case below is a file we must neither read nor
// delete, and each fails a bare ".json"-suffix check differently.
func TestListIgnoresForeignFiles(t *testing.T) {
	sandbox(t)
	_, err := Write(msg("real", "/repo", "x"))
	require.NoError(t, err)

	// WriteFileAtomic's in-flight temp for a concurrent send.
	writeRaw(t, ".0000000000000000001-aaaaaaaa.json.tmp-9999", `{"version":1}`)
	// A hidden file that *does* end in .json — the case a suffix check misses.
	writeRaw(t, ".hidden.json", `{"version":1,"title":"t","path":"/p","text":"x"}`)
	// Right suffix, wrong shape: not something Write could have produced.
	writeRaw(t, "notes.json", `{"version":1,"title":"t","path":"/p","text":"x"}`)
	writeRaw(t, "notes.txt", "scratch")

	entries, err := List()
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "real", entries[0].Message.Title)
}

// TestIsMessageFile documents the filename contract directly, including the
// boundary a 19-digit timestamp sits on.
func TestIsMessageFile(t *testing.T) {
	for name, want := range map[string]bool{
		"0000000000000000001-aaaaaaaa.json":           true,
		"1700000000000000000-0f0f0f0f.json":           true,
		"170000000000000000-0f0f0f0f.json":            false, // 18 digits
		"17000000000000000000-0f0f0f0f.json":          false, // 20 digits
		".1700000000000000000-0f0f0f0f.json":          false, // hidden
		".1700000000000000000-0f0f0f0f.json.tmp-1234": false, // atomic-write temp
		"1700000000000000000-0f0f0f0f.json.bak":       false,
		"notes.json":                                  false,
		"1700000000000000000-zzzz.json":               false, // nonce not hex
	} {
		assert.Equal(t, want, isMessageFile(name), "isMessageFile(%q)", name)
	}
}

// TestListOnMissingDirIsEmpty: no send has ever run, so the spool dir does not
// exist. That is the steady state for most users and must not be an error.
func TestListOnMissingDirIsEmpty(t *testing.T) {
	sandbox(t)
	dir, err := Dir()
	require.NoError(t, err)
	require.NoDirExists(t, dir)

	entries, err := List()
	require.NoError(t, err)
	assert.Empty(t, entries)
}

// TestExpiredBoundary pins the TTL comparison as strictly-greater-than, so a
// message exactly at the horizon still delivers.
func TestExpiredBoundary(t *testing.T) {
	sandbox(t)
	now := time.Unix(0, 1_700_000_000_000_000_000).UTC()
	m := Message{CreatedAt: now.Add(-TTL)}

	assert.False(t, m.Expired(now), "a message exactly at the TTL horizon is still valid")
	assert.True(t, m.Expired(now.Add(time.Nanosecond)))
	assert.False(t, Message{CreatedAt: now}.Expired(now))
}

// TestExpiredIgnoresZeroCreatedAt: a message with no timestamp must not be
// treated as infinitely old and silently dropped.
func TestExpiredIgnoresZeroCreatedAt(t *testing.T) {
	sandbox(t)
	assert.False(t, Message{}.Expired(time.Now()))
}

func TestRemove(t *testing.T) {
	sandbox(t)
	name, err := Write(msg("t", "/repo", "x"))
	require.NoError(t, err)
	require.NoError(t, Remove(name))

	entries, err := List()
	require.NoError(t, err)
	assert.Empty(t, entries)
}

// TestWriteCreatesDirWithSaneMode: the spool dir is created on demand by the
// first send, matching the 0755 convention used for worktrees/ and hooks/.
func TestWriteCreatesDirWithSaneMode(t *testing.T) {
	sandbox(t)
	_, err := Write(msg("t", "/repo", "x"))
	require.NoError(t, err)

	dir, err := Dir()
	require.NoError(t, err)
	info, err := os.Stat(dir)
	require.NoError(t, err)
	assert.True(t, info.IsDir())
	// Not an exact mode assertion: the process umask reduces MkdirAll's argument,
	// so only the owner bits are guaranteed across environments.
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm()&0o700)
}

// TestWriteIsAtomic asserts the spool never exposes a torn file: Write must go
// through config.WriteFileAtomic, which commits by rename.
func TestWriteIsAtomic(t *testing.T) {
	sandbox(t)
	name, err := Write(msg("t", "/repo", "x"))
	require.NoError(t, err)

	data, err := os.ReadFile(name)
	require.NoError(t, err)
	var m Message
	require.NoError(t, json.Unmarshal(data, &m), "a committed spool file is always complete JSON")

	dir, err := Dir()
	require.NoError(t, err)
	left, err := filepath.Glob(filepath.Join(dir, ".*.tmp-*"))
	require.NoError(t, err)
	assert.Empty(t, left, "no temp file should survive a successful write")
}

func writeRaw(t *testing.T, name, content string) {
	t.Helper()
	dir, err := Dir()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
}
