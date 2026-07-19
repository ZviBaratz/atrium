package app

import (
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/ui/theme"

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
