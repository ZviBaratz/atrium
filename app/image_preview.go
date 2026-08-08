package app

// image_preview.go owns the image overlay's state: which glyph rung to draw it
// with, opening it when a load lands, and closing it (#398).

import (
	"github.com/ZviBaratz/atrium/ui/imageview"
	"github.com/ZviBaratz/atrium/ui/overlay"
	"github.com/ZviBaratz/atrium/ui/theme"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/mattn/go-runewidth"
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

// halfBlocksMeasureOneCell reports whether every measurer in the render path
// agrees that a half block occupies exactly one cell.
//
// This is measured rather than derived from RUNEWIDTH_EASTASIAN, and that is the
// difference between a guard and a guess. Two libraries measure this frame —
// go-runewidth for the list and the rows (ui/list.go, ui/row.go,
// ui/theme/panel.go) and x/ansi for the overlay and the composite (lipgloss.Width
// is a per-line ansi.StringWidth, and PlaceOverlay calls ansi.StringWidth
// directly) — and they read that variable by DIFFERENT rules. go-runewidth falls
// back to the LOCALE when it is empty and otherwise accepts only "1"
// (runewidth.go handleEnv); x/ansi ignores the locale and parses the value with
// strconv.ParseBool (method.go init). Measured on go-runewidth v0.0.24 and
// x/ansi v0.11.7:
//
//	RUNEWIDTH_EASTASIAN   locale         go-runewidth   x/ansi
//	unset                 C                    1           1
//	"1"                   C                    2           2
//	"0"                   C                    1           1
//	unset                 ja_JP.UTF-8          2           1   ← disagree
//	"true"                C                    1           2   ← disagree
//
// "Is the variable non-empty", which this used to ask, is wrong on the last three
// rows: it refuses half blocks under the documented force-narrow value, and it
// ships them in the two ordinary environments — a Japanese locale, and a spelling
// ParseBool accepts — where the two libraries disagree WITH EACH OTHER. That last
// case is the worse one, because the frame is then laid out by one measurer and
// composited by the other, which is the ghost-row desync with no environment
// variable to blame it on.
//
// Measuring closes all five rows without encoding either rule, and stays closed
// if either library changes it.
func halfBlocksMeasureOneCell() bool {
	for _, g := range imageview.HalfBlockGlyphs() {
		if runewidth.StringWidth(g) != 1 || lipgloss.Width(g) != 1 {
			return false
		}
	}
	return true
}

// currentImageRenderMode resolves imageRenderMode against the live environment.
func (m *home) currentImageRenderMode() imageview.Mode {
	return imageRenderMode(m.appConfig.GetGlyphSet(), theme.Mono(), halfBlocksMeasureOneCell())
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
