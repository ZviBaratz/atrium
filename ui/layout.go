package ui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// centerInBox centers content both horizontally and vertically within a
// width×height box — the shared placeholder/fallback layout used by the diff,
// preview, error, and menu panes — and clamps it to that box. lipgloss.Place
// centers but does not clip, so content wider or taller than the box (a
// fallback line on a narrow pane) would silently inflate the whole frame and
// throw every centered overlay off-center (#251); the MaxWidth/MaxHeight guard
// truncates the overflow and is a no-op when the content already fits.
func centerInBox(width, height int, content string) string {
	return lipgloss.NewStyle().MaxWidth(width).MaxHeight(height).Render(
		lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, content))
}

// fallbackBlock composes the wordmark-over-message placeholder both empty panes
// show — the preview's paused/idle/setup states and the terminal's — laid out for
// a width×height pane. Callers pass the message as separate lines and center the
// result with centerInBox.
//
// It is width-aware by construction, which is the whole point (#355). The block
// this replaces was composed with no width at all and only met the pane later, at
// MaxWidth, which hard-truncates from the right: a paused session on a long branch
// rendered its banner sheared and its message chopped to "Session is pau". Two
// mechanics made that worse than plain clipping. JoinVertical(Center, ...) pads
// every line out to the block's *longest*, so one oversize line baked leading
// padding into the banner and shoved it right — a non-local effect. And
// PlaceHorizontal is a no-op when content is wider than its target, so an oversize
// block was not merely clipped, it was left-anchored too.
//
// Style.Width wraps (via cellbuf.Wrap: ANSI-aware, and it force-breaks a token
// longer than the limit, so an unbounded branch name cannot overhang) and
// Align(Center) centers each line within the pane. Every line therefore fits, the
// block is exactly the pane's width, and PlaceHorizontal starts centering again.
//
// Wrapping, not truncating, is deliberate: the branch name is the reason the paused
// screen exists, so it is broken across rows rather than elided. Shortening the
// literals cannot substitute — the name is interpolated and unbounded.
func fallbackBlock(width, height int, lines ...string) string {
	fit := lipgloss.NewStyle().Width(width).Align(lipgloss.Center)
	body := strings.Join(lines, "\n")

	// The wordmark is art: it cannot wrap (that shreds it) or truncate (that shears
	// it mid-glyph), only be omitted — so it yields to the message rather than the
	// other way round. The gate measures the *wrapped* message instead of taking a
	// fixed floor like splashFits because that height is not knowable in advance:
	// one row for "Setting up workspace...", nine for the paused view at 28 cols.
	// Height matters as much as width here, since the pane clamp drops rows from the
	// bottom — where the "switch your main repo off" warning sits. Extents are
	// measured, not hardcoded, so retouching the art cannot silently break the gate.
	banner := trimBlankLines(FallbackBanner())
	const gap = 1 // blank row between the wordmark and the message
	if lipgloss.Width(banner) <= width &&
		lipgloss.Height(banner)+gap+lipgloss.Height(fit.Render(body)) <= height {
		body = banner + "\n\n" + body
	}
	return fit.Render(body)
}
