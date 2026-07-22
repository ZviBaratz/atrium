package app

import (
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/ui/theme"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSpinnerReseededOnGlyphSetChange pins the applySettingChange contract for
// the "glyph_set" key: switching from plain to ASCII must update m.spinner.Spinner.Frames
// in-place so the running spinner immediately shows the ASCII rung (|/-\) without
// requiring a relaunch.
//
// The spinner snapshots its frames at assembleHome time (the comment in applySettingChange
// notes this explicitly). Without the re-seed, a glyph-set change from the settings
// panel would leave the spinner on the old frames until restart.
func TestSpinnerReseededOnGlyphSetChange(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// SetGlyphSet returns the restore function; register it as cleanup so global
	// theme state doesn't leak into other tests regardless of which rung was active.
	t.Cleanup(theme.SetGlyphSet(theme.GlyphSetPlain))

	h := newWheelHome(t)
	h.appConfig.GlyphSet = config.GlyphSetASCII

	_ = h.applySettingChange("glyph_set")

	want := theme.Current().Glyphs.SpinnerFrames
	require.Equal(t, want, h.spinner.Spinner.Frames,
		"applySettingChange must re-seed spinner frames for the new glyph set")
	require.Equal(t, "|", h.spinner.Spinner.Frames[0],
		"the ASCII rung's first spinner frame must be the classic '|'")
}

// --- Project-scan rows (#399 item 5) ------------------------------------------
//
// The settings panel can now switch the repo scan off mid-session. assembleHome
// gates the persisted cache on the same condition at launch ("a cache written
// before the user disabled the scan must not keep surfacing", #120), but that
// gate only runs at construction — before the rows existed, disabling meant
// editing config.json and relaunching, so it was the only gate needed.

// Switching the scan off retires the results already in memory, so the picker
// stops offering them for the rest of the session.
func TestApplySettingChange_ScanDisabledRetiresCachedRepos(t *testing.T) {
	scanned := t.TempDir()
	h := newCreateFormHome(t)
	h.scannedRepos = []string{scanned}
	h.lastScanAt = time.Now()
	require.Contains(t, h.candidateRepoPaths(), scanned, "precondition: the picker offers it")

	h.appConfig.ProjectSearchDepth = intp(0)
	_ = h.applySettingChange("project_search_depth")

	assert.Empty(t, h.scannedRepos, "a disabled scan's cached repos must be retired")
	assert.NotContains(t, h.candidateRepoPaths(), scanned, "and must leave the picker")
}

// A scope change that leaves the scan enabled keeps the current results — they
// are the best answer until the new scope's walk lands — but marks them stale so
// the next form-open re-walks instead of serving the old scope for a whole TTL.
func TestApplySettingChange_ScanScopeChangeForcesARewalk(t *testing.T) {
	scanned := t.TempDir()
	for _, key := range []string{"project_search_roots", "project_search_depth"} {
		t.Run(key, func(t *testing.T) {
			h := newCreateFormHome(t)
			h.scannedRepos = []string{scanned}
			h.lastScanAt = time.Now()
			h.appConfig.ProjectSearchRoots = []string{t.TempDir()}
			h.appConfig.ProjectSearchDepth = intp(2)

			_ = h.applySettingChange(key)

			assert.Equal(t, []string{scanned}, h.scannedRepos,
				"results stand until the new scope's walk lands")
			h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("N")})
			assert.True(t, h.scanInFlight, "the next form-open must re-walk under the new scope")
		})
	}
}

// Retiring the in-memory results also drops the on-disk cache, so a relaunch
// cannot resurrect them either.
func TestApplySettingChange_ScanDisabledClearsThePersistedCache(t *testing.T) {
	scanned := t.TempDir()
	h := newCreateFormHome(t)
	require.NoError(t, h.appState.SetScannedRepos([]string{scanned}))

	h.appConfig.ProjectSearchDepth = intp(0)
	_ = h.applySettingChange("project_search_depth")

	persisted, _ := h.appState.GetScannedRepos()
	assert.Empty(t, persisted, "the disabled scan's cache must not survive on disk")
}
