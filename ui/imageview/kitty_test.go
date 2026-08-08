package imageview

import (
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/ansi/kitty"
	"github.com/mattn/go-runewidth"
	"github.com/muesli/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// requirePlaceholdersMeasureOne skips a placement assertion in an environment
// where a placeholder cell does not measure one cell, for the same reason
// requireBlocksMeasureOne exists: production picks a different rung there, and
// asserting this one anyway asserts a combination nothing renders.
func requirePlaceholdersMeasureOne(t *testing.T) {
	t.Helper()
	if !PlaceholdersMeasureOneCell() {
		t.Skip("placeholders do not measure one cell here; production falls back " +
			"to a glyph rung — see PlaceholdersMeasureOneCell")
	}
}

// placeholderProbeVar marks the re-executed child of the width probe below.
const placeholderProbeVar = "ATRIUM_TEST_PLACEHOLDER_PROBE"

// This is #398's spike question Q1, and it is the one that could have killed the
// rung: a placeholder cell that measures two while rendering one is the
// accumulating ghost-row desync theme.SanitizeWidth documents, across the whole
// overlay rather than at one glyph.
//
// The measured answer is that U+10EEEE is East-Asian AMBIGUOUS — which is the
// half of Q1 that could not be settled by reading, since the trie's
// classification of plane-16 PUA is only observable when EastAsianWidth is on.
// So it produces the SAME five-row table as the Block Elements, including the
// two ordinary environments where the two libraries disagree with each other.
// One rule covers both rungs, and it is "measure", not "read the variable".
//
// Subprocess, because both libraries decide at package init: t.Setenv cannot
// reach a value read before the test started, and the go test cache cannot see
// the variable at all, so an in-process version replays a cached green.
func TestPlaceholdersMeasureOneCell_AcrossWidthEnvironments(t *testing.T) {
	if os.Getenv(placeholderProbeVar) == "1" {
		fmt.Println(PlaceholdersMeasureOneCell())
		return
	}
	cases := []struct {
		name string
		env  []string
		want bool
	}{
		{"unset, non-CJK locale", []string{"RUNEWIDTH_EASTASIAN=", "LC_ALL=C", "LC_CTYPE=C", "LANG=C"}, true},
		{"set to 1: both libraries measure two", []string{"RUNEWIDTH_EASTASIAN=1", "LC_ALL=C", "LC_CTYPE=C", "LANG=C"}, false},
		{"set to 0", []string{"RUNEWIDTH_EASTASIAN=0", "LC_ALL=C", "LC_CTYPE=C", "LANG=C"}, true},
		// go-runewidth falls back to the locale when the variable is empty;
		// x/ansi never consults it.
		{"unset, CJK locale", []string{"RUNEWIDTH_EASTASIAN=", "LC_ALL=", "LC_CTYPE=", "LANG=ja_JP.UTF-8"}, false},
		// x/ansi parses with strconv.ParseBool, which accepts "true";
		// go-runewidth accepts only "1".
		{"set to true", []string{"RUNEWIDTH_EASTASIAN=true", "LC_ALL=C", "LC_CTYPE=C", "LANG=C"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.CommandContext(t.Context(), os.Args[0],
				"-test.run=^TestPlaceholdersMeasureOneCell_AcrossWidthEnvironments$")
			cmd.Env = append(append(os.Environ(), placeholderProbeVar+"=1"), tc.env...)

			out, err := cmd.CombinedOutput()
			require.NoError(t, err, "probe failed: %s", out)
			answer, _, _ := strings.Cut(string(out), "\n")
			assert.Equal(t, strconv.FormatBool(tc.want), answer, "env %v produced %q", tc.env, out)
		})
	}
}

// maxPlaceholderIndex is a claim about somebody else's table, so it is measured
// rather than counted by eye or inferred from the overlay's ≤120×40 cap.
//
// kitty.Diacritic CLAMPS: an out-of-range index returns diacritics[0] instead of
// erroring, so exceeding the table places the whole row at row/column 0 with
// nothing to notice it. This pins where that edge is, in the only terms the
// package exposes — the first index that aliases back to the first entry.
func TestDiacriticTableBound(t *testing.T) {
	first := kitty.Diacritic(0)

	assert.NotEqual(t, first, kitty.Diacritic(maxPlaceholderIndex),
		"index %d must still be a distinct diacritic; the table shrank", maxPlaceholderIndex)
	assert.Equal(t, first, kitty.Diacritic(maxPlaceholderIndex+1),
		"index %d must clamp; the table grew and maxPlaceholderIndex is now too small",
		maxPlaceholderIndex+1)
	assert.Equal(t, first, kitty.Diacritic(-1), "a negative index clamps too")
}

// A cell is the placeholder character, its row diacritic, its column diacritic,
// and a foreground carrying the image ID. This asserts all four byte for byte,
// because that function is pure and is where every protocol mistake lives.
func TestPlaceholders_CellsCarryRowColumnAndID(t *testing.T) {
	got, err := Placeholders(0xAABBCC, 3, 2)
	require.NoError(t, err)

	base := string(kitty.Placeholder)
	d := kitty.Diacritic
	want := "\x1b[38;2;170;187;204m" +
		base + string(d(0)) + string(d(0)) +
		base + string(d(0)) + string(d(1)) +
		base + string(d(0)) + string(d(2)) + "\x1b[0m\n" +
		"\x1b[38;2;170;187;204m" +
		base + string(d(1)) + string(d(0)) +
		base + string(d(1)) + string(d(1)) +
		base + string(d(1)) + string(d(2)) + "\x1b[0m"

	assert.Equal(t, want, got)
}

// The ID rides the foreground, so the three bytes must land in R, G, B in that
// order. A fixture whose bytes are all equal would pass however they were
// permuted, so every byte here is distinct and none is zero.
func TestPlaceholders_ForegroundIsTheImageID(t *testing.T) {
	got, err := Placeholders(0x123456, 1, 1)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(got, "\x1b[38;2;18;52;86m"), "got %q", got)
}

// An ID above 24 bits grows a third diacritic, and one that fits must NOT — a
// "byte 0" diacritic is a different cluster to a terminal comparing them, not a
// harmless zero.
func TestPlaceholders_ThirdDiacriticOnlyAboveTwentyFourBits(t *testing.T) {
	base := string(kitty.Placeholder)

	high, err := Placeholders(0x0100BBCC, 1, 1)
	require.NoError(t, err)
	assert.Equal(t, "\x1b[38;2;0;187;204m"+base+
		string(kitty.Diacritic(0))+string(kitty.Diacritic(0))+string(kitty.Diacritic(1))+
		"\x1b[0m", high, "the high byte must ride a third diacritic")

	// The negative control: the largest ID that still fits the foreground.
	low, err := Placeholders(0xFFFFFF, 1, 1)
	require.NoError(t, err)
	assert.Equal(t, "\x1b[38;2;255;255;255m"+base+
		string(kitty.Diacritic(0))+string(kitty.Diacritic(0))+
		"\x1b[0m", low, "an ID that fits must not grow a third diacritic")
}

func TestPlaceholders_Refusals(t *testing.T) {
	for _, tc := range []struct {
		name             string
		id               uint32
		cols, rows, want int
	}{
		{name: "zero id", id: 0, cols: 2, rows: 2},
		{name: "zero cols", id: 1, cols: 0, rows: 2},
		{name: "zero rows", id: 1, cols: 2, rows: 0},
		{name: "cols past the table", id: 1, cols: maxPlaceholderIndex + 2, rows: 1},
		{name: "rows past the table", id: 1, cols: 1, rows: maxPlaceholderIndex + 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Placeholders(tc.id, tc.cols, tc.rows)
			assert.Error(t, err)
		})
	}

	// The boundary is inclusive on the other side, or the refusal is off by one
	// and the largest legal placement is unreachable.
	_, err := Placeholders(1, maxPlaceholderIndex+1, 1)
	assert.NoError(t, err, "the last addressable column must be allowed")
}

// The width invariant: a block of N columns must measure N columns to both
// libraries that lay out and composite an Atrium frame. A row that measures
// wider than it renders is the ghost-row desync, and here it would be every row
// of the overlay rather than one glyph.
//
// This is not a tautology on either measurer — Placeholders builds its string
// with Fprintf and consults neither — and it is not implied by
// PlaceholdersMeasureOneCell: that predicate says one cell is one column, and
// this says the diacritics stay attached and the SGR stays uncounted when the
// cells are strung together.
//
// The measurer that is NOT usable here is the one the glyph rungs' width guard
// uses; see TestPlaceholders_PerRuneMeasurersOvercount for why.
func TestPlaceholders_EveryLineMeasuresItsColumns(t *testing.T) {
	requirePlaceholdersMeasureOne(t)

	for _, box := range [][2]int{{1, 1}, {13, 7}, {120, 40}} {
		got, err := Placeholders(0x0100BBCC, box[0], box[1])
		require.NoError(t, err)
		lines := strings.Split(got, "\n")
		require.Len(t, lines, box[1], "box %v: wrong row count", box)
		for i, l := range lines {
			assert.Equal(t, box[0], lipgloss.Width(l),
				"box %v row %d: x/ansi, which measures the overlay composite", box, i)
			assert.Equal(t, box[0], runewidth.StringWidth(xansi.Strip(l)),
				"box %v row %d: go-runewidth, which measures the list and the rows", box, i)
		}
	}
}

// muesli/ansi.PrintableRuneWidth over-counts a placeholder row by a lot, and
// that is a property of the measurer rather than a defect in the row.
//
// It sums runewidth.RuneWidth PER RUNE with no grapheme clustering, and 120 of
// the 297 diacritics kitty addresses cells with are Hebrew accents (U+0592 and
// up) that go-runewidth's per-rune table gives width 1. Cluster-aware measurers
// — lipgloss/x-ansi and go-runewidth's own StringWidth — and every terminal that
// implements this protocol all agree the marks are nonspacing.
//
// It is pinned because it is a trap, not a curiosity: PrintableRuneWidth is what
// the glyph rungs' width guard and app's TestViewFitsTerminalBoundsEveryState
// measure with, so the first person to route a kitty-rung frame past a bounds
// sweep will see a failure whose obvious reading — "the row is too wide" — is
// wrong, and whose obvious fix is to change production.
func TestPlaceholders_PerRuneMeasurersOvercount(t *testing.T) {
	requirePlaceholdersMeasureOne(t)

	// Column 0 uses diacritic 0, a combining overline every table agrees is
	// zero-width, so a narrow row cannot show the divergence at all.
	narrow, err := Placeholders(0xAABBCC, 1, 1)
	require.NoError(t, err)
	assert.Equal(t, 1, ansi.PrintableRuneWidth(narrow),
		"the divergence needs a column whose diacritic is one of the accents")

	wide, err := Placeholders(0xAABBCC, 120, 1)
	require.NoError(t, err)
	assert.Equal(t, 120, lipgloss.Width(wide), "the cluster-aware answer")
	assert.Greater(t, ansi.PrintableRuneWidth(wide), 120,
		"if this ever equals 120, go-runewidth's per-rune table learned the accents "+
			"and this test can be deleted along with the warning it carries")
}

// Every row restates the foreground and ends with a reset, for the reason the
// glyph rungs do: PlaceOverlay composites with ansi.Truncate, which keeps
// collecting escapes past the cut, so a row that opened on an inherited colour
// would carry the wrong image ID — and here the colour is not decoration, it is
// the address of the image.
func TestPlaceholders_EveryRowRestatesTheIDAndResets(t *testing.T) {
	got, err := Placeholders(0xAABBCC, 4, 3)
	require.NoError(t, err)
	for i, l := range strings.Split(got, "\n") {
		assert.True(t, strings.HasPrefix(l, "\x1b[38;2;170;187;204m"), "row %d: %q", i, l)
		assert.True(t, strings.HasSuffix(l, "\x1b[0m"), "row %d: %q", i, l)
	}
}

// noisyImage is incompressible enough that its PNG cannot collapse below the
// chunk threshold, which is what makes the chunking assertion below real.
func noisyImage(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	seed := uint32(0x9E3779B9)
	for y := range h {
		for x := range w {
			seed = seed*1664525 + 1013904223
			img.SetRGBA(x, y, color.RGBA{
				R: uint8(seed >> 24), G: uint8(seed >> 16), B: uint8(seed >> 8), A: 0xff,
			})
		}
	}
	return img
}

// Two option keys decide whether this transmits a PNG or eight megabytes of raw
// pixels, and neither is visible in the option NAMES.
//
// f=100 must be present: EncodeGraphics defaults a zero Format to RGBA. o=z must
// be ABSENT: the encoder wraps the writer in zlib before the format switch, so
// PNG plus Zlib is deflate over deflate. Asserting the payload's magic bytes is
// what makes the first half more than a claim about a key.
func TestTransmitPNG_SendsAPNGAndNotRawPixels(t *testing.T) {
	got, err := TransmitPNG(noisyImage(8, 8), 7)
	require.NoError(t, err)

	require.True(t, strings.HasPrefix(got, "\x1b_G"), "APC-introduced: %q", got)
	require.True(t, strings.HasSuffix(got, "\x1b\\"), "ST-terminated: %q", got)

	opts, payload, ok := strings.Cut(strings.TrimSuffix(strings.TrimPrefix(got, "\x1b_G"), "\x1b\\"), ";")
	require.True(t, ok, "a transmission carries a payload")

	keys := strings.Split(opts, ",")
	assert.Contains(t, keys, "f=100", "a zero Format transmits raw RGBA")
	assert.Contains(t, keys, "I=7", "the number is how the reply is matched")
	assert.NotContains(t, keys, "o=z", "PNG is already deflated; zlib over it is pure cost")
	for _, k := range keys {
		assert.NotEqual(t, "q=1", k, "the reply carries the image ID; it must not be suppressed")
		assert.NotEqual(t, "q=2", k, "the reply carries the image ID; it must not be suppressed")
	}

	raw, err := base64.StdEncoding.DecodeString(payload)
	require.NoError(t, err, "the payload is base64")
	assert.Equal(t, []byte{0x89, 'P', 'N', 'G'}, raw[:4], "the payload is a PNG, not pixels")
}

// A payload over the 4 KB chunk size must arrive as several escapes, the first
// carrying the options and the last carrying m=0 — otherwise a large screenshot
// is one oversized write the terminal may not accept.
func TestTransmitPNG_ChunksALargePayload(t *testing.T) {
	got, err := TransmitPNG(noisyImage(256, 256), 3)
	require.NoError(t, err)

	n := strings.Count(got, "\x1b_G")
	require.Greater(t, n, 1, "a %d-byte transmission must chunk", len(got))
	assert.Contains(t, got, "m=1", "every chunk but the last says more is coming")
	assert.Contains(t, got, "m=0", "the last chunk terminates the transmission")
	// Only the first chunk carries the full options; the rest would be rejected
	// as a second transmission if they repeated them.
	assert.Equal(t, 1, strings.Count(got, "I=3"), "the number rides the first chunk only")
}

func TestPlaceVirtual(t *testing.T) {
	got := PlaceVirtual(42, 1, 10, 5)
	assert.Equal(t, "\x1b_Gq=1,i=42,p=1,U=1,c=10,r=5,a=p\x1b\\", got)
	// U=1 is the whole difference between a virtual placement the placeholder
	// cells refer to and one drawn at the cursor.
	assert.Contains(t, got, "U=1")
	assert.NotContains(t, got, ";", "a placement carries no payload")
}

// d=I frees the pixels; d=i would drop the placements and leak them for the life
// of the terminal. DeleteResources is the only thing that tells the two apart.
func TestDeleteImage_FreesTheData(t *testing.T) {
	got := DeleteImage(42)
	assert.Equal(t, "\x1b_Gq=1,i=42,d=I,a=d\x1b\\", got)
	assert.NotContains(t, got, "d=i", "the lowercase form keeps the image data")
}

// The terminal preserves aspect within whatever rectangle it is given, so this
// only decides how many overlay rows are spent — but spending all of them on a
// wide image would letterbox inside a box that could have been shorter.
func TestFitCells(t *testing.T) {
	cols, rows := FitCells(80, 20, 40, 20)
	assert.Equal(t, 40, cols)
	assert.Equal(t, 5, rows, "a 4:1 image over cells twice as tall as wide")

	cols, rows = FitCells(20, 80, 40, 20)
	assert.Equal(t, 20, rows, "a tall image is bounded by rows")
	assert.Equal(t, 10, cols)

	cols, rows = FitCells(10, 10, 0, 0)
	assert.Equal(t, 1, cols, "a degenerate box still yields a placeable cell")
	assert.Equal(t, 1, rows)
}
