package app

import (
	"github.com/ZviBaratz/atrium/chrome"
	"github.com/ZviBaratz/atrium/session"

	tea "charm.land/bubbletea/v2"
)

// refreshOSChrome recomputes the fleet counts and stores what the terminal's OS
// chrome (window title + OSC 9;4 taskbar progress) should say; View hands the stored
// values to Bubble Tea, which emits only what changed. It runs once per metadata
// tick, so the title reflects a status change within one tick.
//
// The derivation stays here rather than in View for two reasons: errored is
// tick-scoped (a session death observed in *this* poll, which no frame-time read can
// recover), and the walk over every instance would otherwise run at frame rate.
//
//   - running: sessions actively working (Running or Loading) → the progress bar's
//     indeterminate state and the "M running" title segment.
//   - needYou: sessions awaiting the user — blocked on a prompt (NeedsInput) or
//     finished-but-unread — → the "N need you" segment. Paused sessions never count.
//   - errored: a session death this tick → the progress bar's error state, cleared
//     on the next healthy tick (this recomputes every tick).
//
// With the OSChrome config switch off, both values are zeroed, which is also their
// zero value — so a hand-built test home that never calls this renders no chrome.
func (m *home) refreshOSChrome(errored bool) {
	if m.appConfig == nil || !m.appConfig.GetOSChrome() {
		m.osChromeTitle, m.osChromeProgress = "", tea.ProgressBarNone
		return
	}
	var needYou, running int
	for _, inst := range m.list.GetInstances() {
		if inst.Paused() {
			continue
		}
		switch inst.GetStatus() {
		case session.Running, session.Loading:
			running++
		case session.NeedsInput:
			needYou++
		default: // Ready / Pending: a finished turn you have not looked at yet still wants you.
			if inst.Unread() {
				needYou++
			}
		}
	}
	m.osChromeTitle = chrome.Title(needYou, running)
	m.osChromeProgress = chrome.Progress(running, errored)
}
