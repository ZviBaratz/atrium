package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui/theme"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/lipgloss"
	zone "github.com/lrstanley/bubblezone"
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
	// hideAccountBadge suppresses the per-row Claude-account badge. Set by List.String
	// when account grouping is visually active (mode == account and >1 account), so the
	// cluster divider + tinted header carry the identity instead of every row repeating it.
	hideAccountBadge bool
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

	var right1 []rowSeg
	// Per-session Claude account badge: accent for a routed account, dim for the
	// default/fallback. Shown only when an account was resolved (empty = feature
	// off / legacy session).
	if acct := i.ClaudeAccountName(); acct != "" && !r.hideAccountBadge {
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
	if i.AwaitingSetup() {
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

	// Render the list group by group, in the user's existing (reorderable) order. Every group
	// gets a header, so the project a session belongs to is always visible — even with a single
	// repo. Only a multi-repo list is foldable; a lone group's header drops its fold marker and
	// is never a selectable row, so selectedIdx stays a flat index into l.items. A collapsed
	// group renders only its header (which doubles as its anchor's row) and suppresses its
	// members. An active filter is the sole visibility gate and overrides collapse (see
	// isHidden), so a folded group expands to reveal its matches while filtering.
	filtering := l.filterQuery != ""
	distinct := l.distinctRepoCount()
	showRepos := distinct > 0
	foldable := distinct > 1
	accountGroupingVisible := l.accountGrouped() && l.distinctAccountCount() > 1
	// Default to showing row badges; the row loop suppresses each one only when it is
	// redundant with the cluster it renders under (see below).
	l.renderer.hideAccountBadge = false
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
			var accent lipgloss.TerminalColor
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
				// cluster this row renders under — i.e. its account matches the block
				// anchor's, the one the divider and tinted header already show. A session
				// whose account diverges from its repo anchor (a mixed-account repo) keeps
				// its badge, so the divider/tint never silently mislabel its identity.
				if accountGroupingVisible {
					l.renderer.hideAccountBadge = accountKey(l.items[j]) == accountKey(l.items[start])
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
	return zone.Mark(listPanelZoneID, theme.Current().PanelWithBadges("Sessions", []string{l.updateBadge, l.driftBadge}, content, l.width, l.height, true))
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
