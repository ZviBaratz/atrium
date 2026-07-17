package app

import (
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/keys"

	"github.com/charmbracelet/x/ansi"
)

// The cheatsheet is generated from keys.HelpGroups, whose table-level guards
// already tie it to the registry — but this pins the rendered artifact:
// a generator bug that drops a group or a row is invisible to the table
// guards and caught here. (The '-'/'+' fold survives as belt-and-braces
// normalization; both surfaces now spell chords with '-'.)
func TestHelpScreen_CoversEveryBinding(t *testing.T) {
	content := ansi.Strip(helpTypeGeneral{}.toContent())
	content = strings.ReplaceAll(content, "-", "+")

	for name, binding := range keys.GlobalKeyBindings {
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

// The cheatsheet is generated from keys.HelpGroups; these pin the three
// generator rules against rendered output. The key column is joined from the
// bindings' Help().Key (" / ", or " " for Compact rows), padded to the helpRow
// key column; LayerAttached rows get their "in a session: " prefix from the
// generator, not the table — the layer tag, not prose discipline, is what
// keeps that truth in place.
func TestHelpScreen_GeneratedRows(t *testing.T) {
	content := ansi.Strip(helpTypeGeneral{}.toContent())
	for _, want := range []string{
		// " / " join, key label padded to the 12-column key column.
		"u / b       jump to next unread / blocked",
		// Compact join; the label outgrows the key column, so a single space.
		"shift-↑ shift-↓ scroll the active pane",
		// LayerAttached row: the prefix is generated from the layer tag.
		"ctrl-pgup/pgdn in a session: cycle to prev / next session in the repo group",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("cheatsheet missing generated row %q", want)
		}
	}
}

// The cheatsheet gains a short Mouse section: click targets, wheel, divider
// drag, and the Shift bypass for native selection (#397). This pins its presence
// so a future edit can't silently drop the one place the mouse map is documented
// in-app.
func TestHelpScreen_MouseSection(t *testing.T) {
	content := ansi.Strip(helpTypeGeneral{}.toContent())
	for _, want := range []string{
		"Mouse",       // the section header
		"hint-bar",    // click targets include the hint bar
		"repo header", // and repo headers
		"wheel",       // wheel scrolling
		"divider",     // divider drag
		"Shift+drag",  // the native-selection bypass
	} {
		if !strings.Contains(content, want) {
			t.Errorf("help cheatsheet mouse section missing %q", want)
		}
	}
}

// The screensaver backtick is a deliberate easter egg. It surfaces to users by
// exactly two routes, so this guards both: a GlobalKeyBindings entry (which
// would feed it to the coverage guard above *and* to the hint bar — see
// ui.renderHintLine, the map's only other reader), and the hand-written prose.
//
// The structural half is the load-bearing one: the absence of a binding is what
// makes the omission automatic rather than a thing every future editor has to
// remember.
func TestHelpScreen_OmitsScreensaverKey(t *testing.T) {
	if _, ok := keys.GlobalKeyBindings[keys.KeyScreensaver]; ok {
		t.Error("KeyScreensaver must have no GlobalKeyBindings entry — that absence is " +
			"what keeps the easter egg out of both the help cheatsheet and the hint bar")
	}

	content := strings.ToLower(ansi.Strip(helpTypeGeneral{}.toContent()))
	if strings.Contains(content, "screensaver") {
		t.Error("help cheatsheet must not mention the screensaver")
	}
}
