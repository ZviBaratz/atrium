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
