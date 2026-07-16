package app

import "github.com/ZviBaratz/atrium/session"

// applyOSChrome recomputes the fleet counts and repaints the terminal's OS chrome
// (window title + OSC 9;4 taskbar progress) via m.chrome. It runs once per metadata
// tick, so the title reflects a status change within one tick; the emitter itself
// writes only when the composed strings changed, so a steady fleet is silent.
//
//   - running: sessions actively working (Running or Loading) → the progress bar's
//     indeterminate state and the "M running" title segment.
//   - needYou: sessions awaiting the user — blocked on a prompt (NeedsInput) or
//     finished-but-unread — → the "N need you" segment. Paused sessions never count.
//   - errored: a session death this tick → the progress bar's error state, cleared
//     on the next healthy tick (this recomputes every tick).
//
// A nil m.chrome (hand-built test homes) or the OSChrome config switch being off
// makes this a no-op.
func (m *home) applyOSChrome(errored bool) {
	if m.chrome == nil {
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
	m.chrome.Apply(needYou, running, errored)
}
