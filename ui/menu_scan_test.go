package ui

import (
	"fmt"
	"strings"
	"testing"

	"charm.land/bubbles/v2/key"
	"github.com/ZviBaratz/atrium/keys"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// The drift guard for the hint bar, in both directions. Direction 2: every key
// label a bar names must exist in the registry — the global bindings or the
// mode hint tables (keys.ModeHintTables). A hardcoded segment smuggled into a
// bar fails here (its leading token resolves nowhere); the only non-key text a
// bar may carry is the filter's predicate-syntax tail, allowed solely by exact
// match against the constant the bar itself renders (a closed whitelist, not a
// pattern escape). Direction 3: every mode table must render its own
// vocabulary in the bar arm declared for it below — checked per table, not
// against a pooled token set, because the tables share tokens (esc is in all
// five, the nav pair is in two), so "some token seen somewhere" would pass for
// a bar this scan never renders. That pooled-check shape is exactly how the
// diff-comment bar shipped unscanned.
//
// The residual gap, named rather than papered over: a bar variant whose table
// never enters ModeHintTables and whose trigger is not a MenuState (a bool
// like paneFocused) is invisible to all three directions. The sentinel pin
// catches new states; a new stateless variant has to be added to
// ModeHintTables by hand, where the arms table below then demands its arm.
func TestMenuBars_KeysExistInRegistry(t *testing.T) {
	// A new MenuState bumps the sentinel and fails here on purpose: decide
	// whether its bar carries key hints (add it to the walk and, if it renders
	// a mode table, to keys.ModeHintTables) or runtime progress text
	// (StateBusy is exempt — its line is progress, not keys).
	require.Equal(t, 7, int(menuStateCount), "MenuState enum changed — classify the new state for this scan")

	known := map[string]bool{}
	for _, b := range keys.GlobalKeyBindings {
		known[b.Help().Key] = true
	}
	for _, table := range keys.ModeHintTables() {
		for _, b := range table {
			known[b.Help().Key] = true
		}
	}

	// scanBar applies direction 2 and returns the bar's leading tokens.
	scanBar := func(desc string, m *Menu) map[string]bool {
		line := strings.TrimSpace(xansi.Strip(m.String()))
		require.NotEmpty(t, line, "%s renders no bar", desc)

		tokens := map[string]bool{}
		for _, seg := range strings.Split(line, separator) {
			if seg == filterSyntaxHint {
				continue
			}
			token, _, _ := strings.Cut(seg, " ")
			require.Truef(t, known[token],
				"%s names key %q (segment %q), which no registry binding or mode hint table carries",
				desc, token, seg)
			tokens[token] = true
		}
		return tokens
	}

	for _, state := range []MenuState{StateDefault, StateEmpty, StateFilter, StateHints, StateVisual, StateDiffComment} {
		m := NewMenu()
		m.SetSize(400, 3) // wide, so truncation can't eat trailing segments
		m.SetState(state)
		scanBar(fmt.Sprintf("state %v", state), m)
	}

	// Direction 3: one arm per mode table, each arming the Menu the way the
	// app does for that variant. The pane-focus variant swaps the default bar
	// without a MenuState of its own — the app derives pane focus and pushes
	// it per render — which is why the arms are declared here and not derived
	// from the enum walk above.
	arms := map[string]func(*Menu){
		"filter":       func(m *Menu) { m.SetState(StateFilter) },
		"hints":        func(m *Menu) { m.SetState(StateHints) },
		"visual":       func(m *Menu) { m.SetState(StateVisual) },
		"diff-comment": func(m *Menu) { m.SetState(StateDiffComment) },
		"pane-focus":   func(m *Menu) { m.SetState(StateDefault); m.SetPaneFocus(true) },
	}
	for name, table := range keys.ModeHintTables() {
		arm, ok := arms[name]
		require.Truef(t, ok, "mode table %q has no scan arm — declare how Menu renders it", name)

		m := NewMenu()
		m.SetSize(400, 3)
		arm(m)
		tokens := scanBar("bar "+name, m)
		for _, b := range table {
			require.Truef(t, tokens[b.Help().Key],
				"mode table %q entry %q does not render in its own bar — its arm renders a different table", name, b.Help().Key)
		}
	}
}

// renderModeLine skips an entry whose display key is empty — the shape a table
// takes when every action behind a label is unbound (keys.PaneFocusHints drops
// such an entry itself; this is the render-side guard for tables that don't).
// Rendering it would promise an action with no way to reach it, as a keyless
// " desc" segment with a dangling separator.
func TestMenu_ModeLineSkipsKeylessEntry(t *testing.T) {
	m := NewMenu()
	m.SetSize(80, 3)
	line := xansi.Strip(m.renderModeLine([]key.Binding{
		key.NewBinding(key.WithHelp("", "scroll")),
		key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "exit")),
	}))
	require.Equal(t, "esc exit", line, "a keyless entry must be skipped whole, separator included")
}
