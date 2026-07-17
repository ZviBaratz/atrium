package ui

import "github.com/charmbracelet/x/ansi"

// hyperlink wraps already-rendered visible text in an OSC 8 hyperlink to url.
// The OSC 8 open/close escapes are zero-width — both ansi.StringWidth and
// lipgloss.Width ignore them — so a wrapped chip measures exactly like its
// unwrapped self and never perturbs row/column alignment. On a terminal without
// OSC 8 support the escapes are silently dropped and only the text shows.
// Callers must pass a non-empty url; an empty one would emit a hyperlink with no
// target (a no-op that still spends bytes), so guard it upstream.
func hyperlink(url, text string) string {
	return ansi.SetHyperlink(url) + text + ansi.ResetHyperlink()
}
