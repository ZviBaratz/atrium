package doctor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/internal/repotrust"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRepoTrustSilentWhenClean: the steady state for anyone not using
// repo-local config is no section at all.
func TestRepoTrustSilentWhenClean(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	entries, err := CheckRepoTrust(context.Background(), true)
	require.NoError(t, err)
	assert.Empty(t, entries)
	assert.Empty(t, RenderRepoTrust(entries, err))
}

// TestRepoTrustReportsEachGrant: every record gets a line, with the live
// comparison beside it. The keys here name repos that do not exist, so the
// state is "missing" without any git in play — hermetic on purpose.
func TestRepoTrustReportsEachGrant(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	gone := filepath.Join(t.TempDir(), "gone-repo")
	require.NoError(t, repotrust.Grant(gone, "aaaabbbbccccdddd", "git@example.com:x.git", time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)))

	entries, err := CheckRepoTrust(context.Background(), true)
	require.NoError(t, err)
	require.Len(t, entries, 1, "a check that reports nothing for a real grant would be vacuously silent")
	assert.Equal(t, gone, entries[0].Key)
	assert.Contains(t, entries[0].State, "missing")
	assert.Equal(t, "aaaabbbbcccc", entries[0].Hash, "the hash is shortened for the table")
	assert.Equal(t, "2026-08-01", entries[0].Granted)

	out := RenderRepoTrust(entries, err)
	assert.Contains(t, out, "Repo trust")
	assert.Contains(t, out, gone)
}

// TestRepoTrustReportsAnUnreadableLedger: a corrupt ledger means every repo
// reads as untrusted; doctor is the persistent surface that says why.
func TestRepoTrustReportsAnUnreadableLedger(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path, err := repotrust.Path()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("{not json"), 0o600))

	entries, checkErr := CheckRepoTrust(context.Background(), true)
	require.Error(t, checkErr)
	out := RenderRepoTrust(entries, checkErr)
	assert.Contains(t, out, "untrusted until this is fixed")
	// The file must still be exactly where and as it was: doctor reads, never heals.
	data, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	assert.Equal(t, "{not json", string(data))
}

// TestRepoTrustRenderKeepsColumnsAligned pins the row shape loosely: state and
// key both present on one line per entry.
func TestRepoTrustRenderKeepsColumnsAligned(t *testing.T) {
	out := RenderRepoTrust([]RepoTrustEntry{
		{Key: "/repo/a", State: "current", Granted: "2026-08-01", Hash: "abc"},
		{Key: "/repo/b", State: "changed (re-allow to use)", Granted: "2026-08-02", Hash: "def"},
	}, nil)
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	require.Len(t, lines, 3, "a header plus one line per grant")
	assert.Contains(t, lines[1], "/repo/a")
	assert.Contains(t, lines[2], "/repo/b")
}

// TestRenderRepoTrustNamesWhatAGrantCovers: doctor is the surface a user reaches
// when they cannot tell why a repo's config is or is not applying, so "trusted" is
// not enough on its own — the grant covers a whole file, and this says which parts
// of it a session would take. The line only appears for a current grant, because
// LiveState leaves Covers empty for any other state.
func TestRenderRepoTrustNamesWhatAGrantCovers(t *testing.T) {
	got := RenderRepoTrust([]RepoTrustEntry{
		{Key: "/src/web", State: "current", Covers: "setup script + 2 carried files", Granted: "2026-08-01", Hash: "abc123abc123"},
		{Key: "/src/api", State: "changed (re-allow to use)", Granted: "2026-07-01", Hash: "def456def456"},
	}, nil)

	assert.Contains(t, got, "covers: setup script + 2 carried files")
	assert.NotContains(t, got, "covers: \n", "an empty Covers must render no line at all")
	// One continuation line, not two: the changed grant covers nothing that applies.
	assert.Equal(t, 1, strings.Count(got, "covers:"))
}
