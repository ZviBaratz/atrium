package ui

// The empty-state splash: a slow-drifting field that appears to emanate from
// the ATRIUM wordmark and fades out at the pane's edges. The field is sampled
// per character cell from one of several generators (see splash.Variant),
// modulated by a radial envelope, colored by a theme-anchored gradient, and
// composited *behind* the existing wordmark+message block (which is left
// untouched, so its styling survives). Only the idle "no agents" screen uses
// it; every other empty state keeps the plain FallbackBanner.
//
// This file owns the scene composition (compositing the wordmark and message
// over the field) and the opaque overlay compositor. The field engine — the
// field math, the gradient LUT, the emitter, and the variant vocabulary — lives
// in the splash package, driven through splash.Render; variant selection and the
// ATRIUM_SPLASH_* env overrides live in splash_variants.go.

import (
	"strings"

	"github.com/ZviBaratz/atrium/splash"
	"github.com/ZviBaratz/atrium/ui/theme"

	"github.com/charmbracelet/lipgloss"
	xansi "github.com/charmbracelet/x/ansi"
)

const (
	// minSplashW/minSplashH gate the effect: below this the pane is too small
	// for the field to read, so String() falls back to the plain placeholder. The
	// width floor sits just above the 48-col wordmark; the height floor leaves
	// room for a few ring rows above and below the ~10-row text block.
	//
	// That 48-col relationship is why the fallback is the *placeholder* and not
	// simply the wordmark: this floor is only two columns above the art, so just
	// below it the wordmark still fits and still renders — but keep narrowing (or
	// shortening) and fallbackBlock drops it and shows the message alone.
	minSplashW = 50
	minSplashH = 18
)

// splashFits reports whether a pane is large enough to render the splash field
// legibly. Callers fall back to the plain centered placeholder when it is not —
// which keeps the wordmark only where it fits (see fallbackBlock), not always.
func splashFits(w, h int) bool { return w >= minSplashW && h >= minSplashH }

// SplashFits is splashFits for callers outside ui — the screensaver's entry
// and stay-alive gate in app.
func SplashFits(w, h int) bool { return splashFits(w, h) }

// SplashScreensaver renders the full-window splash easter egg: the same
// animated scene as the idle empty state, wordmark centered, but without the
// guidance message line. Callers gate on SplashFits and own the frame ticks.
func SplashScreensaver(width, height, frame int) string {
	return splashScene(width, height, frame, "")
}

// splashScene composites the idle empty screen: the animated field with the
// wordmark centered on top and the message tucked just below it. The wordmark and
// message are overlaid separately at their own widths (not one padded block) so
// each stays centered on its own width rather than on the wider of the two. The
// overlay is opaque, so nothing bleeds through the text and no clearing under it
// is needed (see overlayAt and TestOverlayIsOpaque; V5 retired the clearing the
// organic fields once wore). The outer clamp honors the pane box (#251). Shared by
// the preview and terminal panes so their idle empty states match. Callers gate on
// splashFits.
//
// The message line is optional: an empty message renders the wordmark alone over
// an uninterrupted field, which is the screensaver (see SplashScreensaver). Both
// panes always pass one.
func splashScene(width, height, frame int, message string) string {
	variant := splashActiveVariant()

	word := trimBlankLines(FallbackBanner())
	wordW, wordH := lipgloss.Width(word), lipgloss.Height(word)

	const gap = 2 // blank rows between the wordmark and the message
	cy := (height - 1) / 2
	wordX := (width - wordW) / 2
	wordY := max(0, cy-wordH/2) // wordmark centered on the pane

	// The wordmark's centre row is the field's focal row: the origin its
	// focal-relative coordinates are measured from, so the pattern emanates from
	// the wordmark, and the anchor for the focal-point-to-corner radius a
	// size-relative variant scales itself against (see splash.Render).
	focalRow := wordY + wordH/2

	var msg string
	var msgX, msgY int
	if message != "" {
		msg = theme.Current().FgStyle().Render(message)
		msgX = (width - lipgloss.Width(msg)) / 2
		msgY = wordY + wordH + gap
	}

	// splashLumRange resolves the dev-only ATRIUM_SPLASH_LUMRANGE override here,
	// in ui, and passes it in — the splash engine reads no environment itself.
	var lum *float64
	if r, ok := splashLumRangeOverride(); ok {
		lum = &r
	}
	field := splash.Render(width, height, frame, splash.Options{
		Palette:  splashPalette(theme.Current().Palette),
		Variant:  variant,
		FocalRow: focalRow,
		LumRange: lum,
	})
	scene := overlayAt(field, word, wordX, wordY)
	if message != "" {
		scene = overlayAt(scene, msg, msgX, msgY)
	}
	return lipgloss.NewStyle().MaxWidth(width).MaxHeight(height).Render(scene)
}

// splashPalette maps the active theme's five splash tokens onto splash.Palette:
// the warm→cool anchors (Danger→Purple→Accent→Cyan) and the highlight (Fg). It
// is the whole of Atrium's coupling to the splash engine.
func splashPalette(pal theme.Palette) splash.Palette {
	return splash.Palette{
		A0:        string(pal.Danger),
		A1:        string(pal.Purple),
		A2:        string(pal.Accent),
		A3:        string(pal.Cyan),
		Highlight: string(pal.Fg),
	}
}

// overlayAt composites fg over bg (the splash field) at cell (placeX, placeY),
// splicing width-correctly around bg's ANSI escapes. Adapted from
// overlay.PlaceOverlay (ui/overlay/overlay.go) but deliberately WITHOUT its
// background fade — the gradient must show through, not be dimmed — and without
// the whitespace-option plumbing (plain spaces fill any gap). The overlay is
// opaque: every fg cell replaces the field cell under it, so nothing colored
// bleeds through the text and no clearing beneath it is needed (see
// TestOverlayIsOpaque).
func overlayAt(bg, fg string, placeX, placeY int) string {
	fgLines, fgWidth := splashLines(fg)
	bgLines, bgWidth := splashLines(bg)
	fgHeight, bgHeight := len(fgLines), len(bgLines)

	if fgWidth >= bgWidth && fgHeight >= bgHeight {
		return fg
	}

	placeX = clampInt(placeX, 0, max(0, bgWidth-fgWidth))
	placeY = clampInt(placeY, 0, max(0, bgHeight-fgHeight))

	var b strings.Builder
	for i, bgLine := range bgLines {
		if i > 0 {
			b.WriteByte('\n')
		}
		if i < placeY || i >= placeY+fgHeight {
			b.WriteString(bgLine)
			continue
		}

		pos := 0
		if placeX > 0 {
			left := xansi.Truncate(bgLine, placeX, "")
			pos = xansi.StringWidth(left)
			b.WriteString(left)
			if pos < placeX {
				b.WriteString(strings.Repeat(" ", placeX-pos))
				pos = placeX
			}
		}

		fgLine := fgLines[i-placeY]
		b.WriteString(fgLine)
		pos += xansi.StringWidth(fgLine)

		right := xansi.TruncateLeft(bgLine, pos, "")
		bgLineWidth := xansi.StringWidth(bgLine)
		rightWidth := xansi.StringWidth(right)
		if rightWidth <= bgLineWidth-pos {
			b.WriteString(strings.Repeat(" ", bgLineWidth-rightWidth-pos))
		}
		b.WriteString(right)
	}
	return b.String()
}

// splashLines splits s into lines and returns the widest visible (ANSI-aware)
// line width. Mirrors overlay.getLines.
func splashLines(s string) (lines []string, widest int) {
	lines = strings.Split(s, "\n")
	for _, l := range lines {
		if wdt := xansi.StringWidth(l); wdt > widest {
			widest = wdt
		}
	}
	return lines, widest
}

// trimBlankLines drops leading/trailing all-whitespace lines (the wordmark art
// is padded with blank rows) so the composited block is exactly its glyph rows —
// keeping the opaque overlay tight to the wordmark so the field flows right up to
// its edges instead of being blanked by padding rows around it.
func trimBlankLines(s string) string {
	lines := strings.Split(s, "\n")
	start, end := 0, len(lines)
	for start < end && strings.TrimSpace(xansi.Strip(lines[start])) == "" {
		start++
	}
	for end > start && strings.TrimSpace(xansi.Strip(lines[end-1])) == "" {
		end--
	}
	return strings.Join(lines[start:end], "\n")
}

// clampInt bounds v to [lo, hi]. Kept here for overlayAt; the splash engine has
// its own copy.
func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
