package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/ZviBaratz/atrium/keys"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui/theme"

	"charm.land/bubbles/v2/spinner"
	"charm.land/lipgloss/v2"
	zone "github.com/lrstanley/bubblezone/v2"
)

// Row and viewport rendering: the InstanceRenderer that paints a single
// session row, the full-list String(), and the scroll windowing that keeps the
// selection visible.

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
	// effortIndicator is the reasoning-effort chip mode (config.GetEffortIndicator):
	// "off" hides the chip, anything else shows it. The chip reflects the live level
	// (Instance.EffortInfo: hook-reported truth, falling back to the --effort launch
	// flag), so it tracks an in-session /effort switch and knows the level a session
	// with no flag inherited; it is drawn whenever an effort is known.
	effortIndicator string
	// contextIndicator is the session chip mode (config.GetContextIndicator):
	// "off" hides the chip, "count" / "percent" / "bar" pick the shape of a
	// context-occupancy reading, "cost" swaps it for a spend estimate, and the
	// zero value means "percent" so the renderer's default matches the config
	// accessor's. The chip reflects a transcript reading — Instance.UsageInfo in
	// the occupancy modes, Instance.CostInfo in "cost" — and is absent whenever
	// there is none.
	contextIndicator string
	// hideClaudeAccountBadge suppresses the per-row Claude-account badge. Set by
	// List.String when account grouping is visually active (mode == account and >1
	// account), so the cluster divider + tinted header carry the identity instead of
	// every row repeating it.
	//
	// Claude-specific by name because it is Claude-specific in fact, and the two
	// account axes are independent: the suppression means "this badge repeats the
	// divider above it", and the divider labels a Claude account (accountKey →
	// session.Instance.AccountClusterKey, which reads claudeAccountPool then
	// claudeAccount). The agy badge is redundant with no divider — nothing clusters
	// on it — so it is never suppressed. Sharing one bool would make an agy badge
	// vanish because a session's CLAUDE account happened to match the divider (#457).
	hideClaudeAccountBadge bool
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

// fmtPendingElapsed formats how long a session has held the Pending status as a compact
// label: "<N>s", "<N>m", "<N>h", or "<N>d". Unlike fmtAge it does NOT blank the sub-minute
// range — a session that has only just entered Pending should still show "12s", since the
// most common Pending window is short and "how long has it been churning?" is exactly the
// question this cue answers (a long elapsed hints the autonomous work may be stuck). A zero
// time still returns "" so a never-stamped status stays uncluttered.
func fmtPendingElapsed(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
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
func (r *InstanceRenderer) stateGlyph(i *session.Instance, th *theme.Theme) (glyph string, color theme.Color) {
	switch i.GetStatus() {
	case session.Running, session.Loading:
		return r.spinner.View(), th.Palette.Working
	case session.Pending:
		// Pending (main turn ended, but a background sub-agent is still in flight — #290)
		// is busy autonomous work: it must never read as the green "done" glyph, but it is
		// also not foreground work. A *still* glyph in a calm cyan tint separates it from
		// Running by motion (still vs the moving spinner), shape, and color — colorblind-safe
		// without relying on color, and consistent with the in-session header (see barState).
		return th.Glyphs.Pending, th.Palette.Pending
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
func (r *InstanceRenderer) Render(i *session.Instance, idx int, selected, marked bool) string {
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
	// Pending rows carry a faint elapsed cue ("· 12s") right after the name — how long the
	// session has been doing autonomous background work. Only Pending shows it (the state
	// where "how long / is it stuck?" matters most); it rides the left cluster so it never
	// collides with the right-aligned badges/agent icon, and appends after index 1 so the
	// line-2 indent (gutter+space) is unaffected. The separator is the shared collapsible
	// sepSeg, so at a width too narrow to keep any of the name it drops out (composeLine's
	// collapseSeps) and the row degrades to "⧗ 12s" rather than a dangling "⧗  · 12s".
	if i.GetStatus() == session.Pending {
		if elapsed := fmtPendingElapsed(i.StatusChangedAt()); elapsed != "" {
			left1 = append(left1, p.sepSeg(), p.seg(elapsed, th.Palette.FgFaint))
		}
	}

	var right1 []rowSeg
	// Muted marker: a durable, dim glyph on any session the user has silenced (M),
	// so which sessions won't notify is visible at a glance. Rides the right cluster
	// like the other per-session badges.
	if i.Muted() {
		right1 = append(right1, p.seg(g.Muted, th.Palette.FgDim))
	}
	// Pending-prompt marker: a durable signal that this session has a queued prompt
	// awaiting delivery (waiting or in flight), so the user can tell "queued" from
	// "delivered" without relying on the transient submit/delivery toast. Clears the
	// moment the queue drains. Orthogonal to the status gutter, so it rides the right
	// cluster rather than replacing the stateGlyph.
	if i.HasQueuedPrompt() {
		label := g.Queued
		if n := i.QueueLen(); n > 1 {
			label = fmt.Sprintf("%s%d", g.Queued, n)
		}
		right1 = append(right1, p.seg(label, th.Palette.Accent))
	}
	// Per-session Claude account badge: accent for a routed account, dim for the
	// default/fallback. Shown only when an account was resolved (empty = feature
	// off / legacy session).
	if acct := i.ClaudeAccountName(); acct != "" && !r.hideClaudeAccountBadge {
		acctColor := th.Palette.Accent
		if i.ClaudeAccountIsDefault() {
			acctColor = th.Palette.FgDim
		}
		right1 = append(right1, p.seg(" "+acct+" ", acctColor))
	}
	// Per-session Antigravity (agy) account badge (#457), on the same axis-identity
	// footing as the Claude badge above and rendered beside it.
	//
	// Shown only on a session that actually RUNS agy. The account is stamped on every
	// session regardless of program — app's spawn calls SetAgyAccount unconditionally,
	// and a catch-all agy account matches every repo — but the pinned dir is honoured
	// only where the launch path resolves the agy adapter (session/tmux's
	// `if t.adapter.Key == agent.KeyAgy` before wrapAgyBwrap). Badging a claude row
	// would report a route that does nothing.
	//
	// Prefixed where the Claude badge is bare, because the two axes are independent
	// and can both land on one row: two unlabelled names would not say which is which.
	// The Claude one keeps its bare form — it predates the second axis, and every
	// row-width guard in this package is calibrated against it.
	//
	// Accent, and never FgDim: the agy section has no default/fallback account flag to
	// mirror (see RestampAgyAccount), so there is no dim state to render — and FgDim is
	// an open contrast bug on this band (#555).
	//
	// Deliberately NOT gated on hideClaudeAccountBadge — see that field's comment.
	if acct := i.AgyAccountName(); acct != "" && runsAgy(i) {
		right1 = append(right1, p.seg(" agy:"+acct+" ", th.Palette.Accent))
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
	// Per-session reasoning-effort chip: hook-reported truth first, --effort flag
	// fallback (see Instance.EffortInfo). Sits between the model and permission
	// chips, reading as one brand-colored phrase with them ("opus 4.8 max plan").
	// Shown whenever the level is known; "off" hides it. Unknown stays unbadged
	// rather than guessing a default — a session that has never run a tool has
	// reported no effort, and claude's default is not Atrium's to assume.
	if r.effortIndicator != "off" {
		if effort := i.EffortInfo(); effort != "" {
			right1 = append(right1, p.seg(" "+effortLabel(effort), p.agentColor(i)))
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
	// Per-session transcript chip: how full the agent's context is
	// (Instance.UsageInfo), or what it has spent (Instance.CostInfo), depending
	// on the configured mode. It comes LAST in the cluster, after the
	// brand-coloured model/effort/permission phrase and just inside the agent
	// icon, for two reasons: it keeps that phrase ("opus 5 max plan") reading as
	// one unit, and in its occupancy modes this is the one chip whose colour
	// tracks urgency (see contextColor), so it belongs at the edge the eye scans.
	// Absent whenever there is no reading, so a non-claude session's row is
	// unchanged.
	//
	// One column for both readings rather than two, because a ninth item in this
	// cluster would take a further six cells out of the flex name segment on a
	// row that has 21 to give. Sharing is not free either — the widest cost
	// figure is 5 cells against an occupancy chip's 4 — but it is bounded by the
	// budget the layout was already sized against, and it is paid only in the
	// mode that asks for it. Measured: the name column holds 26 cells in cost
	// mode against 28 in percent, and 19 against 21 on a fully loaded row. All
	// four are asserted in ui/list_context_test.go.
	//
	// There is no suppression rule here. A reading that must not be shown —
	// two sessions sharing one transcript directory, most of all — is one that
	// must not be HELD either, so the poll layer refuses to take it (app's
	// usagePolicy) and UsageInfo/CostInfo simply report nothing. Hiding it at this
	// level instead would leave the poisoned value in the instance, to reappear
	// the moment the neighbour that poisoned it went away.
	if chip, ok := contextChip(i.UsageInfo(), i.CostInfo(), r.contextIndicator, th.Glyphs.ContextRamp); ok {
		right1 = append(right1, p.seg(" "+chip, contextColor(th, i.UsageInfo(), r.contextIndicator)))
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
	if phase := i.SetupPhase(); phase != "" {
		// The per-repo setup script is running (#389). It takes the line before every
		// other case because it is the only one that answers "why has this been Loading
		// for two minutes" — and because while it runs there is nothing else worth
		// showing: the worktree exists but is not yet the environment it will be.
		// A phase rather than a session.Status, deliberately: Status values are
		// persisted in state.json and read by a dozen-odd sites, and this is a phase OF
		// Loading, not a state beside it.
		left2 := []rowSeg{p.flexSeg(phase, th.Palette.FgDim, false)}
		line2 = p.composeLine(W, left2, nil)
	} else if i.AwaitingSetup() {
		// Blocked on a one-time startup/trust screen (PaneGate). Replace the
		// version-control line with a dim hint so the block is legible on every row —
		// only the selected row's preview shows the screen itself, and the status glyph
		// alone doesn't distinguish a setup gate from an ordinary prompt.
		left2 := []rowSeg{p.flexSeg("waiting on setup screen · attach to continue", th.Palette.FgDim, false)}
		line2 = p.composeLine(W, left2, nil)
	} else if i.IsDirect() {
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

		// The managed dev-server chip (#389) goes last, and only if the row can still
		// hold it. Dropped rather than squeezed, and measured rather than trusted to
		// the width budget, because composeLine has nothing to spend here: it shrinks
		// the single flex segment, and line 2's only flex is the branch — which is
		// itself present only for a renamed session (above). On every other row the
		// content is all fixed, so an overlong line is not truncated but RENDERED
		// overlong, wrapping into the ghost row the drift-sites skill warns about.
		//
		// It is the right thing to drop even carrying the run-command state: the port is
		// an identifier to go look up and the server is one keypress from being checked,
		// where the git chips beside it are state changing under the user — and a list
		// pane this narrow is one the user widens to read anything at all.
		if seg, ok := portSeg(p, i.Port(), i.RunLive()); ok && line2Fits(p, indentW, groups, right2, seg, W) {
			groups = append(groups, []rowSeg{seg})
		}
		for gi, grp := range groups {
			if gi > 0 {
				left2 = append(left2, p.sepSeg())
			}
			left2 = append(left2, grp...)
		}

		line2 = p.composeLine(W, left2, right2)
	}

	// Session note: when the session is paused, the note takes line 2's
	// (now-frozen) version-control slot, keeping the age on the right. When it is
	// running, line 2's live VC signal is preserved and the note gets its own
	// indented third line. No note → both branches are skipped and the row is
	// unchanged.
	var line3 string
	if note := p.noteSeg(i); note.plain != "" {
		indent := p.seg(strings.Repeat(" ", indentW), th.Palette.FgDim)
		if i.Paused() {
			var right2 []rowSeg
			if age, ok := p.ageSeg(i); ok {
				right2 = append(right2, age)
			}
			line2 = p.composeLine(W, []rowSeg{indent, note}, right2)
		} else {
			line3 = p.composeLine(W, []rowSeg{indent, note}, nil)
		}
	}

	// --- Left marker (mark glyph when marked, else accent bar when selected) ---
	// Marked outranks selected for line 1's one-column gutter: a marked row still
	// shows as the cursor via its elevated row background (newRowPaint), so the
	// mark glyph can claim column 0 without losing the cursor. The check is a
	// discrete glyph, so it leads line 1 alone — continuation lines carry the
	// accent bar (a continuous rail) only when this is the cursor row, never a
	// repeated check, which would read as duplicate marks.
	bar := p.seg(g.SelectionMark, th.Palette.Accent).render()
	marker := p.pad(1)
	switch {
	case marked:
		marker = p.seg(g.MarkChecked, th.Palette.Accent).render()
	case selected:
		marker = bar
	}
	cont := p.pad(1)
	if selected {
		cont = bar
	}
	rows := []string{marker + line1, cont + line2}
	if line3 != "" {
		rows = append(rows, cont+line3)
	}
	return lipgloss.JoinVertical(lipgloss.Left, rows...)
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
	if l.Filtering() || l.filterActive {
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

	// Render the list group by group, in the user's existing (reorderable) order. Every group
	// gets a header, so the project a session belongs to is always visible — even with a single
	// repo. Only a multi-repo list is foldable; a lone group's header drops its fold marker and
	// is never a selectable row, so selectedIdx stays a flat index into l.items. A collapsed
	// group renders only its header (which doubles as its anchor's row) and suppresses its
	// members. An active filter is the sole visibility gate and overrides collapse (see
	// isHidden), so a folded group expands to reveal its matches while filtering.
	filtering := l.Filtering()
	distinct := l.distinctRepoCount()
	showRepos := distinct > 0
	foldable := distinct > 1
	accountGroupingVisible := l.AccountClusteringVisible()
	// Default to showing row badges; the row loop suppresses each one only when it is
	// redundant with the cluster it renders under (see below).
	l.renderer.hideClaudeAccountBadge = false
	haveAcct := false
	prevAcct := ""
	first := true
	for i := 0; i < len(l.items); {
		key := repoKey(l.items[i])
		start, end := l.groupBounds(i)
		collapsed := foldable && l.collapsed[key] && !filtering

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

		if accountGroupingVisible {
			acct := accountKey(l.items[start])
			if !haveAcct || acct != prevAcct {
				appendBlock(l.renderAccountDivider(acct))
			}
			haveAcct = true
			prevAcct = acct
		}

		if showRepos {
			headerSelected := collapsed && l.selectedIdx == start
			ni := l.groupNeedsInputCount(start, end)
			ur := l.groupUnreadCount(start, end)
			var accent theme.AnyColor
			if accountGroupingVisible {
				anchor := l.items[start]
				if anchor.ClaudeAccountName() != "" && !anchor.ClaudeAccountIsDefault() {
					accent = theme.Current().Palette.Accent
				}
			}
			at := appendBlock(zone.Mark(listHeaderZoneID(key), l.renderRepoHeader(key, collapsed, end-start, ni, ur, headerSelected, foldable, accent)))
			if headerSelected {
				selStart, selH = at, len(lines)-at
			}
		}
		if !collapsed {
			for j := start; j < end; j++ {
				if l.isHidden(j) {
					continue
				}
				// Suppress the per-row account badge only when it is redundant with the
				// divider this row renders under — i.e. the row's concrete account name
				// equals the cluster's divider label (accountKey of the block anchor: the
				// pool name for a pooled cluster, else the account name). Comparing
				// accountKey-to-accountKey would hide EVERY member badge inside a pool
				// cluster (all members share the one pool key), erasing the per-member
				// distribution the account view exists to reveal; the badge shows the
				// concrete member, so it is redundant only against a same-named divider. A
				// session whose account diverges from its repo anchor (a mixed-account
				// repo) likewise keeps its badge, so the divider/tint never mislabel it.
				//
				// Only the Claude badge: the divider labels a Claude account, so it is the
				// only badge a match can make redundant. The agy badge has no divider to
				// repeat and is never suppressed here (#457).
				if accountGroupingVisible {
					l.renderer.hideClaudeAccountBadge = l.items[j].ClaudeAccountName() == accountKey(l.items[start])
				}
				at := appendBlock(zone.Mark(listRowZoneID(l.items[j]), l.renderer.Render(l.items[j], j+1, j == l.selectedIdx, l.IsMarked(l.items[j]))))
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

	// A genuinely empty list (no sessions, not even mid-filter) would otherwise
	// render a blank panel interior. Show a small CTA card instead: why the panel
	// is empty, plus the single next action. This is *content*, not a duplicate of
	// the bottom hint bar's keys, so it renders regardless of the hint_bar setting
	// (#381) — a fresh user is never stranded on a silent screen. The `n` glyph
	// comes from the registry through LabelOf, so a rebind can't strand the call to
	// action and an unbound new says "(unbound)" rather than rendering "Press  to
	// start your first agent" — a sentence with a hole where its key belongs. Guard on
	// filterActive too: an empty-query filter still shows its filter bar above, and
	// a no-match filter renders its own "no matches" line, so the CTA stays out of
	// both filtered states.
	if len(l.items) == 0 && !filtering && !l.filterActive {
		th := theme.Current()
		innerW := l.width - 2
		if innerW < 1 {
			innerW = 1
		}
		nKey := keys.LabelOf(keys.KeyNew)
		title := lipgloss.NewStyle().Width(innerW).Align(lipgloss.Center).
			Render(th.BoldStyle().Render("No sessions yet"))
		action := lipgloss.NewStyle().Width(innerW).Align(lipgloss.Center).Render(
			th.DimStyle().Render("Press ") +
				th.AttentionStyle().Render(nKey) +
				th.DimStyle().Render(" to start your first agent"))
		card := lipgloss.JoinVertical(lipgloss.Center, title, "", action)
		cardLines := strings.Split(card, "\n")
		lines = append(lines, cardLines...)
		// Vertically center the card within the panel interior so the empty state
		// reads as intentional rather than top-anchored.
		for top := (innerH - len(cardLines)) / 2; top > 0; top-- {
			lines = append([]string{""}, lines...)
		}
	}
	lines = l.windowLines(lines, selStart, selH, innerH)
	content := strings.Join(lines, "\n")

	// The list is the primary navigation surface, so its panel is always drawn
	// active (accent border). A dynamic focus model can flip this later.
	// The panel zone wraps outside Panel so its internal clipping cannot
	// truncate the end marker.
	k := panelKey{
		content:     content,
		updateBadge: l.updateBadge,
		driftBadge:  l.driftBadge,
		width:       l.width,
		height:      l.height,
		theme:       theme.Current(),
	}
	// zone.Mark sits INSIDE the memo, so a hit returns the already-marked bytes.
	// Outside it, every hit would still concatenate gid + panel + gid — a fresh
	// allocation the size of the whole panel, which is most of what the memo was
	// added to avoid. It is safe in the key for the same reason the tabbed window
	// marks inside its compose: Mark is `gid + v + gid` for an id already in the
	// manager's map, and the gid is stable for the process lifetime.
	//
	// The "outside Panel" note above is about PanelWithBadges' internal clipping,
	// not about the memo, and still holds — Mark wraps the panel either way.
	return l.panelMemo.Get(k, func() string {
		return zone.Mark(listPanelZoneID, k.theme.PanelWithBadges("Sessions",
			[]string{k.updateBadge, k.driftBadge}, k.content, k.width, k.height, true))
	})
}

// PanelComposeRuns reports how many times the panel chrome has actually been
// drawn, and ResetMemo drops the cached panel. Exported for the same reason
// TabbedWindow.ComposeRuns is: a test that renders twice and compares the two
// strings passes identically against a memo that never ran, so the assertion that
// carries weight is the count.
func (l *List) PanelComposeRuns() int { return l.panelMemo.Runs() }

// ResetMemo drops the memoized panel and the compose count. See PanelComposeRuns.
func (l *List) ResetMemo() { l.panelMemo.Reset() }

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
