package keys

import (
	"fmt"
	"sort"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
)

// The key-string vocabulary.
//
// A binding names a keystroke with the same string tea.KeyPressMsg.String()
// reports for it — "enter", "ctrl+s", "shift+tab", "?" — because that string is
// what GlobalKeyStringsMap is keyed by and what handleKeyPress looks up. So the
// vocabulary needs no enumeration: s is legal exactly when ParseKey(s) succeeds
// and the message it builds stringifies back to s. Anything else would be a
// binding no keypress can ever match — the failure mode that shipped the mark
// key dead once already (see KeyToggleMark in registry.go).
//
// Defining it by round-trip rather than by a list is what lets the validator
// name the user's mistake instead of just refusing it: the canonical spelling
// falls out of the same parse, so every rejection can carry a suggestion.

// modifiers maps a chord prefix onto its modifier bit. String() emits these in a
// fixed order (ctrl, alt, shift), so only the canonical order round-trips —
// which is exactly what makes "shift+ctrl+n" rejectable with a suggestion.
var modifiers = map[string]tea.KeyMod{
	"ctrl":  tea.ModCtrl,
	"alt":   tea.ModAlt,
	"shift": tea.ModShift,
}

// specialKeys maps a key name onto the key code that produces it.
//
// Built by asking Bubble Tea rather than by asserting: each entry's name is
// derived from tea.KeyPressMsg{Code: c}.String(), so the table cannot claim a
// name Bubble Tea does not use. TestKeyVocabulary_RoundTrips pins that.
//
// The keypad codes are deliberately absent: KeyKpUp and KeyUp both stringify to
// "up", so including them would silently overwrite half the table with codes no
// binding wants.
var specialKeys = func() map[string]rune {
	codes := []rune{
		tea.KeyEnter, tea.KeyTab, tea.KeyEsc, tea.KeySpace,
		tea.KeyBackspace, tea.KeyDelete, tea.KeyInsert,
		tea.KeyUp, tea.KeyDown, tea.KeyLeft, tea.KeyRight,
		tea.KeyHome, tea.KeyEnd, tea.KeyPgUp, tea.KeyPgDown,
		tea.KeyF1, tea.KeyF2, tea.KeyF3, tea.KeyF4, tea.KeyF5, tea.KeyF6,
		tea.KeyF7, tea.KeyF8, tea.KeyF9, tea.KeyF10, tea.KeyF11, tea.KeyF12,
	}
	m := make(map[string]rune, len(codes))
	for _, c := range codes {
		m[tea.KeyPressMsg{Code: c}.String()] = c
	}
	return m
}()

// ParseKey builds the key message a terminal produces for the keystroke named by
// s, using the vocabulary msg.String() reports: "enter", "esc", "shift+tab",
// "ctrl+s", "alt+enter", "pgup", or a single printable character ("j", "?").
//
// It fails for anything that would not round-trip, and the error names the
// canonical spelling whenever one can be recovered, because every rejection here
// is a line in somebody's config.json that they have to fix by hand.
func ParseKey(s string) (tea.KeyPressMsg, error) {
	if s == "" {
		return tea.KeyPressMsg{}, fmt.Errorf("key is required")
	}
	msg, ok := buildKey(s)
	if !ok || msg.String() != s {
		return tea.KeyPressMsg{}, keySpellingError(s)
	}
	// shift+<printable> parses and even round-trips through String(), but no
	// terminal sends it: a shifted character arrives as the character itself
	// (Text "K"), never as a shift bit over "k". Accepting it would register a
	// binding that can never fire, which is the whole class this vocabulary
	// exists to make impossible.
	if base, shifted := shiftedPrintable(s); shifted {
		return tea.KeyPressMsg{}, fmt.Errorf(
			"key %q is not a keystroke a terminal sends — shift is folded into the "+
				"character, so use %q", s, base)
	}
	return msg, nil
}

// ValidKey reports whether s is a legal key string.
func ValidKey(s string) bool {
	_, err := ParseKey(s)
	return err == nil
}

// buildKey is the parse half of ParseKey: it peels modifiers off the front and
// resolves the remainder as a named key or a single character. It reports
// whether the spelling resolved at all, never whether it round-trips.
func buildKey(spec string) (tea.KeyPressMsg, bool) {
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
		return tea.KeyPressMsg{Code: code, Mod: mod}, true
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
		return key, true
	}
	return tea.KeyPressMsg{}, false
}

// shiftedPrintable reports whether s is shift over a single printable character,
// and if so the character a real press of it would deliver.
func shiftedPrintable(s string) (string, bool) {
	base, ok := strings.CutPrefix(s, "shift+")
	if !ok {
		return "", false
	}
	r := []rune(base)
	if len(r) != 1 || !unicode.IsPrint(r[0]) {
		return "", false
	}
	return strings.ToUpper(base), true
}

// keySpellingError explains why s is not a key string, naming the canonical
// spelling when one is recoverable. The three repairs it tries are the three
// mistakes the tree's own history predicts: the display spelling of a chord
// ("ctrl-x" is what the cheatsheet prints, "ctrl+x" is what dispatch matches),
// a capitalised modifier, and the space bar written as a literal space.
func keySpellingError(s string) error {
	const lead = "key %q is not a key name"

	if s == " " {
		return fmt.Errorf("key is a literal space — the space bar arrives as %q, so bind it as %q",
			"space", "space")
	}

	// A chord spelled with the cheatsheet's separator. Only rewrite when the
	// leading token really is a modifier, so a lone "-" or a name like "f-1"
	// falls through to the generic message rather than getting nonsense advice.
	if prefix, rest, ok := strings.Cut(s, "-"); ok && rest != "" {
		if _, isMod := modifiers[strings.ToLower(prefix)]; isMod {
			if fixed := strings.ToLower(prefix) + "+" + rest; ValidKey(fixed) {
				return fmt.Errorf(lead+" — %q is how the cheatsheet spells a chord, but a "+
					"binding joins it with %q; did you mean %q?", s, "-", "+", fixed)
			}
		}
	}

	// A capitalised or reordered modifier: rebuild from the canonical parse.
	if fixed, ok := canonicalize(s); ok {
		return fmt.Errorf(lead+" — did you mean %q?", s, fixed)
	}

	// A near-miss on a key name ("pgdn" for "pgdown").
	if fixed, ok := nearestKeyName(s); ok {
		return fmt.Errorf(lead+" — did you mean %q?", s, fixed)
	}

	return fmt.Errorf(lead+" — use a single character like %q, or a key name like "+
		"%q or %q", s, "?", "enter", "ctrl+s")
}

// canonicalize lowercases the modifier tokens of s and reports the spelling
// String() would emit for the keystroke that results, when it differs from s.
func canonicalize(s string) (string, bool) {
	parts := strings.Split(s, "+")
	for i := 0; i < len(parts)-1; i++ {
		parts[i] = strings.ToLower(parts[i])
	}
	msg, ok := buildKey(strings.Join(parts, "+"))
	if !ok {
		return "", false
	}
	fixed := msg.String()
	if fixed == s || !ValidKey(fixed) {
		return "", false
	}
	return fixed, true
}

// nearestKeyName finds the key name s was probably reaching for, by the longest
// shared prefix of at least three characters. Deliberately crude: it exists to
// turn "pgdn" into "pgdown", not to guess at arbitrary typos, and it stays
// silent rather than suggesting something unrelated.
func nearestKeyName(s string) (string, bool) {
	// Peel every modifier, not just the first: "ctrl+shift+pgdn" is reaching for
	// a key name too, and cutting once would leave "shift+pgdn" as the base and
	// match nothing.
	prefix, base := "", s
	for {
		head, rest, ok := strings.Cut(base, "+")
		if !ok || rest == "" {
			break
		}
		if _, isMod := modifiers[strings.ToLower(head)]; !isMod {
			break
		}
		prefix, base = prefix+strings.ToLower(head)+"+", rest
	}
	base = strings.ToLower(base)

	names := make([]string, 0, len(specialKeys))
	for name := range specialKeys {
		names = append(names, name)
	}
	sort.Strings(names) // deterministic pick among equally close names

	best, bestShared := "", 2
	for _, name := range names {
		shared := 0
		for shared < len(name) && shared < len(base) && name[shared] == base[shared] {
			shared++
		}
		if shared > bestShared {
			best, bestShared = name, shared
		}
	}
	if best == "" {
		return "", false
	}
	fixed := prefix + best
	if !ValidKey(fixed) {
		return "", false
	}
	return fixed, true
}
