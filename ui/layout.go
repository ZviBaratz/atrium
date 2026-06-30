package ui

import "github.com/charmbracelet/lipgloss"

// centerInBox centers content both horizontally and vertically within a
// width×height box — the shared placeholder/fallback layout used by the diff,
// preview, error, and menu panes. It does not clip oversized content: a caller
// whose content may exceed the box (e.g. a fallback line wider than a narrow
// pane) wraps the result in a MaxWidth/MaxHeight style — see PreviewPane.String.
func centerInBox(width, height int, content string) string {
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, content)
}
