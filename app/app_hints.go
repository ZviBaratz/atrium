package app

// Hint (fingers) mode: freeze the preview, label its URLs/paths/SHAs with
// short hints, and let one keystroke copy (or copy+open) a match. The hints
// package owns matching/labels/rendering; this file owns the mode's state
// machine and actions. See docs/superpowers/specs/2026-06-10-hints-copy-open-design.md.

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/ZviBaratz/atrium/hints"
	"github.com/ZviBaratz/atrium/internal/actions"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui"
	"github.com/ZviBaratz/atrium/ui/theme"

	tea "github.com/charmbracelet/bubbletea"
)

// hintStyles builds the renderer's three roles from the active theme: dim
// backdrop, success-colored match text, and the label in reverse-video
// attention color so it pops over the match's first cells.
func hintStyles() hints.Styles {
	t := theme.Current()
	return hints.Styles{
		Backdrop: t.DimStyle(),
		Match:    t.SuccessStyle(),
		MatchURL: t.SuccessStyle().Underline(true),
		Label:    t.AttentionStyle().Reverse(true).Bold(true),
	}
}

// enterHintMode validates the f keypress and enters hint mode over the
// selected session's live preview (or the visible scroll viewport when the
// pane is in scroll mode). Guards explain themselves via notices instead of
// silently swallowing the key.
func (m *home) enterHintMode() (tea.Model, tea.Cmd) {
	if !m.tabbedWindow.IsInPreviewTab() {
		return m, m.handleInfoNotice("hints work in the preview tab")
	}
	selected := m.list.GetSelectedInstance()
	if selected == nil {
		return m, nil
	}
	if selected.Paused() {
		return m, m.handleInfoNotice("session is paused — press r to resume")
	}
	// Scroll mode: hint over the visible viewport snapshot. The scroll state
	// is preserved — exitHintMode clears only hintContent, so String() falls
	// through to viewport.View() and the scroll position is intact on exit.
	if m.tabbedWindow.IsPreviewInScrollMode() {
		content, ok := m.tabbedWindow.PreviewScrollContent()
		if !ok {
			return m, m.handleInfoNotice("nothing to copy from yet")
		}
		return m.startHints(selected, content)
	}
	content, ok := m.tabbedWindow.PreviewLiveContent()
	if !ok {
		return m, m.handleInfoNotice("nothing to copy from yet")
	}
	return m.startHints(selected, content)
}

// startHints enters hint mode over content, the frozen capture of selected's
// pane. Split from enterHintMode so tests can inject pane content directly.
func (m *home) startHints(selected *session.Instance, content string) (tea.Model, tea.Cmd) {
	width, height := m.tabbedWindow.GetPreviewSize()
	// height-1 mirrors the live preview's reserved ellipsis row, so hints land
	// on exactly the rows the user is looking at (ui.PreviewPane.String).
	screen := hints.NewScreen(content, width, height-1)
	if screen.MatchCount() == 0 {
		return m, m.handleInfoNotice("no copyable matches on screen")
	}
	m.hintScreen = screen
	m.hintTyped = ""
	m.hintOpenVariant = false
	m.state = stateHints
	m.tabbedWindow.SetPreviewHintOverlay(selected, screen.Render("", hintStyles()))
	m.menu.SetState(ui.StateHints)
	m.recomputeLayout() // the hint bar may claim a row, like stateFilter
	return m, nil
}

// exitHintMode returns to the default state and the live preview.
func (m *home) exitHintMode() {
	m.state = stateDefault
	m.hintScreen = nil
	m.hintTyped = ""
	m.hintOpenVariant = false
	m.tabbedWindow.ClearPreviewHintOverlay()
	m.menu.SetState(ui.StateDefault)
	m.recomputeLayout()
}

// handleHintsState consumes every key while hint mode is up: hint characters
// narrow toward a match, anything else exits. An uppercase hint character
// selects the copy+open variant.
func (m *home) handleHintsState(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if !m.tabbedWindow.InPreviewHintMode() {
		// The pane dropped the overlay out from under us (owner paused or
		// replaced between keys); self-heal instead of acting on a stale
		// frozen screen. Normally previewTickMsg catches this within 100ms —
		// this guard closes the window in between.
		m.exitHintMode()
		return m, m.instanceChanged()
	}
	if msg.Type != tea.KeyRunes || len(msg.Runes) != 1 {
		m.exitHintMode()
		return m, m.instanceChanged()
	}
	r := msg.Runes[0]
	lower := unicode.ToLower(r)
	if !strings.ContainsRune(hints.Alphabet, lower) {
		m.exitHintMode()
		return m, m.instanceChanged()
	}
	typed := m.hintTyped + string(lower)
	match, valid := m.hintScreen.Resolve(typed)
	if !valid {
		m.exitHintMode()
		return m, m.instanceChanged()
	}
	if unicode.IsUpper(r) {
		m.hintOpenVariant = true
	}
	if match == nil {
		// A valid proper prefix: narrow the overlay, wait for the next key.
		m.hintTyped = typed
		m.tabbedWindow.SetPreviewHintOverlay(
			m.list.GetSelectedInstance(), m.hintScreen.Render(typed, hintStyles()))
		return m, nil
	}
	open := m.hintOpenVariant
	selected := *match
	m.exitHintMode()
	return m, tea.Batch(m.actHint(selected, open), m.instanceChanged())
}

// actHint copies the match and, on the open variant, opens URLs in the
// browser. Non-URL kinds degrade to plain copy in v1 (see the design doc).
func (m *home) actHint(match hints.Match, open bool) tea.Cmd {
	if err := actions.CopyToClipboard(match.Text); err != nil {
		return m.handleError(fmt.Errorf("copy hint: %w", err))
	}
	if open && match.Kind == hints.KindURL && actions.OpenableURL(match.Text) {
		if err := actions.OpenInBrowser(match.Text); err != nil {
			return m.handleError(fmt.Errorf("open url: %w", err))
		}
		return m.handleInfoNotice(fmt.Sprintf("copied + opened %s", truncateForNotice(match.Text)))
	}
	return m.handleInfoNotice(fmt.Sprintf("'%s' copied", truncateForNotice(match.Text)))
}

// truncateForNotice keeps toasts one line short; the menu row truncates too,
// but an early cut keeps the "copied" suffix visible.
func truncateForNotice(s string) string {
	const maxRunes = 40
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes-1]) + "…"
}
