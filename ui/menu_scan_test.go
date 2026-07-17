package ui

import (
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/keys"
	"github.com/charmbracelet/bubbles/key"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// Direction 2 of the drift guard, for the hint bar: every key label a bar
// names must exist in the registry — the global bindings or the mode hint
// tables. A hardcoded segment smuggled into a bar fails here (its leading
// token resolves nowhere); the only non-key text a bar may carry is the
// filter's predicate-syntax tail, allowed solely by exact match against the
// constant the bar itself renders (a closed whitelist, not a pattern escape).
func TestMenuBars_KeysExistInRegistry(t *testing.T) {
	// A new MenuState shifts this count and fails here on purpose: decide
	// whether its bar carries key hints (add it to the scan) or runtime
	// progress text (StateGeneratingName and StateBusy are exempt — their
	// lines are progress, not keys).
	require.Equal(t, 6, int(StateVisual), "MenuState enum changed — classify the new state for this scan")

	known := map[string]bool{}
	for _, b := range keys.GlobalKeyBindings {
		known[b.Help().Key] = true
	}
	for _, table := range [][]key.Binding{keys.FilterModeHints, keys.HintModeHints, keys.VisualModeHints} {
		for _, b := range table {
			known[b.Help().Key] = true
		}
	}

	for _, state := range []MenuState{StateDefault, StateEmpty, StateFilter, StateHints, StateVisual} {
		m := NewMenu()
		m.SetSize(400, 3) // wide, so truncation can't eat trailing segments
		m.SetState(state)
		line := strings.TrimSpace(xansi.Strip(m.String()))
		require.NotEmpty(t, line, "state %v renders no bar", state)

		for _, seg := range strings.Split(line, separator) {
			if seg == filterSyntaxHint {
				continue
			}
			token, _, _ := strings.Cut(seg, " ")
			require.Truef(t, known[token],
				"state %v names key %q (segment %q), which no registry binding or mode hint table carries",
				state, token, seg)
		}
	}
}
