package app

// The keyboard-focus model for the stateDefault surface (#803): which region —
// the session list, the tabbed panes, or (later) the inspector — owns the nav
// keys. Overlays are deliberately NOT focus targets: a non-default state
// already models "an overlay owns the keyboard" through surfaceSpecs' keys
// routing, so a focus stack would re-model what the state enum models — one
// enum, no stack (the program design record for #793 names the stack as the
// trap). For the same reason stateDiffComment stays a state, not a focus
// value: its surface handler already owns its keys, which is all "tabs
// focused" means.

import (
	"github.com/ZviBaratz/atrium/keys"

	tea "charm.land/bubbletea/v2"
)

// focusTarget names the region of the stateDefault frame that owns the nav
// keys. Distinct from three neighbours that also say "focus": home.focused
// (terminal-window focus, tea.FocusMsg), the "focus" layout preset (a zoomed
// layout with the list hidden), and layoutPreset.focusTab (a tab jump).
type focusTarget int

const (
	// focusList is the default: every global key behaves exactly as it always
	// has, with the nav keys moving the list selection.
	focusList focusTarget = iota
	// focusTabs hands the nav keys to the active tab's pane. It absorbs what
	// used to be implicit: a pane in scroll mode owns navigation. currentFocus
	// derives it from the pane state rather than storing it, and home.focus
	// can hold it explicitly (nothing sets that yet — the seam for review-mode
	// file navigation).
	focusTabs
	// focusInspector is declared for the inspector surface — #804's skeleton
	// tab, a constant placeholder until #805 lands its content. routeFocusKey
	// deliberately routes nothing for it: the skeleton has nothing to scroll,
	// and scrolling the tabbed panes on behalf of a surface the name does not
	// point at would be worse than falling through — so only the esc pop
	// (escLadder) acts on it. Nothing can reach it today.
	focusInspector
)

// currentFocus is the effective focus target. A scroll-captured active pane
// outranks even explicit focus: scroll mode is a modal capture — the pane
// border lights, the bar swaps to the pane vocabulary, and esc's first rung is
// the scroll exit — so the nav keys must agree with all three, whatever the
// explicit target says (it resurfaces when the capture ends, matching the
// ladder's order: scroll exit before focus pop). The predicate is
// TabbedWindow.ActivePaneInScrollMode — tab-scoped, so a preview snapshot left
// scrolled in the background cannot re-target the diff tab's nav keys; the esc
// ladder's scroll rung reads the same predicate, and the pane border reads it
// plus the preview's hint overlay (activePaneCaptured). Deriving rather than
// storing is what keeps the pane-internal scroll exits (wheel at the bottom,
// snapshot owner change) from ever leaving a stale focus bit behind.
//
// Focus is a stateDefault concept: in every other state the surface handler
// owns the keys, so the question this answers does not arise there. The nil
// guard tolerates the minimal fixture homes (settings/account tests) that run
// stateDefault without a tabbed window; escLadder's scroll rung guards the
// same way.
func (m *home) currentFocus() focusTarget {
	if m.state != stateDefault || m.tabbedWindow == nil {
		return focusList
	}
	if m.tabbedWindow.ActivePaneInScrollMode() {
		return focusTabs
	}
	return m.focus
}

// routeFocusKey consumes the pane-local nav keys while the tabs hold focus;
// every other key reports unhandled and falls through to dispatchAction, so q
// quits, n creates, and the palette reaches everything. Both of its callers
// run it after the busy gate (handleKeyPress, runPaletteAction) — the gate
// already admits the nav keys, and keeping it the single busy chokepoint means
// any key this switch grows is busy-checked the same way a dispatched one is.
//
// Down at the snapshot's bottom is consumed and held rather than passed to the
// pane — WHEN there is scrollback above to hold for: the pane's own ScrollDown
// exits scroll mode there (the wheel's and shift+↓'s tmux-copy-mode exit), and
// under key autorepeat that exit would hand the very next press to the list
// and switch the selected session mid-read. A held j pins at the bottom
// instead; esc stays the routed exit. A zero-travel snapshot — scrollback
// shorter than the viewport, so the entry position is top and bottom at once —
// exits instead of holding: there is nothing above to read, and holding would
// leave both nav keys dead on a frozen pane (this keeps the accidental-entry
// self-heal PreviewPane.ScrollDown documents for the wheel).
// On the preview and terminal tabs, up on a live pane enters scroll mode
// exactly as shift+↑ does (same TabbedWindow.ScrollUp; the diff tab scrolls
// live with no mode), and down with no snapshot is consumed with nothing to
// do — the pane owns the key even when it has no travel.
//
// The routed keys return instanceChanged like the scroll actions in
// dispatchAction do: the pane content repaints immediately instead of on the
// next poll tick (the terminal pane clears its viewport on a bottom exit and
// waits for fresh content).
func (m *home) routeFocusKey(name keys.KeyName) (tea.Cmd, bool) {
	switch m.currentFocus() {
	case focusTabs:
		switch name {
		case keys.KeyUp:
			m.tabbedWindow.ScrollUp(1)
			return m.instanceChanged(), true
		case keys.KeyDown:
			if m.tabbedWindow.ActivePaneScrollAtBottom() && !m.tabbedWindow.ActivePaneScrollAtTop() {
				return nil, true
			}
			m.tabbedWindow.ScrollDown(1)
			return m.instanceChanged(), true
		}
	case focusInspector:
		// The inspector tab is a skeleton whose constant placeholder has
		// nothing to scroll (#805 lands the content): route nothing, so its
		// nav keys fall through to dispatch instead of scrolling a pane the
		// focus name does not point at. See the focusInspector const.
	}
	return nil, false
}

// escRung is one step of esc's contextual unwind in stateDefault. when reports
// whether the rung applies; fire performs it. The rungs are ordered — see
// escLadder.
type escRung struct {
	when func(m *home) bool
	fire func(m *home) tea.Cmd
}

// escLadder is esc's contextual unwind, in the order a repeated Esc peels the
// surface back: scroll exit, focus pop, filter clear, layout exit. Exactly one
// rung fires per press (escUnwind takes the first whose when holds), so the
// order IS the behavior: a rung moved above another steals its press. The
// ladder's order test pins each adjacent pair.
//
// A function returning a fresh slice each call, like frameStates(); nothing
// here needs package-level state.
func escLadder() []escRung {
	return []escRung{
		// Scroll exit: the active tab's pane leaves scroll mode and resumes
		// the live view. Tab-scoped like currentFocus (the shared
		// ActivePaneInScrollMode), and for the same reason: esc on the diff
		// tab must not kill a background preview snapshot. This rung cannot
		// read currentFocus() == focusTabs instead: explicit focus satisfies
		// that with no scroll mode to exit, and would steal the pop rung's
		// press. The nil guard, like currentFocus's, tolerates the minimal
		// fixture homes (settings/account tests) that run stateDefault
		// without a tabbed window — no fixture or production path pairs a
		// scroll-captured pane with a nil list, so fire assumes the list the
		// way instanceChanged does.
		{
			when: func(m *home) bool {
				return m.tabbedWindow != nil && m.tabbedWindow.ActivePaneInScrollMode()
			},
			fire: func(m *home) tea.Cmd {
				if m.tabbedWindow.IsInPreviewTab() {
					// Use the selected instance from the list
					selected := m.list.GetSelectedInstance()
					if err := m.tabbedWindow.ResetPreviewToNormalMode(selected); err != nil {
						return m.handleError(err)
					}
					return m.instanceChanged()
				}
				m.tabbedWindow.ResetTerminalToNormalMode()
				return m.instanceChanged()
			},
		},
		// Focus pop: explicit focus (home.focus, not the derived reading —
		// the scroll rung above already unwinds what derivation adds) hands
		// the nav keys back to the list. The bar self-corrects on the next
		// frame; viewContent pushes the derived focus every render.
		{
			when: func(m *home) bool { return m.focus != focusList },
			fire: func(m *home) tea.Cmd {
				m.focus = focusList
				return nil
			},
		},
		// A committed filter (typed with /, accepted with Enter) is still
		// narrowing the list; Esc clears it, the expected escape hatch.
		{
			when: func(m *home) bool { return m.list != nil && m.list.FilterQuery() != "" },
			fire: func(m *home) tea.Cmd {
				m.list.ClearFilter()
				return m.instanceChanged()
			},
		},
		// The focus layout preset hides the list; Esc backs out to the preset
		// that preceded it so that zoom is never a dead end (the layout key
		// instead cycles onward). Last on purpose: it only fires once scroll
		// mode, explicit focus and any filter are already unwound, matching
		// what a user expects a repeated Esc to peel back.
		{
			when: func(m *home) bool { return m.listHidden() },
			fire: func(m *home) tea.Cmd { return m.exitFocusLayout() },
		},
	}
}

// escUnwind fires the first applicable rung of escLadder and reports whether
// one consumed the press. When none applies, esc falls through handleKeyPress
// and is swallowed at the dispatch lookup (KeyEscape is DocOnly, so it never
// resolves to an action).
func (m *home) escUnwind() (tea.Cmd, bool) {
	for _, rung := range escLadder() {
		if rung.when(m) {
			return rung.fire(m), true
		}
	}
	return nil, false
}
