package testutil

import tea "charm.land/bubbletea/v2"

// The mouse counterparts to Key and Runes: one place the suite builds a mouse
// event.
//
// Bubble Tea v1 had a single flat MouseMsg carrying an Action field; v2 splits it
// into one message type per gesture, because the kind of an event is what
// dispatch actually branches on. These constructors name the gesture at the call
// site, which is what a test means anyway — a click, a release, a scroll — rather
// than an (action, button) pair a reader has to decode.

// MouseClick is a button press at (x, y).
func MouseClick(x, y int, b tea.MouseButton) tea.MouseClickMsg {
	return tea.MouseClickMsg{X: x, Y: y, Button: b}
}

// MouseRelease is a button release at (x, y).
func MouseRelease(x, y int, b tea.MouseButton) tea.MouseReleaseMsg {
	return tea.MouseReleaseMsg{X: x, Y: y, Button: b}
}

// MouseMotion is a pointer move to (x, y) with no button transition. Bubble Tea
// reports a drag as motion with the held button set, which is what the divider
// drag reads.
func MouseMotion(x, y int, b tea.MouseButton) tea.MouseMotionMsg {
	return tea.MouseMotionMsg{X: x, Y: y, Button: b}
}

// MouseWheel is a scroll tick at (x, y); b is one of tea.MouseWheelUp/Down/
// Left/Right.
//
// Under v1 a wheel tick arrived as a *press* whose button was a wheel button, and
// Atrium's handler still flattens it that way (see newMouseGesture) so the
// press-gated routing keeps working. Tests should build the v2 shape regardless —
// that is what a terminal now sends.
func MouseWheel(x, y int, b tea.MouseButton) tea.MouseWheelMsg {
	return tea.MouseWheelMsg{X: x, Y: y, Button: b}
}
