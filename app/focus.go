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
	// focusInspector is declared for the inspector surface, which does not
	// exist yet; routeFocusKey treats it like focusTabs so the enum is
	// complete, but nothing can reach it.
	focusInspector
)

// currentFocus is the effective focus target. It reads the explicit home.focus
// first, then derives focusTabs from the ACTIVE tab's scroll mode — tab-scoped
// on purpose, not TabbedWindow.paneScrolling: a preview snapshot left scrolled
// in the background must not re-target the diff tab's nav keys (the diff pane
// scrolls live without a mode and never claims focus). Deriving rather than
// storing is what keeps the pane-internal scroll exits (wheel at the bottom,
// snapshot owner change) from ever leaving a stale focus bit behind.
//
// Focus is a stateDefault concept: in every other state the surface handler
// owns the keys, so the question this answers does not arise there.
func (m *home) currentFocus() focusTarget {
	if m.state != stateDefault || m.tabbedWindow == nil {
		return focusList
	}
	if m.focus != focusList {
		return m.focus
	}
	if (m.tabbedWindow.IsInPreviewTab() && m.tabbedWindow.IsPreviewInScrollMode()) ||
		(m.tabbedWindow.IsInTerminalTab() && m.tabbedWindow.IsTerminalInScrollMode()) {
		return focusTabs
	}
	return focusList
}

// routeFocusKey consumes the pane-local nav keys while the tabs (or, later,
// the inspector) hold focus; every other key reports unhandled and falls
// through to dispatchAction, so q quits, n creates, and the palette reaches
// everything. Runs after the busy gate — the gate already admits the nav
// keys, and keeping it the single busy chokepoint means any key this switch
// grows is busy-checked the same way a dispatched one is.
//
// The routed keys return instanceChanged like the scroll actions in
// dispatchAction do: the pane content repaints immediately instead of on the
// next poll tick (the terminal pane clears its viewport on a bottom exit and
// waits for fresh content).
func (m *home) routeFocusKey(name keys.KeyName) (tea.Cmd, bool) {
	switch m.currentFocus() {
	case focusTabs, focusInspector:
		switch name {
		case keys.KeyUp:
			m.tabbedWindow.ScrollUp(1)
			return m.instanceChanged(), true
		case keys.KeyDown:
			m.tabbedWindow.ScrollDown(1)
			return m.instanceChanged(), true
		}
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
// A function returning the slice, not a package-level var: the rungs close
// over home methods, and a package-level initializer here would join the
// init-order contract surfaceSpecs documents (#856) for no benefit.
func escLadder() []escRung {
	return []escRung{
		// Scroll exit: the active tab's pane leaves scroll mode and resumes
		// the live view. Tab-scoped like currentFocus, and for the same
		// reason: esc on the diff tab must not kill a background preview
		// snapshot. The two arms are mutually exclusive (one tab is active),
		// so this is one rung, not an ordered pair.
		{
			when: func(m *home) bool {
				return (m.tabbedWindow.IsInPreviewTab() && m.tabbedWindow.IsPreviewInScrollMode()) ||
					(m.tabbedWindow.IsInTerminalTab() && m.tabbedWindow.IsTerminalInScrollMode())
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
			when: func(m *home) bool { return m.list.FilterQuery() != "" },
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
