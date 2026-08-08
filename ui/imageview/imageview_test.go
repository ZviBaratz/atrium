package imageview

import (
	"image"
	"image/color"
	"strings"
	"testing"

	xansi "github.com/charmbracelet/x/ansi"
	"github.com/muesli/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// solid builds a w×h image whose pixel (x, y) is at(x, y).
func solid(w, h int, at func(x, y int) color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.SetRGBA(x, y, at(x, y))
		}
	}
	return img
}

// requireBlocksMeasureOne skips a half-block assertion in an environment where
// Block Elements do not measure one cell.
//
// RUNEWIDTH_EASTASIAN=1 makes East-Asian Ambiguous characters — which Block
// Elements are — measure two. Half blocks really are broken there, which is why
// app's imageRenderMode selects the ASCII rung instead; asserting the half-block
// invariant anyway would be asserting a combination nothing renders. Verified by
// running the suite with the variable set: without this guard these are the only
// failures in the tree.
func requireBlocksMeasureOne(t *testing.T) {
	t.Helper()
	if ansi.PrintableRuneWidth("▀") != 1 {
		t.Skip("Block Elements measure two cells here (RUNEWIDTH_EASTASIAN); " +
			"production uses the ASCII rung in this environment — see app.imageRenderMode")
	}
}

var (
	red   = color.RGBA{R: 255, A: 255}
	green = color.RGBA{G: 255, A: 255}
	blue  = color.RGBA{B: 255, A: 255}
	white = color.RGBA{R: 255, G: 255, B: 255, A: 255}
)

// A 2×2 image in a 2×1 cell box is the exact case half blocks exist for: one
// cell per column, the top pixel as foreground and the bottom as background.
func TestRender_HalfBlockPairsTopAndBottom(t *testing.T) {
	requireBlocksMeasureOne(t)
	img := solid(2, 2, func(x, y int) color.RGBA {
		switch {
		case x == 0 && y == 0:
			return red
		case x == 1 && y == 0:
			return green
		case x == 0 && y == 1:
			return blue
		default:
			return white
		}
	})

	got := Render(img, 2, 1, HalfBlock)

	require.Equal(t, 1, len(strings.Split(got, "\n")), "one cell row")
	assert.Equal(t,
		"\x1b[38;2;255;0;0m\x1b[48;2;0;0;255m▀"+
			"\x1b[38;2;0;255;0m\x1b[48;2;255;255;255m▀"+
			"\x1b[0m",
		got)
}

// The renderer never emits a run of SGR it has already established: adjacent
// cells of the same colour pair carry the glyph alone. This is what keeps a
// full-window image out of the per-frame cost budget, so it is pinned rather
// than left to observation.
func TestRender_RunLengthSkipsRepeatedColours(t *testing.T) {
	requireBlocksMeasureOne(t)
	img := solid(4, 2, func(x, y int) color.RGBA {
		if y == 0 {
			return red
		}
		return blue
	})

	got := Render(img, 4, 1, HalfBlock)

	assert.Equal(t, "\x1b[38;2;255;0;0m\x1b[48;2;0;0;255m▀▀▀▀\x1b[0m", got)
}

// Every row must re-state its colours even when they match the previous row's
// last cell: PlaceOverlay splices a background line in front of each row with
// ansi.Truncate, which carries that line's live SGR state into the cut, so a row
// that opens with an inherited attribute bleeds into its first pixel.
func TestRender_EachRowRestatesItsColours(t *testing.T) {
	requireBlocksMeasureOne(t)
	img := solid(1, 4, func(x, y int) color.RGBA { return red })

	got := Render(img, 1, 2, HalfBlock)

	lines := strings.Split(got, "\n")
	require.Len(t, lines, 2)
	for i, l := range lines {
		assert.True(t, strings.HasPrefix(l, "\x1b[38;2;255;0;0m\x1b[48;2;255;0;0m"),
			"row %d must open with a full colour pair, got %q", i, l)
		assert.True(t, strings.HasSuffix(l, "\x1b[0m"),
			"row %d must end reset, got %q", i, l)
	}
}

// An odd number of sample rows leaves the final cell with no bottom pixel. It
// takes the terminal's default background (SGR 49) rather than a guessed colour,
// so the overlay's own background shows through the half-filled row.
func TestRender_OddSampleRowsLeaveDefaultBackground(t *testing.T) {
	requireBlocksMeasureOne(t)
	img := solid(2, 1, func(x, y int) color.RGBA { return red })

	got := Render(img, 2, 4, HalfBlock)

	require.Equal(t, 1, len(strings.Split(got, "\n")))
	assert.Contains(t, got, "\x1b[49m")
	assert.NotContains(t, got, "48;2;")
}

// ASCII maps luminance onto the ramp and keeps the colour. The darkest sample
// takes the first rung and the brightest the last.
func TestRender_ASCIIUsesLuminanceRamp(t *testing.T) {
	img := solid(2, 1, func(x, y int) color.RGBA {
		if x == 0 {
			return color.RGBA{A: 255} // black
		}
		return white
	})

	got := Render(img, 2, 1, ASCII)

	stripped := xansi.Strip(got)
	require.Equal(t, 2, len([]rune(stripped)))
	assert.Equal(t, ramp[0], []rune(stripped)[0])
	assert.Equal(t, ramp[len(ramp)-1], []rune(stripped)[1])
	assert.Contains(t, got, "\x1b[38;2;255;255;255m")
	assert.NotContains(t, got, "48;2;", "the ascii rung paints foreground only")
}

// Every rendered line must occupy exactly the columns it claims, measured by a
// library that is not the one doing the rendering. A cell whose measured width
// differs from its rendered width overflows the pane, wraps, and desyncs
// bubbletea's incremental renderer into accumulating ghost rows — the failure
// theme.SanitizeWidth exists to prevent.
func TestRender_EveryLineMeasuresItsColumns(t *testing.T) {
	img := solid(64, 64, func(x, y int) color.RGBA {
		return color.RGBA{R: uint8(x * 4), G: uint8(y * 4), B: 128, A: 255}
	})

	modes := []Mode{ASCII}
	if ansi.PrintableRuneWidth("▀") == 1 {
		modes = append(modes, HalfBlock)
	}
	for _, mode := range modes {
		for _, box := range [][2]int{{40, 20}, {13, 7}, {1, 1}, {120, 40}} {
			out := Render(img, box[0], box[1], mode)
			require.NotEmpty(t, out)
			lines := strings.Split(out, "\n")
			assert.LessOrEqual(t, len(lines), box[1], "mode %v box %v: too many rows", mode, box)
			want := ansi.PrintableRuneWidth(lines[0])
			assert.LessOrEqual(t, want, box[0], "mode %v box %v: too wide", mode, box)
			for i, l := range lines {
				assert.Equal(t, want, ansi.PrintableRuneWidth(l),
					"mode %v box %v row %d: ragged", mode, box, i)
			}
		}
	}
}

// A wide image in a square box keeps its aspect by using fewer rows, and a tall
// one by using fewer columns. Half blocks give two square samples per cell, so a
// 2:1 image fills the width of a box whose cells are twice as tall as wide.
func TestRender_PreservesAspect(t *testing.T) {
	requireBlocksMeasureOne(t)
	wide := solid(80, 20, func(x, y int) color.RGBA { return red })
	out := Render(wide, 40, 20, HalfBlock)
	lines := strings.Split(out, "\n")
	assert.Equal(t, 40, ansi.PrintableRuneWidth(lines[0]), "fills the width")
	assert.Equal(t, 5, len(lines), "80x20 at 40 cols is 10 sample rows = 5 cells")

	tall := solid(20, 80, func(x, y int) color.RGBA { return red })
	out = Render(tall, 40, 20, HalfBlock)
	lines = strings.Split(out, "\n")
	assert.Equal(t, 20, len(lines), "fills the height")
	assert.Equal(t, 10, ansi.PrintableRuneWidth(lines[0]), "20x80 at 40 sample rows is 10 cols")
}

// A degenerate box has no rendering, and saying so with an empty string lets the
// overlay fall through to its own empty state instead of rendering a stray row.
func TestRender_DegenerateBoxRendersNothing(t *testing.T) {
	img := solid(4, 4, func(x, y int) color.RGBA { return red })
	assert.Equal(t, "", Render(img, 0, 5, HalfBlock))
	assert.Equal(t, "", Render(img, 5, 0, HalfBlock))
	assert.Equal(t, "", Render(nil, 5, 5, HalfBlock))
}

// Downscale bounds what the overlay pins in memory and what a resize has to
// re-walk. It preserves aspect, and it never enlarges — an image already inside
// the box is not worth interpolating up.
func TestDownscale(t *testing.T) {
	big := solid(2000, 1000, func(x, y int) color.RGBA { return red })
	got := Downscale(big, 1024, 1024)
	require.NotNil(t, got)
	assert.Equal(t, 1024, got.Bounds().Dx())
	assert.Equal(t, 512, got.Bounds().Dy(), "aspect preserved")

	tall := solid(100, 4000, func(x, y int) color.RGBA { return red })
	got = Downscale(tall, 1024, 1024)
	assert.Equal(t, 1024, got.Bounds().Dy())
	assert.Equal(t, 25, got.Bounds().Dx())

	small := solid(8, 6, func(x, y int) color.RGBA { return red })
	got = Downscale(small, 1024, 1024)
	assert.Equal(t, 8, got.Bounds().Dx(), "never enlarged")
	assert.Equal(t, 6, got.Bounds().Dy())
	assert.Equal(t, color.RGBA{R: 255, A: 255}, got.RGBAAt(3, 3), "opaque, colour preserved")

	assert.Nil(t, Downscale(nil, 10, 10))
	assert.Nil(t, Downscale(small, 0, 10))
}

// The box filter averages every source pixel covering a sample, so downscaling a
// checkerboard yields its mean rather than whichever pixel a nearest-neighbour
// sampler happened to land on.
func TestRender_BoxFilterAveragesSources(t *testing.T) {
	img := solid(2, 2, func(x, y int) color.RGBA {
		if (x+y)%2 == 0 {
			return color.RGBA{R: 255, G: 255, B: 255, A: 255}
		}
		return color.RGBA{A: 255}
	})

	// One cell, one sample: the whole 2×2 collapses to the mean of two white and
	// two black pixels.
	got := Render(img, 1, 1, ASCII)
	assert.Contains(t, got, "\x1b[38;2;127;127;127m")
}
