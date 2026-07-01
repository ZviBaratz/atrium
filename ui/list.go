package ui

import (
	"errors"
	"fmt"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui/theme"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	zone "github.com/lrstanley/bubblezone"
	"github.com/mattn/go-runewidth"
)

// listRowZoneID is the bubblezone marker id for a session row. It is keyed by
// group + title — Title alone is only unique within a repo group, and two
// same-titled rows sharing an id would route clicks to whichever registered
// first. NUL can appear in neither part, so the join is unambiguous. Stale zones
// from removed sessions are never queried (InstanceAtZone only checks rows
// currently in the list).
func listRowZoneID(i *session.Instance) string {
	return "list-row-" + i.GroupKey() + "\x00" + i.Title
}

// listHeaderZoneID is the bubblezone marker id for a repo-group header row,
// keyed by the group's repoKey. Like row zones, only keys currently present are
// ever queried (HeaderAtZone walks the live groups).
func listHeaderZoneID(key string) string { return "list-header-" + key }

// listPanelZoneID marks the entire session-list panel (its outer border box),
// wrapping the per-row/header zones nested inside it. Wheel events landing here
// are routed to selection movement rather than the right pane.
const listPanelZoneID = "list-panel"

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

// repoKey returns the grouping key for an instance. It delegates to
// session.Instance.GroupKey, the single source of grouping truth, which also
// resolves the repo root for not-yet-started instances so a Loading row lands
// in the group it will join once started.
func repoKey(i *session.Instance) string {
	return i.GroupKey()
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

// renderRepoHeader renders a repo group header as an optional fold marker, the uppercased name
// (with a member count when collapsed), and a dim rule filling the rest of the panel width, so
// it reads as a section divider. A collapsed group's header doubles as its selectable row, so it
// gets the same left accent bar as a selected item when selected is true.
//
// foldable reports whether the list spans more than one repo. Only a foldable group carries a
// fold marker; a lone group (the sole-repo case) renders as a plain section label, since folding
// it does nothing and the marker would advertise an absent affordance.
//
// needsInput is how many sessions in the group are blocked on user input, and unread how many
// are Ready but not yet visited. When the group is collapsed (so its member rows — and their
// per-row state glyphs — are hidden) the non-zero counts are appended as badges ("◆N" in the
// attention color, "●N" in the success color) so the group still signals what wants the user
// without being expanded.
func (l *List) renderRepoHeader(key string, collapsed bool, count, needsInput, unread int, selected, foldable bool) string {
	th := theme.Current()
	g := th.Glyphs
	name := strings.ToUpper(key)
	if foldable {
		marker := g.FoldOpen + " "
		if collapsed {
			marker = g.FoldClosed + " "
		}
		name = marker + name
	}
	badgePlain, badgeStyled := "", ""
	if collapsed {
		name = fmt.Sprintf("%s (%d)", name, count)
		// Build the badge cluster twice: plain for width math, styled for display
		// (ANSI styling adds no columns, so the plain width is the rendered width).
		appendBadge := func(text string, style lipgloss.Style) {
			if badgePlain != "" {
				badgePlain += " "
				badgeStyled += " "
			}
			badgePlain += text
			badgeStyled += style.Render(text)
		}
		if needsInput > 0 {
			appendBadge(fmt.Sprintf("%s%d", g.Waiting, needsInput), th.AttentionStyle())
		}
		if unread > 0 {
			appendBadge(fmt.Sprintf("%s%d", g.Ready, unread), th.SuccessStyle())
		}
	}
	header := repoHeaderStyle().Render(name)
	// repoHeaderStyle pads the name with one space on each side; a selected header also gains
	// a one-cell left accent bar, so reserve for both when sizing the trailing rule (hence -2,
	// and an extra -1 when selected). The badge cluster sits in the padding's right space and
	// is followed by one separator space before the rule, so it consumes its plain-text width
	// plus that one space.
	ruleLen := l.renderer.width - runewidth.StringWidth(name) - 2
	if badgePlain != "" {
		ruleLen -= runewidth.StringWidth(badgePlain) + 1
	}
	if selected {
		ruleLen--
	}
	if ruleLen < 0 {
		ruleLen = 0
	}
	line := header
	if badgePlain != "" {
		line += badgeStyled + " "
	}
	line += repoRuleStyle().Render(strings.Repeat("─", ruleLen))
	if selected {
		return selectedItemStyle().Render(line)
	}
	return line
}

// countInRange returns how many indices in the half-open range [start, end) satisfy
// pred. It backs the group counters below, which differ only in their predicate; an
// index-based predicate lets visibleCount reuse isHidden(idx) too.
func (l *List) countInRange(start, end int, pred func(idx int) bool) int {
	n := 0
	for j := start; j < end; j++ {
		if pred(j) {
			n++
		}
	}
	return n
}

// visibleCount returns how many items in the half-open range [start, end) are not hidden.
// Used to decide whether a (non-collapsed) group has any rows to render under the active filter.
func (l *List) visibleCount(start, end int) int {
	return l.countInRange(start, end, func(j int) bool { return !l.isHidden(j) })
}

// groupNeedsInputCount returns how many sessions in the half-open item range [start, end) are
// blocked on user input. Used to badge a collapsed repo-group header, whose member rows would
// otherwise carry the per-row waiting glyph.
func (l *List) groupNeedsInputCount(start, end int) int {
	return l.countInRange(start, end, func(j int) bool { return l.items[j].GetStatus() == session.NeedsInput })
}

// groupUnreadCount returns how many sessions in the half-open item range [start, end) are
// Ready but not yet visited. Used to badge a collapsed repo-group header, whose member rows
// would otherwise carry the per-row unread glyph.
func (l *List) groupUnreadCount(start, end int) int {
	return l.countInRange(start, end, func(j int) bool {
		return l.items[j].GetStatus() == session.Ready && l.items[j].Unread()
	})
}

// filterBarStyle renders the incremental search bar that appears below the list header
// when a filter is active; filterBarActiveStyle brightens it while the user is typing.
// Both read the active theme at render time like every other style in this file.
func filterBarStyle() lipgloss.Style       { return theme.Current().DimStyle() }
func filterBarActiveStyle() lipgloss.Style { return theme.Current().FgStyle() }

// List is the left panel: the instance list grouped by repo, with collapse
// state, incremental filtering, and the selection the rest of the UI follows.
type List struct {
	items         []*session.Instance
	selectedIdx   int
	height, width int
	renderer      *InstanceRenderer
	// collapsed records which repo groups are folded, keyed by repoKey. It is a pure
	// display/navigation flag — never authoritative over membership or order, which stay
	// derived from items. All reads go through effectiveCollapsed so the "only meaningful
	// with >1 repo" rule is enforced in exactly one place.
	collapsed map[string]bool

	// filterQuery is the current incremental filter string, echoed verbatim in the filter
	// bar. Items that fail parsedFilter are hidden exactly like collapsed group members —
	// isHidden returns true for them. An empty string disables filtering.
	filterQuery string
	// parsedFilter is filterQuery compiled into a predicate matcher (status:/dirty/behind/
	// pr: predicates plus substring terms, AND-combined). Recompiled on every SetFilter;
	// its zero value matches all, so an empty query disables filtering.
	parsedFilter session.Filter
	// filterActive is true while the user is actively typing the filter (stateFilter in
	// app.go). It controls the cursor indicator in the filter bar.
	filterActive bool

	// hideEmptyHint suppresses the centered first-run hint in an empty list. Set
	// when the always-on bottom hint bar is enabled, whose hints supersede it —
	// without the bar (hint_bar off) the in-list hint is the only affordance left.
	hideEmptyHint bool

	// updateBadge is the persistent update indicator inset in the panel's top
	// border ("⇡ v0.7.1"). The app sets it once when the startup update check
	// resolves and never clears it: unlike toast notices it must survive
	// overlays and hint_bar:false, so it lives in panel chrome.
	updateBadge string

	// driftBadge is the persistent agent-heuristic drift indicator inset in the
	// panel's top border ("⚠ stale"). Set only when the startup drift hint could
	// not be shown (hint bar off / overlay), so it reaches users who'd miss the
	// toast; like updateBadge it must survive overlays and hint_bar:false.
	driftBadge string

	// sortMode selects the within-group ordering: "" / "creation" keeps items as
	// the manual order (the default — no sorting runs); "status" sorts each repo
	// group by action-priority. See config.SessionSort* for the values.
	sortMode string
	// manual is the canonical creation/manual order, snapshotted lazily: it is nil
	// in creation mode (where items IS canonical and untouched) and holds the
	// pre-sort order while a non-creation mode is active. Persistence and the
	// switch back to creation read it; applySort derives the displayed items from
	// it. Keeping it nil in the common case guarantees creation mode is unchanged.
	manual []*session.Instance

	// marked is the set of sessions tagged in multi-select ("visual") mode, keyed
	// by instance pointer. It is ephemeral — cleared on mode exit and after a
	// batch action — so it is empty (and invisible) during normal navigation. A
	// stale pointer (instance removed while marked) is harmless: every read
	// intersects with items, so it simply drops out. See MarkedInstancesInView.
	marked map[*session.Instance]bool
}

// NewList returns an empty List.
func NewList(spinner *spinner.Model) *List {
	return &List{
		items:     []*session.Instance{},
		renderer:  &InstanceRenderer{spinner: spinner},
		collapsed: map[string]bool{},
	}
}

// SetShowEmptyHint controls whether an empty list renders its centered
// first-run hint ("n new · ? keys"). The app disables it when the always-on
// bottom hint bar already carries those keys.
func (l *List) SetShowEmptyHint(show bool) {
	l.hideEmptyHint = !show
}

// SetUpdateBadge sets the plain-text update badge ("⇡ v0.7.1") shown in the
// panel's top border. The panel styles and width-degrades it; pass plain
// text, no ANSI.
func (l *List) SetUpdateBadge(text string) {
	l.updateBadge = text
}

// SetDriftBadge sets the plain-text drift badge ("⚠ stale") shown in the
// Sessions panel border as a fallback when the startup drift hint can't render.
func (l *List) SetDriftBadge(text string) {
	l.driftBadge = text
}

// SetBranchPrefix sets the git-branch prefix stripped from each row's branch
// label (see InstanceRenderer.branchPrefix). The app passes the configured
// BranchPrefix so the redundant per-row namespace (e.g. "zvi/") is hidden.
func (l *List) SetBranchPrefix(prefix string) {
	l.renderer.branchPrefix = prefix
}

// SetModelIndicator sets the model-chip mode (see
// InstanceRenderer.modelIndicator). The app passes the normalized
// config.GetModelIndicator value at startup and on settings changes.
func (l *List) SetModelIndicator(mode string) {
	l.renderer.modelIndicator = mode
}

// SetPermissionIndicator sets the permission-chip mode (see
// InstanceRenderer.permissionIndicator). The app passes the normalized
// config.GetPermissionIndicator value at startup and on settings changes.
func (l *List) SetPermissionIndicator(mode string) {
	l.renderer.permissionIndicator = mode
}

// SetFilter updates the incremental filter query and clamps the selection to the
// nearest still-visible item. Pass an empty string to disable filtering.
func (l *List) SetFilter(query string) {
	l.filterQuery = query
	l.parsedFilter = session.ParseFilter(query)
	l.clampSelectionToNavigable()
}

// ClearFilter resets both the filter query and the active state.
func (l *List) ClearFilter() {
	l.filterQuery = ""
	l.parsedFilter = session.ParseFilter("")
	l.filterActive = false
	l.clampSelectionToNavigable()
}

// FilterQuery returns the current filter string.
func (l *List) FilterQuery() string { return l.filterQuery }

// SetFilterActive sets whether the user is currently typing the filter. This drives
// the cursor indicator in the rendered filter bar.
func (l *List) SetFilterActive(active bool) { l.filterActive = active }

// filterMatches reports whether an instance should be shown given the current filter.
// The query is compiled (in SetFilter) into predicate + substring terms; see
// session.Filter. An empty query yields the zero Filter, which matches everything.
func (l *List) filterMatches(i *session.Instance) bool {
	return l.parsedFilter.Matches(i)
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

// NumInstances returns the total number of instances, ignoring filtering and
// collapsed groups.
func (l *List) NumInstances() int {
	return len(l.items)
}

// InPanelBounds reports whether the mouse event lands within the list panel's
// rendered box. Used to route wheel events to selection movement. False before
// the first zone scan (zero ZoneInfo), so early frames route nowhere.
func (l *List) InPanelBounds(msg tea.MouseMsg) bool {
	return zone.Get(listPanelZoneID).InBounds(msg)
}

// InstanceAtZone returns the instance whose rendered row contains the given mouse
// event, or nil if the click did not land on a session row. Only rows currently
// in the list are considered, so stale zones from removed sessions are ignored.
func (l *List) InstanceAtZone(msg tea.MouseMsg) *session.Instance {
	for _, it := range l.items {
		if zone.Get(listRowZoneID(it)).InBounds(msg) {
			return it
		}
	}
	return nil
}

// HeaderAtZone returns the repo-group key of the header row containing the given
// mouse event, and whether any header was hit. Mirrors InstanceAtZone: only the
// groups currently in the list are considered.
func (l *List) HeaderAtZone(msg tea.MouseMsg) (string, bool) {
	for i := 0; i < len(l.items); {
		key := repoKey(l.items[i])
		if zone.Get(listHeaderZoneID(key)).InBounds(msg) {
			return key, true
		}
		_, end := l.groupBounds(i)
		i = end
	}
	return "", false
}

// ClickHeader toggles the fold of the repo group named key — the mouse
// counterpart of the ←/→ keyboard fold — snapping the selection to the group's
// anchor so keyboard and mouse agree on where the cursor is. Returns whether
// anything changed (false for an unknown key or when folding is meaningless,
// i.e. fewer than two repos), so the caller can skip the persistence write.
func (l *List) ClickHeader(key string) bool {
	if l.distinctRepoCount() <= 1 {
		return false
	}
	anchor := -1
	for i, item := range l.items {
		if repoKey(item) == key {
			anchor = i
			break
		}
	}
	if anchor < 0 {
		return false
	}
	l.selectedIdx = anchor
	if l.collapsed[key] {
		delete(l.collapsed, key)
	} else {
		l.collapsed[key] = true
	}
	l.clampSelectionToNavigable()
	return true
}

// moveSelection advances the selection by delta (+1 next, -1 previous), wrapping at
// either end and skipping hidden rows (collapsed-group members, filtered-out items).
// It backs both Down and Up so the wrap-and-skip logic lives in one place.
func (l *List) moveSelection(delta int) {
	n := len(l.items)
	if n == 0 {
		return
	}
	idx := l.selectedIdx
	for range n {
		idx = (idx + delta + n) % n
		if !l.isHidden(idx) {
			l.selectedIdx = idx
			return
		}
	}
}

// Down selects the next visible item in the list, wrapping at the end and skipping the hidden
// members of collapsed groups.
func (l *List) Down() { l.moveSelection(1) }

// NextUnread moves the selection to the next unread Ready session (forward,
// wrapping). Reports whether one was found.
func (l *List) NextUnread() bool {
	return l.nextActionable(
		func(i *session.Instance) bool { return i.GetStatus() == session.Ready && i.Unread() },
		l.groupUnreadCount,
	)
}

// NextNeedsInput moves the selection to the next session blocked on input
// (forward, wrapping). Reports whether one was found.
func (l *List) NextNeedsInput() bool {
	return l.nextActionable(
		func(i *session.Instance) bool { return i.GetStatus() == session.NeedsInput },
		l.groupNeedsInputCount,
	)
}

// nextActionable moves the selection forward (wrapping, others-only) to the next
// visible row that needs the user, landing on a collapsed group's anchor when the
// match is folded inside it. Reports whether such a row was found. member judges an
// individual row; groupCount badges a collapsed group's [start, end) range.
func (l *List) nextActionable(member func(*session.Instance) bool, groupCount func(start, end int) int) bool {
	n := len(l.items)
	for off := 1; off < n; off++ { // others only, full wrap; off == n (self) excluded
		idx := (l.selectedIdx + off) % n
		if l.isHidden(idx) {
			continue
		}
		if l.rowNeedsUser(idx, member, groupCount) {
			l.SetSelectedInstance(idx) // idx is visible; no clamp needed
			return true
		}
	}
	return false
}

// rowNeedsUser judges a visible row. With no active filter a collapsed group's
// anchor stands in for its hidden members (via groupCount), matching the header
// badge; otherwise (and always under a filter, where matches surface individually)
// the per-row predicate applies.
func (l *List) rowNeedsUser(idx int, member func(*session.Instance) bool, groupCount func(start, end int) int) bool {
	if l.filterQuery == "" && l.effectiveCollapsed(repoKey(l.items[idx])) {
		start, end := l.groupBounds(idx)
		return groupCount(start, end) > 0
	}
	return member(l.items[idx])
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

	// Under a sort mode, drop the target from the canonical order and re-sort once
	// the removal + selection recovery below has fully settled. As a deferred call it
	// runs LAST — after clampSelectionToNavigable has recovered the selection — and
	// applySort then preserves that recovered selection by identity. Placed after the
	// idx==-1 guard so it pairs with a real items removal (no spurious re-sort when
	// target isn't in the list). In creation mode manual is nil and this is skipped.
	if l.sortActive() {
		defer func() {
			l.manual = removeInstance(l.manual, target)
			l.applySort()
		}()
	}

	// Kill the tmux session and clean up the worktree.
	if err := target.Kill(); err != nil {
		log.ErrorLog.Printf("could not kill instance: %v", err)
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

// Up selects the prev visible item in the list, wrapping at the top and skipping the hidden
// members of collapsed groups.
func (l *List) Up() { l.moveSelection(-1) }

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
	// Under a sort mode, mirror the insertion into the canonical order and re-sort
	// so the new session lands at its priority position (selection is preserved by
	// identity). In creation mode manual is nil and this is skipped.
	if l.sortActive() {
		l.manual = insertByRepo(l.manual, instance)
		l.applySort()
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

// isAttachable reports whether a session can be attached to right now — the same
// condition the KeyEnter handler guards on before attaching (app.go). Started() is
// checked first because TmuxAlive dereferences the tmux session, which a not-yet-
// started instance does not have.
func isAttachable(i *session.Instance) bool {
	return i != nil && i.Started() && !i.Paused() && i.GetStatus() != session.Loading && i.TmuxAlive()
}

// SiblingInGroup returns the next attachable session in inst's repo group, moving
// dir (+1 next, -1 previous) and wrapping within the group. Sessions that cannot be
// attached (paused, still loading, or with no live tmux session) are skipped. When
// inst is the only attachable member — or is not in the list — inst itself is
// returned, so an in-session jump becomes a harmless no-op. This drives Ctrl+PgDn/
// PgUp cycling from app.go's attachLoop; it is independent of collapse/filter state,
// which are list-view concerns that don't apply while attached.
func (l *List) SiblingInGroup(inst *session.Instance, dir int) *session.Instance {
	return l.siblingInGroup(inst, dir, isAttachable)
}

// siblingInGroup is the predicate-injectable core of SiblingInGroup; ok decides
// which candidates are eligible. Split out so the ring-walk can be tested without a
// live tmux session.
func (l *List) siblingInGroup(inst *session.Instance, dir int, ok func(*session.Instance) bool) *session.Instance {
	if dir == 0 {
		return inst
	}
	idx := -1
	for i, it := range l.items {
		if it == inst {
			idx = i
			break
		}
	}
	if idx < 0 {
		return inst
	}
	start, end := l.groupBounds(idx)
	n := end - start
	if n <= 1 {
		return inst
	}
	step := 1
	if dir < 0 {
		step = -1
	}
	// Walk the group ring from the neighbour outward, wrapping within [start, end).
	for k := 1; k < n; k++ {
		off := ((idx-start)+step*k)%n + n
		cand := l.items[start+off%n]
		if ok(cand) {
			return cand
		}
	}
	return inst
}

// MoveUp swaps the selected instance with the one above it. The swap is confined to within a repo group: if the item
// above belongs to a different group, this is a no-op so the move cannot split a group and produce a duplicate header.
// (Single-repo lists share one key, so reordering stays unrestricted there.)
func (l *List) MoveUp() bool {
	// A sort mode owns within-group order; manual reorder is disabled there (the app
	// surfaces a hint via ManualReorderEnabled).
	if l.sortActive() {
		return false
	}
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
	if l.sortActive() {
		return false
	}
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
// is only meaningful when more than one repo is present (a lone group's header has no fold marker), so
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
	l.syncManualGroupOrder()
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
	l.syncManualGroupOrder()
	return true
}

// syncManualGroupOrder realigns the canonical manual order to the group sequence
// just produced in items by a whole-group move, then re-sorts. A no-op in creation
// mode (manual is nil). Group order is manual in every mode; only the within-group
// order is owned by the sort, so the move must be reflected in manual too.
func (l *List) syncManualGroupOrder() {
	if !l.sortActive() {
		return
	}
	l.manual = regroupManualLike(l.manual, l.items)
	l.applySort()
}

// GetInstances returns all instances in the list
func (l *List) GetInstances() []*session.Instance {
	return l.items
}
