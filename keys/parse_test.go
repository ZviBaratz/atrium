package keys

import (
	"strings"
	"testing"
	"unicode"

	tea "charm.land/bubbletea/v2"
)

// The vocabulary's whole definition is the round trip, so this is the test that
// says what "a legal key string" means. Every name the table carries, every
// printable ASCII character, and the ctrl/alt chords over both must build a
// message that stringifies back to the string that built it — otherwise a
// binding written that way would match no keypress.
func TestKeyVocabulary_RoundTrips(t *testing.T) {
	var specs []string
	for name := range specialKeys {
		specs = append(specs, name, "ctrl+"+name, "alt+"+name)
	}
	for r := rune(0x21); r <= rune(0x7e); r++ {
		specs = append(specs, string(r))
		// ctrl over an uppercase letter is a distinct spelling String() emits
		// verbatim, so it has to round-trip too.
		specs = append(specs, "ctrl+"+string(r), "alt+"+string(r))
	}
	for _, spec := range specs {
		msg, err := ParseKey(spec)
		if err != nil {
			// shift is the one modifier that cannot ride a printable character;
			// it is covered by its own case below.
			t.Errorf("ParseKey(%q) = %v, want it to parse", spec, err)
			continue
		}
		if got := msg.String(); got != spec {
			t.Errorf("ParseKey(%q).String() = %q, want %q", spec, got, spec)
		}
	}
}

// Every key the registry ships must itself be legal, or the vocabulary the
// validator enforces is narrower than the defaults it validates against — a
// user could be refused a spelling Atrium uses on the very next line.
func TestKeyVocabulary_CoversEveryRegistryKey(t *testing.T) {
	for _, e := range Registry {
		for _, s := range e.Binding.Keys() {
			if _, err := ParseKey(s); err != nil {
				t.Errorf("registry key %q (%s) is not in the vocabulary: %v",
					s, e.Binding.Help().Desc, err)
			}
		}
	}
	// The screensaver's key is hand-appended to the dispatch map rather than
	// registered, so the loop above cannot see it.
	if _, err := ParseKey("`"); err != nil {
		t.Errorf("screensaver key %q is not in the vocabulary: %v", "`", err)
	}
}

// Each row is a spelling a user will really write, and the assertion is on the
// suggestion as much as the refusal: a rejection that does not name the working
// spelling leaves them guessing at a config file with no feedback loop.
func TestKeyVocabulary_RejectsNearMisses(t *testing.T) {
	for _, tc := range []struct {
		spec, suggest, why string
	}{
		{"Ctrl+N", `"ctrl+N"`, "a capitalised modifier"},
		{"ctrl-n", `"ctrl+n"`, "the cheatsheet's chord separator"},
		{" ", `"space"`, "the space bar written literally"},
		{"shift+k", `"K"`, "shift folded into a character"},
		{"pgdn", `"pgdown"`, "a near miss on a key name"},
		{"shift+ctrl+n", `"ctrl+shift+n"`, "modifiers out of String()'s order"},
		{"hello", `"enter"`, "not a keystroke at all"},
	} {
		_, err := ParseKey(tc.spec)
		if err == nil {
			t.Errorf("ParseKey(%q) succeeded, want it rejected (%s)", tc.spec, tc.why)
			continue
		}
		if !strings.Contains(err.Error(), tc.suggest) {
			t.Errorf("ParseKey(%q) error %q does not offer %s", tc.spec, err, tc.suggest)
		}
	}
}

// A suggestion that is itself illegal would send the user round the loop again.
func TestKeyVocabulary_SuggestionsAreThemselvesLegal(t *testing.T) {
	for _, spec := range []string{"Ctrl+N", "ctrl-n", " ", "shift+k", "pgdn", "shift+ctrl+n"} {
		err := func() error { _, e := ParseKey(spec); return e }()
		if err == nil {
			t.Fatalf("ParseKey(%q) unexpectedly succeeded", spec)
		}
		for _, quoted := range quotedRuns(err.Error()) {
			if quoted == spec || quoted == "-" || quoted == "+" {
				continue // the input and the two separators the prose names
			}
			if !ValidKey(quoted) {
				t.Errorf("ParseKey(%q) suggests %q, which is not itself a legal key", spec, quoted)
			}
		}
	}
}

// A shifted letter is the case round-tripping alone cannot catch: "shift+k"
// stringifies back to itself, yet a real press of that key arrives as "K".
func TestKeyVocabulary_ShiftedLetterIsUnreachable(t *testing.T) {
	press := tea.KeyPressMsg{Code: 'K', Text: "K"}
	if got := press.String(); got != "K" {
		t.Fatalf("a shifted K reports %q, so the reason shift+k is refused has changed", got)
	}
	if _, err := ParseKey("K"); err != nil {
		t.Errorf("ParseKey(%q) = %v, want the spelling a real press produces to be legal", "K", err)
	}
}

func quotedRuns(s string) []string {
	var out []string
	for {
		_, rest, ok := strings.Cut(s, `"`)
		if !ok {
			return out
		}
		run, tail, ok := strings.Cut(rest, `"`)
		if !ok {
			return out
		}
		if run != "" && !strings.ContainsFunc(run, unicode.IsSpace) {
			out = append(out, run)
		}
		s = tail
	}
}
