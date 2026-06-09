package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"

	"github.com/ZviBaratz/atrium/ui/theme"
)

// rowSeg is one rendered piece of a row line. plain is the text used for width
// math (ANSI styling adds no columns, so plain's width equals the rendered
// width); style carries the foreground plus the row background. flex marks the
// single elastic segment per line — it absorbs leftover width and is truncated
// with "…" to fit. sep marks a separator (" · ") that is dropped when it would
// otherwise dangle next to a flex segment emptied for lack of room. rendered (if
// hasRendered) overrides the styled output for self-styled chips like the AUTO
// badge, which carry their own background.
type rowSeg struct {
	plain       string
	style       lipgloss.Style
	flex        bool
	sep         bool
	rendered    string
	hasRendered bool
}

func (s rowSeg) width() int { return runewidth.StringWidth(s.plain) }

func (s rowSeg) render() string {
	if s.hasRendered {
		return s.rendered
	}
	return s.style.Render(s.plain)
}

// rawSeg wraps a fully pre-styled chip (carrying its own colors/background, e.g.
// the AUTO badge) as a fixed segment whose width is measured from plain.
func rawSeg(plain, styled string) rowSeg {
	return rowSeg{plain: plain, rendered: styled, hasRendered: true}
}

// rowPaint builds segments and gaps that all bake in a shared background, so the
// selected-row fill survives the ANSI reset at the end of each styled span (an
// end-of-span reset also clears the background, so it must live on every piece
// rather than wrap the line). For an unselected row bg is NoColor and segments
// render plain.
type rowPaint struct {
	th *theme.Theme
	bg lipgloss.TerminalColor
}

func newRowPaint(th *theme.Theme, selected bool) rowPaint {
	var bg lipgloss.TerminalColor = lipgloss.NoColor{}
	if selected {
		bg = th.Palette.BgElevated
	}
	return rowPaint{th: th, bg: bg}
}

// seg builds a fixed (non-elastic) colored segment.
func (p rowPaint) seg(text string, c lipgloss.Color) rowSeg {
	return rowSeg{plain: text, style: lipgloss.NewStyle().Foreground(c).Background(p.bg)}
}

// flexSeg builds the single elastic segment for a line (truncated to fit by
// composeLine). bold renders the selected row's name.
func (p rowPaint) flexSeg(text string, c lipgloss.Color, bold bool) rowSeg {
	st := lipgloss.NewStyle().Foreground(c).Background(p.bg)
	if bold {
		st = st.Bold(true)
	}
	return rowSeg{plain: text, style: st, flex: true}
}

// sepSeg builds a dim middot separator that collapses if the flex it sits next
// to is emptied for lack of room.
func (p rowPaint) sepSeg() rowSeg {
	return rowSeg{plain: " · ", style: lipgloss.NewStyle().Foreground(p.th.Palette.FgDim).Background(p.bg), sep: true}
}

// pad renders n background-aware blank columns (n < 0 → 0).
func (p rowPaint) pad(n int) string {
	if n < 0 {
		n = 0
	}
	return lipgloss.NewStyle().Background(p.bg).Render(strings.Repeat(" ", n))
}

// composeLine lays out one row line to exactly width columns: it gives leftover
// width to the single flex segment in left (truncating with "…", or emptying it
// and collapsing any adjacent separator when there is no room), then joins the
// left segments, a background-aware gap of at least one column, and the right
// segments flush to the right edge.
func (p rowPaint) composeLine(width int, left, right []rowSeg) string {
	rightW := 0
	for _, s := range right {
		rightW += s.width()
	}
	fixed := 0
	flexIdx := -1
	for i, s := range left {
		if s.flex {
			flexIdx = i
			continue
		}
		fixed += s.width()
	}
	if flexIdx >= 0 {
		budget := width - fixed - rightW - 1 // 1 = minimum gap before the right group
		if budget < 1 {
			left[flexIdx].plain = ""
		} else if left[flexIdx].width() > budget {
			left[flexIdx].plain = runewidth.Truncate(left[flexIdx].plain, budget, "…")
		}
		if left[flexIdx].plain == "" {
			left = collapseSeps(left, flexIdx)
		}
	}
	leftW := 0
	var b strings.Builder
	for _, s := range left {
		leftW += s.width()
		b.WriteString(s.render())
	}
	gap := width - leftW - rightW
	if gap < 1 {
		gap = 1
	}
	b.WriteString(p.pad(gap))
	for _, s := range right {
		b.WriteString(s.render())
	}
	return b.String()
}

// collapseSeps returns left with the emptied flex segment at idx removed, plus
// any separator segment immediately before or after it (orphaned by the empty
// flex). Other separators — between two present chips — are kept.
func collapseSeps(left []rowSeg, idx int) []rowSeg {
	out := make([]rowSeg, 0, len(left))
	for i, s := range left {
		if i == idx {
			continue // the emptied flex segment itself
		}
		if s.sep && (i == idx-1 || i == idx+1) {
			continue // a separator orphaned by the emptied flex
		}
		out = append(out, s)
	}
	return out
}
