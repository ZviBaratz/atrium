// Package handover records, for the duration of one terminal handover, that the
// running TUI has given its terminal away and is therefore draining nothing.
//
// Atrium's outbox spools are drained by the metadata tick, which is a self-chaining
// Cmd re-armed only from its own handler in the Bubble Tea update loop. A tmux
// attach (and a terminal-mode custom command) runs through tea.Exec, which blocks
// that loop for the whole takeover: no Update runs, so the tick is never re-armed
// and neither `atrium send` nor `atrium new` is picked up until the user returns.
// Nothing is lost — both drains sit outside the attachGen staleness guard, so the
// one tick message parked on Bubble Tea's unbuffered message channel drains them on
// resume — but the wait is as long as the attach, and `atrium new` exists precisely
// so an agent inside a session can hand off, which is disproportionately a moment
// somebody is attached to that session (#760).
//
// What was missing was any way for another process to know. tmux's own attached
// flag is an in-process atomic, and tui.lock is held identically whether the loop
// is running or parked, so a headless command could not tell "nobody is draining
// this" from "it will be along in a second". This is that signal, and its only
// consumers phrase a warning with it: nothing here decides whether an operation is
// safe.
//
// # Why a lock rather than a marker file
//
// A marker written on attach and removed on detach can be left behind by a kill -9,
// and a reader has no way to tell a stale one from a live one — so it would need a
// liveness cross-check, and a rule about who clears it and when. An flock needs
// neither: the kernel drops it when the owning process dies, cleanly or not, which
// is the same argument tuiLockFilename's own doc comment makes for tui.lock. A
// stale handover lock is not representable.
//
// # Why the payload can be believed
//
// The lock file also carries what the terminal was handed to, so the warning can
// name it. That is written INTO the flocked descriptor — never through
// config.WriteFileAtomic, whose rename would leave a different inode at the path
// and so a lock every later probe would take: the payload has to share the
// descriptor that carries the lock, or the lock stops meaning anything.
//
// Held reads the payload only while the lock is held, and Hold truncates before
// writing and again on release. Together those make a misread impossible rather
// than unlikely: a payload seen under a held lock was written by the holder; a
// process that died mid-handover leaves its payload behind but frees the lock, so
// the reader never consults it; and the window between taking the lock and writing
// the payload reads as empty, which callers render as "unknown" rather than as the
// previous holder's session.
package handover

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/ZviBaratz/atrium/config"
)

// LockFilename is the advisory-lock file a TUI holds while its terminal is handed
// to a child, sitting next to tui.lock / daemon.lock / update.lock in the data dir.
// Named in the README's "Scripting Atrium" section, which TestReadmeNamesTheHandoverLock
// holds to this constant.
const LockFilename = "handover.lock"

// Kind says what the terminal was handed to. The two values are the two callers of
// attachCommand.Run, which is the one place a tea.Exec suspension is entered.
const (
	KindAttach  = "attach"  // an interactive tmux attach to a session
	KindCommand = "command" // a custom command running in terminal mode
)

// Payload is what a headless command can learn about a handover in progress. Both
// fields are advisory decoration for a warning; the fact that matters is whether
// the lock is held at all.
type Payload struct {
	Kind  string `json:"kind"`
	Label string `json:"label"` // the session's title, or the command's name
}

// Describe renders the payload as a noun phrase for the middle of a sentence, or ""
// when there is nothing trustworthy to say — an empty payload (the write window, or
// a Hold that could not record one) or a label carrying a control character.
//
// The control-rune check is outbox.FirstControlRune's concern arriving by a second
// route. A session title cannot hold one, but a custom command's name comes from
// repo config, and this string is printed to a terminal by a command an agent runs
// unattended.
func (p Payload) Describe() string {
	label := strings.TrimSpace(p.Label)
	if label == "" || strings.IndexFunc(label, unicode.IsControl) >= 0 {
		return ""
	}
	switch p.Kind {
	case KindAttach:
		return "attached to session " + quote(label)
	case KindCommand:
		return "running the terminal command " + quote(label)
	default:
		return ""
	}
}

// quote wraps s in double quotes without %q's escaping, which would render a
// non-ASCII session title as \uXXXX in a message meant to be read.
func quote(s string) string { return `"` + s + `"` }

// Path returns <data dir>/handover.lock. It does not create it, or the data dir.
func Path() (string, error) {
	dir, err := config.GetConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, LockFilename), nil
}

// encodePayload renders p for the lock file. A failure is impossible for this
// struct, so the error is dropped rather than propagated into the attach path.
func encodePayload(p Payload) []byte {
	b, err := json.Marshal(p)
	if err != nil {
		return nil
	}
	return b
}

// decodePayload reads a payload back. Anything unparseable — including the empty
// file left by a release, and a partial read of a write in progress — yields the
// zero Payload, which Describe renders as "".
func decodePayload(b []byte) Payload {
	var p Payload
	if err := json.Unmarshal(b, &p); err != nil {
		return Payload{}
	}
	return p
}
