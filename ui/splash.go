package ui

// The empty-state splash: a slow-drifting field that appears to emanate from
// the ATRIUM wordmark and fades out at the pane's edges. The field is sampled
// per character cell from one of several generators (see fresco.Variant),
// modulated by a radial envelope, colored by a theme-anchored gradient, and
// composited *behind* the existing wordmark+message block (which is left
// untouched, so its styling survives). Only the idle "no agents" screen uses
// it; every other empty state keeps the plain FallbackBanner.
//
// This file owns the scene composition (compositing the wordmark and message
// over the field) and the opaque overlay compositor. The field engine — the
// field math, the gradient LUT, the emitter, and the variant vocabulary — lives
// in the fresco package, driven through fresco.Render; variant selection and the
// ATRIUM_SPLASH_* env overrides live in splash_variants.go.

import (
	"strings"

	"github.com/ZviBaratz/atrium/ui/theme"
	"github.com/ZviBaratz/fresco/v2"

	"charm.land/lipgloss/v2"
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
	// size-relative variant scales itself against (see fresco.Render).
	focalRow := wordY + wordH/2

	var msg string
	var msgX, msgY int
	if message != "" {
		msg = theme.Current().FgStyle().Render(message)
		msgX = (width - lipgloss.Width(msg)) / 2
		msgY = wordY + wordH + gap
	}

	field := fresco.Render(width, height, frame, fresco.Options{
		Palette:  splashPalette(theme.Current().Palette),
		Variant:  variant,
		FocalRow: focalRow,
		LumRange: splashLumRange(variant),
		Profile:  splashProfile(),
	})
	scene := overlayAt(field, word, wordX, wordY)
	if message != "" {
		scene = overlayAt(scene, msg, msgX, msgY)
	}
	return lipgloss.NewStyle().MaxWidth(width).MaxHeight(height).Render(scene)
}

// splashLumRange resolves how the splash field splits brightness between glyph
// density and colour luminance: the dev override if set, else 0 on a light palette
// (except for rain), else nil (fresco's per-variant default).
//
// It also owns the ui-side half of the ATRIUM_SPLASH_LUMRANGE plumbing, because the
// splash engine reads no environment itself — see splashLumRangeOverride.
//
// The light rung is not a taste choice. fresco's luminance ramp walks L* from a
// near-black floor up to the hue (shade.go's splashLumHexAt), and shadeAt's own
// comment says the consequence out loud: "stop 0 is near-black ink on a dark pane
// and never worth emitting." Correct on a dark pane, inverted on a light one, where
// "barely there" would render as the heaviest ink on screen and a field's fading
// edge would come out as a dark halo. Atrium supplies hues, not the ramp, and
// rainRampFloor is not a parameter, so no amount of retuning the five splash tokens
// reaches it. lumRange 0 is fresco's documented endpoint where the ramp is never
// consulted at all and density carries everything, which is the only direction that
// works on both polarities without a fresco change. Filed upstream as
// ZviBaratz/fresco#82 for a real light ramp.
//
// RAIN IS EXEMPT, and it is the one variant that had to be looked at to know so.
// Its brightness is ENTIRELY luminance — fresco ships it at lumRange 1 — so moving
// that brightness onto density gives it nowhere to go and the pane fills solid.
// Measured at 120x40 on tokyo-night-day: 95% of cells inked and an edge:core ratio
// of 83:100, i.e. no vignette at all, against 31% and 16:45 when it is left alone.
// That is the absurdity the ATRIUM_SPLASH_LUMRANGE override's own comment in
// splash_variants.go already predicts for a low override on rain ("the pane fills
// with white katakana"); the light rung would have made it the shipped path rather
// than a dev-only footgun. Leaving rain on the ramp is merely inverted, which is the
// lesser harm and is fresco#82's to fix properly.
//
// The other four all read better at 0 than on the ramp, because a density vignette
// survives the polarity flip and a luminance one does not: ripple's edge:core goes
// 25:43 to 13:33, galaxy's 65:94 to 32:71.
//
// A monochrome render will take the same rung for the mirror-image reason: with
// colour stripped, a colour-borne brightness channel carries nothing, so the field
// would flatten. See theme.Mono (#394 Stage D). Rain's exemption will need
// revisiting there — with no colour at all it has neither channel.
//
// The variant is a parameter rather than another splashActiveVariant() call so the
// rung is a pure function of what the caller is actually about to render. The
// process-wide pick is latched, so a second call would agree today; taking it as an
// argument means the rule cannot drift from the field it describes.
func splashLumRange(variant fresco.Variant) *float64 {
	if r, ok := splashLumRangeOverride(); ok {
		return &r
	}
	if theme.IsLight(theme.Current().Palette) && variant != fresco.Rain {
		zero := 0.0
		return &zero
	}
	return nil
}

// splashProfile is the colour depth the splash field emits at: full fidelity,
// always.
//
// That is not a decision to ignore the terminal — it is where the decision now
// lives. Bubble Tea v2 down-samples everything it renders, at the writer, against
// the profile it detected; Lip Gloss v2 styles emit full-fidelity colour for the
// same reason. The field is just more styled text in the frame, so it should
// arrive in the same state as everything around it and be converted once, by the
// one component that knows what the terminal can show.
//
// Pinning also keeps Render pure over its inputs, which fresco.Auto is not: Auto
// probes stdout, so an identical Options would produce different bytes depending
// on where the process was attached. The frame goldens depend on that purity.
//
// Under v1 this function was a bridge between two colour systems — fresco
// detecting through colorprofile while the rest of Atrium down-sampled through
// Lip Gloss v1's termenv global — and it had to reconcile them by hand. There is
// one system now, so the bridge collapses to a constant, and with it the last use
// of termenv outside tests.
func splashProfile() fresco.ColorProfile { return fresco.TrueColor }

// splashPalette maps the active theme's five splash tokens onto fresco.Palette:
// the warm→cool anchors (Danger→Purple→Accent→Cyan) and the highlight (Fg). It
// is the whole of Atrium's coupling to the splash engine.
func splashPalette(pal theme.Palette) fresco.Palette {
	return fresco.Palette{
		A0:        theme.Hex(pal.Danger),
		A1:        theme.Hex(pal.Purple),
		A2:        theme.Hex(pal.Accent),
		A3:        theme.Hex(pal.Cyan),
		Highlight: theme.Hex(pal.Fg),
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
