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

	dir, loaded, problems := CheckThemes()

	assert.NotEmpty(t, dir)
	assert.Equal(t, []string{"midnight"}, loaded)
	require.Len(t, problems, 1)
	assert.Contains(t, problems[0].Error(), "washed.json")
	assert.Contains(t, problems[0].Error(), "not legible",
		"doctor must carry the reason, not just the filename")
}

// TestCheckThemes_SaysNothingWithoutAThemesDirectory. Almost every install is in this
// state, so a section here would be noise on every run.
func TestCheckThemes_SaysNothingWithoutAThemesDirectory(t *testing.T) {
	dir, loaded, problems := CheckThemes()
	assert.Empty(t, loaded)
	assert.Empty(t, problems)
	assert.Empty(t, RenderThemes(dir, loaded, problems), "no themes and no problems renders no section")
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
	assert.NotContains(t, clean, "may extend", "the vocabulary is help for a failure, not noise on every run")
	assert.Contains(t, RenderThemes("/data/themes", nil, []error{errors.New("x.json: bad")}), "x.json")
}
