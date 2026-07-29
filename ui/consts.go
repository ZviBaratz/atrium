// Package ui contains the presentational Bubble Tea components of the TUI —
// session list, preview/diff/terminal panes, tabbed window, menu bar, and error
// box. Components hold view state but defer domain actions to the app package's
// home model.
package ui

import (
	"github.com/ZviBaratz/atrium/ui/theme"

	"charm.land/lipgloss/v2"
)

// fallbackArt is the raw "ATRIUM" wordmark shown in empty preview/terminal
// panes. It is colored at render time by FallbackBanner so it follows the theme.
var fallbackArt = lipgloss.JoinVertical(lipgloss.Center, `
░█████╗░████████╗██████╗░██╗██╗░░░██╗███╗░░░███╗
██╔══██╗╚══██╔══╝██╔══██╗██║██║░░░██║████╗░████║
███████║░░░██║░░░██████╔╝██║██║░░░██║██╔████╔██║
██╔══██║░░░██║░░░██╔══██╗██║██║░░░██║██║╚██╔╝██║
██║░░██║░░░██║░░░██║░░██║██║╚██████╔╝██║░╚═╝░██║
╚═╝░░╚═╝░░░╚═╝░░░╚═╝░░╚═╝╚═╝░╚═════╝░╚═╝░░░░░╚═╝
`)

// FallbackBanner returns the wordmark colored in the active theme's accent hue.
func FallbackBanner() string {
	return theme.Current().PurpleStyle().Render(fallbackArt)
}
