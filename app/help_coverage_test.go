package app

import (
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/keys"

	"github.com/charmbracelet/x/ansi"
)

// The help cheatsheet is hand-written prose, decoupled from the binding maps —
// so a rebind that forgets the help screen drifts silently. This guard pins
// them together: every binding's displayed key must appear in the cheatsheet.
//
// Normalization: the cheatsheet writes chords with '-' ("shift-tab") while
// key.WithHelp uses '+' ("shift+tab"); fold one into the other before matching.
func TestHelpScreen_CoversEveryBinding(t *testing.T) {
	content := ansi.Strip(helpTypeGeneral{}.toContent())
	content = strings.ReplaceAll(content, "-", "+")
	// The cheatsheet compacts the scroll pair to "shift-↑↓"; expand it so both
	// bindings count as covered.
	content = strings.ReplaceAll(content, "shift+↑↓", "shift+↑ shift+↓")

	for name, binding := range keys.GlobalkeyBindings {
		k := strings.ReplaceAll(binding.Help().Key, "-", "+")
		if !strings.Contains(content, k) {
			t.Errorf("binding %v (%q) is missing from the help cheatsheet", name, binding.Help().Key)
		}
	}
}

// The inverse: keys retired by remaps must not linger in the help text. This
// list grows whenever a binding moves.
func TestHelpScreen_NoRetiredKeys(t *testing.T) {
	content := ansi.Strip(helpTypeGeneral{}.toContent())

	for _, stale := range []string{"checkout"} {
		if strings.Contains(strings.ToLower(content), stale) {
			t.Errorf("help cheatsheet still mentions retired %q", stale)
		}
	}
}
