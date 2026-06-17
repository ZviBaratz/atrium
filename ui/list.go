package ui

import (
	"errors"
	"fmt"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui/theme"
	"sort"
	"strings"
	"time"

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

// renderRepoHeader renders a repo group header as a fold marker, the uppercased name (with a
// member count when collapsed), and a dim rule filling the rest of the panel width, so it
// reads as a section divider. A collapsed group's header doubles as its selectable row, so it
// gets the same left accent bar as a selected item when selected is true.
//
// needsInput is how many sessions in the group are blocked on user input, and unread how many
// are Ready but not yet visited. When the group is collapsed (so its member rows — and their
// per-row state glyphs — are hidden) the non-zero counts are appended as badges ("◆N" in the
// attention color, "●N" in the success color) so the group still signals what wants the user
// without being expanded.
func (l *List) renderRepoHeader(key string, collapsed bool, count, needsInput, unread int, selected bool) string {
	th := theme.Current()
	g := th.Glyphs
	marker := g.FoldOpen + " "
	if collapsed {
		marker = g.FoldClosed + " "
	}
	name := marker + strings.ToUpper(key)
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

// groupUnreadCount returns how many sessions in the half-open item range [start, end) are
// Ready but not yet visited. Used to badge a collapsed repo-group header, whose member rows
// would otherwise carry the per-row unread glyph.
func (l *List) groupUnreadCount(start, end int) int {
	n := 0
	for _, item := range l.items[start:end] {
		if item.GetStatus() == session.Ready && item.Unread() {
			n++
		}
	}
	return n
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

	// filterQuery is the current incremental filter string. Items whose DisplayName and
	// Branch both fail a case-insensitive contains check are hidden exactly like collapsed
	// group members — isHidden returns true for them. An empty string disables filtering.
	filterQuery string
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

// NumInstances returns the total number of instances, ignoring filtering and
// collapsed groups.
func (l *List) NumInstances() int {
	return len(l.items)
}

// InstanceRenderer handles rendering of session.Instance objects
type InstanceRenderer struct {
	spinner *spinner.Model
	width   int
	// branchPrefix is the configured git-branch prefix (e.g. "zvi/"). It is
	// stripped from each row's branch label — every session shares it, so it is
	// pure repetition on the version-control line. Empty disables stripping.
	branchPrefix string
	// modelIndicator is the model-chip mode (config.GetModelIndicator): "off"
	// hides the chip, anything else — including the zero value — shows it, so
	// normalization stays in config and the ui package needs no config import.
	modelIndicator string
	// permissionIndicator is the permission-mode chip mode
	// (config.GetPermissionIndicator): "off" hides the chip, anything else
	// shows it. The chip reflects the live mode (Instance.PermissionModeInfo:
	// footer-detected truth, falling back to the --permission-mode launch flag),
	// so it tracks an in-session switch; it is drawn for any non-default mode but
	// never for a detected "default" or no flag.
	permissionIndicator string
}

func (r *InstanceRenderer) setWidth(width int) {
	if width < 1 {
		width = 1
	}
	r.width = width
}

// displayBranch returns the session branch with the configured prefix stripped
// (see branchPrefix). The prefix is removed only on an exact match, so a branch
// under a different namespace keeps its meaningful prefix; if stripping would
// empty the label, the full branch is kept.
func (r *InstanceRenderer) displayBranch(i *session.Instance) string {
	branch := i.Branch
	if r.branchPrefix != "" {
		if trimmed := strings.TrimPrefix(branch, r.branchPrefix); trimmed != "" {
			branch = trimmed
		}
	}
	return branch
}

// fmtAge formats a time.Time as a compact elapsed-time label: "<N>m", "<N>h", or "<N>d".
// Sub-minute and zero times return "" so very fresh sessions stay uncluttered.
func fmtAge(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return ""
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// stateGlyph returns the glyph and color describing an instance's status, for
// the leading status gutter. Running/Loading use the animated spinner frame;
// the others use theme glyphs. The state word is intentionally not returned —
// the color-coded glyph carries the signal on its own.
func (r *InstanceRenderer) stateGlyph(i *session.Instance, th *theme.Theme) (glyph string, color lipgloss.Color) {
	switch i.GetStatus() {
	case session.Running, session.Loading:
		return r.spinner.View(), th.Palette.Working
	case session.Ready:
		// Unread (the agent finished a turn the user hasn't visited) keeps the
		// bright filled glyph; a seen session dims to the hollow variant. Shape
		// and color both change so the signal survives colorblindness.
		if i.Unread() {
			return th.Glyphs.Ready, th.Palette.Success
		}
		return th.Glyphs.ReadySeen, th.Palette.SuccessDim
	case session.NeedsInput:
		return th.Glyphs.Waiting, th.Palette.Attention
	case session.Paused:
		return th.Glyphs.Paused, th.Palette.FgDim
	default:
		return " ", th.Palette.FgDim
	}
}

// Render draws a session as two lines. Line 1 is identity: a leading status
// gutter (color-coded glyph — no word) and the name, with the account and AUTO
// badges and the agent icon right-aligned — the agent icon pinned to the far
// edge so it forms a fixed column mirroring the status gutter. Line 2 (dim) is
// version control: behind/ahead/dirty + PR on the left and the diff stat on the
// right, "·"-separated. The branch leads line 2 only when a label-only rename has
// decoupled it from the visible name (else it is a slug echo and is dropped); a
// fresh session with nothing to show falls back to its age. A direct (non-git)
// session instead shows a dim marker and its age. The selected row carries a left
// accent bar and a filled background. idx is unused (kept for the caller's signature).
func (r *InstanceRenderer) Render(i *session.Instance, idx int, selected bool) string {
	_ = idx
	th := theme.Current()
	g := th.Glyphs
	p := newRowPaint(th, selected)

	// One column is reserved for the left marker/pad; build content to W.
	W := r.width - 1
	if W < 1 {
		W = 1
	}
	space := p.seg(" ", th.Palette.FgDim)

	// --- Line 1: gutter + name (left) · account + AUTO + agent icon (right) ---
	left1 := []rowSeg{r.gutterSeg(p, i), space, p.nameSeg(i, selected)}

	var right1 []rowSeg
	// Per-session Claude account badge: accent for a routed account, dim for the
	// default/fallback. Shown only when an account was resolved (empty = feature
	// off / legacy session).
	if acct := i.ClaudeAccountName(); acct != "" {
		acctColor := th.Palette.Accent
		if i.ClaudeAccountIsDefault() {
			acctColor = th.Palette.FgDim
		}
		right1 = append(right1, p.seg(" "+acct+" ", acctColor))
	}
	// Per-session AUTO badge (not while paused) so "yolo" state is unmistakable.
	// The badge carries its own background, so wrap it as a pre-rendered chip.
	if i.AutoYes && !i.Paused() {
		if len(right1) > 0 {
			right1 = append(right1, space)
		}
		badge := " " + g.AutoBadge + "AUTO "
		right1 = append(right1, rawSeg(badge, th.BadgeStyle().Render(badge)))
	}
	// Per-session model chip: transcript truth first, --model flag fallback (see
	// Instance.ModelInfo). It rides the agent icon as one brand-colored unit —
	// last before the icon, one space apart, always in the agent's full brand
	// accent. Shown whenever the model is known; "off" hides it.
	if r.modelIndicator != "off" {
		if model := i.ModelInfo(); model != "" {
			right1 = append(right1, p.seg(" "+shortModelName(model), p.agentColor(i)))
		}
	}
	// Per-session permission-mode chip: live footer truth first, --permission-mode
	// flag fallback (see Instance.PermissionModeInfo). Tracks an in-session mode
	// switch (e.g. plan-launched then accepted into auto) instead of the stale
	// launch flag. Shown for any non-default mode; a detected "default", no flag,
	// or "off" stays unbadged.
	if r.permissionIndicator != "off" {
		if mode := i.PermissionModeInfo(); mode != "" && mode != "default" {
			right1 = append(right1, p.seg(" "+permissionModeLabel(mode), p.agentColor(i)))
		}
	}
	// Agent-identity icon (which CLI the session runs), pinned to the far right so
	// it sits in a fixed column — a right-edge counterpart to the left status
	// gutter — instead of stacking another glyph at the left edge.
	if len(right1) > 0 {
		right1 = append(right1, space)
	}
	right1 = append(right1, p.agentSeg(i))

	line1 := p.composeLine(W, left1, right1)

	// Indent line 2 so its content aligns under the name: gutter + space (both
	// width-1 by theme invariant). The agent icon no longer leads line 1, so the
	// name — and thus this indent — starts two columns in, not four.
	indentW := left1[0].width() + left1[1].width()

	var line2 string
	if i.IsDirect() {
		// Direct (non-git) session: no branch/ahead/behind/diff. Show a dim marker
		// (consistent with the diff pane and picker hint) as the flex field, with
		// the age right-aligned.
		left2 := []rowSeg{p.flexSeg("direct · no git isolation", th.Palette.FgDim, false)}
		var right2 []rowSeg
		if age, ok := p.ageSeg(i); ok {
			right2 = append(right2, age)
		}
		line2 = p.composeLine(W, left2, right2)
	} else {
		stat := i.GetDiffStats()
		left2 := []rowSeg{p.seg(strings.Repeat(" ", indentW), th.Palette.FgDim)}

		// Line-2 left content is a set of separator-joined groups. The branch is
		// shown only when a label-only rename has decoupled it from the visible
		// name (DisplayName != Title): otherwise the branch is just
		// sanitizeBranchName(Title), a slug echo of the name on line 1, so it
		// carries no information and is dropped to let the git state lead. The full
		// branch is still reachable in the preview/diff panes.
		var groups [][]rowSeg
		if i.DisplayName() != i.Title {
			groups = append(groups, []rowSeg{p.flexSeg(r.displayBranch(i), th.Palette.FgDim, false)})
		}
		if chips := gitChips(p, stat); len(chips) > 0 {
			groups = append(groups, chips)
		}
		if seg, ok := prSeg(p, i.GetPRStatus()); ok {
			groups = append(groups, []rowSeg{seg})
		}
		for gi, grp := range groups {
			if gi > 0 {
				left2 = append(left2, p.sepSeg())
			}
			left2 = append(left2, grp...)
		}

		// Age is omitted from a populated version-control line (the weakest signal
		// there), but used as a fallback when line 2 would otherwise be empty — a
		// fresh, unchanged session with no decoupled branch — so every row keeps two
		// lines and the would-be-blank one still says something useful.
		right2 := changeSegs(p, stat)
		if len(groups) == 0 && len(right2) == 0 {
			if age, ok := p.ageSeg(i); ok {
				right2 = append(right2, age)
			}
		}
		line2 = p.composeLine(W, left2, right2)
	}

	// --- Left marker (accent bar when selected) + compose ---
	marker := p.pad(1)
	if selected {
		marker = p.seg(g.SelectionMark, th.Palette.Accent).render()
	}
	return lipgloss.JoinVertical(lipgloss.Left, marker+line1, marker+line2)
}

func (l *List) String() string {
	// The list is a pure (scrollable) stream of repo groups and session rows;
	// its only chrome is the panel border (the title rides the border's top edge).
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
			cursor = theme.Current().Glyphs.TextCursor
		}
		style := filterBarStyle()
		if l.filterActive {
			style = filterBarActiveStyle()
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
			ur := l.groupUnreadCount(start, end)
			at := appendBlock(zone.Mark(listHeaderZoneID(key), l.renderRepoHeader(key, collapsed, end-start, ni, ur, headerSelected)))
			if headerSelected {
				selStart, selH = at, len(lines)-at
			}
		}
		if !collapsed {
			for j := start; j < end; j++ {
				if l.isHidden(j) {
					continue
				}
				at := appendBlock(zone.Mark(listRowZoneID(l.items[j]), l.renderer.Render(l.items[j], j+1, j == l.selectedIdx)))
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
		lines = append(lines, filterBarStyle().Render("   no matches"))
	}

	// Inner content area inside the panel border (2 cols / 2 rows of chrome).
	innerH := l.height - 2
	if innerH < 1 {
		innerH = 1
	}

	// A genuinely empty list (no sessions, not even mid-filter) would otherwise render a
	// blank panel interior. With the contextual hint bar hidden during plain navigation,
	// this empty list is the primary first-run surface, so center the two essential keys
	// here — a fresh user (or one who dismissed the welcome) is never stranded on a
	// silent screen. Pressing `n` makes the list non-empty and the contextual bar takes
	// over. Guard on filterActive too: an empty-query filter still shows its bar above.
	// hideEmptyHint (set when the always-on bottom hint bar is enabled) suppresses
	// this so first-run guidance isn't shown twice.
	if len(l.items) == 0 && !filtering && !l.filterActive && !l.hideEmptyHint {
		th := theme.Current()
		// Kept terse so it never clips: the list panel is only ~30% of the terminal
		// width, so a longer "new session / all keys" phrasing truncates on normal and
		// narrow terminals. The styled key glyphs carry the meaning (n = new, ? = keys).
		hint := th.AttentionStyle().Render("n") + " " + th.DimStyle().Render("new") +
			th.FaintStyle().Render("  ·  ") +
			th.AttentionStyle().Render("?") + " " + th.DimStyle().Render("keys")
		lines = append(lines, lipgloss.PlaceHorizontal(l.width-2, lipgloss.Center, hint))
		// Vertically center within the panel interior so the empty state reads as
		// intentional rather than top-anchored.
		for top := (innerH - 1) / 2; top > 0; top-- {
			lines = append([]string{""}, lines...)
		}
	}
	lines = l.windowLines(lines, selStart, selH, innerH)
	content := strings.Join(lines, "\n")

	// The list is the primary navigation surface, so its panel is always drawn
	// active (accent border). A dynamic focus model can flip this later.
	// The panel zone wraps outside Panel so its internal clipping cannot
	// truncate the end marker.
	return zone.Mark(listPanelZoneID, theme.Current().PanelWithBadge("Sessions", l.updateBadge, content, l.width, l.height, true))
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

	// When the "↑ more" indicator would consume a content line while the very
	// next line is a group separator, start the window one line later: the
	// indicator then replaces the blank instead of a real row, and the gap
	// under it stays constant while scrolling instead of breathing 0–1 lines.
	// Skipped when it would violate the selection's one-line top margin.
	if offset > 0 && offset+1 <= len(lines)-avail &&
		lines[offset] != "" && lines[offset+1] == "" &&
		(selStart < 0 || selStart-1 >= offset+1) {
		offset++
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

// Attach attaches the user's terminal to the selected instance's tmux session
// (see Instance.Attach).
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

// Collapse folds the selected session's repo group, snapping the selection to the group
// anchor. It is a no-op (returns false) when the group is already folded — so the caller can
// skip the persistence write — or when fewer than two repos are present, since folding is
// meaningless there.
func (l *List) Collapse() bool {
	if len(l.items) == 0 || l.distinctRepoCount() <= 1 {
		return false
	}
	key := repoKey(l.items[l.selectedIdx])
	if l.collapsed[key] {
		return false
	}
	l.collapsed[key] = true
	l.clampSelectionToNavigable()
	return true
}

// Expand unfolds the selected (folded) repo group, leaving the selection on the anchor.
// It is a no-op (returns false) when the group is already expanded or with fewer than two
// repos, mirroring Collapse.
func (l *List) Expand() bool {
	if len(l.items) == 0 || l.distinctRepoCount() <= 1 {
		return false
	}
	key := repoKey(l.items[l.selectedIdx])
	if !l.collapsed[key] {
		return false
	}
	delete(l.collapsed, key)
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

// PausedInstancesInView returns every Paused instance that passes the active
// filter (all paused when no filter is set), in list order. Collapsed groups
// are included — folding is a display state, not a scope boundary — so a batch
// "resume all" restores paused sessions the user can't currently see folded
// away, which is what they expect after a reboot parked everything.
func (l *List) PausedInstancesInView() []*session.Instance {
	var out []*session.Instance
	for _, it := range l.items {
		if it.GetStatus() == session.Paused && l.filterMatches(it) {
			out = append(out, it)
		}
	}
	return out
}

// ActiveInstancesInView returns every pausable instance that passes the active
// filter (all of them when no filter is set), in list order — the scope of a
// batch "pause all". An instance is pausable when it is:
//   - not already Paused (nothing to park),
//   - not Loading (its Start() is still building the worktree/tmux and the
//     Loading→Running transition is still pending on the main loop; pausing now
//     would race that setup, exactly why single-pause refuses a Loading session),
//   - not direct (a direct session has no worktree to free, so it cannot be parked).
//
// Like PausedInstancesInView, collapsed groups are included: folding is a display
// state, not a scope boundary, so a pre-restart "pause all" parks sessions the
// user has folded away too. A Loading session left unparked is no gap — the
// post-restart recovery loop is the safety net for it.
func (l *List) ActiveInstancesInView() []*session.Instance {
	var out []*session.Instance
	for _, it := range l.items {
		status := it.GetStatus()
		if status != session.Paused && status != session.Loading && !it.IsDirect() && l.filterMatches(it) {
			out = append(out, it)
		}
	}
	return out
}
