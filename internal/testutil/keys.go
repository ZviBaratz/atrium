package testutil

import (
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/ZviBaratz/atrium/keys"
)

// Key and Runes are the single place the whole suite builds a key message.
//
// They were introduced on Bubble Tea v1 for exactly this moment (#393): the suite
// built roughly seven hundred key literals, and v2 restructures every one of them
// — tea.KeyMsg became an interface, tea.KeyPressMsg carries the press, Type became
// a rune Code, Runes became a Text string, and the Alt bool became a Mod bitmask.
// Funnelling them through here meant the port was these two function bodies rather
// than a diff nobody could review.
//
// The vocabulary is the one msg.String() emits ("enter", "ctrl+s", "shift+tab",
// "alt+enter"), which is also the one keys.GlobalKeyStringsMap is keyed by — so a
// test presses the same string the dispatch matches. It is deliberately unchanged
// across the migration: every call site reads identically on both sides of the cut.

// The spec vocabulary itself now lives in keys.ParseKey — the same table
// production code validates a user's config.json against, so a test can only
// press a keystroke a binding could legally name, and the two can never drift
// into disagreeing about how a chord is spelled.

// keyAliases holds specs whose spelling differs between Bubble Tea versions, so a
// call site can use either and the helper absorbs the difference.
//
// There is exactly one, and it is why the mark key still works. v1 reported the
// space bar from String() as a literal " "; v2 renames it to "space". Atrium's
// dispatch is keyed by that string — keys/registry.go bound KeyToggleMark with
// " " — so the rename is a silent break: it compiles, and the key simply stops
// working. The registry moved to "space" at the cut; this keeps the older spelling
// accepted so a call site written either way builds the space bar.
//
// Note the direction flipped at the cut. On v1 this mapped "space" → " " (the
// spelling v1 understood); now it maps " " → "space".
var keyAliases = map[string]string{" ": "space"}

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
func Key(spec string) tea.KeyPressMsg {
	if canonical, ok := keyAliases[spec]; ok {
		spec = canonical
	}
	msg, err := keys.ParseKey(spec)
	if err != nil {
		panic(fmt.Sprintf("testutil.Key: %v (Runes builds literal text)", err))
	}
	return msg
}

// Runes builds a text key message carrying exactly s, with no keystroke-name
// interpretation. Use it when the typed text would otherwise be read as a key
// name — Runes("enter") types the five letters, where Key("enter") presses the
// return key — and for multi-character input a test drives in one go.
//
// Code is the first rune because that is what a terminal reports for a keypress;
// Text carries the whole string, which is what a paste, or a test typing several
// characters at once, delivers.
func Runes(s string) tea.KeyPressMsg {
	key := tea.KeyPressMsg{Text: s}
	if r := []rune(s); len(r) > 0 {
		key.Code = r[0]
	}
	return key
}
