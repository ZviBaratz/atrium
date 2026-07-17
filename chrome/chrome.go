// Package chrome emits out-of-band terminal escapes that surface fleet state in
// the OS window chrome: the window title (OSC 2) and the taskbar progress bar
// (OSC 9;4). Atrium is a monitoring surface, but its signal otherwise stops at
// its own panel borders — when it is one tab among many, "does any agent need
// me?" requires switching to it. These escapes carry the answer to the terminal's
// own chrome, so the fleet is legible without focus (#379).
//
// Atrium's TUI runs outside its private tmux server, so tmux's OSC-forwarding
// limits do not apply — the sequences reach the real terminal. Terminals that do
// not understand them ignore them, so there are no visible artifacts anywhere.
package chrome

import (
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// OSC 9;4 taskbar-progress sequences (Windows Terminal / ConEmu / Ghostty 1.2+ /
// kitty). Written as literals rather than via x/ansi so the set is independent of
// the pinned x/ansi version (v0.8.0 predates its indeterminate/reset helpers) and
// so the emitted bytes are pinned by test. See
// https://learn.microsoft.com/en-us/windows/terminal/tutorials/progress-bar-sequences
const (
	progressReset         = "\x1b]9;4;0\x07"   // clear the bar
	progressIndeterminate = "\x1b]9;4;3\x07"   // busy, no percentage
	progressError         = "\x1b]9;4;2;0\x07" // error state
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

// progressSeq is the taskbar sequence for a fleet state: an error this tick wins,
// otherwise a working session shows the indeterminate bar, otherwise it is clear.
func progressSeq(running int, errored bool) string {
	switch {
	case errored:
		return progressError
	case running > 0:
		return progressIndeterminate
	default:
		return progressReset
	}
}

// Emitter writes the window title and taskbar progress to an out-of-band writer
// (the TUI's real stdout). It caches the last emitted values and writes only the
// parts that changed, so a steady fleet produces no per-tick output. It is used
// only from the main Bubble Tea loop (the metadata tick and the attach handlers),
// so it needs no locking.
type Emitter struct {
	out       io.Writer
	enabled   bool
	lastTitle string
	lastProg  string
}

// New builds an Emitter writing to out (normally os.Stdout). enabled seeds the
// config switch; when false, Apply/Reset are no-ops so a user whose shell owns the
// title sees nothing.
func New(out io.Writer, enabled bool) *Emitter {
	return &Emitter{out: out, enabled: enabled}
}

// SetEnabled toggles emission live (a settings change). Turning it off resets the
// chrome so no stale title/progress lingers.
func (e *Emitter) SetEnabled(on bool) {
	if on == e.enabled {
		return
	}
	if !on {
		e.reset() // clear while still enabled, before flipping the gate
	}
	e.enabled = on
}

// Apply paints the chrome for the current fleet: needYou = sessions awaiting the
// user (blocked on a prompt, or finished-but-unread), running = sessions working
// (Running/Loading), errored = a session death or failed worktree op this tick
// (cleared on the next healthy tick, since Apply recomputes every tick).
func (e *Emitter) Apply(needYou, running int, errored bool) {
	if !e.enabled {
		return
	}
	e.writeTitle(Title(needYou, running))
	e.writeProg(progressSeq(running, errored))
}

// Reset returns the chrome to a resting state — an empty title and a cleared
// progress bar — so quit, signal-shutdown cleanup, and the handoff to a tmux
// attach never leave a stale "5 running" behind. A disabled emitter is a no-op.
func (e *Emitter) Reset() {
	if !e.enabled {
		return
	}
	e.reset()
}

func (e *Emitter) reset() {
	e.write(ansi.SetWindowTitle(""))
	e.write(progressReset)
	e.lastTitle = ""
	e.lastProg = progressReset
}

func (e *Emitter) writeTitle(title string) {
	if title == e.lastTitle {
		return
	}
	e.write(ansi.SetWindowTitle(title))
	e.lastTitle = title
}

func (e *Emitter) writeProg(seq string) {
	if seq == e.lastProg {
		return
	}
	e.write(seq)
	e.lastProg = seq
}

// write emits an out-of-band escape to the terminal. A write error to the TUI's
// own stdout is not actionable (the whole UI would be broken), so it is
// deliberately ignored — the chrome is a best-effort peripheral signal.
func (e *Emitter) write(s string) {
	_, _ = io.WriteString(e.out, s)
}
