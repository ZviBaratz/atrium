package app

// Error and transient-notice feedback for the home model.

import (
	"time"

	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/ui"
	"github.com/ZviBaratz/atrium/ui/overlay"

	tea "github.com/charmbracelet/bubbletea"
)

// hideErrMsg implements tea.Msg and clears the transient toast (menu notice or
// error box). gen identifies which toast the timer belongs to: a stale timer's
// message must not clear a newer toast.
type hideErrMsg struct {
	gen int
}

// infoMsg requests a dismissible information modal carrying actionable text.
// Confirmation-action callbacks return it to surface a message that must persist
// until the user dismisses it, instead of the auto-hiding transient error box.
type infoMsg string

// errToastDuration is how long the transient error box stays before auto-hiding.
const errToastDuration = 5 * time.Second

// handleError surfaces an error in the UI. Short, single-line errors get a
// transient toast (auto-hidden after errToastDuration): when the always-on hint
// bar is up, the toast rides the bar's reserved row so the layout never shifts;
// otherwise it falls back to the error box's own row. An error that a one-line
// toast cannot actually convey — multi-line, or wider than the row can show
// (e.g. a failed push's git output) — is routed to the persistent info modal
// instead, but only from stateDefault: in any overlay state (e.g. a form
// validation error) switching to stateInfo would clobber the open overlay, so
// those always use the toast.
func (m *home) handleError(err error) tea.Cmd {
	if m.state == stateDefault && !m.errBox.Fits(err) {
		return m.showInfo(err.Error()) // showInfo logs the message itself
	}
	log.ErrorLog.Printf("%v", err)
	if m.menuVisible() && m.menu != nil {
		m.menu.SetNotice(err.Error(), ui.NoticeError)
	} else {
		m.errBox.SetError(err)
		m.recomputeLayout() // give the error its row; panes shrink by one
	}
	return m.scheduleNoticeHide()
}

// persistInstances writes the current instance list to disk. It is the single
// chokepoint for the SaveInstances(GetInstances()) pattern; callers choose how to
// surface the error (m.handleError for user-driven actions, log for bulk/background
// paths).
func (m *home) persistInstances() error {
	return m.storage.SaveInstances(m.list.GetInstances())
}

// moveAndPersist runs a list-reorder closure; if it changed the order it persists
// and refreshes the selected session's preview. A persist failure is surfaced; a
// no-op move is a clean no-op.
func (m *home) moveAndPersist(move func() bool) (tea.Model, tea.Cmd) {
	if !move() {
		return m, nil
	}
	if err := m.persistInstances(); err != nil {
		return m, m.handleError(err)
	}
	return m, m.instanceChanged()
}

// handleInfoNotice flashes a neutral acknowledgment ("branch copied") on the
// hint bar's reserved row. Unlike errors, info is chrome: when the user runs
// without the hint bar there is no reserved row to ride, so the notice is
// dropped rather than claiming one.
func (m *home) handleInfoNotice(text string) tea.Cmd {
	if !m.menuVisible() || m.menu == nil {
		return nil
	}
	m.menu.SetNotice(text, ui.NoticeInfo)
	return m.scheduleNoticeHide()
}

// scheduleNoticeHide stamps the just-shown toast with a fresh generation and
// returns the command that clears it after errToastDuration. The generation
// keeps an older toast's timer from clearing a newer toast early.
func (m *home) scheduleNoticeHide() tea.Cmd {
	m.noticeGen++
	gen := m.noticeGen
	return func() tea.Msg {
		select {
		case <-m.ctx.Done():
		case <-time.After(errToastDuration):
		}

		return hideErrMsg{gen: gen}
	}
}

// showInfo displays an actionable message in a dismissible modal (reusing the
// TextOverlay the help screen uses). Unlike handleError's 3-second box, it stays
// until the user presses a key — appropriate for errors that require the user to
// read and act (e.g. "branch is checked out at <path>"). It reuses m.textOverlay,
// which is safe because only one modal state is active at a time.
func (m *home) showInfo(text string) tea.Cmd {
	log.ErrorLog.Printf("%s", text)
	m.textOverlay = overlay.NewTextOverlay(text)
	m.textOverlay.SetHint("press any key to close")
	m.state = stateInfo
	// Size the overlay now rather than waiting for the next resize.
	m.recomputeLayout()
	return nil
}
