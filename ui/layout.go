package ui

import "github.com/charmbracelet/lipgloss"

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
