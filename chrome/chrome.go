// Package chrome derives the fleet's presence in the OS window chrome: the window
// title (OSC 2) and the taskbar progress bar (OSC 9;4). Atrium is a monitoring
// surface, but its signal otherwise stops at its own panel borders — when it is one
// tab among many, "does any agent need me?" requires switching to it. These carry
// the answer to the terminal's own chrome, so the fleet is legible without focus
// (#379).
//
// Both are declarative: this package computes what the chrome should say, and
// app's View() hands the result to Bubble Tea as tea.View.WindowTitle and
// tea.View.ProgressBar. The renderer owns the wire — it diffs frame to frame, emits
// only what changed, and clears both when it releases the terminal (to a tmux
// attach) or shuts down, so no stale "5 running" can outlive the state it described.
//
// Atrium's TUI runs outside its private tmux server, so tmux's OSC-forwarding
// limits do not apply — the sequences reach the real terminal. Terminals that do
// not understand them ignore them, so there are no visible artifacts anywhere.
package chrome

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// Title composes the window-title string from fleet counts. It always leads with
// "atrium"; the "N need you" and "M running" segments are each omitted when their
// count is zero, so an idle fleet is a bare "atrium" and no segment ever reads
// "0 running".
func Title(needYou, running int) string {
	var segs []string
	if needYou > 0 {
		segs = append(segs, fmt.Sprintf("%d need you", needYou))
	}
	if running > 0 {
		segs = append(segs, fmt.Sprintf("%d running", running))
	}
	if len(segs) == 0 {
		return "atrium"
	}
	return "atrium · " + strings.Join(segs, " · ")
}

// Progress is the taskbar state for a fleet: an error this tick wins, otherwise a
// working session shows the indeterminate bar, otherwise it is clear. The three
// states map onto the OSC 9;4 sequences terminals actually implement — see
// https://learn.microsoft.com/en-us/windows/terminal/tutorials/progress-bar-sequences
// — but the mapping is Bubble Tea's to make, which is why this returns a state
// rather than bytes.
func Progress(running int, errored bool) tea.ProgressBarState {
	switch {
	case errored:
		return tea.ProgressBarError
	case running > 0:
		return tea.ProgressBarIndeterminate
	default:
		return tea.ProgressBarNone
	}
}
