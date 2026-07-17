package app

// Named layout presets cycled on a single key (keys.KeyLayoutPreset). Atrium's
// one split — the session list takes listRatio of the width, the tabbed window
// the rest — becomes a set of one-key modes: monitor (wide list), default,
// review (thin list, diff focused), and focus (list hidden, tabs full width).
// The active preset persists in State and is restored on relaunch; < / > (and a
// divider drag) still fine-tune the split as a custom override that complements
// the cycle rather than fighting it (btop's per-box override model).

import (
	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/ui"

	tea "github.com/charmbracelet/bubbletea"
)

// layoutPreset is one named arrangement in the cycle. Presets differ along three
// axes: how much width the list gets (ratio), whether the list is hidden so the
// tabbed window fills the terminal (listHidden — "focus"), and whether
// activating the preset jumps to a tab (focusTab; -1 leaves the tab as-is).
type layoutPreset struct {
	name string
	// ratio is the list's width fraction when the preset activates. Zero means
	// "keep the current split" — used by focus, where the list is hidden and its
	// width is irrelevant, so the pre-focus ratio survives for the trip back.
	ratio      float64
	listHidden bool
	focusTab   int
	// notice is the transient text flashed through the notice ladder on activation
	// so the switch is legible.
	notice string
}

// layoutPresets is the cycle the layout key steps through, in order. The list
// shrinks at each step until focus hides it: monitor (widest) → default →
// review (thinnest, diff focused) → focus (hidden, full-width tabs) → wrap. The
// split ratios are pinned to the persisted clamp bounds (config.*ListRatio) so a
// preset can never name a split the divider itself couldn't reach.
var layoutPresets = []layoutPreset{
	{name: "monitor", ratio: config.MaxListRatio, focusTab: -1, notice: "layout: monitor — wide list"},
	{name: "default", ratio: config.DefaultListRatio, focusTab: -1, notice: "layout: default"},
	{name: "review", ratio: config.MinListRatio, focusTab: ui.DiffTab, notice: "layout: review — diff focused"},
	{name: "focus", ratio: 0, listHidden: true, focusTab: -1, notice: "layout: focus — list hidden (esc to exit)"},
}

// defaultPresetIndex is the preset a fresh install (or an unrecognized persisted
// name) starts on: "default" reproduces the historical single-layout behavior.
const defaultPresetIndex = 1

// presetIndexByName returns the index of the named preset, or defaultPresetIndex
// when the name is empty or unrecognized (an older state file, or a hand-edited
// state.json).
func presetIndexByName(name string) int {
	for i, p := range layoutPresets {
		if p.name == name {
			return i
		}
	}
	return defaultPresetIndex
}

// currentPreset is the active named preset — the base the layout key cycles from
// and < / > override. The index is always in range (seeded from presetIndexByName,
// only ever stepped modulo len), but clamp defensively so a bare struct-literal
// home in tests can't panic.
func (m *home) currentPreset() layoutPreset {
	if m.layoutIndex < 0 || m.layoutIndex >= len(layoutPresets) {
		return layoutPresets[defaultPresetIndex]
	}
	return layoutPresets[m.layoutIndex]
}

// listHidden reports whether the session list is hidden so the tabbed window
// fills the terminal. Only the focus preset hides it, and only while the user
// has not overridden the split with < / > (or a drag): adjusting the divider is
// an explicit request for a list, so a custom override always shows it — which
// also keeps < / > from being a dead key in focus.
func (m *home) listHidden() bool {
	return m.currentPreset().listHidden && !m.layoutCustom
}

// cycleLayoutPreset advances to the next preset (wrapping) and applies it. It
// clears any < / > override so the cycle lands on the clean named preset, and
// remembers the previous index so Esc can back out of focus to it.
func (m *home) cycleLayoutPreset() tea.Cmd {
	m.layoutPrev = m.layoutIndex
	m.layoutIndex = (m.layoutIndex + 1) % len(layoutPresets)
	m.layoutCustom = false
	return m.applyLayoutPreset()
}

// exitFocusLayout returns from focus to the preset that preceded it, so focus is
// never a dead end. This is Esc's escape hatch; the layout key instead continues
// the cycle onward (both leave focus). Harmless if called outside focus — it just
// re-applies a preset.
func (m *home) exitFocusLayout() tea.Cmd {
	m.layoutIndex = m.layoutPrev
	m.layoutCustom = false
	return m.applyLayoutPreset()
}

// applyLayoutPreset installs the current preset: it sets the list ratio (unless
// the preset keeps the current split), jumps to the preset's tab if it names one,
// persists the choice alongside the live ratio, reflows the panes, and flashes
// the preset name through the notice ladder so the switch is legible.
func (m *home) applyLayoutPreset() tea.Cmd {
	p := m.currentPreset()
	if p.ratio > 0 {
		m.listRatio = p.ratio
	}
	if p.focusTab >= 0 && m.tabbedWindow != nil {
		m.tabbedWindow.SetActiveTab(p.focusTab)
		if m.menu != nil {
			m.menu.SetActiveTab(m.tabbedWindow.GetActiveTab())
		}
	}
	if err := m.appState.SetLayout(p.name, m.layoutCustom, m.listRatio); err != nil {
		return m.handleError(err)
	}
	m.recomputeLayout()
	return tea.Batch(m.handleInfoNotice(p.notice), m.instanceChanged())
}

// setCustomRatio applies a < / > (or divider-drag) split adjustment: it flags the
// layout as a custom override of the current preset — which un-hides the list in
// focus and drops the preset's exact ratio while keeping it as the cycle base —
// persists preset+override+ratio together, and reflows. The clamp lives in
// appState (SetLayout), so the stored and live values stay in lockstep exactly
// as SetListRatio did before.
func (m *home) setCustomRatio(ratio float64) tea.Cmd {
	m.layoutCustom = true
	if err := m.appState.SetLayout(m.currentPreset().name, true, ratio); err != nil {
		return m.handleError(err)
	}
	m.listRatio = m.appState.GetListRatio()
	m.recomputeLayout()
	return m.instanceChanged()
}
