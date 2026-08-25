package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/ui/theme"

	xansi "github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newHomeWithThemes builds a home from a data dir holding the given theme files, plus
// a config.json naming themeName, through the real load path — so what is exercised is
// the wire from the files to the active palette, not an in-memory shortcut.
func newHomeWithThemes(t *testing.T, themeName string, files map[string]string) *home {
	t.Helper()
	// newHome moves process-global theme state on THREE axes, and all three are restored
	// here. The palette and the user registry are obvious; the scheme is not — newHome
	// calls ApplyThemeAtLaunch, whose extra step over applyThemeSelection is
	// SetScheme(initialScheme()), and that restore func is discarded by design (there is
	// no previous scheme at startup).
	//
	// What the third line buys was MEASURED, and it is not a failing test: with it deleted
	// and COLORFGBG="0;15" exported, `go test ./app/ -shuffle=on` is green. initialScheme
	// would answer SchemeLight there and this helper would leave it that way for the rest
	// of the binary, but no test in this package currently reads the scheme without
	// setting it first. So this is defence-in-depth, exactly like
	// app/frameparity_test.go's own SetScheme cleanup — and it is what keeps THAT file's
	// stated measurement ("every SetScheme in app is paired with its restore") true, which
	// is the part a reader would otherwise have to re-derive. Do not upgrade either line
	// to a claim the suite would disprove.
	//
	// Registered before newHome runs so each restore captures what this test found.
	t.Cleanup(theme.Set(config.DefaultConfig().Theme))
	t.Cleanup(theme.SetUserThemes(nil))
	t.Cleanup(theme.SetScheme(theme.CurrentScheme()))

	t.Setenv("HOME", t.TempDir())
	cfgDir, err := config.GetConfigDir()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(cfgDir, "themes"), 0o750))
	for name, body := range files {
		require.NoError(t, os.WriteFile(filepath.Join(cfgDir, "themes", name), []byte(body), 0o600))
	}

	cfg := config.DefaultConfig()
	cfg.Theme = themeName
	raw, err := json.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(cfgDir, config.ConfigFileName), raw, 0o644))

	h, err := newHome(context.Background(), "claude", false, "v", "atr")
	require.NoError(t, err)
	return h
}

// TestConstruct_ActivatesAUserTheme is the end-to-end claim: a palette that exists only
// as a file in the data dir is the one the UI renders, and the refusals are carried out
// of the constructor rather than dropped.
func TestConstruct_ActivatesAUserTheme(t *testing.T) {
	h := newHomeWithThemes(t, "midnight", map[string]string{
		"midnight.json": `{"extends": "tokyo-night", "palette": {"attention": "#ffb454"}}`,
	})

	assert.Equal(t, "midnight", theme.Current().Name)
	assert.Equal(t, "#ffb454", theme.Hex(theme.Current().Palette.Attention))
	assert.Equal(t, theme.Hex(theme.Get("tokyo-night").Palette.Fg), theme.Hex(theme.Current().Palette.Fg),
		"the tokens the file leaves alone come from its base")
	assert.Empty(t, h.pendingThemeProblems)
}

// TestConstruct_BuffersRefusedThemes: a refused file must reach the buffer the preview
// tick flushes, and must not take the launch's palette down with it.
func TestConstruct_BuffersRefusedThemes(t *testing.T) {
	h := newHomeWithThemes(t, "washed", map[string]string{
		"washed.json": `{"palette": {"fg": "#111111"}}`,
	})

	require.Len(t, h.pendingThemeProblems, 2)
	// The configured-name entry LEADS, because the modal shows five and this is the only
	// line describing what the user is looking at. Here it overlaps the refusal below it —
	// the broken file is also the configured theme — which is the accepted cost of neither
	// entry parsing the other's message.
	assert.Contains(t, h.pendingThemeProblems[0].Error(), `"washed" is configured`)
	assert.Contains(t, h.pendingThemeProblems[1].Error(), "washed.json")
	assert.Equal(t, theme.DefaultThemeName, theme.Current().Name,
		"a config naming a refused theme falls back rather than rendering nothing")

	// And the case that has no refusal behind it at all, which is the one every other
	// surface is silent about: a config naming a theme with no file anywhere.
	gone := newHomeWithThemes(t, "vanished", nil)
	require.Len(t, gone.pendingThemeProblems, 1)
	assert.Contains(t, gone.pendingThemeProblems[0].Error(), `"vanished" is configured`)
}

// TestThemeProblemsReport is the modal's shape, and the consequence line that
// distinguishes it from the other three startup reports: a refused theme is not in the
// picker at all, so the only symptom is a palette that never appears.
//
// The heading counts PROBLEMS rather than FILES, and that is not a wording preference.
// ApplyThemeAtLaunch pushes directory-level failures into this same slice — an
// unreadable themes/, a data dir that would not resolve — where one entry stands for
// every theme the user owns, none of which was read. "1 theme file was ignored" is a
// specific claim about a specific file, and it is at its most confident in the one case
// where no file was opened at all.
func TestThemeProblemsReport(t *testing.T) {
	report := themeProblemsReport([]error{errors.New("washed.json: palette is not legible")})

	assert.Contains(t, report, "1 problem loading user themes:")
	assert.Contains(t, report, "washed.json: palette is not legible")
	// One consequence line, and it has to hold for all three kinds of entry this slice
	// carries — a refused file, a directory that would not resolve, and a `theme` naming
	// something no file loads under. The pair it replaced ("any palette named above",
	// "a theme naming one") described the first kind and was false of the other two.
	assert.Contains(t, report, "Nothing named above is selectable")
	assert.Contains(t, report, "falls back")
	assert.NotContains(t, report, "… and")

	assert.Empty(t, themeProblemsReport(nil))
	assert.Contains(t, themeProblemsReport([]error{errors.New("a"), errors.New("b")}),
		"2 problems loading user themes:")

	// The directory-level entry, rendered verbatim: nothing in the heading above it may
	// promise the reader a file it can name.
	dirFailure := themeProblemsReport([]error{errors.New("themes directory /home/u/.atrium/themes: permission denied")})
	assert.NotContains(t, dirFailure, "theme file",
		"a whole-directory failure must not be reported as one refused file")
}

// TestThemeProblemsReportFitsANarrowTerminal pins the width the fixed lines were
// written to. A copy change is a width change: the info overlay hugs its content and
// caps at the terminal width less four, padding two columns each side, so 72 cells is
// what an 80-column terminal shows unwrapped — and a wrapped line costs the modal a row
// its height budget never counted.
//
// Measured against 80 rather than against the renderer, which is the whole point: a
// bound the overlay itself produced could not fail.
//
// Only the lines this file authors. The entry lines carry a user-authored filename and
// are bounded by reportLineBudget instead, which is deliberately wider than any
// terminal — the same trade every sibling report makes.
func TestThemeProblemsReportFitsANarrowTerminal(t *testing.T) {
	const inner = 80 - 4 - 4 // terminal - border/margin - padding; see TextOverlay.boxWidth
	report := themeProblemsReport([]error{errors.New("x.json: bad")})

	for _, line := range strings.Split(report, "\n") {
		if strings.HasPrefix(line, "  ") {
			continue // an entry line, bounded by reportLineBudget
		}
		assert.LessOrEqualf(t, xansi.StringWidth(line), inner,
			"%q is %d cells; it wraps on an 80-column terminal", line, xansi.StringWidth(line))
	}
}

// TestThemeProblemsReportTruncates: a themes directory can hold any number of broken
// files, and the filename is user-authored, so the modal is bounded on both axes the
// way its three siblings are.
func TestThemeProblemsReportTruncates(t *testing.T) {
	var problems []error
	for i := range customCommandProblemsShown + 3 {
		problems = append(problems, errors.New(string(rune('a'+i))+".json: bad"))
	}
	report := themeProblemsReport(problems)
	assert.Contains(t, report, "… and 3 more")
}

// TestThemeProblemsFlushWaitsForTheScreen mirrors flushKeybindingProblems: a modal
// opened while an overlay owns the screen would clobber it, and a buffer that is only
// read reopens the modal on every 100ms tick.
func TestThemeProblemsFlushWaitsForTheScreen(t *testing.T) {
	h, _ := newUnreadHome(t)
	h.pendingThemeProblems = []error{errors.New("washed.json: palette is not legible")}

	h.state = stateHelp
	h.flushThemeProblems()
	assert.Equal(t, stateHelp, h.state, "it must wait while an overlay owns the screen")
	assert.NotEmpty(t, h.pendingThemeProblems, "and stay buffered")

	h.state = stateDefault
	h.flushThemeProblems()
	assert.Equal(t, stateInfo, h.state, "then open the persistent modal")
	assert.Contains(t, xansi.Strip(h.textOverlay.Render()), "washed.json")
	assert.Empty(t, h.pendingThemeProblems,
		"and clear the buffer, or the preview tick reopens it forever")

	h.state = stateDefault
	// On the STATE, not on the returned command: showInfo returns nil unconditionally
	// (app_feedback.go), so a Nil assertion on the return value passes whatever this does
	// — including reopening the modal. The observable fact is that the tick left the
	// screen alone.
	h.flushThemeProblems()
	assert.Equal(t, stateDefault, h.state, "a second tick must find nothing to do")
}

// TestOpeningTheSettingsPanelRereadsTheThemesDirectory is the apply-without-restart
// contract, and the reason an fs-watcher was declined rather than missed.
//
// The event is OPENING the panel, not saving the row, and the assertion that says why is
// the one on the picker's OPTIONS. The row's options are theme.SelectableNames() read
// live, so a file written after launch is absent from the list a save has to choose from:
// cycling cannot reach it, and the first press would select and PERSIST some other
// palette on the way past. Re-reading on save could not have fixed that — by the time a
// save happens the wrong value has already been chosen.
//
// The other half of the wire — that the row's options really are SelectableNames, plus a
// configured name the registry has lost — is asserted in ui/overlay, which can read the
// row table without this package growing an exported accessor for it
// (TestThemeRowOffersWhatIsRegisteredPlusTheConfiguredName).
func TestOpeningTheSettingsPanelRereadsTheThemesDirectory(t *testing.T) {
	h := newHomeWithThemes(t, theme.DefaultThemeName, nil)
	require.NotContains(t, theme.Names(), "midnight", "precondition: written after launch")

	cfgDir, err := config.GetConfigDir()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(cfgDir, "themes", "midnight.json"),
		[]byte(`{"palette": {"attention": "#ffb454"}}`), 0o600))

	h.openSettings()
	assert.Contains(t, theme.Names(), "midnight", "opening the panel must re-read the directory")
	assert.Contains(t, theme.SelectableNames(), "midnight",
		"the picker reads SelectableNames when the overlay is BUILT, which openSettings does next")

	// And saving it activates it, through the arm the overlay calls.
	h.appConfig.Theme = "midnight"
	h.applySettingChange("theme")
	assert.Equal(t, "midnight", theme.Current().Name)
	assert.Equal(t, "#ffb454", theme.Hex(theme.Current().Palette.Attention))
}

// TestSavingTheThemeRowLeavesTheDetectedSchemeAlone.
//
// The save path re-runs the loader and re-applies the palette, and for a while it did
// that by calling ApplyThemeAtLaunch — whose extra step is SetScheme(initialScheme()).
// initialScheme() reads COLORFGBG, which is unset on most terminals and resolves to
// SchemeUnknown; the OSC 11 ladder above it exists for exactly that reason. So on a
// light terminal under the shipped `theme: auto`, saving any row in this arm threw away
// the detected polarity and composed the dark default.
//
// glyph_set is the case with no way back: applySchemeQueryCmd re-queries for the theme
// row only, so the flip stood until an unrelated blur and refocus. It is the arm this
// drives for that reason.
func TestSavingTheThemeRowLeavesTheDetectedSchemeAlone(t *testing.T) {
	t.Setenv("COLORFGBG", "") // the ordinary terminal: the lower rung answers nothing
	h := newHomeWithThemes(t, theme.AutoThemeName, nil)
	t.Cleanup(theme.SetScheme(theme.SchemeUnknown))

	theme.SetScheme(theme.SchemeLight) // as an OSC 11 answer would
	require.True(t, theme.IsLight(theme.Current().Palette), "precondition: detection took")

	h.applySettingChange("glyph_set")

	assert.Equal(t, theme.SchemeLight, theme.CurrentScheme(),
		"a settings save must not overwrite a detected polarity with COLORFGBG's silence")
	assert.True(t, theme.IsLight(theme.Current().Palette),
		"and the composed palette must still be the light one the terminal is showing")
}

// TestOpeningTheSettingsPanelBuffersRefusalsOnce: a file edited into an illegible state
// must report — and must report exactly once, however many times the panel is opened.
//
// The second half is the half with teeth. flushThemeProblems clears the buffer as it
// opens the modal, so a reload that refilled it unconditionally would put the same report
// in front of the user every time they touched any setting, with no way out short of
// fixing or deleting the file.
func TestOpeningTheSettingsPanelBuffersRefusalsOnce(t *testing.T) {
	h := newHomeWithThemes(t, theme.DefaultThemeName, nil)
	cfgDir, err := config.GetConfigDir()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(cfgDir, "themes", "washed.json"),
		[]byte(`{"palette": {"fg": "#111111"}}`), 0o600))

	h.openSettings()
	require.Len(t, h.pendingThemeProblems, 1)
	assert.Contains(t, h.pendingThemeProblems[0].Error(), "washed.json")

	// Drained the way the preview tick drains it, then opened again.
	h.state = stateDefault
	h.flushThemeProblems()
	require.Empty(t, h.pendingThemeProblems)

	h.openSettings()
	assert.Empty(t, h.pendingThemeProblems, "a dismissed refusal must not come back on every open")

	// Broken a SECOND way, which is news again: the buffer is keyed on the message.
	// (Continued below in TestReloadKeepsAProblemStillWaitingToBeShown, which covers the
	// other half — what happens to an entry that has NOT been drained yet.)
	require.NoError(t, os.WriteFile(filepath.Join(cfgDir, "themes", "washed.json"),
		[]byte(`{"palette": {"fg": "not-a-colour"}}`), 0o600))
	h.openSettings()
	require.Len(t, h.pendingThemeProblems, 1)
	assert.Contains(t, h.pendingThemeProblems[0].Error(), "not-a-colour")
}

// TestReloadPushesTheBandWhenThePaletteMoves. Opening the settings panel re-reads the
// themes directory, so it can recompose the ACTIVE palette — the user edited the file
// their `theme` names, or deleted it and fell back. The TUI repaints from
// theme.Current() on the next frame; the tmux band does not, because it is a status-style
// baked into the managed conf and a server option on a live tmux server. Repainting one
// and not the other is #574's symptom — frame and band on different palettes — arriving
// by a new road, and the road was opened by this PR's own plumbing.
//
// The negative case is the one with teeth: pushing unconditionally would be correct and
// invisible here, so a test that only checked "the band was pushed" would pass a version
// that rewrites the conf and runs validateConfig's throwaway probe server every time the
// panel is opened.
func TestReloadPushesTheBandWhenThePaletteMoves(t *testing.T) {
	h := newHomeWithThemes(t, "midnight", map[string]string{
		"midnight.json": `{"extends": "tokyo-night", "palette": {"bar_bg": "#414869"}}`,
	})
	// A hair off tokyo-night's own #414868, deliberately: the pair must clear bar_bg's
	// 1.6 floor against bg AND fg's 4.5 against it, which a freely-invented dark hex does
	// not. What this test needs is two band colours that DIFFER as strings, not two that
	// look different.
	require.Equal(t, "#414869", theme.Hex(theme.Current().Palette.BarBg), "precondition")

	cfgDir, err := config.GetConfigDir()
	require.NoError(t, err)

	// Nothing changed on disk: opening the panel must not claim the band moved.
	assert.False(t, h.reloadUserThemes(), "an unchanged directory must not trigger a conf rewrite")

	// The active theme's band colour, edited underneath the running app.
	require.NoError(t, os.WriteFile(filepath.Join(cfgDir, "themes", "midnight.json"),
		[]byte(`{"extends": "tokyo-night", "palette": {"bar_bg": "#41486a"}}`), 0o600))
	assert.True(t, h.reloadUserThemes(), "the band's own colour moved and the conf is now stale")
	assert.Equal(t, "#41486a", theme.Hex(theme.Current().Palette.BarBg))

	// A token the band does not carry: the TUI repaints, the band is untouched, and a
	// rewrite would be work no session can see.
	require.NoError(t, os.WriteFile(filepath.Join(cfgDir, "themes", "midnight.json"),
		[]byte(`{"extends": "tokyo-night", "palette": {"bar_bg": "#41486a", "attention": "#ffb454"}}`), 0o600))
	assert.False(t, h.reloadUserThemes(), "only the band's two tokens should cost a push")
	assert.Equal(t, "#ffb454", theme.Hex(theme.Current().Palette.Attention),
		"precondition: the edit did land, so the False above is about WHICH token moved")

	// And the file vanishing, which falls the palette back to the default.
	require.NoError(t, os.Remove(filepath.Join(cfgDir, "themes", "midnight.json")))
	assert.True(t, h.reloadUserThemes(), "falling back to the default is a palette change like any other")
	assert.Equal(t, theme.DefaultThemeName, theme.Current().Name)
}

// TestReloadKeepsAProblemStillWaitingToBeShown is why reloadUserThemes appends rather than
// assigns, and it is a case that only exists because of when the reload now runs.
//
// The buffer drains in stateDefault only. Opening the settings panel is not that state,
// so a problem the launch found can still be sitting in the buffer unshown at the exact
// moment the panel's reload wants to add another. An assignment there drops the launch's
// finding on the way past — silently, and permanently for that run, since
// themeProblemsSeen has already recorded it as said.
func TestReloadKeepsAProblemStillWaitingToBeShown(t *testing.T) {
	h := newHomeWithThemes(t, theme.DefaultThemeName, map[string]string{
		"washed.json": `{"palette": {"fg": "#111111"}}`,
	})
	require.Len(t, h.pendingThemeProblems, 1, "the launch found one and nothing has drained it")

	cfgDir, err := config.GetConfigDir()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(cfgDir, "themes", "murky.json"),
		[]byte(`{"palette": {"fg": "#141414"}}`), 0o600))

	h.openSettings()
	require.Len(t, h.pendingThemeProblems, 2,
		"the launch's finding must survive a reload that adds to the buffer")
	msgs := h.pendingThemeProblems[0].Error() + " " + h.pendingThemeProblems[1].Error()
	assert.Contains(t, msgs, "washed.json")
	assert.Contains(t, msgs, "murky.json")
}
