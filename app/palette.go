package app

import (
	"github.com/ZviBaratz/atrium/keys"
	"github.com/ZviBaratz/atrium/ui"
	"github.com/ZviBaratz/atrium/ui/overlay"

	tea "github.com/charmbracelet/bubbletea"
)

// The command palette (#374). Atrium has ~50 actions reachable only by memorized
// single keys, and the ? cheatsheet is prose you can read but not act from: a user
// who knows *what* they want has to find the key, close the sheet, and press it.
// The palette closes that loop — type the verb, run the action — and because every
// row carries its key, it teaches its own accelerator and obsoletes itself.
//
// Nothing here is written per action. Rows are generated from the cheatsheet
// layout, which is itself generated from the keymap registry, so a new registry
// entry appears in the palette with no edit to this file — and
// TestPaletteListsEveryDispatchableAction fails if that ever stops being true.

// paletteRow pairs the action a row runs with the row the overlay renders. The
// overlay reports a chosen index; this is what the index means.
type paletteRow struct {
	name   keys.KeyName
	action overlay.PaletteAction
}

// paletteRows builds the palette's rows in the cheatsheet's order, which is
// deliberate: the ? screen already groups the keymap the way a user thinks about
// it (Navigate / Manage / Handoff / Groups / Other), and the palette is that
// screen made executable. Walking HelpGroups rather than the Registry is also
// what supplies each row's prose and section — the registry's own help text is a
// two-word hint-bar label, too terse to search.
//
// Skipped, each for a structural reason rather than a preference:
//   - DocOnly entries (esc, ctrl+l, ctrl+pgup/pgdn) never enter the dispatch map;
//     their keys are handled before dispatch or by the attach layer, so a row for
//     them could only ever be a no-op.
//   - dispatchExempt entries are owned by a mode handler, not the default state.
//   - KeyScreensaver is absent from the Registry entirely, which is the easter
//     egg's exclusion contract — the palette inherits it by construction.
func paletteRows() []paletteRow {
	var rows []paletteRow
	seen := map[keys.KeyName]bool{}
	for _, group := range keys.HelpGroups {
		for _, row := range group.Rows {
			// Mentions as well as Keys: a key taught inside another row's prose
			// (space, in the multi-select row) is still an action, and skipping
			// them would silently drop any future one from the palette while the
			// cheatsheet went on documenting it.
			for _, name := range append(append([]keys.KeyName{}, row.Keys...), row.Mentions...) {
				binding, ok := keys.GlobalKeyBindings[name]
				if !ok || seen[name] || !paletteRunnable(name) {
					continue
				}
				seen[name] = true
				rows = append(rows, paletteRow{
					name: name,
					action: overlay.PaletteAction{
						Key:    binding.Help().Key,
						Label:  binding.Help().Desc,
						Detail: row.Desc,
						Group:  group.Title,
					},
				})
			}
		}
	}
	return rows
}

// paletteRunnable reports whether an action can be run by name at all — the
// question the palette must answer before it offers a row, distinct from whether
// the current selection can take it (that is the row's Inert reason).
func paletteRunnable(name keys.KeyName) bool {
	if dispatchExempt[name] != "" {
		return false
	}
	for _, e := range keys.Registry {
		if e.Name == name {
			return !e.DocOnly
		}
	}
	return false
}

// openCommandPalette opens the palette. Like the command log it is a global
// surface that opens with or without a selection — "what can I do" is a fair
// question with an empty list, and the rows that need a session say so.
func (m *home) openCommandPalette() (tea.Model, tea.Cmd) {
	m.paletteRows = paletteRows()
	actions := make([]overlay.PaletteAction, len(m.paletteRows))
	for i, row := range m.paletteRows {
		actions[i] = row.action
		// Evaluated at open, against the selection as it stands. The palette is
		// modal, so nothing can move the selection out from under a row while it
		// is up (see palette_gates.go for why the gate may pre-empt at all).
		actions[i].Inert = m.paletteInertReason(row.name)
	}
	m.commandPaletteOverlay = overlay.NewCommandPaletteOverlay(actions)
	m.state = stateCommandPalette
	// tea.WindowSize re-runs layout so the overlay gets its responsive size.
	return m, tea.WindowSize()
}

// handleCommandPaletteState routes a key to the palette. Filtering and navigation
// live inside the overlay; this handles only the two ways out — esc, and an enter
// that carries an action to run.
func (m *home) handleCommandPaletteState(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if !m.commandPaletteOverlay.HandleKeyPress(msg) {
		return m, nil
	}
	// Resolve the index to an action *before* dismissing: dismissCommandPalette
	// drops paletteRows, which is what the index means. Reading it afterwards
	// resolves against a nil slice, and every chosen action silently becomes no
	// action at all.
	name, chose := m.chosenPaletteAction()
	m.dismissCommandPalette()
	if !chose {
		return m, nil
	}
	return m.runPaletteAction(name)
}

// chosenPaletteAction resolves the overlay's chosen row index against the rows
// the palette was opened with.
func (m *home) chosenPaletteAction() (keys.KeyName, bool) {
	_, idx, chose := m.commandPaletteOverlay.Chosen()
	if !chose || idx < 0 || idx >= len(m.paletteRows) {
		return 0, false
	}
	return m.paletteRows[idx].name, true
}

// dismissCommandPalette tears the palette down and returns to the list.
func (m *home) dismissCommandPalette() {
	m.commandPaletteOverlay = nil
	m.paletteRows = nil
	m.state = stateDefault
	m.menu.SetState(ui.StateDefault)
}

// runPaletteAction runs a chosen action through the same dispatch a keypress
// uses. The palette must be gone first: several actions open an overlay of their
// own, and leaving this one standing would have it clobbered a frame later.
//
// The busy gate is re-applied by hand because dispatchAction sits below it —
// handleKeyPress checks actionInFlight before resolving a name, and an action can
// go in flight while the palette is open. Without this, the palette would be the
// one way to drive tmux/git on a session an in-flight pause is tearing down.
func (m *home) runPaletteAction(name keys.KeyName) (tea.Model, tea.Cmd) {
	if m.actionInFlight && !keyAllowedWhileBusy(name) {
		return m, m.handleInfoNotice("busy — " + m.menu.BusyText())
	}
	// A dimmed row is still selectable — the settings panel's rule is "dimmed,
	// not hidden", and a row you cannot reach cannot teach you its key. So enter
	// on one has to answer, and the gate is what knows the answer: several of the
	// handlers below would refuse this silently, which from the palette is
	// indistinguishable from the key doing nothing.
	if reason := m.paletteInertReason(name); reason != "" {
		return m, m.handleInfoNotice(reason)
	}
	return m.dispatchAction(name)
}
