// Package imageview renders an image.Image as terminal text: a block of styled
// cells that any lipgloss layout can hold and any terminal can draw.
//
// This is the universal rung of the image ladder (#398). The other rung is the
// kitty graphics protocol, which shows real pixels but exists on two terminals;
// this one trades resolution for reaching every terminal Atrium supports,
// including over SSH and inside tmux.
//
// It deliberately depends on neither Bubble Tea nor ui/theme. The caller picks
// the mode and places the result, which keeps every rule below — the glyph, the
// colour encoding, the width — testable without a frame.
package imageview

import (
	"fmt"
	"image"
	"image/color"
	"strings"
)

// Mode selects the glyph vocabulary.
type Mode int

const (
	// HalfBlock draws each cell as U+2580 with the upper sample as foreground
	// and the lower as background, so one cell carries two square pixels. This
	// is the default and the higher-fidelity rung.
	HalfBlock Mode = iota
	// ASCII draws each cell as a 7-bit luminance ramp rune, keeping the colour.
	// It is the rung for terminals where even Block Elements show tofu
	// (glyph_set: ascii), for NO_COLOR — where every half block would be an
	// identical glyph carrying no information at all — and for
	// RUNEWIDTH_EASTASIAN, which makes Block Elements measure two cells while
	// rendering one.
	ASCII
)

// upperHalf and lowerHalf are U+2580 and U+2584. Block Elements are already the
// plain glyph rung's vocabulary (ui/theme/registry.go's spinner and context
// ramp), so the font-availability question is settled precedent rather than a
// new bet.
//
// There are two of them because of transparency. "▀" paints its top half in the
// foreground and leaves the bottom to the background, so it can only hand the
// BOTTOM half back to whatever is behind the block; a cell whose top sample is
// transparent and whose bottom is not needs the mirror image.
const (
	upperHalf = "▀"
	lowerHalf = "▄"
)

// HalfBlockGlyphs returns every glyph the HalfBlock rung can emit.
//
// Exported so a caller can measure the exact glyphs it is about to ship rather
// than infer their width from an environment variable. Those two are not the
// same question: go-runewidth and x/ansi read RUNEWIDTH_EASTASIAN by different
// rules and disagree about these glyphs under ordinary environments — see
// app.halfBlocksMeasureOneCell.
func HalfBlockGlyphs() []string { return []string{upperHalf, lowerHalf} }

// alphaOpaque is the coverage at which a sample is painted rather than left to
// the surface behind it.
//
// The choice is binary because a half block is one flat colour per half-cell: it
// cannot show partial coverage, and there is no way to blend towards a background
// this package is deliberately not allowed to know. 50% puts the boundary in the
// middle of an antialiased edge, which is where it reads best at this resolution.
const alphaOpaque = 0x80

// ramp runs darkest to lightest. Its first rung is a space, so a black region
// costs one byte and shows the surface underneath.
var ramp = []rune(" .:-=+*#%@")

// Render draws img as at most cols×rows cells and returns the block, newline
// separated, with no trailing newline.
//
// The result is the FITTED size, not the box: an image that does not match the
// box's aspect comes back narrower or shorter, and centring it is the caller's
// job. Returning a smaller block rather than padding one keeps the letterbox in
// whatever colour the surrounding overlay is already painting, which is the only
// way to letterbox without knowing that colour.
//
// Returns "" for a degenerate box or a nil image, so a caller can fall through
// to its own empty state instead of placing a stray row.
func Render(img image.Image, cols, rows int, mode Mode) string {
	if img == nil || cols <= 0 || rows <= 0 {
		return ""
	}
	b := img.Bounds()
	if b.Dx() <= 0 || b.Dy() <= 0 {
		return ""
	}

	// Half blocks stack two square samples per cell; an ASCII cell holds one
	// sample and is about twice as tall as it is wide, so its samples count
	// double against the aspect.
	sampleRows, vScale := rows*2, 1
	if mode == ASCII {
		sampleRows, vScale = rows, 2
	}
	w, h := fitSize(b.Dx(), b.Dy(), cols, sampleRows, vScale)
	grid := resample(img, w, h)

	if mode == ASCII {
		return renderASCII(grid, w, h)
	}
	return renderHalfBlock(grid, w, h)
}

// fitSize returns the largest w×h sample grid that fits maxCols×maxRows while
// keeping srcW:srcH, given that one sample is vScale cell-heights tall.
func fitSize(srcW, srcH, maxCols, maxRows, vScale int) (w, h int) {
	w = maxCols
	h = w * srcH / (srcW * vScale)
	if h > maxRows {
		h = maxRows
		w = h * vScale * srcW / srcH
	}
	return max(w, 1), max(h, 1)
}

// sample is one resampled pixel: 8-bit colour with the alpha divided back out,
// plus that alpha as coverage. Keeping the two apart is what lets a transparent
// region show the overlay instead of being painted black.
type sample struct{ r, g, b, a uint8 }

// opaque reports whether the sample should be painted at all.
func (s sample) opaque() bool { return s.a >= alphaOpaque }

// Downscale box-filters img to fit within maxW×maxH, preserving aspect, and
// never enlarges: an image already inside the box comes back at its own size.
//
// This exists so the expensive pass over a full-resolution source runs exactly
// once, off the Bubble Tea goroutine, and everything afterwards — the glyph
// render, and a re-render on every resize — works from a bounded intermediate.
// It shares the box filter with Render so the two never disagree about what a
// pixel of this image is, and it carries alpha through: the intermediate is what
// the overlay renders from, so flattening transparency here would flatten it for
// every rung and every resize.
func Downscale(img image.Image, maxW, maxH int) *image.RGBA {
	if img == nil || maxW <= 0 || maxH <= 0 {
		return nil
	}
	b := img.Bounds()
	if b.Dx() <= 0 || b.Dy() <= 0 {
		return nil
	}

	w, h := b.Dx(), b.Dy()
	if w > maxW {
		w, h = maxW, max(maxW*b.Dy()/b.Dx(), 1)
	}
	if h > maxH {
		w, h = max(maxH*b.Dx()/b.Dy(), 1), maxH
	}

	grid := resample(img, w, h)
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			c := grid[y*w+x]
			// Premultiplied, and carrying its alpha: color.RGBA is a
			// premultiplied format, so this is what resample reads back as the
			// colour and coverage that went in.
			out.SetRGBA(x, y, color.RGBA{R: premul(c.r, c.a), G: premul(c.g, c.a), B: premul(c.b, c.a), A: c.a})
		}
	}
	return out
}

// resample box-filters img down to a w×h grid, averaging every source pixel that
// falls inside a sample. A box filter is the right kernel for the large
// reduction ratios this sees — a browser screenshot into a few thousand cells —
// where a nearest-neighbour or narrow-tap sampler drops most of the image on the
// floor and aliases the rest.
func resample(img image.Image, w, h int) []sample {
	b := img.Bounds()
	grid := make([]sample, w*h)
	for oy := range h {
		y0 := b.Min.Y + oy*b.Dy()/h
		y1 := max(b.Min.Y+(oy+1)*b.Dy()/h, y0+1)
		for ox := range w {
			x0 := b.Min.X + ox*b.Dx()/w
			x1 := max(b.Min.X+(ox+1)*b.Dx()/w, x0+1)

			var sr, sg, sb, sa, n uint64
			for y := y0; y < y1; y++ {
				for x := x0; x < x1; x++ {
					r, g, bl, a := img.At(x, y).RGBA()
					sr += uint64(r)
					sg += uint64(g)
					sb += uint64(bl)
					sa += uint64(a)
					n++
				}
			}
			grid[oy*w+ox] = combine(sr, sg, sb, sa, n)
		}
	}
	return grid
}

// combine turns one sample's accumulated channels into a sample.
//
// color.Color.RGBA returns ALPHA-PREMULTIPLIED channels, so each sum is already
// coverage-weighted and sr/sa is the mean colour with that coverage divided back
// out. Averaging the premultiplied values directly — which this used to do, while
// discarding alpha entirely — makes a fully transparent pixel arithmetically
// identical to a black one, so a plot saved with transparent=True or a screenshot
// with rounded corners came back as a black rectangle.
func combine(sr, sg, sb, sa, n uint64) sample {
	if sa == 0 {
		return sample{} // no coverage anywhere: there is no colour to recover
	}
	return sample{r: unpremul(sr, sa), g: unpremul(sg, sa), b: unpremul(sb, sa), a: to8(sa, n)}
}

// unpremul divides a premultiplied channel sum by the alpha sum, to 8 bits.
//
// A premultiplied channel never exceeds its own alpha, so the sums obey the same
// bound and the quotient cannot exceed 0xffff — which the mask then makes true by
// construction rather than by that argument.
func unpremul(sum, alpha uint64) uint8 {
	return uint8(sum * 0xffff / alpha >> 8 & 0xff)
}

// premul re-applies alpha, because image.RGBA stores premultiplied channels and
// Downscale's output has to round-trip back through resample unchanged. Masked
// for the reason to8 is: the bound holds by construction rather than by an
// argument the compiler cannot check.
func premul(v, a uint8) uint8 {
	return uint8(uint32(v) * uint32(a) / 0xff & 0xff)
}

// to8 averages a channel's accumulated 16-bit samples down to 8 bits.
//
// color.Color.RGBA returns 16-bit values, so the mean is at most 0xffff and the
// shift leaves at most 0xff — but that is an argument about the color interface's
// contract, which the compiler cannot check and gosec will not take on faith. The
// mask makes the bound true by construction instead of by reasoning.
func to8(sum, n uint64) uint8 {
	return uint8(sum / n >> 8 & 0xff)
}

// cellColor is a foreground or background: a colour, or the terminal's own
// default. The default is how a half-covered cell hands its other half back to
// whatever is painting behind the block instead of guessing a colour for it.
type cellColor struct {
	c   sample
	def bool
}

// defaultColor selects SGR 39/49 — the terminal's own foreground/background.
var defaultColor = cellColor{def: true}

// halfBlockCell picks the glyph and the two colours for one cell.
//
// Four cases, because either sample may be absent (an odd final sample row) or
// transparent, and the glyph has to change with them: "▀" can only surrender its
// bottom half to the surface behind it, so a cell that is transparent on top and
// painted underneath needs "▄" instead. Painting it with "▀" and a default
// foreground would fill the top half with the terminal's TEXT colour, which is
// the one colour it certainly is not.
func halfBlockCell(top, bottom sample, haveBottom bool) (glyph string, fg, bg cellColor) {
	switch topOn, bottomOn := top.opaque(), haveBottom && bottom.opaque(); {
	case topOn && bottomOn:
		return upperHalf, cellColor{c: top}, cellColor{c: bottom}
	case topOn:
		return upperHalf, cellColor{c: top}, defaultColor
	case bottomOn:
		return lowerHalf, cellColor{c: bottom}, defaultColor
	default:
		return " ", defaultColor, defaultColor
	}
}

// renderHalfBlock pairs sample rows into cells.
func renderHalfBlock(grid []sample, w, h int) string {
	var out strings.Builder
	out.Grow(w * h * 8)
	for top := 0; top < h; top += 2 {
		if top > 0 {
			out.WriteByte('\n')
		}
		// Colour state resets at every row. PlaceOverlay builds each composited
		// line's left half with ansi.Truncate, which keeps collecting escapes
		// past the cut — so the background line's live SGR reaches the point
		// where this row begins, and a row that opened on an inherited colour
		// would paint its first pixel with it.
		var lastFg, lastBg cellColor
		var haveFg, haveBg bool
		for x := range w {
			var bottom sample
			haveBottom := top+1 < h
			if haveBottom {
				bottom = grid[(top+1)*w+x]
			}
			glyph, fg, bg := halfBlockCell(grid[top*w+x], bottom, haveBottom)
			if !haveFg || fg != lastFg {
				writeFg(&out, fg)
				lastFg, haveFg = fg, true
			}
			if !haveBg || bg != lastBg {
				writeBg(&out, bg)
				lastBg, haveBg = bg, true
			}
			out.WriteString(glyph)
		}
		out.WriteString("\x1b[0m")
	}
	return out.String()
}

// renderASCII maps each sample's luminance onto the ramp, keeping its colour.
func renderASCII(grid []sample, w, h int) string {
	var out strings.Builder
	out.Grow(w * h * 6)
	for y := range h {
		if y > 0 {
			out.WriteByte('\n')
		}
		var lastFg cellColor
		var haveFg bool
		for x := range w {
			c := grid[y*w+x]
			// A transparent sample takes a blank and the default foreground, so
			// the overlay shows through rather than the ramp drawing a rung for
			// a colour that is not there.
			fg, glyph := cellColor{c: c}, ramp[rampIndex(c)]
			if !c.opaque() {
				fg, glyph = defaultColor, ' '
			}
			if !haveFg || fg != lastFg {
				writeFg(&out, fg)
				lastFg, haveFg = fg, true
			}
			out.WriteRune(glyph)
		}
		out.WriteString("\x1b[0m")
	}
	return out.String()
}

// rampIndex picks a ramp rung from Rec. 601 luma. The arithmetic is scaled so
// pure white lands on the last rung exactly rather than one short of it.
func rampIndex(c sample) int {
	luma := 299*int(c.r) + 587*int(c.g) + 114*int(c.b) // 0 … 255000
	return luma * (len(ramp) - 1) / 255000
}

func writeFg(out *strings.Builder, c cellColor) {
	if c.def {
		out.WriteString("\x1b[39m")
		return
	}
	fmt.Fprintf(out, "\x1b[38;2;%d;%d;%dm", c.c.r, c.c.g, c.c.b)
}

func writeBg(out *strings.Builder, c cellColor) {
	if c.def {
		out.WriteString("\x1b[49m")
		return
	}
	fmt.Fprintf(out, "\x1b[48;2;%d;%d;%dm", c.c.r, c.c.g, c.c.b)
}
