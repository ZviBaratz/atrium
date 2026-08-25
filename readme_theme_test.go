package main

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/ui/theme"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestReadmeDocumentsEveryPaletteToken holds the README's user-theme token table to
// ui/theme's own list, in both directions.
//
// The table is a projection of paletteTokens, and a projection nothing checks is how a
// nineteenth token ships undocumented — or, worse, how a token that was renamed leaves a
// key in the README that no theme file can use. It lives in this package because the
// root is where README.md is, and because config deliberately does not import ui/theme.
//
// It reads only the User themes subsection, so a token name mentioned in passing
// somewhere else in a 1700-line README cannot stand in for a row in the table.
func TestReadmeDocumentsEveryPaletteToken(t *testing.T) {
	raw, err := os.ReadFile("README.md")
	require.NoError(t, err)

	section := userThemesSection(t, string(raw))
	documented := map[string]bool{}
	for _, m := range regexp.MustCompile("`([a-z_]+)`").FindAllStringSubmatch(section, -1) {
		documented[m[1]] = true
	}

	names := theme.TokenNames()
	for _, name := range names {
		assert.Truef(t, documented[name],
			"palette token %q is not in the README's User themes token list", name)
	}

	known := map[string]bool{}
	for _, name := range names {
		known[name] = true
	}
	// Only the role bullets, and each bullet WHOLE: a role's names wrap over several
	// lines, so a line-by-line scan would leave every wrapped name unchecked in this
	// direction. The prose around the list legitimately names `extends`, `theme`,
	// `glyph_set` and the built-in theme names — a role bullet is the one shape that is
	// claiming its backticked names ARE tokens.
	for _, bullet := range strings.Split(section, "\n- **")[1:] {
		if end := strings.Index(bullet, "\n\n"); end >= 0 {
			bullet = bullet[:end]
		}
		for _, m := range regexp.MustCompile("`([a-z_]+)`").FindAllStringSubmatch(bullet, -1) {
			assert.Truef(t, known[m[1]],
				"the README's token list names %q, which is not a palette token", m[1])
		}
	}

	assert.Containsf(t, section, "eighteen semantic tokens",
		"the README states the token count; ui/theme has %d", len(names))
	assert.Lenf(t, names, 18,
		"the README says eighteen semantic tokens; update both if the palette grows")
}

// userThemesSection returns the README text between the User themes heading and the
// next one at the same level.
func userThemesSection(t *testing.T, readme string) string {
	t.Helper()
	start := strings.Index(readme, "##### User themes")
	require.GreaterOrEqual(t, start, 0, "the README has no User themes section")
	rest := readme[start+len("##### User themes"):]
	if end := strings.Index(rest, "\n##### "); end >= 0 {
		rest = rest[:end]
	}
	return rest
}
