package ui

import (
	"strings"

	"github.com/ZviBaratz/atrium/keys"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui/theme"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	xansi "github.com/charmbracelet/x/ansi"
	zone "github.com/lrstanley/bubblezone/v2"
	"github.com/mattn/go-runewidth"
)

// Hint-bar styles read the active theme at render time: keys in primary text,
// descriptions dim, separators faint, progress in accent.
func keyStyle() lipgloss.Style      { return theme.Current().FgStyle() }
func descStyle() lipgloss.Style     { return theme.Current().DimStyle() }
func sepStyle() lipgloss.Style      { return theme.Current().FaintStyle() }
func progressStyle() lipgloss.Style { return theme.Current().AccentStyle().Bold(true) }

var separator = " · "

// filterSyntaxHint is the filter bar's predicate-vocabulary tail — syntax
// help rendered desc-only, not a key hint. The bar scan guard whitelists it
// by exact match; any other free text added to a bar fails that guard.
const filterSyntaxHint = "filter: status: dirty behind pr: account: note: effort: model: mode:"

// MenuState represents different states the menu can be in
type MenuState int

const (
	// StateDefault is the hint bar shown when a session is selected.
	StateDefault MenuState = iota
	// StateEmpty is the hint bar shown when no sessions exist.
	StateEmpty
	// StateFilter is the bar shown while typing an incremental filter query.
	StateFilter
	// StateBusy is shown while an operation runs off the UI thread; the bar reports
	// it by name ("pushing…", "resuming 5 sessions…", "generating name…") via
	// busyText instead of the usual options. Set through SetBusy.
	StateBusy
	// StateHints is shown while hint (fingers) mode overlays the preview:
	// the bar teaches the mode's three gestures instead of the usual options.
	StateHints
	// StateVisual is shown while multi-select ("visual") mode is active: the bar
	// teaches the mark/act/exit gestures instead of the usual options.
	StateVisual
	// StateDiffComment is shown while diff-comment mode is active: the bar teaches
	// the line cursor's move/comment/exit gestures instead of the usual options.
	StateDiffComment
)

// defaultHintKeys are the high-value bindings the always-on bar surfaces during
// plain navigation with a running session selected. Deliberately few: the bar
// is a reminder that keys exist (and that ? lists them all), not a reference
// card. hintsFor swaps in status-specific sets so the bar never advertises an
// action the selected session can't take.
var defaultHintKeys = []keys.KeyName{keys.KeyEnter, keys.KeyNew, keys.KeyQuickSend, keys.KeyKill, keys.KeyHelp}

// pausedHintKeys replace the default set for a paused selection: it can't be
// opened or sent to, so the bar points at resume instead. Copy-branch earns a
// slot in this set alone: the branch is what you parked the session to pick up
// elsewhere, and the paused preview names the same key — it has to, since this
// bar can be switched off — so the two must not disagree.
var pausedHintKeys = []keys.KeyName{keys.KeyResume, keys.KeyCopyBranch, keys.KeyNew, keys.KeyKill, keys.KeyHelp}

// pausedDirectHintKeys drop copy-branch for a parked direct (non-git) session:
// it has no worktree and no branch, so y would only report "no branch to copy
// yet". Such a session never reaches Paused through Pause() (which refuses it)
// but does through RecoverLostSession when its pane dies.
var pausedDirectHintKeys = []keys.KeyName{keys.KeyResume, keys.KeyNew, keys.KeyKill, keys.KeyHelp}

// needsInputHintKeys replace the default set while the agent is blocked on a
// prompt: answering it is the action that unblocks everything else, so this
// branch outranks the PR/dirty sets in hintsFor.
var needsInputHintKeys = []keys.KeyName{keys.KeyEnter, keys.KeyNew, keys.KeyApprove, keys.KeyQuickSend, keys.KeyKill, keys.KeyHelp}

// dirtyHintKeys extend the default set when the selected session has work on
// its branch — the moment pause/push become the actions that matter.
var dirtyHintKeys = []keys.KeyName{keys.KeyEnter, keys.KeyNew, keys.KeyQuickSend, keys.KeyPause, keys.KeySubmit, keys.KeyKill, keys.KeyHelp}

// mergeableHintKeys replace the default set for a session whose PR is ready to
// ship (open, not blocked). Merge is the headline action; push stays for any
// last-minute fixups before merging, while quick-send and pause drop out — the
// work is effectively done.
var mergeableHintKeys = []keys.KeyName{keys.KeyEnter, keys.KeyNew, keys.KeyMerge, keys.KeyOpenPR, keys.KeySubmit, keys.KeyKill, keys.KeyHelp}

// prBlockedHintKeys surface for a session that has a PR which isn't ready to
// merge yet (draft, CI pending, conflicts). Merge would no-op, so the headline is
// "open the PR to go look at it"; push stays for last-minute fixups.
var prBlockedHintKeys = []keys.KeyName{keys.KeyEnter, keys.KeyNew, keys.KeyOpenPR, keys.KeySubmit, keys.KeyKill, keys.KeyHelp}

// creatableHintKeys replace the default set for a pushed session with no PR yet —
// the moment "create PR" is the action that matters. Push stays for last-minute
// fixups before opening the PR; create is the headline.
var creatableHintKeys = []keys.KeyName{keys.KeyEnter, keys.KeyNew, keys.KeyCreate, keys.KeySubmit, keys.KeyKill, keys.KeyHelp}

// emptyHintKeys are the bindings surfaced when no sessions exist yet. The n/N
// distinction is noise for a zero-session user, so only n appears.
var emptyHintKeys = []keys.KeyName{keys.KeyNew, keys.KeyHelp, keys.KeyQuit}

// NoticeLevel grades a transient notice shown in the menu row.
type NoticeLevel int

const (
	// NoticeInfo is a neutral acknowledgment ("branch copied").
	NoticeInfo NoticeLevel = iota
	// NoticeError is a failure or a state-guard explanation.
	NoticeError
)

// Menu is the bottom hint bar: a single line of the most useful keybindings for
// the current UI state, with ? as the doorway to the full cheatsheet. It also
// doubles as the home of transient feedback: a notice temporarily replaces the
// hints on the same reserved row, so feedback never changes the frame height.
type Menu struct {
	height, width int
	state         MenuState
	hasInstance   bool
	activeTab     int
	notice        string
	noticeLevel   NoticeLevel
	// quiet blanks the always-on hint line (StateDefault/StateEmpty) so chrome-free
	// mode (hint_bar off) keeps the reserved row but renders nothing on it. A notice
	// still overrides the blank — it is checked first in String — and the contextual
	// states (Filter/Visual/DiffComment/Busy) still render, since they
	// are forced visible to teach a gesture or report progress even with the bar off.
	quiet        bool
	contextHints []keys.KeyName
	// busy holds one progress line per owner. Two kinds of operation compete for
	// this single row: a modal action (kill, push, resume) that also freezes the
	// keyboard, and a background one (name generation, opening a PR) that does
	// not. BusyAction outranks BusyBackground so the row always names the thing
	// the user is actually blocked on.
	//
	// This replaces two hand-rolled half-fixes in the app package whose only job
	// was stopping the name-generation row and the action row from clobbering each
	// other — each guarding one direction only.
	busy [busyOwnerCount]string
	// clickTargets are the dispatch keys of the hint-bar entries the last
	// String() marked as click zones, in render order. KeyAtZone walks them to
	// resolve a click back to the key it fires. Rebuilt every String() so a state
	// or width change can't leave a stale target clickable.
	clickTargets []string
}

// NewMenu returns a Menu in the empty state.
func NewMenu() *Menu {
	return &Menu{state: StateEmpty}
}

// SetState updates the menu state.
func (m *Menu) SetState(state MenuState) {
	m.state = state
}

// BusyOwner distinguishes the two kinds of operation that can claim the progress
// row. They differ in whether the user is blocked, so they must not overwrite each
// other arbitrarily — see Menu.busy.
type BusyOwner int

const (
	// BusyAction is a modal operation: it mutates shared state and gates keys
	// (home.actionInFlight). It wins the row.
	BusyAction BusyOwner = iota
	// BusyBackground is a read-only or additive operation the user can act
	// around. It shows only while no action is in flight.
	BusyBackground
	busyOwnerCount
)

// SetBusy switches the bar to StateBusy and sets owner's progress line (e.g.
// "pushing…"). This state survives the periodic instanceChanged ticks (SetInstance
// only rewrites Default/Empty).
//
// It also clears any live notice. The incoming progress row is strictly newer than
// whatever toast is on screen, and a notice is rendered ahead of every state — so
// without this a 5s toast could hide a just-armed label for its whole lifetime,
// leaving the app looking frozen while it refused keys with a notice.
func (m *Menu) SetBusy(owner BusyOwner, text string) {
	m.state = StateBusy
	m.busy[owner] = text
	m.notice, m.noticeLevel = "", NoticeInfo
}

// ClearBusy drops owner's progress line. When the other owner still holds one the
// bar keeps reporting that instead of falling back to the hints.
func (m *Menu) ClearBusy(owner BusyOwner) {
	m.busy[owner] = ""
	if m.busyText() == "" && m.state == StateBusy {
		m.state = StateDefault
	}
}

// busyText is the line the row should show: the action's if one is in flight,
// else the background operation's.
func (m *Menu) busyText() string {
	if m.busy[BusyAction] != "" {
		return m.busy[BusyAction]
	}
	return m.busy[BusyBackground]
}

// State returns the menu's current state (which hint set the bar shows).
func (m *Menu) State() MenuState {
	return m.state
}

// BusyText returns the progress line the row is currently showing (empty if none).
// The in-flight input gate reuses it so a swallowed key names the operation.
func (m *Menu) BusyText() string {
	return m.busyText()
}

// SetInstance records the selected session and derives the hint set from its
// status, so the bar only advertises actions the selection can actually take.
// Special states (Filter, Busy) persist across the periodic
// instanceChanged ticks.
func (m *Menu) SetInstance(instance *session.Instance) {
	m.hasInstance = instance != nil
	m.contextHints = hintsFor(instance)
	if m.state == StateDefault || m.state == StateEmpty {
		if m.hasInstance {
			m.state = StateDefault
		} else {
			m.state = StateEmpty
		}
	}
}

// hintsFor picks the hint set matching the selection's state. Affordances must
// track state: a bar that advertises "open" and "send" on a paused session is
// advertising no-ops.
func hintsFor(instance *session.Instance) []keys.KeyName {
	return withRunHint(instance, baseHintsFor(instance))
}

// withRunHint adds the dev-command key to a hint set, but only for a running session
// whose repository actually declares a run_command (#389) — the bar's whole rule is that
// it never advertises an action the selection cannot take, and `d` on an unconfigured
// repo is a refusal notice.
//
// It copies rather than appending in place. Every set above is a package-level slice
// shared by every render, and appending to one would either alias into its neighbour's
// backing array or grow it permanently for sessions that never earned the hint. The
// scroll hint in String() takes the same precaution for the same reason.
//
// A paused session is excluded: its worktree is gone, so there is nothing to run in.
// RunConfigured is false until the first poll has looked, so the bar under-advertises
// briefly rather than promising something that might refuse — and the cheatsheet and the
// command palette carry the key regardless.
func withRunHint(instance *session.Instance, base []keys.KeyName) []keys.KeyName {
	if instance == nil || len(base) == 0 || instance.Paused() || instance.IsDirect() || !instance.RunConfigured() {
		return base
	}
	// Second from the end. Every set hintsFor can return closes on `?`, and the entry
	// that teaches where the rest of the keys are belongs last whatever precedes it.
	out := make([]keys.KeyName, 0, len(base)+1)
	out = append(out, base[:len(base)-1]...)
	out = append(out, keys.KeyRunCommand, base[len(base)-1])
	return out
}

// baseHintsFor is hintsFor's status ladder, before the conditional dev-command entry.
func baseHintsFor(instance *session.Instance) []keys.KeyName {
	if instance == nil {
		return nil
	}
	if instance.Paused() {
		if instance.IsDirect() {
			return pausedDirectHintKeys
		}
		return pausedHintKeys
	}
	if instance.GetStatus() == session.NeedsInput {
		return needsInputHintKeys
	}
	if !instance.IsDirect() {
		// A PR that's ready to merge is the most action-relevant state: surface
		// merge ahead of the pause/push pair. A PR that exists but is blocked still
		// surfaces "open PR" so you can go look at it on GitHub.
		if pr := instance.GetPRStatus(); pr != nil && pr.HasPR {
			if pr.MergeBlockedReason() == "" {
				return mergeableHintKeys
			}
			return prBlockedHintKeys
		}
		// A pushed branch with no PR yet is next: surface create. This must come
		// before the dirty check — a pushed branch is also "ahead" (Commits > 0),
		// so create must win to advertise c over a bare push/pause pair.
		if pr := instance.GetPRStatus(); pr != nil && pr.CreateBlockedReason() == "" {
			return creatableHintKeys
		}
		if stats := instance.GetDiffStats(); stats != nil && stats.Error == nil &&
			(stats.Dirty || stats.Commits > 0 || !stats.IsEmpty()) {
			return dirtyHintKeys
		}
	}
	return defaultHintKeys
}

// SetActiveTab updates the currently active tab; the panes with a scroll mode
// (diff/terminal) add a scroll hint to the default bar.
func (m *Menu) SetActiveTab(tab int) {
	m.activeTab = tab
}

// SetNotice shows transient feedback in place of the hints. Newlines are
// flattened (mirroring the error box) because the row is single-line by
// construction.
func (m *Menu) SetNotice(text string, level NoticeLevel) {
	m.notice = strings.Join(strings.Split(text, "\n"), "//")
	m.noticeLevel = level
}

// SetQuiet toggles chrome-free rendering of the always-on hint line. The menu row
// is still reserved by the layout (menuVisible stays true in stateDefault); quiet
// only decides whether that reserved row shows the hints or a blank line.
func (m *Menu) SetQuiet(quiet bool) { m.quiet = quiet }

// ClearNotice restores the hint line.
func (m *Menu) ClearNotice() {
	m.notice = ""
}

// HasNotice reports whether a notice currently occupies the row.
func (m *Menu) HasNotice() bool {
	return m.notice != ""
}

// NoticeText returns the current transient notice (empty when none is set). The
// text is the flattened form stored by SetNotice; it is exposed so callers can
// tell which message currently rides the row.
func (m *Menu) NoticeText() string {
	return m.notice
}

// SetSize sets the width of the window. The menu will be centered horizontally within this width.
func (m *Menu) SetSize(width, height int) {
	m.width = width
	m.height = height
}

// hintZoneID namespaces a hint-bar entry's click zone by the key it dispatches.
// The app resolves a click on this zone back to that key and re-injects it
// through the normal key path (app.handleMouse), so clicking a bar entry is
// exactly the keypress it advertises — every mouse action mirrors a key action.
func hintZoneID(dispatchKey string) string { return "hintbar:" + dispatchKey }

// renderEntry renders one "key desc" hint-bar segment (the same words the bar
// has always shown; clicking anywhere on it fires the key, btop-style).
func renderEntry(b key.Binding) string {
	return keyStyle().Render(b.Help().Key) + " " + descStyle().Render(b.Help().Desc)
}

// markHint wraps a rendered entry in a click zone for dispatchKey and records it
// so KeyAtZone can resolve a later click. An empty dispatchKey marks nothing, so
// a teaching-only cue (hint mode's "a–z", or a compound like "p/r/x" that maps
// to no single key) renders inert — clicking it does nothing surprising.
func (m *Menu) markHint(dispatchKey, seg string) string {
	if dispatchKey == "" {
		return seg
	}
	m.clickTargets = append(m.clickTargets, dispatchKey)
	return zone.Mark(hintZoneID(dispatchKey), seg)
}

// renderModeLine renders a mode bar (filter / hint / visual) from raw bindings.
// A binding with exactly one key is clickable via that key; range/compound
// teaching entries (no keys, or several) stay inert (see singleDispatchKey).
func (m *Menu) renderModeLine(bindings []key.Binding) string {
	var s strings.Builder
	for i, b := range bindings {
		if i > 0 {
			s.WriteString(sepStyle().Render(separator))
		}
		s.WriteString(m.markHint(singleDispatchKey(b), renderEntry(b)))
	}
	return s.String()
}

// renderHintLine renders the named global bindings, each clickable via its
// primary key. Every bar — context hints and mode bars alike — renders through
// renderEntry, so a bar can only name keys some registry table carries (pinned
// by TestMenuBars_KeysExistInRegistry).
func (m *Menu) renderHintLine(names []keys.KeyName) string {
	var s strings.Builder
	for i, name := range names {
		if i > 0 {
			s.WriteString(sepStyle().Render(separator))
		}
		b := keys.GlobalKeyBindings[name]
		s.WriteString(m.markHint(primaryDispatchKey(b), renderEntry(b)))
	}
	return s.String()
}

// primaryDispatchKey is the key a KeyName entry fires on click: its first bound
// key (KeyEnter's "enter", not its "o" alias). A KeyName is one action, so its
// whole "↵/o open" entry dispatches enter even though the binding lists both.
func primaryDispatchKey(b key.Binding) string {
	if ks := b.Keys(); len(ks) > 0 {
		return ks[0]
	}
	return ""
}

// singleDispatchKey is the key a mode-bar entry fires on click, but only when
// the binding carries exactly one. A compound like "p/r/x" (pause/resume/kill
// the marked set) maps to no single action, so it stays inert rather than
// firing a surprising one of the three.
func singleDispatchKey(b key.Binding) string {
	if ks := b.Keys(); len(ks) == 1 {
		return ks[0]
	}
	return ""
}

// KeyAtZone reports the dispatch key of the hint-bar entry a mouse event lands
// on, if any. Only entries the last String() marked are considered, so a bar
// with no clickable entries (busy / notice / generating) resolves nothing —
// and, before the first zone scan, every InBounds check is false.
func (m *Menu) KeyAtZone(msg tea.MouseMsg) (string, bool) {
	for _, k := range m.clickTargets {
		if zone.Get(hintZoneID(k)).InBounds(msg) {
			return k, true
		}
	}
	return "", false
}

func (m *Menu) String() string {
	// Rebuild the click-zone target list from scratch each render: the bar's
	// entries depend on state and width, so a stale target must never survive a
	// change. The notice / busy / generating paths add none (they show no keys).
	m.clickTargets = m.clickTargets[:0]

	if m.notice != "" {
		style := theme.Current().FgStyle()
		if m.noticeLevel == NoticeError {
			style = theme.Current().DangerStyle()
		}
		text := m.notice
		// Truncate rather than wrap: a wrapped notice would grow the row and
		// cause exactly the layout shift this mechanism exists to prevent.
		if limit := m.width - 2; limit >= 0 && runewidth.StringWidth(text) > limit {
			text = runewidth.Truncate(text, limit, "…")
		}
		return centerInBox(m.width, m.height, style.Render(text))
	}

	// Chrome-free: the reserved row (menuVisible stays true in stateDefault) renders
	// blank instead of the hints. Only the always-on hint sets blank — the contextual
	// states in the switch below (Filter/Visual/DiffComment/Busy) are
	// forced visible to teach a gesture or report progress, so they render even with
	// the bar off.
	if m.quiet && (m.state == StateDefault || m.state == StateEmpty) {
		return centerInBox(m.width, m.height, "")
	}

	var line string
	switch m.state {
	case StateBusy:
		// While an operation runs off the UI thread, the bar names it.
		line = progressStyle().Render(m.busyText())
	case StateFilter:
		// Actions first (see keys.FilterModeHints) so that on a narrow terminal
		// the width truncation below drops the predicate vocabulary tail, never
		// the accept/clear actions.
		line = m.renderModeLine(keys.FilterModeHints) +
			sepStyle().Render(separator) +
			descStyle().Render(filterSyntaxHint)
	case StateHints:
		line = m.renderModeLine(keys.HintModeHints)
	case StateVisual:
		line = m.renderModeLine(keys.VisualModeHints)
	case StateDiffComment:
		line = m.renderModeLine(keys.DiffCommentModeHints)
	case StateEmpty:
		line = m.renderHintLine(emptyHintKeys)
	default: // StateDefault
		hints := m.contextHints
		if hints == nil {
			hints = defaultHintKeys
		}
		if m.activeTab == DiffTab || m.activeTab == TerminalTab {
			hints = append(append([]keys.KeyName{}, hints...), keys.KeyShiftUp)
		}
		line = m.renderHintLine(hints)
	}

	// Truncate rather than overflow: a hint line wider than the terminal would
	// wrap and break the one-row layout contract (same rule as notices above).
	if limit := m.width; limit > 0 && lipgloss.Width(line) > limit {
		line = xansi.Truncate(line, limit-1, "…")
	}
	return centerInBox(m.width, m.height, line)
}
