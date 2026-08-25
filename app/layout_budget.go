package app

// This file is the single owner of the frame's geometry arithmetic: the
// vertical partition (computeBudget) and the horizontal body split
// (computeRegions). ultraviolet's Cassowary layout package was considered for
// this job and declined — a pseudo-versioned API is too unstable a foundation
// when three regions of plain arithmetic suffice. frameBudget and bodyRegions
// are the seams a constraint solver would slot into if the regions ever
// multiply.

// frameBudget is the one partition of the terminal height: every row belongs
// to exactly one of the banner (the auto-accept safety strip, at the top when
// armed), the body (the list/preview panes), the hint-bar row and the error
// row — except on a terminal shorter than its own chrome, where the one-row
// body floor deliberately over-commits the height rather than handing a zero
// body downstream. Both height consumers — the resize path and the mouse
// divider's Y-bound (paneContentHeight) — read the partition through
// computeBudget, which is what keeps them from drifting apart; they were two
// hand-synced copies of the same subtraction before this type existed.
type frameBudget struct {
	banner int
	body   int
	menu   int
	err    int
}

// computeBudget partitions height into the frame's rows. The hint bar is
// contextual (see menuVisible): during plain navigation it always claims a
// row — blank when hint_bar is off, so a transient notice rides it with no
// reflow (#438) — and the panes reclaim that row only behind overlays. The
// error box likewise takes a row only while a notice is showing.
// topBannerHeight reserves the auto-accept safety banner's row at the top
// when armed (#378). The body is floored at one row so a degenerate terminal
// never pushes a zero or negative height downstream.
func (m *home) computeBudget(height int) frameBudget {
	b := frameBudget{banner: m.topBannerHeight()}
	if m.menuVisible() {
		b.menu = 1
	}
	if m.errBox.HasContent() {
		b.err = 1
	}
	b.body = max(1, height-b.banner-b.menu-b.err)
	return b
}

// bodyRegions is the horizontal split of the body's row band: the session
// list, the tabbed window, and the inspector — which stays zero until the
// wave-2 docked promotion gives it a column of its own.
type bodyRegions struct {
	list      int
	tabs      int
	inspector int
}

// computeRegions splits width across the body's regions. The session list
// takes listRatio of the width (default 30%, user-adjustable with < / > and
// the divider drag, clamped in appState); the focus preset hides the list
// (View omits it), so its whole column goes to the tabbed window. The tabs
// region is the remainder, so the regions always sum to exactly width.
func (m *home) computeRegions(width int) bodyRegions {
	r := bodyRegions{list: m.listCols(width)}
	if m.listHidden() {
		r.list = 0
	}
	r.tabs = width - r.list - r.inspector
	return r
}

// listCols converts the current listRatio to whole columns at width, in the
// exact truncation form the layout has always used — the goldens encode it,
// and adjustListCols centers its target on the same truncation. Shared by
// computeRegions and adjustListCols; the latter deliberately reads this raw
// conversion rather than the hidden-zeroed region, because < / > escape the
// focus preset by stepping from the remembered split, which applyLayoutPreset
// preserves (the focus preset carries a zero ratio).
func (m *home) listCols(width int) int {
	return int(float32(width) * float32(m.listRatio))
}
