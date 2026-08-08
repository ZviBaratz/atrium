package overlay

import (
	"image"
	"image/color"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/ui/imageview"

	xansi "github.com/charmbracelet/x/ansi"
	"github.com/muesli/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testImage(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.SetRGBA(x, y, color.RGBA{R: uint8(x * 8), G: uint8(y * 8), B: 200, A: 255})
		}
	}
	return img
}

// renderMode is the rung app.imageRenderMode would pick in this environment.
// Where half blocks do not measure one cell, production draws the ASCII ramp —
// and a test that forced half blocks there would assert a combination nothing
// renders. It asks the same predicate production does, rather than measuring
// with one library of its own, which disagrees with production under
// RUNEWIDTH_EASTASIAN=true.
func renderMode() imageview.Mode {
	if !imageview.HalfBlocksMeasureOneCell() {
		return imageview.ASCII
	}
	return imageview.HalfBlock
}

func newSizedImageOverlay(t *testing.T, w, h int) *ImageOverlay {
	t.Helper()
	o := NewImageOverlay(Image{Path: "/tmp/shots/screenshot.png", Pixels: testImage(64, 32), Width: 64, Height: 32}, renderMode())
	o.SetSize(w, h)
	return o
}

// The box must occupy exactly the width it was given, borders included — the
// invariant every overlay here shares and the one lipgloss v2 inverted.
func TestImageOverlay_RendersToItsDeclaredWidth(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {120, 40}, {60, 20}, {31, 12}, {25, 12}, {20, 9}} {
		o := newSizedImageOverlay(t, size[0], size[1])
		out := o.Render()
		for i, l := range strings.Split(out, "\n") {
			assert.Equal(t, size[0], ansi.PrintableRuneWidth(l),
				"size %v row %d: %q", size, i, l)
		}
	}
}

// The box must also not exceed the height it was given, or PlaceOverlay takes
// the overflow off the bottom border.
//
// Two picture shapes, because only one of them can see this. A 2:1 WIDE picture
// is short at a narrow width, so it never fills the row budget and a stray extra
// row still fits inside it — which is why this test passed while the box really
// did overflow. A TALL picture fills the budget exactly, and then one wrapped
// chrome line is one row too many. The wrapped line was the footer: innerWidth
// floors at 1, so below 15 columns the inner width is under the nine cells "esc
// close" needs and lipgloss took two rows for it, after fitLines had already
// clipped. Measured before the fix: 14×14 rendered 15 rows, 10×14 rendered 16.
func TestImageOverlay_FitsItsDeclaredHeight(t *testing.T) {
	sizes := [][2]int{
		{80, 24}, {120, 40}, {100, 12}, {60, 10}, {31, 12}, {25, 12}, {20, 9},
		// Below 15 columns, where the footer no longer fits the content area.
		{16, 16}, {15, 15}, {14, 14}, {13, 13}, {14, 20}, {10, 14}, {12, 6}, {7, 7},
	}
	for _, shape := range []struct {
		name string
		pic  *image.RGBA
	}{
		{"wide", testImage(64, 32)},
		{"tall", testImage(8, 100)},
	} {
		for _, size := range sizes {
			o := NewImageOverlay(Image{
				Path: "/tmp/shots/screenshot.png", Pixels: shape.pic,
				Width: shape.pic.Bounds().Dx(), Height: shape.pic.Bounds().Dy(),
			}, renderMode())
			o.SetSize(size[0], size[1])

			out := o.Render()
			rows := len(strings.Split(out, "\n"))
			assert.LessOrEqual(t, rows, size[1], "%s picture at %v rendered %d rows", shape.name, size, rows)
			for i, l := range strings.Split(out, "\n") {
				assert.Equal(t, size[0], ansi.PrintableRuneWidth(l),
					"%s picture at %v row %d: %q", shape.name, size, i, l)
			}
		}
	}
}

// The title names the file and what it is, because "screenshot.png" alone does
// not tell you whether the thing you are looking at is the whole picture.
func TestImageOverlay_TitleNamesFileAndPixels(t *testing.T) {
	o := newSizedImageOverlay(t, 80, 24)
	out := xansi.Strip(o.Render())
	assert.Contains(t, out, "screenshot.png")
	assert.Contains(t, out, "64×32")
	assert.Contains(t, out, imageFooter)
}

// A filename with no ceiling must be cut to the box rather than wrap it. A wrap
// costs a row the height budget never counted.
func TestImageOverlay_TitleTruncatesToTheBox(t *testing.T) {
	long := "/tmp/" + strings.Repeat("verylongname", 20) + ".png"
	o := NewImageOverlay(Image{Path: long, Pixels: testImage(8, 8), Width: 8, Height: 8}, renderMode())
	o.SetSize(60, 20)

	out := o.Render()
	for i, l := range strings.Split(out, "\n") {
		require.Equal(t, 60, ansi.PrintableRuneWidth(l), "row %d", i)
	}
	assert.Contains(t, xansi.Strip(out), "…")
}

// Render runs on every repaint. Re-walking the pixels each time is this
// feature's whole performance risk, so the picture is computed once per size.
func TestImageOverlay_CachesThePictureBySize(t *testing.T) {
	o := newSizedImageOverlay(t, 80, 24)

	o.Render()
	key := o.cacheKey
	cached := o.cache
	require.True(t, key.valid)
	require.NotEmpty(t, cached)

	// A second render at the same size must not recompute: poison the cache and
	// watch the poison come back.
	o.cache = "POISONED"
	assert.Contains(t, o.Render(), "POISONED", "a same-size repaint recomputed the picture")

	// A resize must recompute, poison or no poison — and the two dimensions are
	// checked one at a time. Changing both at once passes even when the key
	// tracks only one of them, which is exactly how a half-keyed cache ships.
	o.SetSize(100, 24) // width only: same rows, more columns
	assert.NotContains(t, o.Render(), "POISONED", "a width-only resize served a stale picture")
	assert.NotEqual(t, key, o.cacheKey)

	o.cache = "POISONED"
	o.SetSize(100, 30) // height only: same columns, more rows
	assert.NotContains(t, o.Render(), "POISONED", "a height-only resize served a stale picture")

	// A mode change is part of the key too.
	o.mode = imageview.ASCII
	o.cache = "POISONED"
	assert.NotContains(t, o.Render(), "POISONED", "a mode change served a stale picture")
}

// The picture is bounded regardless of terminal size, because every cell of it
// is measured again on every composited frame.
func TestImageOverlay_BoundsThePicture(t *testing.T) {
	o := NewImageOverlay(Image{Path: "/tmp/x.png", Pixels: testImage(4000, 4000), Width: 4000, Height: 4000}, renderMode())
	o.SetSize(400, 200)

	cols, rows := o.pictureSize()
	assert.Equal(t, imageMaxCols, cols)
	assert.Equal(t, imageMaxRows, rows)
}

// The picture block carries its own colours all the way out. A box background
// wrapped around it would fight the resets each image row ends on.
func TestImageOverlay_KeepsThePictureColours(t *testing.T) {
	if renderMode() != imageview.HalfBlock {
		t.Skip("only the half-block rung paints a background; see renderMode")
	}
	o := newSizedImageOverlay(t, 80, 24)
	out := o.Render()
	assert.Contains(t, out, "\x1b[48;2;", "half blocks must keep their background colour")
	assert.Contains(t, out, "\x1b[38;2;")
}

// A load that produced nothing must say so rather than render an empty frame
// whose rows the height budget never counted.
func TestImageOverlay_NilImageExplainsItself(t *testing.T) {
	o := NewImageOverlay(Image{Path: "/tmp/x.png"}, renderMode())
	o.SetSize(80, 24)

	out := o.Render()
	assert.Contains(t, xansi.Strip(out), imageEmptyNotice)
	for i, l := range strings.Split(out, "\n") {
		assert.Equal(t, 80, ansi.PrintableRuneWidth(l), "row %d", i)
	}
	assert.LessOrEqual(t, len(strings.Split(out, "\n")), 24)

	// The notice is fifteen cells, so it outgrows the content area before the
	// footer does — a box under 21 columns wraps it, and a wrapped line is a row
	// the height budget never counted.
	for _, size := range [][2]int{{22, 12}, {20, 12}, {15, 12}, {12, 12}, {20, 8}, {14, 7}} {
		o.SetSize(size[0], size[1])
		out := o.Render()
		lines := strings.Split(out, "\n")
		assert.LessOrEqual(t, len(lines), size[1], "size %v rendered %d rows", size, len(lines))
		for i, l := range lines {
			assert.Equal(t, size[0], ansi.PrintableRuneWidth(l), "size %v row %d: %q", size, i, l)
		}
	}
}

// An unsized overlay — rendered before the first WindowSizeMsg — must not panic
// or produce a negative-width box.
func TestImageOverlay_UnsizedDoesNotPanic(t *testing.T) {
	o := NewImageOverlay(Image{Path: "/tmp/x.png", Pixels: testImage(8, 8), Width: 8, Height: 8}, renderMode())
	assert.NotPanics(t, func() { _ = o.Render() })
}

// The title names the FILE's size, not the intermediate's.
//
// Found by looking at the running app: a 2048×1024 screenshot was captioned
// "1024×512" — the bounded intermediate's size — so the one line whose whole job
// is to say what the picture is was wrong about it. Pinned here because nothing
// else can see it: every other assertion passes either way.
func TestImageOverlay_TitleNamesTheFileNotTheIntermediate(t *testing.T) {
	o := NewImageOverlay(Image{
		Path:   "/tmp/shot.png",
		Pixels: testImage(1024, 512), // what the loader shrank it to
		Width:  2048, Height: 1024,   // what the file actually is
	}, renderMode())
	o.SetSize(80, 24)

	out := xansi.Strip(o.Render())
	assert.Contains(t, out, "2048×1024")
	assert.NotContains(t, out, "1024×512")
}
