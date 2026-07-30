package app

import (
	"context"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/ui/theme"

	tea "charm.land/bubbletea/v2"
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

// --- The tmux bar band (#394 Stage A) -----------------------------------------

// swapBarStyleApplier substitutes the tmux bar push for the duration of a test, so
// the theme arm can be driven end to end without shelling to a real server.
func swapBarStyleApplier(fn func(context.Context, bool)) (restore func()) {
	prev := barStyleApplier
	barStyleApplier = fn
	return func() { barStyleApplier = prev }
}

// runCmdTree executes every leaf of a tea.Cmd tree, in order, discarding the
// messages. tea.Batch's BatchMsg is exported but tea.Sequence's sequenceMsg is
// not, so the recursion matches structurally on "a slice of tea.Cmd" instead of on
// either type — both are ~[]Cmd. Same shape as clipboardPayload's walker, which
// only had to handle the batch half.
func runCmdTree(c tea.Cmd) {
	if c == nil {
		return
	}
	msg := c()
	v := reflect.ValueOf(msg)
	if v.IsValid() && v.Kind() == reflect.Slice && v.Type().Elem() == reflect.TypeOf(tea.Cmd(nil)) {
		for i := range v.Len() {
			runCmdTree(v.Index(i).Interface().(tea.Cmd))
		}
	}
}

// applySettingChange("theme") must return a Cmd that includes the tmux bar push.
// This is the wire the ladder's own guards cannot see: theme.Set and the spinner
// re-seed are both observable in-process, but the bar lives in another process,
// so nothing else in the suite notices when the push is dropped.
func TestApplySettingChange_ThemePushesTmuxBarStyle(t *testing.T) {
	m := newCreateFormHome(t)
	m.appConfig.SessionContextBar = boolPtr(true)
	m.appConfig.Theme = "catppuccin-mocha"

	require.NotNil(t, m.applyBarStyleCmd(),
		"with the context bar on, a theme change must push the bar style")

	m.appConfig.SessionContextBar = boolPtr(false)
	require.Nil(t, m.applyBarStyleCmd(),
		"with the context bar off there is no band to restyle")
}

// The helper existing is not the same as the arm calling it. #391's finding: a
// derived value passed as an argument is a wire nothing guards — and so is a Cmd
// the ladder forgot to batch. So this drives applySettingChange itself and runs
// what it returns; the test above passes with the call deleted from the arm, which
// is exactly the mutation this one exists to catch.
func TestApplySettingChange_ThemeArmBatchesTheBarPush(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // applySettingChange persists the config first
	// The arm calls theme.Set on a package global; restore it so -shuffle=on
	// cannot leak catppuccin-mocha into a test that renders against the default.
	t.Cleanup(theme.Set(theme.Current().Name))

	m := newCreateFormHome(t)
	m.appConfig.SessionContextBar = boolPtr(true)
	m.appConfig.Theme = "catppuccin-mocha"

	var pushed int32
	defer swapBarStyleApplier(func(context.Context, bool) { atomic.AddInt32(&pushed, 1) })()

	cmd := m.applySettingChange("theme")
	require.NotNil(t, cmd, "the theme arm must return a repaint + push")
	runCmdTree(cmd)

	require.Equal(t, int32(1), atomic.LoadInt32(&pushed),
		"the theme arm must batch the bar push, not merely have a helper that could")
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
			h.handleKeyPress(textMsg("N"))
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
