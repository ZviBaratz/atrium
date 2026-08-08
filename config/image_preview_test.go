package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestGetImagePreview pins the normalization: every value in the vocabulary
// survives, and everything else — including the empty string a config predating
// the key carries, and a nil Config — lands on auto.
//
// Unlike GetGlyphSet there is no legacy key to fall back through, so the whole
// rule is the switch. The garbage case is what makes that switch load-bearing:
// without it a typo would reach kittyEligible as an unknown preference and be
// compared against the vocabulary there instead, one layer further from the
// user's file.
func TestGetImagePreview(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  *Config
		want string
	}{
		{"explicit auto", &Config{ImagePreview: ImagePreviewAuto}, ImagePreviewAuto},
		{"explicit kitty", &Config{ImagePreview: ImagePreviewKitty}, ImagePreviewKitty},
		{"explicit glyph", &Config{ImagePreview: ImagePreviewGlyph}, ImagePreviewGlyph},
		{"explicit off", &Config{ImagePreview: ImagePreviewOff}, ImagePreviewOff},
		{"garbage normalizes to auto", &Config{ImagePreview: "sixel"}, ImagePreviewAuto},
		{"empty normalizes to auto", &Config{}, ImagePreviewAuto},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.cfg.GetImagePreview())
		})
	}
	assert.Equal(t, ImagePreviewAuto, (*Config)(nil).GetImagePreview(), "nil Config")

	// The four values must stay distinct spellings: settings_schema.go glosses
	// them one by one and the doctor section names the resolved one, so a
	// duplicate would silently merge two rungs.
	seen := map[string]bool{}
	for _, v := range []string{ImagePreviewAuto, ImagePreviewKitty, ImagePreviewGlyph, ImagePreviewOff} {
		assert.False(t, seen[v], "duplicate image-preview value %q", v)
		seen[v] = true
	}
}
