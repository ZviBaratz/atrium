package ui

import (
	"strings"

	"github.com/ZviBaratz/atrium/keys"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui/theme"

	"github.com/charmbracelet/lipgloss"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
)

// Hint-bar styles read the active theme at render time: keys in primary text,
// descriptions dim, separators faint, progress in accent.
func keyStyle() lipgloss.Style      { return theme.Current().FgStyle() }
func descStyle() lipgloss.Style     { return theme.Current().DimStyle() }
func sepStyle() lipgloss.Style      { return theme.Current().FaintStyle() }
func progressStyle() lipgloss.Style { return theme.Current().AccentStyle().Bold(true) }

var separator = " · "

// MenuState represents different states the menu can be in
type MenuState int

const (
	// StateDefault is the hint bar shown when a session is selected.
	StateDefault MenuState = iota
	// StateEmpty is the hint bar shown when no sessions exist.
	StateEmpty
	// StateFilter is the bar shown while typing an incremental filter query.
	StateFilter
	// StateGeneratingName is shown while a session name is being generated in the
	// background; the hint bar reports progress instead of the usual options.
	StateGeneratingName
)

// defaultHintKeys are the high-value bindings the always-on bar surfaces during
// plain navigation with a running session selected. Deliberately few: the bar
// is a reminder that keys exist (and that ? lists them all), not a reference
// card. hintsFor swaps in status-specific sets so the bar never advertises an
// action the selected session can't take.
var defaultHintKeys = []keys.KeyName{keys.KeyEnter, keys.KeyNew, keys.KeyQuickSend, keys.KeyKill, keys.KeyHelp}

// pausedHintKeys replace the default set for a paused selection: it can't be
// opened or sent to, so the bar points at resume instead.
var pausedHintKeys = []keys.KeyName{keys.KeyResume, keys.KeyNew, keys.KeyKill, keys.KeyHelp}

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
	contextHints  []keys.KeyName
}

// NewMenu returns a Menu in the empty state.
func NewMenu() *Menu {
	return &Menu{state: StateEmpty}
}

// SetState updates the menu state.
func (m *Menu) SetState(state MenuState) {
	m.state = state
}

// SetInstance records the selected session and derives the hint set from its
// status, so the bar only advertises actions the selection can actually take.
// Special states (Filter, GeneratingName) persist across the periodic
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
	if instance == nil {
		return nil
	}
	if instance.Paused() {
		return pausedHintKeys
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

// ClearNotice restores the hint line.
func (m *Menu) ClearNotice() {
	m.notice = ""
}

// HasNotice reports whether a notice currently occupies the row.
func (m *Menu) HasNotice() bool {
	return m.notice != ""
}

// SetSize sets the width of the window. The menu will be centered horizontally within this width.
func (m *Menu) SetSize(width, height int) {
	m.width = width
	m.height = height
}

// renderHintLine renders a flat "key desc · key desc" line for the given bindings.
func renderHintLine(names []keys.KeyName) string {
	var s strings.Builder
	for i, k := range names {
		binding := keys.GlobalkeyBindings[k]
		if i > 0 {
			s.WriteString(sepStyle().Render(separator))
		}
		s.WriteString(keyStyle().Render(binding.Help().Key))
		s.WriteString(" ")
		s.WriteString(descStyle().Render(binding.Help().Desc))
	}
	return s.String()
}

func (m *Menu) String() string {
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
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, style.Render(text))
	}

	var line string
	switch m.state {
	case StateGeneratingName:
		// While generating a name, the bar shows a single status line.
		line = progressStyle().Render("✨ Generating name…")
	case StateFilter:
		line = keyStyle().Render("enter") + " " + descStyle().Render("accept") +
			sepStyle().Render(separator) +
			keyStyle().Render("esc") + " " + descStyle().Render("clear")
	case StateEmpty:
		line = renderHintLine(emptyHintKeys)
	default: // StateDefault
		hints := m.contextHints
		if hints == nil {
			hints = defaultHintKeys
		}
		if m.activeTab == DiffTab || m.activeTab == TerminalTab {
			hints = append(append([]keys.KeyName{}, hints...), keys.KeyShiftUp)
		}
		line = renderHintLine(hints)
	}

	// Truncate rather than overflow: a hint line wider than the terminal would
	// wrap and break the one-row layout contract (same rule as notices above).
	if limit := m.width; limit > 0 && lipgloss.Width(line) > limit {
		line = xansi.Truncate(line, limit-1, "…")
	}
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, line)
}
