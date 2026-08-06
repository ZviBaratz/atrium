package keys

import "strings"

// A binding carries two spellings of the same keystroke, and they are not the
// same string. WithKeys holds the dispatch spelling — what msg.String() emits,
// "ctrl+x", "shift+up", "up" — and WithHelp holds the display spelling the hint
// bar and cheatsheet print: "ctrl-x", "shift-↑", "↑". Chords join with a hyphen
// rather than a plus, arrows are glyphs, and an action's aliases are joined with
// a slash ("↑/k").
//
// The display spellings ship hand-authored, one per entry. That is fine while
// the keymap is fixed and wrong the moment it isn't: an override changes the
// keys but cannot change 62 hand-written labels, so every surface would go on
// printing the key the user just replaced. Label is the generator that closes
// that gap, and TestLabelReproducesEveryDefault proves it agrees with all 59
// hand-authored labels it could be asked to replace — so a regenerated label is
// indistinguishable in style from the ones around it.

// keyGlyphs are the key names the display spelling renders as a symbol.
var keyGlyphs = map[string]string{
	"up":    "↑",
	"down":  "↓",
	"left":  "←",
	"right": "→",
	"enter": "↵",
}

// Label renders the display spelling of a set of dispatch key strings: the
// label a hint-bar entry or cheatsheet row shows for an action bound to them.
func Label(keyStrings []string) string {
	labels := make([]string, 0, len(keyStrings))
	for _, k := range keyStrings {
		labels = append(labels, labelOne(k))
	}
	return strings.Join(labels, "/")
}

// labelOne renders one keystroke: modifiers joined with a hyphen, the base key
// as its glyph where it has one.
func labelOne(k string) string {
	parts := strings.Split(k, "+")
	for i, p := range parts {
		if glyph, ok := keyGlyphs[p]; ok {
			parts[i] = glyph
		}
	}
	return strings.Join(parts, "-")
}

// LabelOf is the display spelling of the key that currently runs an action —
// the string to put in a sentence that names a key ("press r to resume").
//
// Read it, never a literal: a literal is correct only until the user rebinds
// the action, and then it is a sentence telling them to press a key that does
// something else. Two of those shipped ("press k to kill", where k moves the
// selection); ui/key_prose_test.go is what now catches the next one.
func LabelOf(name KeyName) string {
	return GlobalKeyBindings[name].Help().Key
}

// PrimaryKey is the dispatch spelling of the key an action fires on: its first
// bound key, since an action with aliases (KeyEnter's "enter" and "o") is still
// one action. Empty when the action is unbound.
func PrimaryKey(name KeyName) string {
	if ks := GlobalKeyBindings[name].Keys(); len(ks) > 0 {
		return ks[0]
	}
	return ""
}

// KillKey is the chord that triggers a kill from the session list. It mirrors
// the in-session kill byte (ctrlX, session/tmux/attach.go) so the same key tears
// a session down whether you're on the list or attached to it, and
// session/tmux/keys_link_test.go pins the two together.
//
// It reads the applied binding rather than naming a chord, because the callers
// that used to take the constant — the marked-set kill and the kill dialog's
// double-tap confirmation — would otherwise listen for a key nobody presses
// once the action is rebound.
func KillKey() string {
	return PrimaryKey(KeyKill)
}
