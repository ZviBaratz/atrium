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

// imageFooter teaches the only gesture. Deliberately not a fitHint ladder: at 9
// cells it fits every width this box can be given (the inner floor is 20), so
// there is a bound to prove rather than rungs to order.
const imageFooter = "esc close"

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
// The rendered picture is CACHED. Render runs on every repaint, and re-walking
// the pixels tens of times a second for a frame that has not changed is the
// whole of this feature's performance risk. The cache key is everything the
// picture depends on, so a resize or a mode change recomputes and nothing else
// does.
type ImageOverlay struct {
	src  Image
	mode imageview.Mode

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
func (o *ImageOverlay) innerWidth() int {
	inner := o.width - 6 // border (2) + horizontal padding (2*2)
	if inner < 20 {
		inner = 20
	}
	return inner
}

// picture renders — or replays — the image at the current size.
func (o *ImageOverlay) picture(cols, rows int) string {
	key := imageCacheKey{cols: cols, rows: rows, mode: o.mode, valid: true}
	if o.cacheKey == key {
		return o.cache
	}
	o.cache = imageview.Render(o.src.Pixels, cols, rows, o.mode)
	o.cacheKey = key
	return o.cache
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
		b.WriteString(overlayDimStyle().Render("nothing to show") + "\n\n")
	} else {
		cols, rows := o.pictureSize()
		pic := o.picture(cols, rows)
		if pic == "" {
			b.WriteString(overlayDimStyle().Render("nothing to show") + "\n\n")
		} else {
			// Centred by padding each line, NOT by wrapping the block in a
			// style: every cell already carries its own foreground and
			// background, and a surrounding lipgloss background would fight the
			// resets those lines end on. Padding is inert.
			b.WriteString(centreBlock(pic, inner) + "\n\n")
		}
	}

	b.WriteString(th.OverlayHintStyle().Render(imageFooter))
	return box.Render(b.String())
}

// title names the file and its pixel size, truncated to the box.
//
// The filename comes from an agent's output and has no ceiling, so it is cut to
// the budget — and guarded, because truncate.StringWithTail replaces a character
// at exactly the budget rather than only above it, which would eat the last
// character of a title that already fit.
func (o *ImageOverlay) title(width int) string {
	name := filepath.Base(o.src.Path)
	if o.src.Width > 0 && o.src.Height > 0 {
		name = fmt.Sprintf("%s · %d×%d", name, o.src.Width, o.src.Height)
	}
	if lipgloss.Width(name) <= width {
		return name
	}
	return truncate.StringWithTail(name, uint(width), "…")
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
