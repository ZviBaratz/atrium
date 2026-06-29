package overlay

import (
	"github.com/ZviBaratz/atrium/ui/theme"

	"github.com/charmbracelet/lipgloss"
)

// Shared overlay styles. The text-input overlay and its embedded pickers (directory,
// branch, profile, model) all render labels, filters, dim hints, and a highlighted
// selection identically; these are the single definitions they delegate to, so the
// look stays consistent and the lipgloss construction isn't copy-pasted per component.

// overlayLabelStyle is the accented, bold style for a field/section label.
func overlayLabelStyle() lipgloss.Style { return theme.Current().AccentStyle().Bold(true) }

// overlayFilterStyle is the foreground style for an active filter/input line.
func overlayFilterStyle() lipgloss.Style { return theme.Current().FgStyle() }

// overlayDimStyle is the muted style for secondary/unfocused text.
func overlayDimStyle() lipgloss.Style { return theme.Current().DimStyle() }

// overlaySelectedStyle is the inverted highlight (accent background, bg foreground)
// for the focused row of a picker or the focused button.
func overlaySelectedStyle() lipgloss.Style {
	return lipgloss.NewStyle().
		Background(theme.Current().Palette.Accent).
		Foreground(theme.Current().Palette.Bg)
}
