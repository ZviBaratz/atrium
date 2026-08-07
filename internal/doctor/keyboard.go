package doctor

import (
	"fmt"
	"strings"
)

// KeyboardResult is what doctor can determine about key disambiguation — the
// terminal feature that makes Shift+Enter a newline in Atrium's composers (#396).
//
// Probed is always false, and, as with SchemeResult.OSC11Probed, that is the point
// rather than a stub: the capability is established by a query the terminal answers
// mid-session, which needs the running TUI. Reporting "not probed here" is honest;
// omitting the section would let a user read Atrium's silence as "your terminal does
// not support it", which is the opposite conclusion and the one that would send them
// to change terminals.
//
// The two environment facts are reported because they are the ones that explain a
// surprise, not because they decide anything. Deliberately absent is a table of
// which terminals support the protocol: that is a list that goes stale between
// releases, and the terminal's own answer already outranks anything Atrium could
// guess from a name.
type KeyboardResult struct {
	Term        string // $TERM, "" when unset
	TermProgram string // $TERM_PROGRAM, "" when unset
	InTmux      bool   // $TMUX is set: this shell is inside a tmux client
	Probed      bool   // always false; see the type comment
}

// CheckKeyboard reads the environment rungs available outside the TUI.
//
// environ is a parameter rather than an os.Environ() call so the rule is a pure
// function of its input, matching CheckScheme and theme.NoColorRequested. Later
// entries win, matching os.Environ semantics for a duplicated name.
func CheckKeyboard(environ []string) KeyboardResult {
	var r KeyboardResult
	for _, kv := range environ {
		name, value, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		switch name {
		case "TERM":
			r.Term = value
		case "TERM_PROGRAM":
			r.TermProgram = value
		case "TMUX":
			// Presence is the signal, as it is for tmux itself — an empty TMUX is not
			// something tmux ever sets.
			r.InTmux = value != ""
		}
	}
	return r
}

// RenderKeyboard formats the report under a "Keyboard protocol:" header, parallel to
// RenderScheme.
func RenderKeyboard(r KeyboardResult) string {
	var b strings.Builder
	b.WriteString("Keyboard protocol:\n")

	fmt.Fprintf(&b, "  %-18s %s\n", "TERM", orUnset(r.Term))
	fmt.Fprintf(&b, "  %-18s %s\n", "TERM_PROGRAM", orUnset(r.TermProgram))

	if !r.Probed {
		// The wrapped half aligns under the value column because it continues a VALUE;
		// the actionable lines below are hints, and hints are the 9-space arrow form
		// every other section uses (capacity.go, deps.go, scheme.go).
		fmt.Fprintf(&b, "  %-18s not probed here — Atrium asks the terminal at startup and\n", "disambiguation")
		fmt.Fprintf(&b, "  %-18s the composer footer shows the answer: ⇧↵ means yes\n", "")
	}

	if r.InTmux {
		fmt.Fprintf(&b, "  %-18s yes — tmux does not forward the kitty keyboard protocol,\n", "inside tmux")
		fmt.Fprintf(&b, "  %-18s so the footer will name ⌃J even where ⇧↵ works\n", "")
		b.WriteString("         → set -g extended-keys always and\n")
		b.WriteString("           set -as terminal-features '*:extkeys' to pass shift+enter through\n")
	}
	b.WriteString("         → ⌃J inserts a newline on every terminal, protocol or not\n")
	return b.String()
}

// orUnset renders an empty environment value as the word doctor uses for one
// everywhere else, so a blank column is never mistaken for a value that is blank.
func orUnset(v string) string {
	if v == "" {
		return "unset"
	}
	return v
}
