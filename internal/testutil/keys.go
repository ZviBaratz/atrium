package testutil

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// Key and Runes are the single place the whole suite builds a key message.
//
// They exist for the Bubble Tea v2 migration (#393). v2 restructures key events
// completely — tea.KeyMsg becomes an interface, tea.KeyPressMsg carries the press,
// Type becomes Code, Runes becomes a Text string, and Alt folds into a Mod
// bitmask — and the suite constructs roughly seven hundred key literals across
// app/ and ui/overlay/. Ported literal-by-literal inside the migration commit,
// that churn would be indistinguishable in review from the semantic changes it
// travels with. Funnelled through here, the port is two function bodies.
//
// The vocabulary is deliberately the one msg.String() already emits ("enter",
// "ctrl+s", "shift+tab", "alt+enter"), not a private shorthand. That matters twice
// over: Atrium's own key dispatch is string-keyed through
// keys.GlobalKeyStringsMap[msg.String()], so tests now speak the same language as
// the code under test; and v2 keeps that vocabulary while changing everything
// underneath, so these call sites read identically before and after the cut.
//
// TestKeySpecsRoundTripThroughString pins the vocabulary to Bubble Tea's own
// String() rather than to a hand-written list, so a spec that stops being what
// Bubble Tea calls that key fails here instead of silently building a message no
// production dispatch will ever match.

// specialKeys maps a keystroke spec onto the v1 key type that produces it.
//
// Built by asking Bubble Tea, not by asserting: each entry's spec is derived from
// tea.KeyMsg{Type: t}.String(), so the table cannot claim a name Bubble Tea does
// not use. Modified forms (alt+, and the pre-composed shift+/ctrl+ arrows) are
// listed as types too, because v1 encodes them as distinct KeyTypes rather than as
// a modifier on a base key.
var specialKeys = func() map[string]tea.KeyType {
	types := []tea.KeyType{
		tea.KeyEnter, tea.KeyTab, tea.KeyShiftTab, tea.KeyEsc, tea.KeySpace,
		tea.KeyBackspace, tea.KeyDelete, tea.KeyUp, tea.KeyDown, tea.KeyLeft,
		tea.KeyRight, tea.KeyHome, tea.KeyEnd, tea.KeyPgUp, tea.KeyPgDown,
		tea.KeyShiftUp, tea.KeyShiftDown, tea.KeyShiftLeft, tea.KeyShiftRight,
		tea.KeyCtrlUp, tea.KeyCtrlDown, tea.KeyCtrlLeft, tea.KeyCtrlRight,
		tea.KeyCtrlA, tea.KeyCtrlB, tea.KeyCtrlC, tea.KeyCtrlD, tea.KeyCtrlE,
		tea.KeyCtrlF, tea.KeyCtrlG, tea.KeyCtrlJ, tea.KeyCtrlK, tea.KeyCtrlL,
		tea.KeyCtrlN, tea.KeyCtrlO, tea.KeyCtrlP, tea.KeyCtrlQ, tea.KeyCtrlR,
		tea.KeyCtrlS, tea.KeyCtrlT, tea.KeyCtrlU, tea.KeyCtrlV, tea.KeyCtrlW,
		tea.KeyCtrlX, tea.KeyCtrlY, tea.KeyCtrlZ,
	}
	m := make(map[string]tea.KeyType, len(types))
	for _, t := range types {
		m[tea.KeyMsg{Type: t}.String()] = t
	}
	return m
}()

// keyAliases holds specs whose spelling differs between Bubble Tea versions, so a
// call site can use either and the helper absorbs the difference.
//
// There is exactly one today, and it is load-bearing for #393. v1 reports the space
// bar from String() as a literal " "; v2 renames it to "space". Atrium's dispatch is
// keyed by that string (keys/registry.go binds KeyToggleMark with " "), so the rename
// is a silent break — it compiles, and the mark key simply stops working. Accepting
// both spellings here means the fourteen call sites that press space need not move at
// the cut, and the rename cannot be missed by forgetting one of them.
var keyAliases = map[string]string{"space": " "}

// Key builds the key message a terminal produces for the keystroke named by spec,
// using the same names msg.String() reports: "enter", "esc", "shift+tab",
// "ctrl+s", "alt+enter", "pgup". A single printable character ("j", "?") builds
// that character as text.
//
// It panics on an unknown spec. That is the right failure for a test helper: a
// typo'd keystroke would otherwise build a zero-valued message that silently
// matches no dispatch case, and the test would fail somewhere far from the cause.
// Use Runes for text that happens to collide with a key name.
//
// "space" and " " are both accepted — see keyAliases.
func Key(spec string) tea.KeyMsg {
	if canonical, ok := keyAliases[spec]; ok {
		spec = canonical
	}
	if alt, rest, ok := strings.Cut(spec, "+"); ok && alt == "alt" {
		msg := Key(rest)
		msg.Alt = true
		return msg
	}
	if t, ok := specialKeys[spec]; ok {
		return tea.KeyMsg{Type: t}
	}
	if r := []rune(spec); len(r) == 1 {
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: r}
	}
	panic(fmt.Sprintf("testutil.Key: unknown key spec %q — add it to specialKeys, or use Runes for literal text", spec))
}

// Runes builds a text key message carrying exactly s, with no keystroke-name
// interpretation. Use it when the typed text would otherwise be read as a key
// name — Runes("enter") types the six letters, where Key("enter") presses the
// return key — and for multi-character input a test drives in one go.
func Runes(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}
