package repotrust

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sandbox points HOME at a fresh temp dir so each test gets its own data dir (and so
// no test can ever read or unlink a file in the user's real ~/.atrium, per CLAUDE.md).
func sandbox(t *testing.T) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	path, err := Path()
	require.NoError(t, err)
	return path
}

// writeRaw plants an arbitrary file at the ledger path, for the shapes the writing
// verbs cannot produce: a hostile file, and one from a newer atrium.
func writeRaw(t *testing.T, path string, data []byte) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, data, 0o600))
}

func TestGrantThenLoadRoundTrip(t *testing.T) {
	sandbox(t)
	granted := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	require.NoError(t, Grant("/repo/a", "hash-a", "git@example.com:a.git", granted))

	l, err := Load()
	require.NoError(t, err)
	assert.True(t, l.GrantedFor("/repo/a", "hash-a", GrantScope{}))
	rec, ok := l.Lookup("/repo/a")
	require.True(t, ok)
	assert.Equal(t, "hash-a", rec.Hash)
	assert.Equal(t, granted, rec.GrantedAt)
	assert.Equal(t, "git@example.com:a.git", rec.Remote)

	// The grant is for exactly that repo and exactly that content.
	assert.False(t, l.GrantedFor("/repo/a", "hash-b", GrantScope{}))
	assert.False(t, l.GrantedFor("/repo/b", "hash-a", GrantScope{}))
}

// TestGrantReplacesTheOldHash pins the one-hash-per-repo rule: a repo is trusted for
// the latest granted content ONLY. If a second grant accumulated instead of replacing,
// every previously-granted version of a setup script would stay runnable forever.
func TestGrantReplacesTheOldHash(t *testing.T) {
	sandbox(t)
	require.NoError(t, Grant("/repo/a", "hash-old", "", time.Now()))
	require.NoError(t, Grant("/repo/a", "hash-new", "", time.Now()))

	l, err := Load()
	require.NoError(t, err)
	assert.True(t, l.GrantedFor("/repo/a", "hash-new", GrantScope{}))
	assert.False(t, l.GrantedFor("/repo/a", "hash-old", GrantScope{}), "an older grant must not survive a newer one")
}

// TestLoadOfAMissingLedgerIsEmptyAndWritesNothing pins the property that separates
// this loader from config.LoadState: reading the ledger must never create it, sweep
// the data dir, or quarantine anything. doctor and the CLI read through this path and
// must be able to inspect a live TUI's data dir without mutating it.
func TestLoadOfAMissingLedgerIsEmptyAndWritesNothing(t *testing.T) {
	path := sandbox(t)

	l, err := Load()
	require.NoError(t, err)
	assert.False(t, l.GrantedFor("/repo/a", "hash-a", GrantScope{}))
	assert.Empty(t, l.Repos)

	_, statErr := os.Stat(path)
	assert.True(t, errors.Is(statErr, os.ErrNotExist), "Load must not create the ledger file")
}

// TestCorruptLedgerReadsAsZeroGrantsAndIsLeftInPlace: fail closed, loudly, without
// destroying the evidence. Every grant is refused while the file is corrupt, the error
// says why, and the bytes stay on disk for the user (and doctor) to look at — no
// quarantine rename, no deletion.
func TestCorruptLedgerReadsAsZeroGrantsAndIsLeftInPlace(t *testing.T) {
	path := sandbox(t)
	writeRaw(t, path, []byte("{not json"))

	l, err := Load()
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrCorrupt))
	assert.False(t, l.GrantedFor("/repo/a", "anything", GrantScope{}))

	data, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	assert.Equal(t, "{not json", string(data), "a corrupt ledger must be left exactly where and as it was")
}

// TestGrantOverACorruptLedgerStartsFresh: a corrupt file's grants are already
// unrecoverable, so the next grant self-heals rather than leaving the feature bricked
// until someone hand-deletes the file.
func TestGrantOverACorruptLedgerStartsFresh(t *testing.T) {
	path := sandbox(t)
	writeRaw(t, path, []byte("{not json"))

	require.NoError(t, Grant("/repo/a", "hash-a", "", time.Now()))
	l, err := Load()
	require.NoError(t, err)
	assert.True(t, l.GrantedFor("/repo/a", "hash-a", GrantScope{}))
}

// TestFutureVersionLedgerRefusesReadsAndWrites: records a newer atrium wrote cannot be
// interpreted on a guess (zero grants, fail closed) and must not be destroyed — Grant,
// Revoke and RevokeAll all refuse, and the file survives byte for byte.
func TestFutureVersionLedgerRefusesReadsAndWrites(t *testing.T) {
	path := sandbox(t)
	future, err := json.Marshal(Ledger{Version: currentVersion + 1, Repos: map[string]Record{
		"/repo/a": {Hash: "hash-a"},
	}})
	require.NoError(t, err)
	writeRaw(t, path, future)

	l, loadErr := Load()
	require.Error(t, loadErr)
	assert.True(t, errors.Is(loadErr, ErrFutureVersion))
	assert.False(t, l.GrantedFor("/repo/a", "hash-a", GrantScope{}), "a future-version grant must not be honored")

	assert.Error(t, Grant("/repo/b", "hash-b", "", time.Now()))
	_, revokeErr := Revoke("/repo/a")
	assert.Error(t, revokeErr)
	_, revokeAllErr := RevokeAll()
	assert.Error(t, revokeAllErr)

	data, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	assert.JSONEq(t, string(future), string(data), "a future-version ledger must never be overwritten")
}

func TestRevoke(t *testing.T) {
	path := sandbox(t)

	// Revoking from a missing ledger reports "was not there" and creates nothing.
	removed, err := Revoke("/repo/a")
	require.NoError(t, err)
	assert.False(t, removed)
	_, statErr := os.Stat(path)
	assert.True(t, errors.Is(statErr, os.ErrNotExist), "a no-op revoke must not create the ledger")

	require.NoError(t, Grant("/repo/a", "hash-a", "", time.Now()))
	require.NoError(t, Grant("/repo/b", "hash-b", "", time.Now()))

	removed, err = Revoke("/repo/a")
	require.NoError(t, err)
	assert.True(t, removed)

	l, err := Load()
	require.NoError(t, err)
	assert.False(t, l.GrantedFor("/repo/a", "hash-a", GrantScope{}))
	assert.True(t, l.GrantedFor("/repo/b", "hash-b", GrantScope{}), "revoking one repo must not touch another")
}

func TestRevokeAll(t *testing.T) {
	sandbox(t)
	require.NoError(t, Grant("/repo/a", "hash-a", "", time.Now()))
	require.NoError(t, Grant("/repo/b", "hash-b", "", time.Now()))

	n, err := RevokeAll()
	require.NoError(t, err)
	assert.Equal(t, 2, n)

	l, err := Load()
	require.NoError(t, err)
	assert.Empty(t, l.Repos)
}

// TestLedgerFileIsOwnerOnly: the ledger names private repo paths and gates code
// execution, so it is written 0600 — unlike config.json's 0644.
func TestLedgerFileIsOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits")
	}
	path := sandbox(t)
	require.NoError(t, Grant("/repo/a", "hash-a", "", time.Now()))

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

// TestGrantRefusesEmptyInputs: an empty key or hash means the caller failed to derive
// one, and a record it could write would be unmatchable garbage at best and a
// wildcard at worst.
func TestGrantRefusesEmptyInputs(t *testing.T) {
	sandbox(t)
	assert.Error(t, Grant("", "hash-a", "", time.Now()))
	assert.Error(t, Grant("/repo/a", "", "", time.Now()))
}

// TestGrantedRefusesEmptyInputs: lookups with an underived key or hash land on the
// refusing side even against a ledger that somehow holds an empty-keyed record.
func TestGrantedRefusesEmptyInputs(t *testing.T) {
	l := Ledger{Repos: map[string]Record{
		"": {Hash: ""},
	}}
	assert.False(t, l.GrantedFor("", "", GrantScope{}))
	assert.False(t, l.GrantedFor("/repo/a", "", GrantScope{}))
	assert.False(t, l.GrantedFor("", "hash-a", GrantScope{}))
}

func TestCanonicalRoot(t *testing.T) {
	t.Run("refuses the empty path", func(t *testing.T) {
		_, err := CanonicalRoot("")
		assert.Error(t, err)
		_, err = CanonicalRoot("   ")
		assert.Error(t, err)
	})

	t.Run("resolves a symlinked path to its target", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlinks")
		}
		base := t.TempDir()
		realDir := filepath.Join(base, "real")
		require.NoError(t, os.Mkdir(realDir, 0o755))
		link := filepath.Join(base, "link")
		require.NoError(t, os.Symlink(realDir, link))

		fromReal, err := CanonicalRoot(realDir)
		require.NoError(t, err)
		fromLink, err := CanonicalRoot(link)
		require.NoError(t, err)
		assert.Equal(t, fromReal, fromLink, "a symlinked path and its target must derive one key")
	})

	t.Run("a vanished path still keys, unresolved", func(t *testing.T) {
		gone := filepath.Join(t.TempDir(), "no-such-repo")
		key, err := CanonicalRoot(gone)
		require.NoError(t, err)
		assert.Equal(t, gone, key)
	})
}

func TestHashBytes(t *testing.T) {
	// A fixed vector so a digest-algorithm change cannot slip through as "some hash".
	assert.Equal(t,
		"9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08",
		HashBytes([]byte("test")))
	// The property enforcement leans on: different bytes, different hash.
	assert.NotEqual(t, HashBytes([]byte("a")), HashBytes([]byte("b")))
}

// TestGrantedForRequiresTheSeedScope: the ledger's answer depends on what the
// caller is about to DO with the file, not only on the bytes. A record written
// before GrantVersionSeeds matches a hash perfectly and still must not authorize
// the seed lists.
//
// This is the check the enforcement funnel makes. It is asserted here, at the
// ledger, because that is the layer both the funnel and the prompt share — the
// defect it replaces was the two of them asking DIFFERENT questions, with only
// the advisory one version-aware.
func TestGrantedForRequiresTheSeedScope(t *testing.T) {
	l := Ledger{Version: currentVersion, Repos: map[string]Record{
		"/repo/old": {Hash: "h", GrantedAt: time.Now()},                                    // pre-#815
		"/repo/new": {Hash: "h", GrantedAt: time.Now(), GrantVersion: currentGrantVersion}, // current
	}}

	// Script-only scope: both records authorize it.
	assert.True(t, l.GrantedFor("/repo/old", "h", GrantScope{}))
	assert.True(t, l.GrantedFor("/repo/new", "h", GrantScope{}))

	// Seed scope: only the record whose prompt described them.
	assert.False(t, l.GrantedFor("/repo/old", "h", GrantScope{Seeds: true}),
		"a grant made before the seed lists were read cannot authorize them, however well the hash matches")
	assert.True(t, l.GrantedFor("/repo/new", "h", GrantScope{Seeds: true}))

	// The hash still governs in both scopes.
	assert.False(t, l.GrantedFor("/repo/new", "other", GrantScope{Seeds: true}))
	assert.False(t, l.GrantedFor("/repo/new", "other", GrantScope{}))
}
