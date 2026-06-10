// Package overlay renders modal dialogs (text input, confirmation, pickers) on
// top of the main view, including the compositing that places one rendered
// block over another.
package overlay

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/ZviBaratz/atrium/ui/theme"

	xansi "github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

// Most of this code is modified from https://github.com/charmbracelet/lipgloss/pull/102

// WhitespaceOption sets a styling rule for rendering whitespace.
type WhitespaceOption func(*whitespace)

// Split a string into lines, additionally returning the size of the widest
// line.
func getLines(s string) (lines []string, widest int) {
	lines = strings.Split(s, "\n")

	for _, l := range lines {
		w := xansi.StringWidth(l)
		if widest < w {
			widest = w
		}
	}

	return lines, widest
}

// CalculateCenterCoordinates returns the x, y offsets that center the
// foreground block within the background block.
func CalculateCenterCoordinates(foregroundLines []string, backgroundLines []string, foregroundWidth, backgroundWidth int) (int, int) {
	// Calculate the x-coordinate to horizontally center the foreground text.
	x := (backgroundWidth - foregroundWidth) / 2

	// Calculate the y-coordinate to vertically center the foreground text.
	y := (len(backgroundLines) - len(foregroundLines)) / 2

	return x, y
}

// Regular expressions matching the color forms a terminal can emit; compiled
// once, used by the fade rewrite on every overlay render.
var (
	// Background color codes like \x1b[48;2;R;G;Bm or \x1b[48;5;Nm
	bgColorRegex = regexp.MustCompile(`\x1b\[48;[25];[0-9;]+m`)
	// Foreground color codes like \x1b[38;2;R;G;Bm or \x1b[38;5;Nm
	fgColorRegex = regexp.MustCompile(`\x1b\[38;[25];[0-9;]+m`)
	// Simple color codes like \x1b[31m
	simpleColorRegex = regexp.MustCompile(`\x1b\[[0-9]+m`)
)

// fadeSGR returns the SGR fragments that repaint background content in the
// active theme's faint colors while a modal is up. All bundled palettes are
// truecolor hex; a non-hex color falls back to the legacy greys rather than
// emitting a broken sequence.
func fadeSGR() (fg, bg string) {
	p := theme.Current().Palette
	fg = "\x1b[38;5;240m"
	bg = "\x1b[48;5;236m"
	if r, g, b, ok := hexRGB(string(p.FgFaint)); ok {
		fg = fmt.Sprintf("\x1b[38;2;%d;%d;%dm", r, g, b)
	}
	if r, g, b, ok := hexRGB(string(p.Bg)); ok {
		bg = fmt.Sprintf("\x1b[48;2;%d;%d;%dm", r, g, b)
	}
	return fg, bg
}

// hexRGB parses a "#rrggbb" color into its components.
func hexRGB(s string) (r, g, b uint8, ok bool) {
	if len(s) != 7 || s[0] != '#' {
		return 0, 0, 0, false
	}
	v, err := strconv.ParseUint(s[1:], 16, 32)
	if err != nil {
		return 0, 0, 0, false
	}
	return uint8(v >> 16 & 0xff), uint8(v >> 8 & 0xff), uint8(v & 0xff), true
}

// PlaceOverlay places fg on top of bg, fading the background into the active
// theme's faint colors so the modal reads as the only live surface.
// If center is true, the foreground is centered on the background; otherwise, the provided x and y are used.
func PlaceOverlay(
	x, y int,
	fg, bg string,
	center bool,
	opts ...WhitespaceOption,
) string {
	fgLines, fgWidth := getLines(fg)
	bgLines, bgWidth := getLines(bg)
	bgHeight := len(bgLines)
	fgHeight := len(fgLines)

	// Apply the fade by rewriting each background line's color codes to the
	// theme-derived faint pair.
	fadeFg, fadeBg := fadeSGR()
	fadedBgLines := make([]string, len(bgLines))
	for i, line := range bgLines {
		content := bgColorRegex.ReplaceAllString(line, fadeBg)
		content = fgColorRegex.ReplaceAllString(content, fadeFg)
		content = simpleColorRegex.ReplaceAllStringFunc(content, func(match string) string {
			// Skip reset codes
			if match == "\x1b[0m" {
				return match
			}
			return fadeFg
		})
		fadedBgLines[i] = content
	}
	bgLines = fadedBgLines

	// Determine placement coordinates
	placeX, placeY := x, y
	if center {
		placeX, placeY = CalculateCenterCoordinates(fgLines, bgLines, fgWidth, bgWidth)
	}

	// Check if foreground exceeds background size
	if fgWidth >= bgWidth && fgHeight >= bgHeight {
		return fg // Return foreground if it's larger than background
	}

	// Clamp coordinates to ensure foreground fits within background. The upper
	// bounds go negative when the foreground is larger than the background in
	// one dimension; flooring them at 0 top/left-anchors an oversize foreground
	// so the render loop bottom/right-truncates it instead of cutting its top
	// off with a negative offset.
	placeX = clamp(placeX, 0, max(0, bgWidth-fgWidth))
	placeY = clamp(placeY, 0, max(0, bgHeight-fgHeight))

	// Apply whitespace options
	ws := &whitespace{}
	for _, opt := range opts {
		opt(ws)
	}

	// Build the output string
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
				b.WriteString(ws.render(placeX - pos))
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
			b.WriteString(ws.render(bgLineWidth - rightWidth - pos))
		}
		b.WriteString(right)
	}

	return b.String()
}

func clamp(v, lower, upper int) int {
	return min(max(v, lower), upper)
}

type whitespace struct {
	style termenv.Style
	chars string
}

// Render whitespaces.
func (w whitespace) render(width int) string {
	if w.chars == "" {
		w.chars = " "
	}

	r := []rune(w.chars)
	j := 0
	b := strings.Builder{}

	// Cycle through runes and print them into the whitespace.
	for i := 0; i < width; {
		b.WriteRune(r[j])
		j++
		if j >= len(r) {
			j = 0
		}
		i += xansi.StringWidth(string(r[j]))
	}

	// Fill any extra gaps white spaces. This might be necessary if any runes
	// are more than one cell wide, which could leave a one-rune gap.
	short := width - xansi.StringWidth(b.String())
	if short > 0 {
		b.WriteString(strings.Repeat(" ", short))
	}

	return w.style.Styled(b.String())
}
