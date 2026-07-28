package overlay

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ZviBaratz/atrium/internal/fuzzy"
	"github.com/ZviBaratz/atrium/ui/theme"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/reflow/truncate"
)

// PaletteAction is one palette row: an action the user can find by typing what it
// does rather than by remembering its key. The overlay renders what it is handed
// and reports which row was chosen by index — the app owns the keymap, the
// context, and what running a row means (see app/palette.go).
type PaletteAction struct {
	// Key is the binding's rendered label ("m", "ctrl-p", "↑/k"), shown on the
	// row so the palette teaches the accelerator it is a substitute for.
	Key string
	// Label is the action's short verb, as the hint bar names it ("merge PR").
	Label string
	// Detail is the cheatsheet's prose for the action — the sentence the ?
	// screen shows. Searched as well as displayed.
	Detail string
	// Group is the cheatsheet section the action belongs to ("Handoff"), used as
	// a header while the list is unfiltered.
	Group string
	// Inert is empty when the action can run against the current selection, and
	// otherwise says why it cannot. An inert row is dimmed and carries its
	// reason — never hidden, and never silently ignored on enter.
	Inert string
}

// runnable reports whether the action can run right now.
func (a PaletteAction) runnable() bool { return a.Inert == "" }

// CommandPaletteOverlay is the fuzzy-over-every-action picker (#374): one key
// opens it, typing narrows it, enter runs the highlighted action in the current
// context. Rows are generated from the keymap registry, so it is a complete index
// of what Atrium can do — and because every row shows its key, it teaches its way
// out of itself.
//
// It embeds the shared Picker (#373) for the filter text, cursor and key grammar,
// and adds only what is specific to actions: the ranking, the grouped layout, and
// the chosen-row report.
type CommandPaletteOverlay struct {
	Picker // shared filter/cursor/key-grammar (sync source: the action list is local)

	actions []PaletteAction
	height  int

	// chosen is the index into actions that enter accepted, or -1. Read once,
	// after HandleKeyPress reports the overlay should close.
	chosen int
}

// NewCommandPaletteOverlay builds the palette over actions, in the order they
// should appear while unfiltered.
func NewCommandPaletteOverlay(actions []PaletteAction) *CommandPaletteOverlay {
	return &CommandPaletteOverlay{
		Picker:  newPicker(false),
		actions: actions,
		height:  24,
		chosen:  -1,
	}
}

// SetSize sets the box dimensions. The floors keep the columns legible rather
// than letting the layout collapse into unreadable slivers on a tiny terminal.
func (p *CommandPaletteOverlay) SetSize(width, height int) {
	if width < 34 {
		width = 34
	}
	if height < 8 {
		height = 8
	}
	p.SetWidth(width)
	p.height = height
}

// HandleKeyPress narrows, moves, runs or closes. It reports true when the overlay
// is finished: check Chosen to tell "ran something" from "changed my mind".
func (p *CommandPaletteOverlay) HandleKeyPress(msg tea.KeyMsg) (shouldClose bool) {
	visible := p.visibleItems()
	switch msg.String() {
	case "esc", "ctrl+c":
		return true
	case "enter":
		if p.cursor < len(visible) {
			p.chosen = visible[p.cursor]
		}
		return true
	}
	p.handleKey(msg, len(visible))
	return false
}

// Chosen reports the action enter accepted, and whether there was one. Rows the
// filter left empty, and an esc, both report false.
func (p *CommandPaletteOverlay) Chosen() (PaletteAction, int, bool) {
	if p.chosen < 0 || p.chosen >= len(p.actions) {
		return PaletteAction{}, -1, false
	}
	return p.actions[p.chosen], p.chosen, true
}

// Query returns the filter text, for tests and for callers that want to echo it.
func (p *CommandPaletteOverlay) Query() string { return p.filter }

// visibleItems returns the indices of the actions matching the filter, best
// first. An empty filter keeps the caller's order, which is the cheatsheet's.
//
// Matching is tiered, and the tier outranks the score. A subsequence match over a
// whole cheatsheet sentence is far too generous — "merge" turns up scattered
// through any long enough sentence — so a hit on the key or the verb (what users
// actually aim at) always sorts above one found only in the prose, which in turn
// sorts above a hit on the section name. The score orders within each tier.
//
// The tiers must also be searched *separately*, not concatenated: gluing the
// group onto the prose lets "Manage" donate the g to "merge" and floods the
// result with actions whose own text never matched at all. Prose stays searchable
// on its own — "worktree" finds pause, whose verb never says so.
func (p *CommandPaletteOverlay) visibleItems() []int {
	if p.filter == "" {
		all := make([]int, len(p.actions))
		for i := range p.actions {
			all[i] = i
		}
		return all
	}

	type scored struct {
		idx, tier, score int
	}
	var hits []scored
	for i, a := range p.actions {
		for tier, field := range []string{a.Key + " " + a.Label, a.Detail, a.Group} {
			if ok, score := fuzzy.Match(p.filter, field); ok {
				hits = append(hits, scored{idx: i, tier: tier, score: score})
				break
			}
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].tier != hits[j].tier {
			return hits[i].tier < hits[j].tier
		}
		return hits[i].score > hits[j].score
	})

	out := make([]int, len(hits))
	for i, h := range hits {
		out[i] = h.idx
	}
	return out
}

// paletteLine is one rendered line: either a group header or an action row. The
// list is flattened this way so the height window can count headers, which
// otherwise push the cursor off the bottom of the box.
type paletteLine struct {
	header string
	action int // index into actions, or -1 for a header
}

// layout flattens the visible actions into lines, inserting group headers while
// unfiltered. Once a query is ranking rows across groups the headers would lie
// about what they contain, so they are dropped.
func (p *CommandPaletteOverlay) layout(visible []int) (lines []paletteLine, cursorLine int) {
	cursorLine = -1
	grouped := p.filter == ""
	lastGroup := ""
	for pos, idx := range visible {
		if grouped && p.actions[idx].Group != lastGroup {
			lastGroup = p.actions[idx].Group
			lines = append(lines, paletteLine{header: lastGroup, action: -1})
		}
		if pos == p.cursor {
			cursorLine = len(lines)
		}
		lines = append(lines, paletteLine{action: idx})
	}
	return lines, cursorLine
}

// columnWidths sizes the key and verb columns to the content, so the rows align
// and a wide terminal shows whole labels instead of the narrowest terminal's
// truncation. The verb column is capped at a third of the inner width: past that
// it starts eating the prose, and the prose is what teaches an action the verb
// only names.
func (p *CommandPaletteOverlay) columnWidths(inner int) (keyWidth, labelWidth int) {
	for _, a := range p.actions {
		if w := lipgloss.Width(a.Key); w > keyWidth {
			keyWidth = w
		}
		if w := lipgloss.Width(a.Label); w > labelWidth {
			labelWidth = w
		}
	}
	if budget := inner / 3; labelWidth > budget {
		labelWidth = budget
	}
	if labelWidth < paletteMinLabelWidth {
		labelWidth = paletteMinLabelWidth
	}
	return keyWidth, labelWidth
}

// Render draws the bordered palette.
func (p *CommandPaletteOverlay) Render() string {
	th := theme.Current()
	// lipgloss sizes the content box, and the border is drawn *outside* it — so the
	// style takes width-2 for the box to occupy width columns on screen. SetSize
	// means the room the palette may take, which is the only reading a caller
	// passing a terminal fraction can act on.
	box := lipgloss.NewStyle().
		Border(th.Borders.Style).
		BorderForeground(th.Palette.Accent).
		Padding(1, 2).
		Width(p.width - 2)

	inner := p.width - 6 // border (2) + horizontal padding (2*2)
	if inner < 20 {
		inner = 20
	}

	visible := p.visibleItems()
	lines, cursorLine := p.layout(visible)

	var b strings.Builder
	b.WriteString(th.OverlayTitleStyle().Render("Command Palette") + "\n")
	b.WriteString(p.renderQueryLine(inner, len(visible)) + "\n\n")

	// Reserve rows for the title, the query line, their blank, the footer and its
	// blank. What is left is the list.
	rows := p.height - 7
	if rows < 3 {
		rows = 3
	}
	if len(visible) == 0 {
		b.WriteString(overlayDimStyle().Render("  no action matches "+quoted(p.filter)) + "\n\n")
	} else {
		start := windowStart(len(lines), cursorLine, rows)
		end := start + rows
		if end > len(lines) {
			end = len(lines)
		}
		for i := start; i < end; i++ {
			b.WriteString(p.renderLine(lines[i], lines[i].action == visible[p.cursor], inner) + "\n")
		}
		if end < len(lines) {
			b.WriteString(overlayDimStyle().Render(fmt.Sprintf("  … %d more", len(lines)-end)) + "\n")
		}
		b.WriteString("\n")
	}

	b.WriteString(th.OverlayHintStyle().Render("type to filter · ↑↓ move · ↵ run · esc close"))
	return box.Render(b.String())
}

// renderQueryLine shows the live query and how much of the keymap survives it.
func (p *CommandPaletteOverlay) renderQueryLine(width, matches int) string {
	th := theme.Current()
	count := fmt.Sprintf("%d actions", len(p.actions))
	if p.filter != "" {
		count = fmt.Sprintf("%d of %d", matches, len(p.actions))
	}
	query := overlayFilterStyle().Render(p.filter) + th.AccentStyle().Render("▏")
	line := query + "  " + overlayDimStyle().Render(count)
	return truncate.StringWithTail(line, uint(width), "…")
}

// renderLine draws a group header or an action row.
func (p *CommandPaletteOverlay) renderLine(line paletteLine, selected bool, width int) string {
	if line.action < 0 {
		return overlayLabelStyle().Render("  " + line.header)
	}
	return p.renderRow(p.actions[line.action], selected, width)
}

// renderRow lays out one action: key column, label column, then as much of the
// prose (or the inert reason, which outranks it — a row that cannot run owes the
// user why, not what it would have done) as the width allows.
func (p *CommandPaletteOverlay) renderRow(a PaletteAction, selected bool, width int) string {
	th := theme.Current()

	keyWidth, labelWidth := p.columnWidths(width)
	label := truncate.StringWithTail(a.Label, uint(labelWidth), "…")
	prefix := fmt.Sprintf("%-*s  %-*s", keyWidth, a.Key, labelWidth, label)

	tail := a.Detail
	tailStyle := overlayDimStyle()
	if !a.runnable() {
		tail = a.Inert
		tailStyle = th.FaintStyle()
	}
	// The prose is the first thing to go: the key and the verb are what make the
	// row actionable, and a narrow terminal must keep those.
	tailWidth := width - lipgloss.Width(prefix) - 4
	if tailWidth < paletteMinTailWidth {
		tail = ""
	} else {
		tail = truncate.StringWithTail(tail, uint(tailWidth), "…")
	}

	if selected {
		row := prefix
		if tail != "" {
			row += "  " + tail
		}
		return overlaySelectedStyle().Render(truncate.StringWithTail("▸ "+row, uint(width), "…"))
	}
	row := th.FgStyle().Render(prefix)
	if !a.runnable() {
		row = th.FaintStyle().Render(prefix)
	}
	if tail != "" {
		row += "  " + tailStyle.Render(tail)
	}
	return "  " + row
}

const (
	// paletteMinLabelWidth floors the verb column on a terminal too narrow for a
	// third of its width to hold a verb — below this the column stops carrying
	// enough letters to tell two actions apart.
	paletteMinLabelWidth = 12
	// paletteMinTailWidth is the point below which the prose column is dropped
	// rather than shown as an ellipsis and two letters.
	paletteMinTailWidth = 12
)

// windowStart returns the first line to render so that cursorLine stays visible
// within rows lines, preferring to keep the list anchored at the top.
func windowStart(total, cursorLine, rows int) int {
	if cursorLine < 0 || total <= rows {
		return 0
	}
	if cursorLine < rows {
		return 0
	}
	start := cursorLine - rows + 1
	if start > total-rows {
		start = total - rows
	}
	if start < 0 {
		start = 0
	}
	return start
}

// quoted renders a query for an error line, keeping an empty one readable.
func quoted(s string) string {
	if s == "" {
		return "the filter"
	}
	return "\"" + s + "\""
}
