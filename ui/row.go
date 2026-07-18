package ui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"

	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/agent"
	"github.com/ZviBaratz/atrium/session/git"
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
// segments flush to the right edge. It never mutates the caller's slices: the
// flex segment is truncated on a local copy.
func (p rowPaint) composeLine(width int, left, right []rowSeg) string {
	left = append([]rowSeg(nil), left...) // own our copy; we truncate the flex element below
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

// gutterSeg is the leading status column: the state glyph in its state color.
func (r *InstanceRenderer) gutterSeg(p rowPaint, i *session.Instance) rowSeg {
	glyph, c := r.stateGlyph(i, p.th)
	return p.seg(glyph, c)
}

// agentSeg is the agent-identity icon (which CLI the session runs).
func (p rowPaint) agentSeg(i *session.Instance) rowSeg {
	icon, c := p.th.AgentGlyph(string(agent.Resolve(i.Program).Key))
	return p.seg(icon, c)
}

// agentColor is the identity color for i's agent (brand accent when the agent
// has one, theme foreground otherwise) — the chip-tinting counterpart of
// agentSeg, so the model chip can ride the icon as one brand-colored unit.
func (p rowPaint) agentColor(i *session.Instance) lipgloss.Color {
	_, c := p.th.AgentGlyph(string(agent.Resolve(i.Program).Key))
	return c
}

// nameSeg is the flex (elastic) display-name segment. NeedsInput recolors the
// name (the one attention state); the selected row bolds it. The name is width-
// sanitized so emoji/ZWJ clusters don't desync the renderer.
func (p rowPaint) nameSeg(i *session.Instance, selected bool) rowSeg {
	c := p.th.Palette.Fg
	if i.GetStatus() == session.NeedsInput {
		c = p.th.Palette.Attention
	}
	return p.flexSeg(theme.SanitizeWidth(i.DisplayName()), c, selected)
}

// noteSeg is the freeform session note as a flex segment: a leading note glyph
// plus the width-sanitized note text, in a distinct accent (Purple) so it reads
// as an annotation, never confused with the branch (dim) or the name (Fg).
// Returns the zero rowSeg (renders nothing) when the instance has no note.
func (p rowPaint) noteSeg(i *session.Instance) rowSeg {
	note := strings.TrimSpace(i.Note())
	if note == "" {
		return rowSeg{}
	}
	text := p.th.Glyphs.Note + " " + theme.SanitizeWidth(note)
	return p.flexSeg(text, p.th.Palette.Purple, false)
}

// gitChips returns the behind/ahead cluster as space-separated segments (behind
// in Attention — it implies a rebase — ahead dim). Empty when neither applies.
// The dirty glyph is intentionally not here: it describes *what changed* rather
// than position vs base, so it rides with the diff counts (see changeSegs).
func gitChips(p rowPaint, stat *git.DiffStats) []rowSeg {
	if stat == nil || stat.Error != nil {
		return nil
	}
	var segs []rowSeg
	if stat.Behind > 0 {
		segs = append(segs, p.seg(fmt.Sprintf("%s%d", p.th.Glyphs.Behind, stat.Behind), p.th.Palette.Attention))
	}
	if stat.Commits > 0 {
		if len(segs) > 0 {
			segs = append(segs, p.seg(" ", p.th.Palette.FgDim))
		}
		segs = append(segs, p.seg(fmt.Sprintf("%s%d", p.th.Glyphs.Ahead, stat.Commits), p.th.Palette.FgDim))
	}
	return segs
}

// changeSegs returns the right-aligned "what changed" group: the dirty glyph (an
// uncommitted-work marker, dim) fronting the "+adds −dels" diff pair. The pencil
// leads because it qualifies the counts that follow — it answers "is any of this
// still uncommitted?", which the magnitude alone can't say. Either part may be
// absent: a dirty worktree whose diff nets to empty shows the pencil alone, while
// a clean tree with a delta vs base shows counts alone. Empty when neither holds.
func changeSegs(p rowPaint, stat *git.DiffStats) []rowSeg {
	if stat == nil || stat.Error != nil {
		return nil
	}
	var segs []rowSeg
	if stat.Dirty {
		segs = append(segs, p.seg(p.th.Glyphs.Dirty, p.th.Palette.FgDim))
	}
	if diff := diffSegs(p, stat); len(diff) > 0 {
		if len(segs) > 0 {
			segs = append(segs, p.seg(" ", p.th.Palette.FgDim))
		}
		segs = append(segs, diff...)
	}
	return segs
}

// diffSegs returns the "+adds −dels" pair, or nil when the diff is empty. A
// nonzero side keeps its semantic color (additions Success, deletions Danger); a
// zero side renders dim (neutral) instead — a green +0 or a red −0 would flag
// attention at nothing, so it recedes to FgDim (#378). Counts are humanized (see
// humanizeCount) so a large churn doesn't crowd the branch off the line.
func diffSegs(p rowPaint, stat *git.DiffStats) []rowSeg {
	if stat == nil || stat.Error != nil || stat.IsEmpty() {
		return nil
	}
	addColor := p.th.Palette.Success
	if stat.Added == 0 {
		addColor = p.th.Palette.FgDim
	}
	delColor := p.th.Palette.Danger
	if stat.Removed == 0 {
		delColor = p.th.Palette.FgDim
	}
	return []rowSeg{
		p.seg("+"+humanizeCount(stat.Added), addColor),
		p.seg(" ", p.th.Palette.FgDim),
		p.seg("-"+humanizeCount(stat.Removed), delColor),
	}
}

// humanizeCount renders a diff line-count compactly: values under 1000 print
// verbatim, larger ones collapse to a "k" suffix with one decimal place (a
// trailing ".0" dropped). So 18918 reads "18.9k" instead of eating six columns
// on the version-control line, while small, precise counts like 142 stay exact.
func humanizeCount(n int) string {
	if n < 1000 {
		return strconv.Itoa(n)
	}
	s := strconv.FormatFloat(float64(n)/1000.0, 'f', 1, 64)
	return strings.TrimSuffix(s, ".0") + "k"
}

// prCheckGlyph returns a compact CI-state glyph appended to the row's PR chip so
// the pipeline state reads by shape, not color alone (#384): ✗ failing, • pending,
// ✓ passing. A merged PR (chip already purple) and a PR with no checks show
// nothing. The glyph inherits the chip's color, so shape and hue agree — a red
// #12✗ survives the desaturation test that a red #12 alone fails. The glyphs match
// the diff-header check tally (ui/diff.go).
func prCheckGlyph(pr *git.PRStatus) string {
	if pr.State == "MERGED" {
		return ""
	}
	switch pr.CI {
	case git.CIFailing:
		return "✗"
	case git.CIPending:
		return "•"
	case git.CIPassing:
		return "✓"
	default:
		return ""
	}
}

// prSeg returns the "#<number>" PR chip — with a CI-state shape glyph (see
// prCheckGlyph) — colored by the most urgent signal, and whether there is a PR to
// show. When the PR carries a URL the chip becomes an OSC 8 hyperlink to it via
// linkSeg — clickable, with the visible "#<number>" text (and thus the row's width
// math) unchanged.
func prSeg(p rowPaint, pr *git.PRStatus) (rowSeg, bool) {
	if pr == nil || !pr.HasPR {
		return rowSeg{}, false
	}
	label := p.th.Glyphs.PR + fmt.Sprintf("#%d", pr.Number) + prCheckGlyph(pr)
	seg := p.seg(label, prBadgeColor(p.th, pr))
	return linkSeg(seg, pr.URL), true
}

// linkSeg turns s into an OSC 8 hyperlink to url, overriding only its rendered
// bytes: width() still reads s.plain (the visible text, escape-free), so the
// link adds no columns and layout is unchanged. An empty url leaves s untouched.
func linkSeg(s rowSeg, url string) rowSeg {
	if url == "" {
		return s
	}
	s.rendered = hyperlink(url, s.render())
	s.hasRendered = true
	return s
}

// ageSeg returns the faint session-age chip (e.g. "2h", "3d") and whether it is
// non-empty (very fresh sessions render no age).
func (p rowPaint) ageSeg(i *session.Instance) (rowSeg, bool) {
	age := fmtAge(i.CreatedAt)
	if age == "" {
		return rowSeg{}, false
	}
	return p.seg(age, p.th.Palette.FgDim), true
}
