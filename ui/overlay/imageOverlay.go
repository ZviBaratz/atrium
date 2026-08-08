package overlay

import (
	"fmt"
	"image"
	"path/filepath"
	"strings"

	"github.com/ZviBaratz/atrium/ui/imageview"
	"github.com/ZviBaratz/atrium/ui/theme"

	"charm.land/lipgloss/v2"
	"github.com/muesli/reflow/truncate"
)

// imageChrome is the rows the box costs before any picture: 2 border, 2 padding,
// the title, the blank under it, the blank above the footer, and the footer.
// Every one of them is unconditional — a row that appears only sometimes is a
// row no golden renders, and the overflow costs the bottom border invisibly.
const imageChrome = 8

// imageMaxCols and imageMaxRows bound the picture regardless of how large the
// terminal is.
//
// This is a cost ceiling, not a taste one. Every image cell carries its own
// foreground and background, PlaceOverlay re-measures each composited line with
// ansi.StringWidth on every repaint, and an idle Atrium builds ~32 frames a
// second — so an unbounded picture puts a five-figure cell count on the hot path
// the #546 idle-CPU work spent four PRs clearing. 120×40 is larger than any
// terminal fraction this is given today; it exists so a very large one cannot
// change that.
const (
	imageMaxCols = 120
	imageMaxRows = 40
)

// imageFooter teaches the only gesture, and imageEmptyNotice stands in for a
// picture there is none of.
//
// Deliberately not a fitHint ladder: there is one rung worth showing, so the
// bound is enforced by truncation instead. It has to be enforced somewhere —
// innerWidth floors at 1, so at nine and fifteen cells these are both wider than
// the box on a narrow terminal, and a wrapped line is a row the height budget
// never counted. (An earlier comment here claimed the nine cells "fits every
// width this box can be given (the inner floor is 20)"; that floor became 1 in
// the same change that documented it, and the claim outlived it.)
const (
	imageFooter      = "esc close"
	imageEmptyNotice = "nothing to show"
)

// Image is a picture to show, and the split between its two size sources is the
// point of the type.
//
// Pixels is the BOUNDED INTERMEDIATE the load produced, which for anything large
// is not the file's resolution. Width and Height are the FILE's. Reading the
// dimensions off Pixels would put the intermediate's size in the title and tell
// the user their 2048×1024 screenshot was 1024×512 — a caption that is wrong
// about the very thing it exists to state.
type Image struct {
	Path          string
	Pixels        image.Image
	Width, Height int
}

// ImageOverlay shows a decoded image inside Atrium's own chrome (#398).
//
// It renders the picture as text — half blocks or an ASCII ramp, see
// ui/imageview — which is what lets it work in every terminal, over SSH, and
// inside tmux. The caller picks the mode; this only places what comes back.
//
// On the kitty rung the picture is text too, and that is the trick rather than a
// compromise: the cells are Unicode placeholders the terminal fills with real
// pixels, so the cell differ handles them like any other content and closing the
// overlay deletes the image by construction. The overlay never speaks the
// protocol — it is handed a confirmed image ID and asked for cells.
//
// The rendered picture is CACHED. Render runs on every repaint, and re-walking
// the pixels tens of times a second for a frame that has not changed is the
// whole of this feature's performance risk. The cache key is everything the
// picture depends on, so a resize or a mode change recomputes and nothing else
// does.
type ImageOverlay struct {
	src  Image
	mode imageview.Mode

	// kittyID is the terminal-assigned image ID, or 0 for "no pixels yet".
	// Zero is the only honest default: the ID exists only once a terminal has
	// answered, and every terminal that never will simply leaves it zero.
	kittyID uint32

	width  int
	height int

	cache    string
	cacheKey imageCacheKey
}

// imageCacheKey is every input to the rendered picture. If a field is added to
// the render and not to this, the overlay serves a stale picture.
type imageCacheKey struct {
	cols, rows int
	mode       imageview.Mode
	kittyID    uint32
	valid      bool
}

// NewImageOverlay builds the overlay for an already-decoded image.
//
// src.Pixels is the bounded intermediate the load command produced, not the
// file's full resolution — see app/image_load.go for why the shrink happens off
// the loop, and Image for why the title cannot be derived from it.
func NewImageOverlay(src Image, mode imageview.Mode) *ImageOverlay {
	return &ImageOverlay{src: src, mode: mode}
}

// Path returns the file being shown.
func (o *ImageOverlay) Path() string { return o.src.Path }

// SetSize records the room the overlay may take, borders included.
func (o *ImageOverlay) SetSize(width, height int) {
	o.width, o.height = width, height
}

// pictureSize returns the cells the picture may occupy.
func (o *ImageOverlay) pictureSize() (cols, rows int) {
	cols = min(max(o.innerWidth(), 1), imageMaxCols)
	rows = min(max(o.height-imageChrome, 1), imageMaxRows)
	return cols, rows
}

// innerWidth is the content width inside border and padding.
//
// Floored at 1, NOT at a comfortable minimum. A floor above the box's real
// content width is not a floor, it is an overflow: pictureSize takes this as the
// picture's column count, so on a box narrower than the floor every picture row
// is wider than the content area, lipgloss wraps all of them, and the box
// overruns the height it declared — which PlaceOverlay then takes off the bottom
// border. Measured before this was 1: a 25×12 box rendered 13 rows, and a 20×9
// box rendered 10. A picture too narrow to be worth much is the correct outcome
// on a 24-column terminal; a broken frame is not.
func (o *ImageOverlay) innerWidth() int {
	return max(o.width-6, 1) // border (2) + horizontal padding (2*2)
}

// SetKittyImage upgrades the overlay to real pixels, once a terminal has
// confirmed the transmission and assigned an image ID.
//
// It is an upgrade and never a mode: the overlay opens on a glyph rung and stays
// there unless this is called, so a terminal that cannot do it, or answers late,
// or never answers, needs no timeout and no fallback path — it simply keeps the
// picture it already had. That is also what makes it impossible to emit a
// placeholder cell before the image it refers to exists.
func (o *ImageOverlay) SetKittyImage(id uint32) { o.kittyID = id }

// KittyCells is the cell rectangle the placeholder block occupies, and the same
// rectangle the virtual placement must be created with.
//
// One function answers it for both, on purpose. The placeholders address rows
// and columns of the grid the placement declares, so a placement built from
// different arithmetic than the cells would scale the image into a rectangle the
// cells are not describing — every cell then shows the wrong slice, which is a
// far stranger failure than a blank box.
//
// ok is false when there is nothing to place.
func (o *ImageOverlay) KittyCells() (cols, rows int, ok bool) {
	if o.kittyID == 0 || o.src.Pixels == nil {
		return 0, 0, false
	}
	b := o.src.Pixels.Bounds()
	if b.Dx() <= 0 || b.Dy() <= 0 {
		return 0, 0, false
	}
	maxCols, maxRows := o.pictureSize()
	// The INTERMEDIATE's aspect, not the file's: the intermediate is what was
	// transmitted, so it is what the terminal scales into this rectangle. For a
	// GIF the two genuinely differ — the file reports its logical screen and the
	// decoded frame can be a subrectangle of it.
	cols, rows = imageview.FitCells(b.Dx(), b.Dy(), maxCols, maxRows)
	return cols, rows, true
}

// picture renders — or replays — the image at the current size.
func (o *ImageOverlay) picture(cols, rows int) string {
	key := imageCacheKey{cols: cols, rows: rows, mode: o.mode, kittyID: o.kittyID, valid: true}
	if o.cacheKey == key {
		return o.cache
	}
	o.cache = o.render(cols, rows)
	o.cacheKey = key
	return o.cache
}

// render draws the picture on whichever rung is currently available.
//
// A refused placement falls back to the glyphs rather than to nothing, and the
// honest note about that branch is that nothing can currently reach it:
// Placeholders refuses a rectangle wider or taller than the diacritic table can
// address, and imageMaxCols/imageMaxRows are both far inside that bound. It is
// kept because those are independent constants that can drift apart, and pinned
// by TestPictureCapsStayInsideAPlacement so raising a cap past the bound fails a
// test instead of silently dropping the pixel rung back to glyphs.
func (o *ImageOverlay) render(cols, rows int) string {
	if o.kittyID != 0 {
		if kCols, kRows, ok := o.KittyCells(); ok {
			if cells, err := imageview.Placeholders(o.kittyID, kCols, kRows); err == nil {
				return cells
			}
		}
	}
	return imageview.Render(o.src.Pixels, cols, rows, o.mode)
}

// Render draws the bordered box.
func (o *ImageOverlay) Render() string {
	th := theme.Current()
	// lipgloss v2 counts the border inside Width, so Width(o.width) occupies
	// exactly o.width columns on screen. See theme.Panel.
	box := lipgloss.NewStyle().
		Border(th.Borders.Style).
		BorderForeground(th.Palette.Accent).
		Padding(1, 2).
		Width(o.width)

	inner := o.innerWidth()

	var b strings.Builder
	b.WriteString(th.OverlayTitleStyle().Render(o.title(inner)) + "\n\n")

	if o.src.Pixels == nil {
		b.WriteString(overlayDimStyle().Render(fitCells(imageEmptyNotice, inner)) + "\n\n")
	} else {
		cols, rows := o.pictureSize()
		pic := o.picture(cols, rows)
		if pic == "" {
			b.WriteString(overlayDimStyle().Render(fitCells(imageEmptyNotice, inner)) + "\n\n")
		} else {
			// Centred by padding each line, NOT by wrapping the block in a
			// style: every cell already carries its own foreground and
			// background, and a surrounding lipgloss background would fight the
			// resets those lines end on. Padding is inert.
			b.WriteString(centreBlock(pic, inner) + "\n\n")
		}
	}

	b.WriteString(th.OverlayHintStyle().Render(fitCells(imageFooter, inner)))
	return box.Render(fitLines(b.String(), o.height-4)) // border (2) + padding (2)
}

// fitCells truncates s to at most width cells.
//
// Guarded by the width check rather than handed straight to the helper, because
// truncate.StringWithTail replaces a character at EXACTLY the budget rather than
// only above it — so a string that already fits comes back one character short
// and an ellipsis long.
func fitCells(s string, width int) string {
	if width < 1 {
		return ""
	}
	if lipgloss.Width(s) <= width {
		return s
	}
	return truncate.StringWithTail(s, uint(width), "…")
}

// fitLines hard-clips content to at most n lines.
//
// The last word on the height invariant, and it has to be unconditional rather
// than a case: pictureSize floors the picture at one row, so a box shorter than
// imageChrome renders more rows than it was given no matter what the picture
// does — measured at 12×6, which produced ten rows. Reasoning about which rows
// to shed at which height is how a box comes to overflow at some size nobody
// listed; clipping the composed content bounds every size instead.
//
// It bounds it only in company, though, and the missing half is the defect this
// comment used to describe away. Clipping is exact "since the box adds exactly
// its border and padding to whatever it is handed" — which is true right up
// until a content line is wider than the box, because then lipgloss wraps it and
// the box adds a row this function already counted. Every line handed to it is
// therefore truncated to inner width first: the picture by construction (its
// column count IS the inner width), the title, footer and notice by fitCells.
// Measured before they were: a 14×14 box rendered 15 rows and a 10×14 box 16.
//
// A degenerate box loses its footer, and that is the right trade: a lost hint is
// a hint, a lost bottom border is a frame PlaceOverlay never gets back.
func fitLines(content string, n int) string {
	if n < 1 {
		n = 1
	}
	lines := strings.Split(content, "\n")
	if len(lines) <= n {
		return content
	}
	return strings.Join(lines[:n], "\n")
}

// title names the file and its pixel size, truncated to the box. The filename
// comes from an agent's output and has no ceiling.
func (o *ImageOverlay) title(width int) string {
	name := filepath.Base(o.src.Path)
	if o.src.Width > 0 && o.src.Height > 0 {
		name = fmt.Sprintf("%s · %d×%d", name, o.src.Width, o.src.Height)
	}
	return fitCells(name, width)
}

// centreBlock pads each line of a rendered picture so the block sits in the
// middle of width. Lines are padded, never truncated: the picture was rendered
// to fit, and a truncation here would cut a cell's escape sequence in half.
func centreBlock(block string, width int) string {
	lines := strings.Split(block, "\n")
	pad := (width - lipgloss.Width(lines[0])) / 2
	if pad <= 0 {
		return block
	}
	prefix := strings.Repeat(" ", pad)
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}
