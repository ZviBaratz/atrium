package app

// image_preview.go owns the image overlay's state: which glyph rung to draw it
// with, opening it when a load lands, and closing it (#398).

import (
	"os"

	"github.com/ZviBaratz/atrium/ui/imageview"
	"github.com/ZviBaratz/atrium/ui/overlay"
	"github.com/ZviBaratz/atrium/ui/theme"

	tea "charm.land/bubbletea/v2"
)

// imagePreviewMaxWidth and imagePreviewMaxHeight cap the box. They are the
// picture's own caps plus its chrome, so a larger box would only pad a picture
// that cannot grow — see ui/overlay's imageMaxCols/imageMaxRows/imageChrome.
const (
	imagePreviewMaxWidth  = 126 // imageMaxCols + border (2) + padding (4)
	imagePreviewMaxHeight = 48  // imageMaxRows + imageChrome
)

// imageRenderMode picks the glyph rung for a picture.
//
// Half blocks are the default and carry twice the vertical resolution. Three
// things take that away, and all three are silent failures if they are not
// checked here:
//
//   - glyph_set "ascii" means the user is on a terminal where even Block
//     Elements show tofu; a picture of them would be a field of boxes.
//   - NO_COLOR strips the colour a half block carries its information in,
//     leaving an identical glyph in every cell — a solid rectangle, not a
//     picture. This is the one that would ship broken without anyone noticing,
//     because it renders "successfully".
//   - RUNEWIDTH_EASTASIAN makes East-Asian Ambiguous characters — which Block
//     Elements are — measure two cells while still rendering as one. That
//     divergence is what desyncs the alt-screen renderer into accumulating ghost
//     rows (see theme.SanitizeWidth), and a full-window picture of them would do
//     it on every row at once.
//
// Taking the environment as a parameter rather than reading it keeps the rule
// testable, matching internal/doctor's convention.
func imageRenderMode(glyphSet string, mono bool, eastAsian string) imageview.Mode {
	if glyphSet == theme.GlyphSetASCII || mono || eastAsian != "" {
		return imageview.ASCII
	}
	return imageview.HalfBlock
}

// currentImageRenderMode resolves imageRenderMode against the live environment.
func (m *home) currentImageRenderMode() imageview.Mode {
	return imageRenderMode(m.appConfig.GetGlyphSet(), theme.Mono(), os.Getenv("RUNEWIDTH_EASTASIAN"))
}

// handleImageLoaded opens the overlay on a decoded image, or explains why not.
//
// The load ran in a command, so this is a different Update from the keypress
// that asked for it. That is deliberate — decoding on the loop would stall every
// session's poll — and it is also why the overlay is opened by a message rather
// than by a key.
func (m *home) handleImageLoaded(msg imageLoadedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		return m, m.handleInfoNotice(truncateForNotice(msg.err.Error()))
	}
	m.openImagePreview(overlay.Image{
		Path: msg.path, Pixels: msg.img, Width: msg.width, Height: msg.height,
	})
	return m, nil
}

// openImagePreview arms the overlay and switches state. Split from
// handleImageLoaded so a test can open the real overlay the way production does
// rather than assigning the field by hand.
func (m *home) openImagePreview(src overlay.Image) {
	m.imageOverlay = overlay.NewImageOverlay(src, m.currentImageRenderMode())
	m.imageOverlay.SetSize(
		min(int(float32(m.windowWidth)*0.85), imagePreviewMaxWidth),
		min(int(float32(m.windowHeight)*0.85), imagePreviewMaxHeight),
	)
	m.state = stateImagePreview
	m.recomputeLayout() // the hint bar gives up its row while the box is open
}

// closeImagePreview returns to the default state and drops the decoded image.
//
// Dropping it matters: the intermediate is up to a megapixel of RGBA, and an
// overlay left armed would pin it for the rest of the session.
func (m *home) closeImagePreview() {
	m.state = stateDefault
	m.imageOverlay = nil
	m.recomputeLayout()
}

// handleImagePreviewState consumes every key while the box is up. It is a
// read-only view with exactly one gesture, so anything that is not esc is
// swallowed rather than falling through to the global keys behind it — q in
// particular, which would otherwise quit the app out from under an open box.
func (m *home) handleImagePreviewState(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if msg.Code == tea.KeyEsc {
		m.closeImagePreview()
		return m, m.instanceChanged()
	}
	return m, nil
}
