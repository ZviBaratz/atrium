package overlay

import (
	"github.com/charmbracelet/x/ansi"
)

// Rail line geometry: [selection 1][space 1][label ...][space 1][handoff 1].
const (
	railMarkerCells = 2 // the selection mark and the space after it
	railTrailCells  = 2 // the space before the handoff cell, and the cell itself
)

// railWidth is the rail's fixed width: the widest rail label plus its marker and
// handoff cells. Derived from railEntries() rather than a literal, so renaming a
// category moves the rail — and, through twoPaneMinInner, the degradation threshold with
// it (spec §10: the threshold "must be computed from the parts, not hardcoded").
func railWidth() int {
	w := 0
	for _, e := range railEntries() {
		if n := ansi.StringWidth(e.label); n > w {
			w = n
		}
	}
	return railMarkerCells + w + railTrailCells
}

// helpHeight is the help pane's line count: helpPaneLines whenever the terminal can afford
// them, fewer only when it cannot.
//
// It reads the terminal height and NOTHING else — in particular not the cursor. That
// independence is the fix for D5 and is what TestSelectingTheLongestHelpRowKeepsTheRowCount
// and TestHelpHeightIgnoresTheCursor pin. The -1 reserves the separator, which is drawn only
// alongside a help pane.
func (s *SettingsOverlay) helpHeight() int {
	return clamp(s.height-settingsVChrome-settingsMinBody-1, 0, helpPaneLines)
}

// helpBlockHeight is the help pane plus its separator, or 0 when there is no help pane.
func (s *SettingsOverlay) helpBlockHeight() int {
	if h := s.helpHeight(); h > 0 {
		return h + 1
	}
	return 0
}

// maxPaneLines is the tallest content any rail entry could need: the All settings view, with
// a header per category and a spacer between them. Capping paneHeight at it means the box
// grows with the terminal but never past what it can fill. Pinned against the flat view's
// real line count by TestMaxPaneLinesMatchesTheFlatView.
func (s *SettingsOverlay) maxPaneLines() int {
	cats := len(allCategories())
	return max(len(railEntries()), len(s.rows)+cats+(cats-1))
}

// paneHeight is the shared height of the rail and rows panes.
//
// It is a function of the terminal size alone — not of the rail cursor, not of the row
// cursor — so the centered box never changes height as you navigate. At 80x24 it is 13,
// which is exactly the thirteen rail entries (spec §4's invariant).
func (s *SettingsOverlay) paneHeight() int {
	return clamp(s.height-settingsVChrome-s.helpBlockHeight(), settingsMinBody, s.maxPaneLines())
}
