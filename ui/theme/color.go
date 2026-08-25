package theme

import (
	"fmt"
	"image/color"

	"charm.land/lipgloss/v2"
)

// The colour seam, now on Lip Gloss v2 (#393).
//
// v2 changed what a colour *is*: lipgloss.Color is a constructor returning
// image/color.Color rather than a string type, and the TerminalColor interface is
// gone in favour of color.Color. Naming the positions here is what kept that from
// touching the 41 sites across ui, session/tmux and the overlay compositor that
// hold or stringify a palette colour — they were repointed at these names before
// the cut, so this file absorbed the change.
//
// Color and AnyColor are now the same type. They stayed distinct through the port
// because v1 genuinely had two (a concrete string type and an interface), and
// collapsing them mid-migration would have hidden which call sites meant "a
// palette token" and which meant "a colour or the absence of one". Keeping both
// names keeps that distinction readable at the call site even though the compiler
// no longer enforces it.

// Color is a palette token: one concrete colour.
type Color = color.Color

// AnyColor is a colour-valued parameter that may also carry the absence of colour
// — the row background is the case that needs it, being either a selection fill or
// nothing at all.
type AnyColor = color.Color

// NoColor is the absence of colour — a background that should not paint.
//
// It survived the v2 rewrite unchanged and still satisfies color.Color, so this is
// the same value it always was. It stays a function so callers cannot share and
// mutate one instance.
func NoColor() AnyColor { return lipgloss.NoColor{} }

// Hex renders a palette token in the "#rrggbb" form that consumers outside the
// Lip Gloss renderer need: the in-session tmux status bar interpolates colours
// into tmux's own "#[fg=...]" markup, and the splash hands its palette to fresco
// as strings. Neither speaks ANSI, so neither can take a color.Color.
//
// Under v1 this was string(c), because a colour *was* its hex text. A v2 colour is
// only an RGBA source, so the text has to be derived. color.Color reports
// 16-bit alpha-premultiplied components, which for the opaque palette colours
// Atrium ships is exactly the 8-bit value repeated (0xNN * 0x101), so >>8 recovers
// the original byte without rounding.
//
// A nil colour — the absence of one — has no hex form to give, and silently
// emitting "#000000" would paint black where the caller meant "leave it alone".
// tmux treats an empty value as "unset", which is the honest translation.
func Hex(c Color) string {
	if c == nil {
		return ""
	}
	r, g, b, _ := c.RGBA()
	// Masked rather than converted to uint8: >>8 already leaves a value in 0..255
	// for the opaque colours Atrium ships, but gosec cannot prove that (G115), and
	// the mask says the range out loud instead of asserting it in a cast.
	return fmt.Sprintf("#%02x%02x%02x", r>>8&0xff, g>>8&0xff, b>>8&0xff)
}

// ParseHex is the inverse of Hex: it turns a canonical "#rrggbb" string into a palette
// colour. It lives beside Hex so both directions of the colour seam are in one file.
//
// It does NOT validate. A caller handing it something other than canonical hex — a
// colour word, empty, anything lipgloss cannot parse — gets lipgloss.NoColor, and that is
// worse than an error in a specific way: Hex renders it "#000000" and the contrast oracle
// therefore MEASURES it as black, while a terminal renders it as the terminal's own
// default foreground. A palette carrying one would be judged on a colour nobody sees.
// (Shorthand is the exception that proves it: lipgloss expands #fff to white.)
//
// Which is why validation belongs at the boundary that has a filename and a key to name
// in the message — ui/theme/themefile refuses on hexRE before reaching this — rather than
// here, where doing it twice would mean two spellings of "what counts as a colour".
func ParseHex(s string) Color { return lipgloss.Color(s) }
