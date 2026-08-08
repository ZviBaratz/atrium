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

// upperHalf is U+2580 UPPER HALF BLOCK. Block Elements are already the plain
// glyph rung's vocabulary (ui/theme/registry.go's spinner and context ramp), so
// the font-availability question is settled precedent rather than a new bet.
const upperHalf = "▀"

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

// rgb is one sample: 8-bit, opaque.
type rgb struct{ r, g, b uint8 }

// Downscale box-filters img to fit within maxW×maxH, preserving aspect, and
// never enlarges: an image already inside the box comes back at its own size.
//
// This exists so the expensive pass over a full-resolution source runs exactly
// once, off the Bubble Tea goroutine, and everything afterwards — the glyph
// render, and a re-render on every resize — works from a bounded intermediate.
// It shares the box filter with Render so the two never disagree about what a
// pixel of this image is.
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
			out.SetRGBA(x, y, color.RGBA{R: c.r, G: c.g, B: c.b, A: 0xff})
		}
	}
	return out
}

// resample box-filters img down to a w×h grid, averaging every source pixel that
// falls inside a sample. A box filter is the right kernel for the large
// reduction ratios this sees — a browser screenshot into a few thousand cells —
// where a nearest-neighbour or narrow-tap sampler drops most of the image on the
// floor and aliases the rest.
func resample(img image.Image, w, h int) []rgb {
	b := img.Bounds()
	grid := make([]rgb, w*h)
	for oy := range h {
		y0 := b.Min.Y + oy*b.Dy()/h
		y1 := max(b.Min.Y+(oy+1)*b.Dy()/h, y0+1)
		for ox := range w {
			x0 := b.Min.X + ox*b.Dx()/w
			x1 := max(b.Min.X+(ox+1)*b.Dx()/w, x0+1)

			var sr, sg, sb, n uint64
			for y := y0; y < y1; y++ {
				for x := x0; x < x1; x++ {
					r, g, bl, _ := img.At(x, y).RGBA()
					sr += uint64(r)
					sg += uint64(g)
					sb += uint64(bl)
					n++
				}
			}
			grid[oy*w+ox] = rgb{r: to8(sr, n), g: to8(sg, n), b: to8(sb, n)}
		}
	}
	return grid
}

// to8 averages a colour channel's accumulated 16-bit samples down to 8 bits.
//
// color.Color.RGBA returns 16-bit values, so the mean is at most 0xffff and the
// shift leaves at most 0xff — but that is an argument about the color interface's
// contract, which the compiler cannot check and gosec will not take on faith. The
// mask makes the bound true by construction instead of by reasoning.
func to8(sum, n uint64) uint8 {
	return uint8(sum / n >> 8 & 0xff)
}

// renderHalfBlock pairs sample rows into cells.
func renderHalfBlock(grid []rgb, w, h int) string {
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
		var lastFg, lastBg rgb
		var haveFg, haveBg bool
		for x := range w {
			fg := grid[top*w+x]
			if !haveFg || fg != lastFg {
				writeFg(&out, fg)
				lastFg, haveFg = fg, true
			}
			if top+1 < h {
				bg := grid[(top+1)*w+x]
				if !haveBg || bg != lastBg {
					writeBg(&out, bg)
					lastBg, haveBg = bg, true
				}
			} else if !haveBg {
				// The image ran out of sample rows half way through this cell.
				// SGR 49 hands the lower half back to whatever is painting
				// behind the block rather than guessing a colour for it.
				out.WriteString("\x1b[49m")
				haveBg = true
			}
			out.WriteString(upperHalf)
		}
		out.WriteString("\x1b[0m")
	}
	return out.String()
}

// renderASCII maps each sample's luminance onto the ramp, keeping its colour.
func renderASCII(grid []rgb, w, h int) string {
	var out strings.Builder
	out.Grow(w * h * 6)
	for y := range h {
		if y > 0 {
			out.WriteByte('\n')
		}
		var lastFg rgb
		var haveFg bool
		for x := range w {
			c := grid[y*w+x]
			if !haveFg || c != lastFg {
				writeFg(&out, c)
				lastFg, haveFg = c, true
			}
			out.WriteRune(ramp[rampIndex(c)])
		}
		out.WriteString("\x1b[0m")
	}
	return out.String()
}

// rampIndex picks a ramp rung from Rec. 601 luma. The arithmetic is scaled so
// pure white lands on the last rung exactly rather than one short of it.
func rampIndex(c rgb) int {
	luma := 299*int(c.r) + 587*int(c.g) + 114*int(c.b) // 0 … 255000
	return luma * (len(ramp) - 1) / 255000
}

func writeFg(out *strings.Builder, c rgb) {
	fmt.Fprintf(out, "\x1b[38;2;%d;%d;%dm", c.r, c.g, c.b)
}

func writeBg(out *strings.Builder, c rgb) {
	fmt.Fprintf(out, "\x1b[48;2;%d;%d;%dm", c.r, c.g, c.b)
}
