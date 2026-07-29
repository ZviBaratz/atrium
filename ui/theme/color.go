package theme

import "github.com/charmbracelet/lipgloss"

// The colour seam for the Lip Gloss v2 migration (#393).
//
// v2 changes what a colour *is*. lipgloss.Color stops being a string type and
// becomes a constructor returning image/color.Color; the TerminalColor interface
// is deleted in favour of color.Color; and because a colour is no longer a string,
// every string(someColor) conversion stops compiling. Atrium uses lipgloss.Color
// in type position 26 times — all eighteen Palette fields among them — and
// converts a colour back to a string in sixteen more.
//
// Naming those positions here means the port is this file. Nothing else has to
// know which concrete type a colour has.
//
// Deliberately aliases rather than defined types: on v1 lipgloss.Color is a string
// type, and a defined type would demand a conversion at every Foreground/Background
// call in the tree — hundreds of sites of churn for no benefit, since the compiler
// at the cut catches anything this seam misses.

// Color is a palette token: one concrete colour.
//
// v1: lipgloss.Color, a hex string like "#7aa2f7". v2: image/color.Color.
type Color = lipgloss.Color

// AnyColor is a colour-valued parameter that may also carry the absence of colour
// — the row background is the case that needs it, being either a selection fill or
// nothing at all.
//
// v1: the lipgloss.TerminalColor interface, which both Color and NoColor satisfy.
// v2: image/color.Color, which both still satisfy, so the two aliases converge and
// this one can fold into Color at the cut.
type AnyColor = lipgloss.TerminalColor

// NoColor is the absence of colour — a background that should not paint.
//
// It is a function rather than a var so callers cannot accidentally share and
// mutate one value, and so the cut has a single body to change if v2's spelling
// ever moves. (As of lipgloss v2.0.5 it does not: NoColor survives the v2 rewrite
// unchanged and still satisfies color.Color.)
func NoColor() AnyColor { return lipgloss.NoColor{} }

// Hex renders a palette token in the "#rrggbb" form that consumers outside the
// Lip Gloss renderer need.
//
// There are two such consumers and neither is ANSI: the in-session tmux status bar
// interpolates colours into tmux's own "#[fg=...]" markup, and the splash hands its
// palette to fresco as strings. Both need the text of a colour, which on v1 is just
// the token itself — every shipped palette is truecolor hex.
//
// This exists so that at the cut there is one body to rewrite (v2 will derive the
// components through color.Color's RGBA method) instead of sixteen string()
// conversions scattered across ui, session/tmux and the overlay compositor. It is
// also why TestNoStringConversionsOfPaletteColors guards the tree against new ones
// appearing before then.
func Hex(c Color) string { return string(c) }
