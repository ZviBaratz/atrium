package overlay

import (
	"charm.land/lipgloss/v2"
	"github.com/muesli/reflow/truncate"
)

// fitHint picks the widest rung whose composed line — prefix plus the rung — fits
// width, falling back to the last (narrowest) rung when none does.
//
// It is the create form's answer to "a copy change is a width change". The overlay
// gets 42 inner cells on an 80-col terminal (int(0.6*80) - 6) and fitOverlay
// truncates anything wider *silently*, so a hint written against a developer's wide
// terminal loses its tail on a default one and nothing says so — that is how five
// lines of this form came to ship cut (#464). The alternative to a ladder is cutting
// every string to the narrowest budget, which taxes the wide terminal for the narrow
// one's sake; here each site lists its rungs widest first and keeps, at every width,
// the most it can afford.
//
// Width 0 means unsized — a component that has not been through SetSize, i.e. a unit
// test or a render before the first WindowSizeMsg — and yields the widest rung, the
// same convention VariantPicker.chipWindow already uses for its chip windowing.
//
// Rungs are ordered, not sorted: fitHint returns the first that fits, so a list whose
// widths are not descending will skip rungs rather than misbehave. Each call site's
// order is pinned by TestHintLadders_OrderedWidestFirst.
func fitHint(width int, prefix string, rungs ...string) string {
	if len(rungs) == 0 {
		return ""
	}
	if width <= 0 {
		return rungs[0]
	}
	budget := width - lipgloss.Width(prefix)
	for _, r := range rungs {
		if lipgloss.Width(r) <= budget {
			return r
		}
	}
	return rungs[len(rungs)-1]
}

// fitPlaceholder is fitHint for a line that has no prefix and no reader downstream
// to cut it honestly: it picks the widest rung that fits, then ELLIPSIZES what is
// left over.
//
// The extra step is the difference between the two helpers, and it exists because
// the prompt textarea is not fitOverlay. fitOverlay truncates with a tail, so a
// create-form line it cuts at least says it was cut; the textarea cuts its
// placeholder at its own width silently and then pads the row back out to that
// width — which is also why no width assertion could ever see the loss (#690). The
// ladder is meant to make the cut unnecessary at every supported width; the tail is
// what a terminal narrower than the 80-col floor gets, and it is a signal rather than
// a fix.
//
// Width 0 (unsized) yields the widest rung uncut, matching fitHint's convention.
func fitPlaceholder(width int, rungs ...string) string {
	fitted := fitHint(width, "", rungs...)
	if width <= 0 || lipgloss.Width(fitted) <= width {
		return fitted
	}
	return truncate.StringWithTail(fitted, uint(width), "…")
}
