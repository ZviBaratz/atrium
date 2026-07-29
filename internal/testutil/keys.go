package testutil

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
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

// modifiers maps a spec prefix onto its v2 modifier bit.
//
// v1 had no such table: it encoded modified keys as distinct KeyTypes
// (KeyShiftTab, KeyCtrlLeft, KeyShiftUp, …) and carried only Alt as a bool. v2
// drops that whole parallel vocabulary for a Code plus a Mod bitmask, which is why
// this table exists at all and why specialKeys shrank to the unmodified keys.
var modifiers = map[string]tea.KeyMod{
	"ctrl":  tea.ModCtrl,
	"alt":   tea.ModAlt,
	"shift": tea.ModShift,
}

// specialKeys maps a keystroke spec onto the v2 key code that produces it.
//
// Built by asking Bubble Tea rather than by asserting: each entry's name is
// derived from tea.KeyPressMsg{Code: c}.String(), so the table cannot claim a name
// Bubble Tea does not use. TestKeySpecsRoundTripThroughString pins that.
var specialKeys = func() map[string]rune {
	codes := []rune{
		tea.KeyEnter, tea.KeyTab, tea.KeyEsc, tea.KeySpace,
		tea.KeyBackspace, tea.KeyDelete, tea.KeyInsert,
		tea.KeyUp, tea.KeyDown, tea.KeyLeft, tea.KeyRight,
		tea.KeyHome, tea.KeyEnd, tea.KeyPgUp, tea.KeyPgDown,
	}
	m := make(map[string]rune, len(codes))
	for _, c := range codes {
		m[tea.KeyPressMsg{Code: c}.String()] = c
	}
	return m
}()

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

	// Peel modifiers off the front. Order is not significant to the bitmask, but
	// String() emits them in a fixed order, so only the canonical spelling
	// round-trips.
	var mod tea.KeyMod
	for {
		prefix, rest, ok := strings.Cut(spec, "+")
		if !ok || rest == "" {
			break
		}
		bit, known := modifiers[prefix]
		if !known {
			break
		}
		mod |= bit
		spec = rest
	}

	if code, ok := specialKeys[spec]; ok {
		return tea.KeyPressMsg{Code: code, Mod: mod}
	}
	if r := []rune(spec); len(r) == 1 {
		// Text is what a terminal reports for a printable key, and String() falls
		// back to it — but only for an unmodified press. A modified one names the
		// key, and carrying Text alongside a ctrl bit would describe a keystroke no
		// terminal sends.
		key := tea.KeyPressMsg{Code: r[0], Mod: mod}
		if mod == 0 {
			key.Text = spec
		}
		return key
	}
	panic(fmt.Sprintf("testutil.Key: unknown key spec %q — add it to specialKeys, or use Runes for literal text", spec))
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
