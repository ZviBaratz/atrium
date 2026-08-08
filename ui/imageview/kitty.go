package imageview

import (
	"fmt"
	"image"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/ansi/kitty"
	"github.com/mattn/go-runewidth"
)

// This file is the kitty rung of the image ladder (#398): real pixels, drawn by
// the terminal, on the two terminals with confirmed Unicode-placeholder support.
//
// It stays in this package rather than in app or ui/overlay for the reason the
// package doc gives — the caller picks the rung and places the result, and every
// rule here (the cell shape, the colour encoding, the width) is then testable
// without a frame. There is no Bubble Tea import: the three protocol strings are
// returned as strings, and whoever owns the event loop decides how to write them.
//
// Why placeholders and not direct or cursor placement: ultraviolet's printString
// (styled.go) understands only SGR and OSC 8. An APC sequence inside the View
// string is accumulated into the pending cell and then DISCARDED by the next
// printable grapheme, so a transmission cannot ride the frame. Placeholders
// sidestep it — the image rides as ordinary text cells, so the cell differ
// handles it natively and closing the overlay deletes the image by construction.

// Placeholder cells are U+10EEEE carrying combining diacritics that spell out the
// cell's row and column, with the image ID in the foreground colour.
//
// maxPlaceholderIndex is the largest row or column a placement can address.
// x/ansi's kitty.Diacritic CLAMPS rather than erroring — it returns diacritics[0]
// for an out-of-range index, which would silently place a cell at row/column 0 —
// so the bound is enforced here instead of being inferred from the caller's cap.
// TestDiacriticTableBound measures it rather than trusting this number.
const maxPlaceholderIndex = 296

// idBitsInForeground is how much of the image ID the foreground colour carries.
// Anything above it goes in a third diacritic. A truecolor foreground is three
// bytes, which is also why the rung requires the TrueColor profile: a
// downsampling profile does not dim the picture, it rewrites the ID.
const idBitsInForeground = 24

// placeholderGlyphs returns every glyph shape this rung emits, for the width
// predicate below. Both forms appear in a rendered row: the base character
// carries the diacritics, and the diacritics are what a naive measurer is most
// likely to count separately.
func placeholderGlyphs() []string {
	base := string(kitty.Placeholder)
	return []string{
		base,
		base + string(kitty.Diacritic(0)) + string(kitty.Diacritic(0)),
		base + string(kitty.Diacritic(maxPlaceholderIndex)) + string(kitty.Diacritic(maxPlaceholderIndex)),
	}
}

// PlaceholdersMeasureOneCell reports whether every measurer in the render path
// agrees that a placeholder cell occupies exactly one cell.
//
// It is the sibling of HalfBlocksMeasureOneCell and exists for the same reason:
// the answer is needed by app (to pick the rung) and by tests (to avoid
// asserting a combination production never renders), and they must not each
// derive their own. Measuring beats reading RUNEWIDTH_EASTASIAN here for a
// sharper reason than it does for the half blocks — U+10EEEE turns out to be
// East-Asian AMBIGUOUS, exactly like Block Elements, so it produces the same
// five-row table, including the two environments where the two libraries
// disagree WITH EACH OTHER:
//
//	RUNEWIDTH_EASTASIAN   locale         go-runewidth   x/ansi
//	unset                 C                    1           1
//	"1"                   C                    2           2
//	"0"                   C                    1           1
//	unset                 ja_JP.UTF-8          2           1   ← disagree
//	"true"                C                    1           2   ← disagree
//
// Measured on go-runewidth v0.0.24 and x/ansi v0.11.7; the drift-sites skill's
// "prefer plain single-cell non-PUA Unicode" rule is deliberately broken here,
// and this predicate plus an exact per-row width assertion is what confines the
// breach to one overlay.
func PlaceholdersMeasureOneCell() bool {
	for _, g := range placeholderGlyphs() {
		if runewidth.StringWidth(g) != 1 || lipgloss.Width(g) != 1 {
			return false
		}
	}
	return true
}

// FitCells picks the cell rectangle to place a srcW×srcH image in, never larger
// than maxCols×maxRows.
//
// Unlike the glyph rungs this is a preference, not a correctness requirement:
// the terminal fits the image to whatever rectangle it is given "its aspect
// ratio preserved" (the protocol says so for virtual placements), so a wrong
// guess about how tall a cell is costs letterboxing INSIDE our rectangle and
// can never distort the picture. That is why this rung asks for no cell-pixel
// geometry — no CellSizeEvent, no TIOCGWINSZ, and no fallback branch for the
// SSH case where both report zero.
//
// vScale 2 is the same "a cell is about twice as tall as it is wide" assumption
// the ASCII rung makes, for the same reason.
func FitCells(srcW, srcH, maxCols, maxRows int) (cols, rows int) {
	return fitSize(srcW, srcH, maxCols, maxRows, 2)
}

// Placeholders renders the cell block that displays image id over cols×rows
// cells, newline separated with no trailing newline — the same shape Render
// returns, so the overlay places either one identically.
//
// Every cell carries its own row and column diacritics, and that is a deliberate
// departure from the protocol's economy. A cell with no diacritics inherits its
// row from the neighbour on the left and its column from that neighbour plus
// one, which would let a row state its position once. Spelling every cell out
// costs about six bytes a cell and buys independence from the cell to the left:
// nothing downstream — a partial repaint by the differ, a border glyph, a
// composite that truncates the line — can shift the rest of a row by damaging
// one cell. At this rung's ceiling that is tens of kilobytes on a string the
// overlay computes once and caches, against a failure mode that is invisible
// until someone looks at a real terminal.
func Placeholders(id uint32, cols, rows int) (string, error) {
	if id == 0 {
		return "", fmt.Errorf("image id must be non-zero")
	}
	if cols <= 0 || rows <= 0 {
		return "", fmt.Errorf("placement is %dx%d cells", cols, rows)
	}
	if cols > maxPlaceholderIndex+1 || rows > maxPlaceholderIndex+1 {
		return "", fmt.Errorf("placement is %dx%d cells, over the %d addressable by a diacritic",
			cols, rows, maxPlaceholderIndex+1)
	}

	base := string(kitty.Placeholder)
	// The high byte rides a third diacritic, and only when there is one: an ID
	// that fits the foreground must not grow a diacritic that says "byte 0",
	// because that is a different cell to a terminal comparing clusters.
	var msb string
	if high := id >> idBitsInForeground; high != 0 {
		msb = string(kitty.Diacritic(int(high)))
	}

	var out strings.Builder
	out.Grow(rows * (cols*10 + 24))
	for r := range rows {
		if r > 0 {
			out.WriteByte('\n')
		}
		// The foreground IS the image ID, so it is restated per row for the same
		// reason the glyph rungs restate their colours: PlaceOverlay composites
		// with ansi.Truncate, which keeps collecting escapes past the cut, so the
		// background line's live SGR reaches the point where this row begins.
		fmt.Fprintf(&out, "\x1b[38;2;%d;%d;%dm",
			(id>>16)&0xff, (id>>8)&0xff, id&0xff)
		row := string(kitty.Diacritic(r))
		for c := range cols {
			out.WriteString(base)
			out.WriteString(row)
			out.WriteString(string(kitty.Diacritic(c)))
			out.WriteString(msb)
		}
		out.WriteString("\x1b[0m")
	}
	return out.String(), nil
}

// TransmitPNG encodes img as a chunked kitty transmission that asks the terminal
// to assign an ID, tagging the request with number so the reply can be matched.
//
// Two things here are load-bearing and neither is obvious from the option names.
// Format MUST be set: kitty.EncodeGraphics defaults a zero Format to RGBA and
// transmits raw pixels, which for a 1080p image is 8.3 MB before base64 — and
// raw RGBA is additionally undecodable unless ImageWidth/ImageHeight are set by
// hand, because the encoder does not fill them from the bounds. Compression is
// deliberately NOT set: the encoder wraps the writer in zlib BEFORE the format
// switch, so PNG plus Zlib is deflate over deflate, paying CPU for nothing.
//
// Quite is left at zero on purpose. The reply is not noise to be suppressed; it
// is both the capability proof and the ID every placeholder cell needs, so this
// is the one command in the file that wants an answer.
func TransmitPNG(img image.Image, number int) (string, error) {
	opts := kitty.Options{
		Action:       kitty.Transmit,
		Format:       kitty.PNG,
		Transmission: kitty.Direct,
		Number:       number,
		Chunk:        true,
	}
	var b strings.Builder
	if err := kitty.EncodeGraphics(&b, img, &opts); err != nil {
		return "", fmt.Errorf("encode graphics: %w", err)
	}
	return b.String(), nil
}

// PlaceVirtual creates or replaces the virtual placement the placeholder cells
// refer to. Re-sending it with the same image and placement ID replaces the
// previous one rather than stacking a second, which is what makes a resize cost
// one short escape and no retransmission.
//
// It carries no payload, so it is built from ansi.KittyGraphics directly:
// EncodeGraphics always encodes the image it is given, and there is no image to
// send here.
func PlaceVirtual(id, placementID, cols, rows int) string {
	opts := kitty.Options{
		Action:           kitty.Put,
		ID:               id,
		PlacementID:      placementID,
		VirtualPlacement: true,
		Columns:          cols,
		Rows:             rows,
		// Suppress the OK. Errors still come back, and an OK here would arrive
		// as a second KittyGraphicsEvent carrying the ID we already hold.
		Quite: 1,
	}
	return ansi.KittyGraphics(nil, opts.Options()...)
}

// DeleteImage frees the terminal-side image data as well as its placements.
//
// DeleteResources is what turns d=i into d=I, and it is the difference between
// releasing the pixels and leaking them for the life of the terminal: the
// lowercase form drops the placements and keeps the data.
func DeleteImage(id int) string {
	opts := kitty.Options{
		Action:          kitty.Delete,
		Delete:          kitty.DeleteID,
		DeleteResources: true,
		ID:              id,
		Quite:           1,
	}
	return ansi.KittyGraphics(nil, opts.Options()...)
}
