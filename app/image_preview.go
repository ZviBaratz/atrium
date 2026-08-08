package app

// image_preview.go owns the image overlay's state: which glyph rung to draw it
// with, opening it when a load lands, and closing it (#398).

import (
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
//   - a half block that does not MEASURE one cell. Block Elements are East-Asian
//     Ambiguous, so under an east-asian width rule they measure two while still
//     rendering as one. That divergence is what desyncs the alt-screen renderer
//     into accumulating ghost rows (see theme.SanitizeWidth), and a full-window
//     picture of them would do it on every row at once.
//
// Taking all three as parameters rather than reading them keeps the rule
// testable, matching internal/doctor's convention.
func imageRenderMode(glyphSet string, mono, blocksMeasureOne bool) imageview.Mode {
	if glyphSet == theme.GlyphSetASCII || mono || !blocksMeasureOne {
		return imageview.ASCII
	}
	return imageview.HalfBlock
}

// currentImageRenderMode resolves imageRenderMode against the live environment.
//
// The width input is MEASURED rather than read off RUNEWIDTH_EASTASIAN, for the
// reasons imageview.HalfBlocksMeasureOneCell sets out: two libraries measure an
// Atrium frame and they read that variable by different rules, so under a
// Japanese locale — or the spelling "true" — they disagree with each other.
func (m *home) currentImageRenderMode() imageview.Mode {
	return imageRenderMode(m.appConfig.GetGlyphSet(), theme.Mono(), imageview.HalfBlocksMeasureOneCell())
}

// handleImageLoaded opens the overlay on a decoded image, or explains why not.
//
// The load ran in a command, so this is a different Update from the keypress
// that asked for it. That is deliberate — decoding on the loop would stall every
// session's poll — and it is also why the overlay is opened by a message rather
// than by a key.
//
// It is ALSO why the result has to be dropped when the user has moved on. A large
// decode takes hundreds of milliseconds, which is long enough to press another
// key, and opening unconditionally would seize whatever state that key reached:
// a half-typed create form replaced by a picture (and discarded by the esc that
// closes it), or — worse — stateHints overwritten without exitHintMode, which
// leaves PreviewPane.hintContent set with nothing to clear it. The tick's
// self-heal cannot recover that one: it is gated on `m.state == stateHints`
// (app/app_msgs.go:192), which is no longer true, so the pane stays frozen on a
// dimmed, hint-labelled snapshot until the selection changes.
//
// Dropping silently rather than notifying is the sibling loader's behaviour
// (handleCheckpointsLoaded, app/app_checkpoints.go:122) and the right one here:
// the user has already moved to something else, and a toast would interrupt that
// too.
func (m *home) handleImageLoaded(msg imageLoadedMsg) (tea.Model, tea.Cmd) {
	if m.state != stateDefault {
		return m, nil
	}
	if msg.err != nil {
		return m, m.handleInfoNotice(truncateForNotice(msg.err.Error()))
	}
	return m, m.openImagePreview(overlay.Image{
		Path: msg.path, Pixels: msg.img, Width: msg.width, Height: msg.height,
	})
}

// openImagePreview arms the overlay and switches state. Split from
// handleImageLoaded so a test can open the real overlay the way production does
// rather than assigning the field by hand.
//
// The command it returns is the pixel rung's opening move, and it is returned
// rather than run because the picture is already complete without it: the
// overlay draws glyphs now, and a terminal that answers the transmission
// upgrades it later. See image_kitty.go.
func (m *home) openImagePreview(src overlay.Image) tea.Cmd {
	m.imageOverlay = overlay.NewImageOverlay(src, m.currentImageRenderMode())
	m.imageOverlay.SetSize(
		min(int(float32(m.windowWidth)*0.85), imagePreviewMaxWidth),
		min(int(float32(m.windowHeight)*0.85), imagePreviewMaxHeight),
	)
	m.state = stateImagePreview
	m.recomputeLayout() // the hint bar gives up its row while the box is open

	if !kittyEligible(m.graphicsEnviron(), m.appConfig.GetImagePreview(), theme.Mono()) {
		return nil
	}
	return m.transmitImageCmd(src.Pixels)
}

// closeImagePreview returns to the default state and drops the decoded image.
//
// Dropping it matters twice over: the intermediate is up to a megapixel of RGBA
// that an overlay left armed would pin for the rest of the session, and on the
// pixel rung the terminal is holding a copy of its own that only an explicit
// delete frees.
func (m *home) closeImagePreview() tea.Cmd {
	m.state = stateDefault
	m.imageOverlay = nil
	m.recomputeLayout()
	return m.releaseKittyImageCmd()
}

// handleImagePreviewState consumes every key while the box is up. It is a
// read-only view with exactly one gesture, so anything that is not esc is
// swallowed rather than falling through to the global keys behind it — q in
// particular, which would otherwise quit the app out from under an open box.
func (m *home) handleImagePreviewState(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if msg.Code == tea.KeyEsc {
		release := m.closeImagePreview()
		return m, tea.Batch(release, m.instanceChanged())
	}
	return m, nil
}
