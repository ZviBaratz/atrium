package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestGetGlyphSet pins the fidelity-rung normalization: an explicit GlyphSet
// wins; an empty GlyphSet falls back to the legacy NerdFont bool (true → nerd,
// else plain) so configs predating the key keep their glyph set; a nil Config or
// any unrecognized value normalizes to the safe plain rung.
func TestGetGlyphSet(t *testing.T) {
	tru, fls := true, false
	for _, tc := range []struct {
		name string
		cfg  *Config
		want string
	}{
		{"explicit nerd", &Config{GlyphSet: GlyphSetNerd}, GlyphSetNerd},
		{"explicit plain", &Config{GlyphSet: GlyphSetPlain}, GlyphSetPlain},
		{"explicit ascii", &Config{GlyphSet: GlyphSetASCII}, GlyphSetASCII},
		{"garbage normalizes to plain", &Config{GlyphSet: "wingdings"}, GlyphSetPlain},
		{"empty + nerd_font on → nerd", &Config{NerdFont: &tru}, GlyphSetNerd},
		{"empty + nerd_font off → plain", &Config{NerdFont: &fls}, GlyphSetPlain},
		{"empty + no nerd_font → plain", &Config{}, GlyphSetPlain},
		{"explicit ascii wins over legacy nerd_font", &Config{GlyphSet: GlyphSetASCII, NerdFont: &tru}, GlyphSetASCII},
	} {
		assert.Equal(t, tc.want, tc.cfg.GetGlyphSet(), tc.name)
	}
	assert.Equal(t, GlyphSetPlain, (*Config)(nil).GetGlyphSet(), "nil Config")
}
