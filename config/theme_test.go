package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestGetTheme pins the theme accessor's normalization: unset (or whitespace-only)
// is DefaultTheme, and everything else is lowercased and trimmed.
//
// The case folding is not cosmetic. ui/theme's Get() already normalizes this way, so
// a hand-edited "Tokyo-Night" has always rendered — but `auto` is not a registry
// entry, and the two places that recognize it (compose() in ui/theme/current.go and
// requestSchemeCmd's gate in app/scheme.go) compare against the literal. Without
// normalizing here, "Auto" would be the one theme value in the file that silently
// does nothing: it would render the dark default and never send a detection query,
// on a light terminal, with no error anywhere.
//
// Doing it in this accessor rather than at those two sites is what keeps it one
// fact. Both of them reach the config only through GetTheme.
func TestGetTheme(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  *Config
		want string
	}{
		{"unset", &Config{}, DefaultTheme},
		{"whitespace only", &Config{Theme: "   "}, DefaultTheme},
		{"an explicit palette", &Config{Theme: "catppuccin-mocha"}, "catppuccin-mocha"},
		{"the reserved auto", &Config{Theme: "auto"}, "auto"},
		{"auto, capitalized", &Config{Theme: "Auto"}, "auto"},
		{"auto, shouted", &Config{Theme: "AUTO"}, "auto"},
		{"auto, padded", &Config{Theme: "  auto  "}, "auto"},
		{"a palette, capitalized", &Config{Theme: "Tokyo-Night"}, "tokyo-night"},
		{"an unknown name is passed through for ui/theme to reject", &Config{Theme: "wingdings"}, "wingdings"},
	} {
		assert.Equal(t, tc.want, tc.cfg.GetTheme(), tc.name)
	}
	assert.Equal(t, DefaultTheme, (*Config)(nil).GetTheme(), "nil Config")
}
