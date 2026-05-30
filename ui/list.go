package ui

import (
	"errors"
	"fmt"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui/theme"
	"path/filepath"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

// Row/header/selection styles read the active theme at render time.
func repoHeaderStyle() lipgloss.Style { return theme.Current().DimStyle().Bold(true).Padding(0, 1) }
func repoRuleStyle() lipgloss.Style   { return theme.Current().FaintStyle() }

// selectedItemStyle draws a left accent bar down the selected row; unselected
// rows get matching left padding so text stays aligned as the selection moves.
func selectedItemStyle() lipgloss.Style {
	return lipgloss.NewStyle().
		Border(lipgloss.Border{Left: theme.Current().Glyphs.SelectionMark}, false, false, false, true).
		BorderForeground(theme.Current().Palette.Accent)
}

// repoKey returns the grouping key for an instance: its repo name once started,
// falling back to the base name of its repo path for not-yet-started instances.
func repoKey(i *session.Instance) string {
	if name, err := i.RepoName(); err == nil {
		return name
	}
	return filepath.Base(i.Path)
}

// distinctRepoCount returns how many distinct repo groups are present across the current
// items, computed from the items themselves via repoKey. Deriving it at render time (rather
// than tracking an incrementally-maintained counter) makes it impossible for the header
// decision to drift from what is actually displayed — the counter could fall out of sync
// during churn, since it was only updated when an instance was started.
func (l *List) distinctRepoCount() int {
	seen := make(map[string]struct{}, len(l.items))
	for _, item := range l.items {
		seen[repoKey(item)] = struct{}{}
	}
	return len(seen)
}

// renderRepoHeader renders a repo group header as a fold marker, the uppercased name (with a
// member count when collapsed), and a dim rule filling the rest of the panel width, so it
// reads as a section divider. A collapsed group's header doubles as its selectable row, so it
// gets the same left accent bar as a selected item when selected is true.
//
// needsInput is how many sessions in the group are blocked on user input. When the group is
// collapsed (so its member rows — and their per-row waiting glyphs — are hidden) and the count
// is non-zero, a "◆N" badge is appended in the attention color so the group still signals that
// it needs attention without being expanded.
func (l *List) renderRepoHeader(key string, collapsed bool, count, needsInput int, selected bool) string {
	g := theme.Current().Glyphs
	marker := g.FoldOpen + " "
	if collapsed {
		marker = g.FoldClosed + " "
	}
	name := marker + strings.ToUpper(key)
	badge := ""
	if collapsed {
		name = fmt.Sprintf("%s (%d)", name, count)
		if needsInput > 0 {
			badge = fmt.Sprintf("%s%d", g.Waiting, needsInput)
		}
	}
	header := repoHeaderStyle().Render(name)
	// repoHeaderStyle pads the name with one space on each side; a selected header also gains
	// a one-cell left accent bar, so reserve for both when sizing the trailing rule (hence -2,
	// and an extra -1 when selected). The badge sits in the padding's right space and is
	// followed by one separator space before the rule, so it consumes its plain-text width
	// (its ANSI styling adds no columns) plus that one space.
	ruleLen := l.renderer.width - runewidth.StringWidth(name) - 2
	if badge != "" {
		ruleLen -= runewidth.StringWidth(badge) + 1
	}
	if selected {
		ruleLen--
	}
	if ruleLen < 0 {
		ruleLen = 0
	}
	line := header
	if badge != "" {
		line += theme.Current().AttentionStyle().Render(badge) + " "
	}
	line += repoRuleStyle().Render(strings.Repeat("─", ruleLen))
	if selected {
		return selectedItemStyle().Render(line)
	}
	return line
}

// visibleCount returns how many items in the half-open range [start, end) are not hidden.
// Used to decide whether a (non-collapsed) group has any rows to render under the active filter.
func (l *List) visibleCount(start, end int) int {
	n := 0
	for j := start; j < end; j++ {
		if !l.isHidden(j) {
			n++
		}
	}
	return n
}

// groupNeedsInputCount returns how many sessions in the half-open item range [start, end) are
// blocked on user input. Used to badge a collapsed repo-group header, whose member rows would
// otherwise carry the per-row waiting glyph.
func (l *List) groupNeedsInputCount(start, end int) int {
	n := 0
	for _, item := range l.items[start:end] {
		if item.GetStatus() == session.NeedsInput {
			n++
		}
	}
	return n
}

// filterBarStyle renders the incremental search bar that appears below the list header
// when a filter is active.
var filterBarStyle = lipgloss.NewStyle().
	Foreground(lipgloss.AdaptiveColor{Light: "#555555", Dark: "#aaaaaa"})

var filterBarActiveStyle = lipgloss.NewStyle().
	Foreground(lipgloss.AdaptiveColor{Light: "#1a1a1a", Dark: "#ffffff"})

type List struct {
	items         []*session.Instance
	selectedIdx   int
	height, width int
	renderer      *InstanceRenderer
	autoyes       bool
	// collapsed records which repo groups are folded, keyed by repoKey. It is a pure
	// display/navigation flag — never authoritative over membership or order, which stay
	// derived from items. All reads go through effectiveCollapsed so the "only meaningful
	// with >1 repo" rule is enforced in exactly one place.
	collapsed map[string]bool

	// filterQuery is the current incremental filter string. Items whose DisplayName and
	// Branch both fail a case-insensitive contains check are hidden exactly like collapsed
	// group members — isHidden returns true for them. An empty string disables filtering.
	filterQuery string
	// filterActive is true while the user is actively typing the filter (stateFilter in
	// app.go). It controls the cursor indicator in the filter bar.
	filterActive bool
}

func NewList(spinner *spinner.Model, autoYes bool) *List {
	return &List{
		items:     []*session.Instance{},
		renderer:  &InstanceRenderer{spinner: spinner},
		autoyes:   autoYes,
		collapsed: map[string]bool{},
	}
}

// SetFilter updates the incremental filter query and clamps the selection to the
// nearest still-visible item. Pass an empty string to disable filtering.
func (l *List) SetFilter(query string) {
	l.filterQuery = query
	l.clampSelectionToNavigable()
}

// ClearFilter resets both the filter query and the active state.
func (l *List) ClearFilter() {
	l.filterQuery = ""
	l.filterActive = false
	l.clampSelectionToNavigable()
}

// FilterQuery returns the current filter string.
func (l *List) FilterQuery() string { return l.filterQuery }

// SetFilterActive sets whether the user is currently typing the filter. This drives
// the cursor indicator in the rendered filter bar.
func (l *List) SetFilterActive(active bool) { l.filterActive = active }

// filterMatches reports whether an instance should be shown given the current filter.
// The match is a case-insensitive substring check against DisplayName and Branch.
func (l *List) filterMatches(i *session.Instance) bool {
	if l.filterQuery == "" {
		return true
	}
	q := strings.ToLower(l.filterQuery)
	return strings.Contains(strings.ToLower(i.DisplayName()), q) ||
		strings.Contains(strings.ToLower(i.Branch), q)
}

// SetSize sets the OUTER height and width of the list (including its panel
// border). Rows render to the inner width (width-2).
func (l *List) SetSize(width, height int) {
	l.width = width
	l.height = height
	l.renderer.setWidth(width - 2)
}

// SetSessionPreviewSize sets the height and width for the tmux sessions. This makes the stdout line have the correct
// width and height.
func (l *List) SetSessionPreviewSize(width, height int) (err error) {
	for i, item := range l.items {
		if !item.Started() || item.Paused() {
			continue
		}

		if innerErr := item.SetPreviewSize(width, height); innerErr != nil {
			err = errors.Join(
				err, fmt.Errorf("could not set preview size for instance %d: %w", i, innerErr))
		}
	}
	return
}

func (l *List) NumInstances() int {
	return len(l.items)
}

// InstanceRenderer handles rendering of session.Instance objects
type InstanceRenderer struct {
	spinner *spinner.Model
	width   int
}

func (r *InstanceRenderer) setWidth(width int) {
	if width < 1 {
		width = 1
	}
	r.width = width
}

// stateParts returns the glyph, word, and color describing an instance's status.
// Running/Loading use the animated spinner frame; the others use theme glyphs.
func (r *InstanceRenderer) stateParts(i *session.Instance, th *theme.Theme) (glyph, word string, color lipgloss.Color) {
	switch i.GetStatus() {
	case session.Running:
		return r.spinner.View(), "working", th.Palette.Working
	case session.Loading:
		return r.spinner.View(), "starting", th.Palette.Working
	case session.Ready:
		return th.Glyphs.Ready, "ready", th.Palette.Success
	case session.NeedsInput:
		return th.Glyphs.Waiting, "waiting", th.Palette.Attention
	case session.Paused:
		return th.Glyphs.Paused, "paused", th.Palette.FgDim
	default:
		return " ", "", th.Palette.FgDim
	}
}

// Render draws a session as two lines: an identity/state line (name + a
// right-aligned state word) and a dim version-control line (branch, ahead/
// behind/dirty, and a right-aligned diff stat). The selected row carries a left
// accent bar and a subtle filled background. idx is unused (kept for the List
// caller's signature).
func (r *InstanceRenderer) Render(i *session.Instance, idx int, selected bool) string {
	_ = idx
	th := theme.Current()
	g := th.Glyphs

	// The selected row carries a subtle filled background. Because an ANSI reset
	// at the end of any styled segment also clears the background, the row bg must
	// be baked into every segment (and gap) rather than wrapped around the line —
	// otherwise the fill drops out after the first reset. seg/pad do that; for an
	// unselected row bg is NoColor, so they're plain.
	var bg lipgloss.TerminalColor = lipgloss.NoColor{}
	if selected {
		bg = th.Palette.BgElevated
	}
	seg := func(c lipgloss.Color) lipgloss.Style { return lipgloss.NewStyle().Foreground(c).Background(bg) }
	pad := func(n int) string {
		if n < 0 {
			n = 0
		}
		return lipgloss.NewStyle().Background(bg).Render(strings.Repeat(" ", n))
	}

	// One column is reserved for the left marker/pad; build content to W.
	W := r.width - 1
	if W < 1 {
		W = 1
	}

	// --- Line 1: name (left) · state word (right) ---
	glyph, word, stateColor := r.stateParts(i, th)
	rightPlain := glyph + " " + word
	rightStyled := seg(stateColor).Render(glyph) + pad(1) + seg(stateColor).Render(word)

	// Per-session AUTO badge (not while paused) so "yolo" state is unmistakable.
	if i.AutoYes && !i.Paused() {
		badge := " " + g.AutoBadge + "AUTO "
		rightPlain = badge + " " + rightPlain
		rightStyled = th.BadgeStyle().Render(badge) + pad(1) + rightStyled
	}

	nameColor := th.Palette.Fg
	if i.GetStatus() == session.NeedsInput {
		nameColor = th.Palette.Attention // the one state that wants attention
	}
	nameStyle := seg(nameColor)
	if selected {
		nameStyle = nameStyle.Bold(true)
	}
	name := i.DisplayName()
	rightW := runewidth.StringWidth(rightPlain)
	nameAvail := W - rightW - 1
	if nameAvail < 1 {
		nameAvail = 1
	}
	if runewidth.StringWidth(name) > nameAvail {
		name = runewidth.Truncate(name, nameAvail, "…")
	}
	gap1 := W - runewidth.StringWidth(name) - rightW
	if gap1 < 1 {
		gap1 = 1
	}
	line1 := nameStyle.Render(name) + pad(gap1) + rightStyled

	// --- Line 2: branch + git context (left) · diff stat (right), dim ---
	stat := i.GetDiffStats()

	var gctxPlain, gctxStyled string
	var diffPlain, diffStyled string
	if stat != nil && stat.Error == nil {
		if stat.Behind > 0 {
			s := " " + g.Behind + fmt.Sprintf("%d", stat.Behind)
			gctxPlain += s
			gctxStyled += seg(th.Palette.Attention).Render(s) // behind implies a rebase: attention
		}
		if stat.Commits > 0 {
			s := " " + g.Ahead + fmt.Sprintf("%d", stat.Commits)
			gctxPlain += s
			gctxStyled += seg(th.Palette.FgDim).Render(s)
		}
		if stat.Dirty {
			s := " " + g.Dirty
			gctxPlain += s
			gctxStyled += seg(th.Palette.FgDim).Render(s)
		}
		if !stat.IsEmpty() {
			added := fmt.Sprintf("+%d", stat.Added)
			removed := fmt.Sprintf("-%d", stat.Removed)
			diffPlain = added + " " + removed
			diffStyled = seg(th.Palette.Success).Render(added) + pad(1) + seg(th.Palette.Danger).Render(removed)
		}
	}

	// Budget the branch (the only variable-length part) so the line fits W.
	fixedW := runewidth.StringWidth(g.Branch+" ") + runewidth.StringWidth(gctxPlain) + runewidth.StringWidth(diffPlain)
	branchBudget := W - fixedW - 1 // 1 = min gap before the diff stat
	branch := i.Branch
	if branchBudget < 1 {
		branch = ""
	} else if runewidth.StringWidth(branch) > branchBudget {
		branch = runewidth.Truncate(branch, branchBudget, "…")
	}
	leftPlain := g.Branch + " " + branch + gctxPlain
	leftStyled := seg(th.Palette.FgDim).Render(g.Branch+" "+branch) + gctxStyled
	gap2 := W - runewidth.StringWidth(leftPlain) - runewidth.StringWidth(diffPlain)
	if gap2 < 1 {
		gap2 = 1
	}
	line2 := leftStyled + pad(gap2) + diffStyled

	// --- Left marker (accent bar when selected) + compose ---
	marker := pad(1)
	if selected {
		marker = seg(th.Palette.Accent).Render(g.SelectionMark)
	}
	return lipgloss.JoinVertical(lipgloss.Left, marker+line1, marker+line2)
}

func (l *List) String() string {
	// The list title and global state moved to the top status bar; the list is
	// now a pure (scrollable) stream of repo groups and session rows.
	// Build the list as a flat slice of lines (each row is two lines; headers one;
	// a blank line separates groups), tracking the selected block's line range so
	// the viewport can scroll to keep it visible.
	var lines []string
	selStart, selH := -1, 0
	appendBlock := func(s string) int {
		start := len(lines)
		lines = append(lines, strings.Split(s, "\n")...)
		return start
	}

	// Render the filter bar as the first line(s) when a query is present or the user is
	// actively typing. Appending here keeps appendBlock's selStart/selH bookkeeping correct
	// for the rows that follow.
	if l.filterQuery != "" || l.filterActive {
		cursor := ""
		if l.filterActive {
			cursor = "▌"
		}
		style := filterBarStyle
		if l.filterActive {
			style = filterBarActiveStyle
		}
		lines = append(lines, style.Render(" / "+l.filterQuery+cursor), "")
	}

	// Render the list group by group, in the user's existing (reorderable) order. Headers are
	// shown only with more than one repo, and are not selectable rows for expanded groups, so
	// selectedIdx stays a flat index into l.items. A collapsed group renders only its header
	// (which doubles as its anchor's row) and suppresses its members.
	// An active filter is the sole visibility gate and overrides collapse (see isHidden), so a
	// folded group expands to reveal its matches while filtering.
	filtering := l.filterQuery != ""
	showRepos := l.distinctRepoCount() > 1
	first := true
	for i := 0; i < len(l.items); {
		key := repoKey(l.items[i])
		start, end := l.groupBounds(i)
		collapsed := showRepos && l.collapsed[key] && !filtering

		// A filter can hide every member of a group; such a group renders neither its header nor
		// a separating blank line. A collapsed group is always represented (by its header).
		if !collapsed && l.visibleCount(start, end) == 0 {
			i = end
			continue
		}

		// Looser spacing before each rendered group (one blank line); items within a group are adjacent.
		if !first {
			lines = append(lines, "")
		}
		first = false

		if showRepos {
			headerSelected := collapsed && l.selectedIdx == start
			ni := l.groupNeedsInputCount(start, end)
			at := appendBlock(l.renderRepoHeader(key, collapsed, end-start, ni, headerSelected))
			if headerSelected {
				selStart, selH = at, len(lines)-at
			}
		}
		if !collapsed {
			for j := start; j < end; j++ {
				if l.isHidden(j) {
					continue
				}
				at := appendBlock(l.renderer.Render(l.items[j], j+1, j == l.selectedIdx))
				if j == l.selectedIdx {
					selStart, selH = at, len(lines)-at
				}
			}
		}
		i = end
	}

	// `first` is still set only if no group rendered any row, i.e. the query matched nothing.
	// Show an explicit hint so the empty list is not mistaken for "no sessions exist".
	if filtering && first {
		lines = append(lines, filterBarStyle.Render("   no matches"))
	}

	// Inner content area inside the panel border (2 cols / 2 rows of chrome).
	innerH := l.height - 2
	if innerH < 1 {
		innerH = 1
	}
	lines = l.windowLines(lines, selStart, selH, innerH)
	content := strings.Join(lines, "\n")

	// The list is the primary navigation surface, so its panel is always drawn
	// active (accent border). A dynamic focus model can flip this later.
	return theme.Current().Panel("Sessions", content, l.width, l.height, true)
}

// windowLines clips lines to the list height, scrolling so the selected block
// ([selStart, selStart+selH)) stays visible with a one-line margin from either
// edge. When content is clipped, the top/bottom visible line becomes a faint
// "↑/↓ N more" indicator (only shown when there is actually more in that
// direction, so the selection is never hidden behind one).
func (l *List) windowLines(lines []string, selStart, selH, avail int) []string {
	if avail < 1 {
		avail = 1
	}
	if len(lines) <= avail {
		return lines
	}

	offset := 0
	if selStart >= 0 {
		selEnd := selStart + selH
		if selEnd+1 > offset+avail {
			offset = selEnd + 1 - avail
		}
		if selStart-1 < offset {
			offset = selStart - 1
		}
	}
	if offset > len(lines)-avail {
		offset = len(lines) - avail
	}
	if offset < 0 {
		offset = 0
	}

	window := make([]string, avail)
	copy(window, lines[offset:offset+avail])
	faint := repoRuleStyle()
	if offset > 0 {
		window[0] = faint.Render(fmt.Sprintf("  ↑ %d more", offset))
	}
	if below := len(lines) - (offset + avail); below > 0 {
		window[avail-1] = faint.Render(fmt.Sprintf("  ↓ %d more", below))
	}
	return window
}

// Down selects the next visible item in the list, wrapping at the end and skipping the hidden
// members of collapsed groups.
func (l *List) Down() {
	if len(l.items) == 0 {
		return
	}
	idx := l.selectedIdx
	for i := 0; i < len(l.items); i++ {
		if idx < len(l.items)-1 {
			idx++
		} else {
			idx = 0
		}
		if !l.isHidden(idx) {
			l.selectedIdx = idx
			return
		}
	}
}

// Kill tears down the selected instance and removes it from the list.
func (l *List) Kill() {
	if len(l.items) == 0 {
		return
	}
	l.KillInstance(l.items[l.selectedIdx])
}

// KillInstance tears down target and removes it from the list, keeping the
// selection pointing at the same logical instance where possible. Unlike Kill,
// target need not be the selected item — the in-session kill path (Ctrl+X) and
// the auto-open path target a specific instance regardless of current selection.
func (l *List) KillInstance(target *session.Instance) {
	idx := -1
	for i, item := range l.items {
		if item == target {
			idx = i
			break
		}
	}
	if idx == -1 {
		return
	}

	// Kill the tmux session and clean up the worktree.
	if err := target.Kill(); err != nil {
		log.ErrorLog.Printf("could not kill instance: %v", err)
	}

	// If the selected item is the last one and we're removing it, select the
	// previous one so the selection doesn't fall off the end.
	if l.selectedIdx == idx && idx == len(l.items)-1 {
		defer l.Up()
	}

	l.items = append(l.items[:idx], l.items[idx+1:]...)

	// Removing an item before the selection shifts every later index down by one;
	// follow the selection so it keeps pointing at the same instance.
	if idx < l.selectedIdx {
		l.selectedIdx--
	}

	// Removing an item can still shift the selection onto a now-hidden index (or
	// off the end), so re-establish the navigable-selection invariant.
	l.clampSelectionToNavigable()
}

func (l *List) Attach() (chan struct{}, error) {
	targetInstance := l.items[l.selectedIdx]
	return targetInstance.Attach()
}

// Up selects the prev visible item in the list, wrapping at the top and skipping the hidden
// members of collapsed groups.
func (l *List) Up() {
	if len(l.items) == 0 {
		return
	}
	idx := l.selectedIdx
	for i := 0; i < len(l.items); i++ {
		if idx > 0 {
			idx--
		} else {
			idx = len(l.items) - 1
		}
		if !l.isHidden(idx) {
			l.selectedIdx = idx
			return
		}
	}
}

// AddInstance adds a new instance to the list. It returns a finalizer function that callers
// invoke once the instance has started (restored/paused instances may call it immediately;
// the new-session flow calls it once the name is entered). The finalizer is currently a
// no-op — repo grouping is derived from the items at render time (see distinctRepoCount), so
// there is no per-instance state to register on start — but it is retained as the start-time
// lifecycle hook the new-session flow is built around.
//
// The instance is inserted immediately after the last existing item sharing its repoKey, so it becomes the next entry
// in that repo's group; if no item shares the key, it is appended as a new group. This keeps same-repo instances
// contiguous, which is the invariant String() relies on to avoid emitting a repo header more than once per group. It
// also self-migrates state loaded from storage, which is added one instance at a time through this method.
func (l *List) AddInstance(instance *session.Instance) (finalize func()) {
	key := repoKey(instance)
	insertAt := len(l.items)
	for i, item := range l.items {
		if repoKey(item) == key {
			insertAt = i + 1
		}
	}
	l.items = append(l.items, nil)
	copy(l.items[insertAt+1:], l.items[insertAt:])
	l.items[insertAt] = instance
	// A newly added session must never land hidden inside a folded group, so expand its group.
	// During startup restore the collapsed set is still empty (it is applied after this loop),
	// so this is a no-op there and doesn't clobber persisted folds.
	delete(l.collapsed, key)
	// Keep the selection on the same logical item if we inserted at or before it.
	if insertAt <= l.selectedIdx && len(l.items) > 1 {
		l.selectedIdx++
	}
	return func() {}
}

// GetSelectedInstance returns the currently selected instance
func (l *List) GetSelectedInstance() *session.Instance {
	if len(l.items) == 0 {
		return nil
	}
	return l.items[l.selectedIdx]
}

// SetSelectedInstance sets the selected index. Noop if the index is out of bounds.
func (l *List) SetSelectedInstance(idx int) {
	if idx >= len(l.items) {
		return
	}
	l.selectedIdx = idx
}

// SelectInstance finds and selects the given instance in the list. If the target sits inside a
// collapsed group, the selection snaps to that group's anchor so the cursor stays visible.
func (l *List) SelectInstance(target *session.Instance) {
	for i, inst := range l.items {
		if inst == target {
			l.SetSelectedInstance(i)
			l.clampSelectionToNavigable()
			return
		}
	}
}

// MoveUp swaps the selected instance with the one above it. The swap is confined to within a repo group: if the item
// above belongs to a different group, this is a no-op so the move cannot split a group and produce a duplicate header.
// (Single-repo lists share one key, so reordering stays unrestricted there.)
func (l *List) MoveUp() bool {
	if l.selectedIdx <= 0 || len(l.items) < 2 {
		return false
	}
	// A collapsed group shows no siblings to swap with, so within-group reorder is inert.
	if l.effectiveCollapsed(repoKey(l.items[l.selectedIdx])) {
		return false
	}
	if repoKey(l.items[l.selectedIdx]) != repoKey(l.items[l.selectedIdx-1]) {
		return false
	}
	l.items[l.selectedIdx], l.items[l.selectedIdx-1] = l.items[l.selectedIdx-1], l.items[l.selectedIdx]
	l.selectedIdx--
	return true
}

// MoveDown swaps the selected instance with the one below it. As with MoveUp, the swap is confined to within a repo
// group so it cannot split a group across a boundary.
func (l *List) MoveDown() bool {
	if l.selectedIdx >= len(l.items)-1 || len(l.items) < 2 {
		return false
	}
	if l.effectiveCollapsed(repoKey(l.items[l.selectedIdx])) {
		return false
	}
	if repoKey(l.items[l.selectedIdx]) != repoKey(l.items[l.selectedIdx+1]) {
		return false
	}
	l.items[l.selectedIdx], l.items[l.selectedIdx+1] = l.items[l.selectedIdx+1], l.items[l.selectedIdx]
	l.selectedIdx++
	return true
}

// groupBounds returns the [start, end) range of the contiguous run of items sharing the
// repoKey of the item at idx — i.e. the repo group idx belongs to. Returns an empty range
// for an out-of-bounds idx. This is the single primitive the whole-group operations build on.
func (l *List) groupBounds(idx int) (start, end int) {
	if idx < 0 || idx >= len(l.items) {
		return idx, idx
	}
	key := repoKey(l.items[idx])
	start = idx
	for start > 0 && repoKey(l.items[start-1]) == key {
		start--
	}
	end = idx + 1
	for end < len(l.items) && repoKey(l.items[end]) == key {
		end++
	}
	return start, end
}

// effectiveCollapsed reports whether a group is folded *and* folding is meaningful. Folding
// is only meaningful when more than one repo is present (headers don't render otherwise), so
// this guard lives here and every collapse read goes through it — preventing a stale flag from
// hiding the sole remaining group after others are killed.
func (l *List) effectiveCollapsed(key string) bool {
	return l.distinctRepoCount() > 1 && l.collapsed[key]
}

// isHidden reports whether the item at idx is suppressed from view. While a filter query is
// active it is the sole visibility gate — an item is hidden iff it does not match — and it
// takes precedence over collapse so matches buried inside a folded group still surface.
// With no active filter, an item is hidden when it is a non-anchor member of a collapsed group.
func (l *List) isHidden(idx int) bool {
	if l.filterQuery != "" {
		return !l.filterMatches(l.items[idx])
	}
	if !l.effectiveCollapsed(repoKey(l.items[idx])) {
		return false
	}
	start, _ := l.groupBounds(idx)
	return idx != start
}

// clampSelectionToNavigable enforces the invariant that the selection always rests on a
// visible item. This is the single place that invariant is maintained, so every mutation just
// calls it afterward.
func (l *List) clampSelectionToNavigable() {
	if len(l.items) == 0 {
		l.selectedIdx = 0
		return
	}
	if l.selectedIdx < 0 {
		l.selectedIdx = 0
	} else if l.selectedIdx >= len(l.items) {
		l.selectedIdx = len(l.items) - 1
	}
	if !l.isHidden(l.selectedIdx) {
		return
	}
	// Prefer the group anchor: for a collapsed group it is always visible and stands in for the
	// whole group. Under an active filter the anchor may itself be filtered out, so fall back to
	// the nearest visible item in either direction. If nothing matches, leave the selection put.
	if anchor, _ := l.groupBounds(l.selectedIdx); !l.isHidden(anchor) {
		l.selectedIdx = anchor
		return
	}
	if idx := l.nearestNavigable(l.selectedIdx); idx >= 0 {
		l.selectedIdx = idx
	}
}

// nearestNavigable returns the index of the closest non-hidden item to from, searching outward
// in both directions (preferring the item below on ties), or -1 when every item is hidden.
func (l *List) nearestNavigable(from int) int {
	for d := 1; d < len(l.items); d++ {
		if i := from + d; i < len(l.items) && !l.isHidden(i) {
			return i
		}
		if i := from - d; i >= 0 && !l.isHidden(i) {
			return i
		}
	}
	if !l.isHidden(from) {
		return from
	}
	return -1
}

// ToggleCollapse folds or unfolds the selected session's repo group. It is a no-op (returns
// false) when fewer than two repos are present, since folding is meaningless there.
func (l *List) ToggleCollapse() bool {
	if len(l.items) == 0 || l.distinctRepoCount() <= 1 {
		return false
	}
	key := repoKey(l.items[l.selectedIdx])
	if l.collapsed[key] {
		delete(l.collapsed, key)
	} else {
		l.collapsed[key] = true
	}
	l.clampSelectionToNavigable()
	return true
}

// ToggleCollapseAll folds every group if any is currently expanded, otherwise unfolds every
// group. No-op (returns false) with fewer than two repos.
func (l *List) ToggleCollapseAll() bool {
	if len(l.items) == 0 || l.distinctRepoCount() <= 1 {
		return false
	}
	anyExpanded := false
	for i := 0; i < len(l.items); {
		_, end := l.groupBounds(i)
		if !l.collapsed[repoKey(l.items[i])] {
			anyExpanded = true
		}
		i = end
	}
	if anyExpanded {
		for i := 0; i < len(l.items); {
			_, end := l.groupBounds(i)
			l.collapsed[repoKey(l.items[i])] = true
			i = end
		}
	} else {
		l.collapsed = map[string]bool{}
	}
	l.clampSelectionToNavigable()
	return true
}

// CollapsedRepos returns the collapsed repo keys still present in the list, sorted for stable
// output. Pruning to live keys happens here (at save time) only — never on load, where the
// instance set is still being assembled.
func (l *List) CollapsedRepos() []string {
	present := map[string]struct{}{}
	for _, item := range l.items {
		present[repoKey(item)] = struct{}{}
	}
	keys := make([]string, 0, len(l.collapsed))
	for k := range l.collapsed {
		if _, ok := present[k]; ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

// SetCollapsedRepos replaces the collapsed set (used to restore persisted state on startup).
func (l *List) SetCollapsedRepos(keys []string) {
	l.collapsed = make(map[string]bool, len(keys))
	for _, k := range keys {
		l.collapsed[k] = true
	}
	l.clampSelectionToNavigable()
}

// MoveGroupUp moves the selected session's entire repo group above the group immediately
// preceding it, as a unit, keeping the same session selected. It is a no-op when the group is
// already first (which also covers the single-group case). Returns whether anything moved.
func (l *List) MoveGroupUp() bool {
	start, end := l.groupBounds(l.selectedIdx)
	if start <= 0 {
		return false
	}
	prevStart, _ := l.groupBounds(start - 1)
	sel := l.items[l.selectedIdx]
	reordered := make([]*session.Instance, 0, len(l.items))
	reordered = append(reordered, l.items[:prevStart]...)
	reordered = append(reordered, l.items[start:end]...)
	reordered = append(reordered, l.items[prevStart:start]...)
	reordered = append(reordered, l.items[end:]...)
	l.items = reordered
	l.SelectInstance(sel)
	return true
}

// MoveGroupDown moves the selected session's entire repo group below the group immediately
// following it, as a unit, keeping the same session selected. It is a no-op when the group is
// already last (which also covers the single-group case). Returns whether anything moved.
func (l *List) MoveGroupDown() bool {
	start, end := l.groupBounds(l.selectedIdx)
	if end >= len(l.items) {
		return false
	}
	_, nextEnd := l.groupBounds(end)
	sel := l.items[l.selectedIdx]
	reordered := make([]*session.Instance, 0, len(l.items))
	reordered = append(reordered, l.items[:start]...)
	reordered = append(reordered, l.items[end:nextEnd]...)
	reordered = append(reordered, l.items[start:end]...)
	reordered = append(reordered, l.items[nextEnd:]...)
	l.items = reordered
	l.SelectInstance(sel)
	return true
}

// GetInstances returns all instances in the list
func (l *List) GetInstances() []*session.Instance {
	return l.items
}
