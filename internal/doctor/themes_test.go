package doctor

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/ui/theme"
	"github.com/ZviBaratz/atrium/ui/theme/themefile"
)

// writeThemes drops files into the sandbox data dir's themes directory. HOME is
// already a temp dir for this package (see TestMain in gates_test.go), so this reaches
// nothing real.
func writeThemes(t *testing.T, files map[string]string) {
	t.Helper()
	dir, err := config.ThemesDir()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(dir, 0o750))
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	for name, body := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600))
	}
}

// TestCheckThemes_ReportsBothHalves. The loaded names matter as much as the refusals:
// a file in NEITHER list is in the wrong directory, and no refusal message can say
// that.
func TestCheckThemes_ReportsBothHalves(t *testing.T) {
	writeThemes(t, map[string]string{
		"midnight.json": `{"palette": {"attention": "#ffb454"}}`,
		"washed.json":   `{"palette": {"fg": "#111111"}}`,
	})

	dir, loaded, problems := CheckThemes(config.DefaultConfig())

	assert.NotEmpty(t, dir)
	assert.Equal(t, []string{"midnight"}, loaded)
	require.Len(t, problems, 1)
	assert.Contains(t, problems[0].Error(), "washed.json")
	assert.Contains(t, problems[0].Error(), "not legible",
		"doctor must carry the reason, not just the filename")
}

// TestRenderThemes_NamesTheDirectoryWhenNothingLoaded is the case the whole report
// exists for, and the one an earlier draft rendered as an empty string.
//
// A themes directory that does not exist is indistinguishable, from every other
// surface, from one holding a file Atrium never looked at: themes/ misspelt, an XDG
// path assumed, a legacy ~/.claude-squad data dir. Load returns no theme AND no
// refusal, so the startup modal is silent, the picker is unchanged, and nothing
// anywhere names the path that would have worked. That is what this line is.
//
// It costs one line on an install with no themes, which is most of them. The trade is
// deliberate: the alternative spends nothing and answers nothing.
func TestRenderThemes_NamesTheDirectoryWhenNothingLoaded(t *testing.T) {
	dir, loaded, problems := CheckThemes(config.DefaultConfig())
	require.Empty(t, loaded)
	require.Empty(t, problems)

	out := RenderThemes(dir, loaded, problems)
	assert.Contains(t, out, "User themes:")
	assert.Contains(t, out, "none loaded")
	assert.Contains(t, out, dir,
		"a file in the wrong directory produces no theme and no refusal; this path is the only answer")
}

// TestRenderThemes_SaysNowhereWhenTheDirCannotBeResolved: CheckThemes returns dir ""
// when config.ThemesDir() itself fails, and "Themes live in " is a sentence naming
// nowhere. The error above it already says what happened.
func TestRenderThemes_SaysNowhereWhenTheDirCannotBeResolved(t *testing.T) {
	out := RenderThemes("", nil, []error{errors.New("home directory: no such user")})
	assert.Contains(t, out, "no such user")
	assert.NotContains(t, out, "Themes live in")
}

// TestRenderThemes_PrintsEveryViolation. The modal shows one miss and counts the rest,
// because it has one clipped line per file; doctor has a page, and reporting every miss
// at once is the whole reason theme.Validate does not stop at the first. Without this,
// that property was tested in the one place no user could observe it.
func TestRenderThemes_PrintsEveryViolation(t *testing.T) {
	writeThemes(t, map[string]string{"washed.json": `{"palette": {"fg": "#111111"}}`})

	dir, loaded, problems := CheckThemes(config.DefaultConfig())
	require.Empty(t, loaded)
	require.Len(t, problems, 1)

	var invalid *themefile.InvalidPaletteError
	require.ErrorAs(t, problems[0], &invalid)
	require.Greater(t, len(invalid.Violations), 1, "the fixture must miss more than one floor")

	out := RenderThemes(dir, loaded, problems)
	for _, v := range invalid.Violations {
		assert.Containsf(t, out, v.Error(), "doctor must print every miss, not the first: %s", v.Error())
	}
}

func TestRenderThemes(t *testing.T) {
	out := RenderThemes("/data/themes", []string{"midnight"}, []error{errors.New("washed.json: not legible")})
	assert.Contains(t, out, "User themes:")
	assert.Contains(t, out, "✓ midnight")
	assert.Contains(t, out, "⚠ washed.json")
	// The two things a refusal message cannot carry: where the files are meant to live,
	// and what a file may extend. The refusals dropped the built-in list precisely
	// because the startup modal clipped it, so this is its only home outside the README.
	assert.Contains(t, out, "/data/themes")
	for _, name := range theme.BuiltinNames() {
		assert.Containsf(t, out, name, "doctor must name %q as extendable", name)
	}

	clean := RenderThemes("/data/themes", []string{"midnight"}, nil)
	assert.Contains(t, clean, "midnight", "a loaded theme is worth reporting even when nothing was refused")
	assert.Contains(t, clean, "/data/themes", "the directory is unconditional; it is the answer to a file in neither list")
	assert.NotContains(t, clean, "may extend", "the vocabulary is help for a failure, not noise on every run")
	assert.Contains(t, RenderThemes("/data/themes", nil, []error{errors.New("x.json: bad")}), "x.json")
}

// TestCheckThemes_ReportsAConfiguredThemeWithNoFile is the failure with no file behind
// it, and the reason CheckThemes takes a config at all — the same join CheckRepoScripts
// and CheckKeybindings make.
//
// themefile.Load reports only files it READ and rejected, so a theme whose file was
// deleted, renamed, or never arrived on this machine produces no refusal anywhere: the
// startup modal is silent, the picker simply stops offering the name, and the UI falls
// back to DefaultThemeName rather than to `auto` — so a light-terminal user loses
// polarity following, permanently, with nothing said. This is the only surface that can
// notice.
func TestCheckThemes_ReportsAConfiguredThemeWithNoFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := config.DefaultConfig()
	cfg.Theme = "vanished"

	_, loaded, problems := CheckThemes(cfg)
	assert.Empty(t, loaded)
	require.Len(t, problems, 1)
	assert.Contains(t, problems[0].Error(), "vanished")
	assert.Contains(t, problems[0].Error(), theme.DefaultThemeName,
		"the report must name what it fell back TO; the name is not auto")

	// A built-in and `auto` are both registered without a file, and must not be reported.
	for _, name := range append(theme.BuiltinNames(), theme.AutoThemeName) {
		cfg.Theme = name
		_, _, problems := CheckThemes(cfg)
		assert.Emptyf(t, problems, "%s needs no theme file", name)
	}
}
