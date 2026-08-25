package overlay

// SizeSpec is one overlay's responsive geometry, declared beside its Render.
// All values are OUTER cells — the box's total footprint, border and padding
// included, which is what lipgloss v2's Width and Height mean (see
// theme.Panel). The app's resize walk feeds Fit's result straight to the
// overlay's SetSize, so the numbers that used to live as fraction+cap
// literals in per-state size closures are inspectable data here: what the box
// takes is the spec; why it takes that much is the comment on the
// declaration.
//
// A constraint solver (ultraviolet's Cassowary layout package) was considered
// for this job and declined — a pseudo-versioned API is too unstable a
// foundation for what one scale-cap-floor pass expresses. SizeSpec is the
// seam it would slot into if overlay geometry ever outgrows that shape.
type SizeSpec struct {
	// WFrac and HFrac are the box's share of the terminal, scaled as
	// int(float32(term) * frac) — float32 on purpose, and load-bearing: the
	// goldens encode that exact truncation, and a float64 reimplementation
	// disagrees with it (0.7 of 90 columns is 63 in float32 and 62 in
	// float64). The width-contract tests pin the discriminating case.
	WFrac, HFrac float32

	// WExtra and HExtra are added to the scaled value before the caps: the
	// palette's +3 keeps its rendered height where it was before its share
	// was measured against the whole box, and the outer-cell conversions of
	// formerly border-exclusive widths land here as +2.
	WExtra, HExtra int

	// WMax and HMax cap the scaled value (zero means uncapped). On an
	// unsized terminal (a zero or negative axis) Fit returns the cap
	// directly — the preferred size — which is what the width helpers this
	// type replaced did for their zero case.
	WMax, HMax int

	// WMin and HMin floor each axis, applied after the caps, so a floor
	// above a cap wins.
	WMin, HMin int
}

// SnapFullBleed is the one statement of the inset rule (#695): a box whose
// outer width lands within three cells of the terminal's renders as a doubled
// border, not as a modal over a background — one stray cell of frame beside a
// border reads as a rendering fault — so such a box takes the full terminal
// width instead. Gaps are either zero or at least two cells per side. The
// content-huggers apply it inside their own width caps; spec-driven boxes
// never reach the zone (their fractions leave real gaps), so Fit does not
// call it. A box already wider than the terminal (a floor on an absurdly
// narrow one) is left alone: PlaceOverlay anchors an oversize box, and
// shrinking it here would undo the floor's promise.
func SnapFullBleed(outer, termW int) int {
	if termW > 0 && outer > termW-4 && outer <= termW {
		return termW
	}
	return outer
}

// Fullscreen hands an overlay the whole terminal, for boxes that size
// themselves: the cheatsheet hugs its content width and windows its lines to
// fit short terminals; the settings and accounts panels cap their own width
// and window their rows.
var Fullscreen = SizeSpec{WFrac: 1, HFrac: 1}

// Fit resolves the spec against a terminal size: scale, add the extra, cap,
// floor — per axis — then clamp the height to the terminal. The height clamp
// is shared data-path rather than per-overlay policy because exactly one spec
// can exceed the terminal (the palette's HExtra); for every other spec a
// fraction of the terminal capped below it cannot, and the clamp is inert.
// Width is deliberately not clamped: a box wider than the terminal is the
// caller's stated preference (the confirm dialog keeps its floor on absurdly
// narrow terminals), and PlaceOverlay top/left-anchors an oversize box.
func (s SizeSpec) Fit(termW, termH int) (w, h int) {
	w = fitAxis(termW, s.WFrac, s.WExtra, s.WMax, s.WMin)
	h = fitAxis(termH, s.HFrac, s.HExtra, s.HMax, s.HMin)
	if termH > 0 && h > termH {
		h = termH
	}
	return w, h
}

// fitAxis resolves one axis: the preferred size (the cap) when the terminal
// axis is unknown, otherwise scale, extra, cap, floor — in that order.
func fitAxis(term int, frac float32, extra, limit, floor int) int {
	if term <= 0 {
		return limit
	}
	v := int(float32(term)*frac) + extra
	if limit > 0 && v > limit {
		v = limit
	}
	if v < floor {
		v = floor
	}
	return v
}
