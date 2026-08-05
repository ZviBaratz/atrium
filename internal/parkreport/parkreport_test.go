package parkreport

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

// sandbox points HOME at a fresh temp dir so each test gets its own data dir (and so no
// test can ever read or unlink a file in the user's real ~/.atrium, per CLAUDE.md).
func sandbox(t *testing.T) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	path, err := Path()
	require.NoError(t, err)
	return path
}

// writeRaw plants an arbitrary file at the spool path, for the shapes Write cannot
// produce: a hostile file, and one from another atrium.
func writeRaw(t *testing.T, path string, data []byte) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, data, 0o644))
}

// TestWriteThenReadRoundTrip is the basic contract: a written report comes back with
// every field intact, and is LEFT ON DISK. That second half is the whole point of the
// package — the caller unlinks it once the notice has actually been shown, because a
// report consumed before anyone read it is the erasure this exists to stop.
func TestWriteThenReadRoundTrip(t *testing.T) {
	path := sandbox(t)

	require.NoError(t, Write(Report{
		Sessions: []Session{{Title: "alpha", Path: "/repo/web"}, {Title: "bravo", Path: "/repo/api"}},
		Limit:    4,
	}))
	assert.FileExists(t, path)

	got, ok := Read(time.Now())
	require.True(t, ok)
	assert.Equal(t, currentVersion, got.Version)
	assert.Equal(t, 4, got.Limit)
	assert.Equal(t, []Session{{Title: "alpha", Path: "/repo/web"}, {Title: "bravo", Path: "/repo/api"}}, got.Sessions)
	assert.WithinDuration(t, time.Now(), got.CreatedAt, time.Minute, "Write stamps the creation time")
	assert.FileExists(t, path, "a usable report survives the read that delivered it to the caller")
}

// A second report supersedes the first: it describes the newer state, and only one can
// ever be undrained (see the package doc).
func TestWriteReplacesAnEarlierReport(t *testing.T) {
	sandbox(t)

	require.NoError(t, Write(Report{Sessions: []Session{{Title: "old"}}, Limit: 1}))
	require.NoError(t, Write(Report{Sessions: []Session{{Title: "new"}}, Limit: 9}))

	got, ok := Read(time.Now())
	require.True(t, ok)
	assert.Equal(t, []Session{{Title: "new"}}, got.Sessions)
	assert.Equal(t, 9, got.Limit)
}

// No file is the steady state for anyone whose fleet always fit. It must be silent, and
// must not create anything.
func TestReadWithNoReport(t *testing.T) {
	path := sandbox(t)

	got, ok := Read(time.Now())
	assert.False(t, ok)
	assert.Equal(t, Report{}, got)
	assert.NoFileExists(t, path, "reading must not create the spool")
}

// A report past the TTL horizon describes a fleet the user has since resumed, killed or
// rearranged. It is dropped AND removed: nothing else would ever delete it, and a file
// nobody drains would be re-read on every launch for the life of the data dir.
func TestReadDropsAndRemovesAnExpiredReport(t *testing.T) {
	path := sandbox(t)
	now := time.Now()

	require.NoError(t, Write(Report{
		Sessions:  []Session{{Title: "alpha"}},
		Limit:     2,
		CreatedAt: now.Add(-TTL - time.Minute),
	}))

	_, ok := Read(now)
	assert.False(t, ok, "past the horizon")
	assert.NoFileExists(t, path, "and cleared, so it is not re-read forever")
}

// One minute inside the horizon still delivers — the boundary is a real decision, not an
// accident of rounding.
func TestReadKeepsAReportInsideTheHorizon(t *testing.T) {
	sandbox(t)
	now := time.Now()

	require.NoError(t, Write(Report{
		Sessions:  []Session{{Title: "alpha"}},
		Limit:     2,
		CreatedAt: now.Add(-TTL + time.Minute),
	}))

	_, ok := Read(now)
	assert.True(t, ok)
}

// A report with no timestamp is never expired: treating a zero time as infinitely old
// would silently discard anything a future version wrote without the field.
func TestReadKeepsAReportWithNoTimestamp(t *testing.T) {
	path := sandbox(t)

	data, err := json.Marshal(Report{Version: currentVersion, Sessions: []Session{{Title: "alpha"}}, Limit: 2})
	require.NoError(t, err)
	writeRaw(t, path, data)

	got, ok := Read(time.Now())
	require.True(t, ok)
	assert.Equal(t, []Session{{Title: "alpha"}}, got.Sessions)
}

// Every unusable shape gets the same answer — nothing to deliver — and every one is
// cleared, for the reason above. The version case is the cross-version guard: a report
// from a different atrium is discarded rather than decoded on a guess.
func TestReadDiscardsUnusableReports(t *testing.T) {
	for name, body := range map[string]string{
		"not json":            "{{{",
		"json but not report": `["alpha","bravo"]`,
		"from another atrium": `{"version":99,"sessions":[{"title":"alpha"}],"limit":2}`,
		"names no session":    `{"version":1,"sessions":[],"limit":2}`,
		"empty file":          "",
	} {
		t.Run(name, func(t *testing.T) {
			path := sandbox(t)
			writeRaw(t, path, []byte(body))

			got, ok := Read(time.Now())
			assert.False(t, ok)
			assert.Equal(t, Report{}, got)
			assert.NoFileExists(t, path, "an unusable report is cleared; nobody else can")
		})
	}
}

// Write refuses an empty report rather than spooling a file that says nothing: a reader
// would drop it anyway, and the file would outlive the condition it fails to describe.
func TestWriteRefusesAnEmptyReport(t *testing.T) {
	path := sandbox(t)
	require.Error(t, Write(Report{Limit: 2}))
	assert.NoFileExists(t, path)
}

// Remove is what the delivering caller runs, and it must tolerate a file that is already
// gone — a second Atrium, or a user clearing the data dir, may have got there first.
func TestRemoveIsIdempotent(t *testing.T) {
	path := sandbox(t)

	require.NoError(t, Write(Report{Sessions: []Session{{Title: "alpha"}}, Limit: 2}))
	require.NoError(t, Remove())
	assert.NoFileExists(t, path)
	require.NoError(t, Remove(), "an already-gone report is not an error")
}

// The spool must live in the data dir config resolves, never a hardcoded ~/.atrium: a
// legacy install keeps using ~/.claude-squad, and a report written to the wrong dir would
// never be found (CLAUDE.md, identity & live-state safety).
func TestPathIsUnderTheResolvedDataDir(t *testing.T) {
	path := sandbox(t)
	dir, err := config.GetConfigDir()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, fileName), path)
}
