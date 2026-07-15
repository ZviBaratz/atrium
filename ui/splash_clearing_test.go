package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// clearingRowSpan reports the widest blanked span on a row, or ok=false if the
// row is untouched by either clearing.
func clearingRowSpan(c splashClearing, width, row int) (lo, hi int, ok bool) {
	cx := float64(width-1) / 2
	lo, hi = width, -1
	for col := 0; col < width; col++ {
		if c.blanks(float64(col)-cx, row) {
			if col < lo {
				lo = col
			}
			if col > hi {
				hi = col
			}
		}
	}
	return lo, hi, hi >= lo
}

// sceneClearing rebuilds the clearing splashScene would construct for a variant
// at a given size, alongside the rows its art actually occupies.
func sceneClearing(v splashVariant, width, height int, message string) (c splashClearing, wordTop, wordBot, msgTop, msgBot int) {
	pad, clears := v.textPad()
	word := trimBlankLines(FallbackBanner())
	wordW, wordH := lipgloss.Width(word), lipgloss.Height(word)
	cy := (height - 1) / 2
	wordY := max(0, cy-wordH/2)
	c = splashClearing{wordCenterRow: wordY + wordH/2}
	if clears {
		c.wordHalfW = wordW/2 + pad.wordX
		c.wordHalfH = wordH/2 + pad.wordY
	}
	msgTop, msgBot = -1, -2 // an empty range when there is no message
	if message != "" {
		msgW, msgH := lipgloss.Width(message), lipgloss.Height(message)
		msgY := wordY + wordH + 2
		if clears {
			c.msgHalfW = msgW/2 + pad.msgX
			c.msgHalfH = msgH/2 + pad.msgY
			c.msgCenterRow = msgY + msgH/2
		}
		msgTop, msgBot = msgY, msgY+msgH-1
	}
	return c, wordY, wordY + wordH - 1, msgTop, msgBot
}

// TestOverlayIsOpaque is the fact the clearing policy rests on, and it is not
// obvious from the clearing's name: the text does not need the field cleared out
// from under it. overlayAt writes each overlaid line's cells wholesale — spaces
// included — so the text always covers its own footprint. That is what makes
// dropping the clearing safe for structured variants; if this ever became a
// fading or transparent composite, the field would start showing through the
// message's spaces and the policy would need revisiting.
func TestOverlayIsOpaque(t *testing.T) {
	bg := strings.Join([]string{
		strings.Repeat("#", 20),
		strings.Repeat("#", 20),
	}, "\n")
	// A foreground whose interior is a space: if overlays were transparent, the
	// background's # would survive in the middle.
	got := ansi.Strip(overlayAt(bg, "A B", 5, 0))
	first := strings.Split(got, "\n")[0]
	require.Equal(t, "#####A B############", first,
		"overlayAt must write the overlaid line's spaces over the background, not through it")
}

// TestBannerIsSolid pins the other half of that fact. The banner fills with ░
// rather than spaces, so it is opaque across its whole box on every row —
// there are no letter gaps for a field to show through even in principle. If a
// future banner introduced spaces, a structured variant would start rendering
// its field inside the wordmark's counters, and the no-clearing policy in
// splashVariant.textPad would need to be revisited.
func TestBannerIsSolid(t *testing.T) {
	banner := ansi.Strip(trimBlankLines(FallbackBanner()))
	for i, line := range strings.Split(banner, "\n") {
		require.NotContainsf(t, line, " ",
			"banner row %d contains a space; the wordmark is assumed solid "+
				"(see splashVariant.textPad)", i)
	}
}

// TestStructuredClearingLeavesNoUncoveredRow is the guard for the failure the
// rain prototype was built to find.
//
// Where a clearing reaches past the text, the field is blanked and nothing is
// drawn in its place. An organic field hides that completely — it fades into
// its clearing, so a soft void reads as gas parting around the wordmark. Rain
// does not: its streams are long, straight and vertical, so a blanked row that
// no glyph covers renders as a horizontal band cut clean through them, and the
// eye reads a rendering fault rather than occlusion.
//
// Measured against the inherited padding this came to three such bands: one
// below the wordmark, and two around a one-row message whose ellipse spanned
// three rows. Structured variants take no clearing at all — which they can
// afford precisely because of TestOverlayIsOpaque and TestBannerIsSolid.
func TestStructuredClearingLeavesNoUncoveredRow(t *testing.T) {
	const width, height = 96, 30

	for name, v := range splashTestVariants() {
		if !v.structured() {
			continue
		}
		for _, message := range []string{"press n to start a session", ""} {
			c, wordTop, wordBot, msgTop, msgBot := sceneClearing(v, width, height, message)
			var uncovered []int
			for row := 0; row < height; row++ {
				covered := (row >= wordTop && row <= wordBot) || (row >= msgTop && row <= msgBot)
				if _, _, ok := clearingRowSpan(c, width, row); ok && !covered {
					uncovered = append(uncovered, row)
				}
			}
			require.Emptyf(t, uncovered,
				"%s (message=%q): the clearing blanks rows %v that no art covers; a "+
					"structured field renders those as bands cut through it",
				name, message, uncovered)
		}
	}
}

// TestOrganicClearingKeepsItsMargin guards the other direction. The generous
// margin is deliberate for fields that fade into it — tightening it globally
// would make the text look pasted onto the gas rather than emerging from it —
// so this is an opt-out for structured variants, not a new default.
func TestOrganicClearingKeepsItsMargin(t *testing.T) {
	require.False(t, splashDefaultVariant.structured(), "the default field is organic")

	pad, clears := splashDefaultVariant.textPad()
	require.True(t, clears, "organic fields keep their clearing")
	require.Positive(t, pad.wordX, "organic fields keep a margin around the wordmark")
	require.Positive(t, pad.msgY, "organic fields keep a margin around the message")

	_, structuredClears := splashVariantRain.textPad()
	require.False(t, structuredClears, "structured fields take no clearing")
}

// TestOrganicClearingStillBlanksItsText is the regression the opt-out must not
// cause: the organic default must keep clearing the text's own rows. Losing
// that would leave the nebula rendering right through the wordmark.
func TestOrganicClearingStillBlanksItsText(t *testing.T) {
	const width, height = 96, 30
	c, wordTop, wordBot, msgTop, _ := sceneClearing(splashDefaultVariant, width, height, "press n to start a session")
	for _, row := range []int{wordTop, (wordTop + wordBot) / 2, wordBot, msgTop} {
		_, _, ok := clearingRowSpan(c, width, row)
		require.Truef(t, ok, "the organic clearing must still blank the text's row %d", row)
	}
}
