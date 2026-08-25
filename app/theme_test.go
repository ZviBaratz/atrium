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
	// newHome moves process-global theme state on both axes. Registered before it runs
	// so each restore captures what this test found.
	t.Cleanup(theme.Set(config.DefaultConfig().Theme))
	t.Cleanup(theme.SetUserThemes(nil))

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

	require.Len(t, h.pendingThemeProblems, 1)
	assert.Contains(t, h.pendingThemeProblems[0].Error(), "washed.json")
	assert.Equal(t, theme.DefaultThemeName, theme.Current().Name,
		"a config naming a refused theme falls back rather than rendering nothing")
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
	assert.Contains(t, report, "not selectable")
	assert.Contains(t, report, "falls back to the default")
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
	assert.Nil(t, h.flushThemeProblems(), "it must wait while an overlay owns the screen")
	assert.NotEmpty(t, h.pendingThemeProblems, "and stay buffered")

	h.state = stateDefault
	h.flushThemeProblems()
	assert.Equal(t, stateInfo, h.state, "then open the persistent modal")
	assert.Contains(t, xansi.Strip(h.textOverlay.Render()), "washed.json")
	assert.Empty(t, h.pendingThemeProblems,
		"and clear the buffer, or the preview tick reopens it forever")

	h.state = stateDefault
	assert.Nil(t, h.flushThemeProblems(), "a second tick must find nothing to do")
}

// TestSavingTheThemeRowRereadsTheThemesDirectory is the apply-on-save contract, and the
// reason an fs-watcher was declined rather than missed: a file written after launch has
// to become selectable without a restart.
//
// It drives applySettingChange, which is what the settings overlay calls when a row is
// saved — not theme.Set, which would prove only that the theme package works.
func TestSavingTheThemeRowRereadsTheThemesDirectory(t *testing.T) {
	h := newHomeWithThemes(t, theme.DefaultThemeName, nil)
	require.NotContains(t, theme.Names(), "midnight", "precondition: written after launch")

	cfgDir, err := config.GetConfigDir()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(cfgDir, "themes", "midnight.json"),
		[]byte(`{"palette": {"attention": "#ffb454"}}`), 0o600))

	h.appConfig.Theme = "midnight"
	h.applySettingChange("theme")

	assert.Contains(t, theme.Names(), "midnight", "saving the row must re-read the directory")
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

// TestSavingTheThemeRowBuffersRefusals: a file edited into an illegible state and saved
// must report, and must not toast over the settings overlay that is still open.
func TestSavingTheThemeRowBuffersRefusals(t *testing.T) {
	h := newHomeWithThemes(t, theme.DefaultThemeName, nil)
	cfgDir, err := config.GetConfigDir()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(cfgDir, "themes", "washed.json"),
		[]byte(`{"palette": {"fg": "#111111"}}`), 0o600))

	h.applySettingChange("theme")

	require.Len(t, h.pendingThemeProblems, 1)
	assert.Contains(t, h.pendingThemeProblems[0].Error(), "washed.json")
}
