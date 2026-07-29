package app

import (
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/session"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/muesli/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Closing a modal must give the hint bar its row back.
//
// A state that hides the bar (menuVisible) sizes the panes one row taller. Closing
// it renders the bar again — so unless the layout is recomputed, the frame is one
// line taller than the terminal: it scrolls, and the alt-screen renderer never
// erases lines, so the top border is gone until a full repaint. Five states left
// it that way permanently (command palette, command log, queue, rename, confirm);
// the rest recomputed only inside the tea.WindowSize() they returned, which is a
// frame late — bubbletea renders the model as soon as Update returns, without
// waiting for the command. So the assertion is deliberately synchronous: the frame
// must be right in the very render that follows the key.
//
// TestViewFitsTerminalBounds could not see any of this: it only ever measures a
// *freshly armed* overlay, never one that has been closed.
//
// Driven through Update — the runtime's own entry point — rather than the dismiss
// helpers: the guard sits there because a state can also be left by a message, and
// a helper called directly would not exercise it.
func TestEveryBarHidingStateRestoresTheFrame(t *testing.T) {
	sizes := [][2]int{{80, 24}, {120, 40}}

	// How a user opens each modal, and the key that closes it again.
	opens := map[state]struct {
		name string
		open func(h *home)
	}{
		statePrompt:         {"create form", func(h *home) { h.Update(runeKey("n")) }},
		stateRename:         {"rename", func(h *home) { h.Update(runeKey("R")) }},
		stateQueue:          {"queue", func(h *home) { h.Update(runeKey("Q")) }},
		stateCmdLog:         {"command log", func(h *home) { h.Update(runeKey("L")) }},
		stateCommandPalette: {"command palette", func(h *home) { h.Update(keyMsg("ctrl+k")) }},
		stateConfirm:        {"confirmation", func(h *home) { h.Update(keyMsg("ctrl+x")) }},
		stateHelp:           {"cheatsheet", func(h *home) { h.Update(runeKey("?")) }},
		stateSettings:       {"settings", func(h *home) { h.Update(runeKey(",")) }},
		stateAccounts:       {"accounts", func(h *home) { h.Update(runeKey("@")) }},
		stateInfo:           {"info modal", func(h *home) { h.showInfo("something worth reading") }},
		stateWelcome:        {"welcome", func(h *home) { h.maybeShowWelcome() }},
	}

	// A bar-hiding state left out of the table above needs a reason, not silence.
	exempt := map[state]string{
		stateHistory: "opened from an empty prompt field inside the create form, and esc " +
			"returns there rather than to the list — the bar is hidden on both sides of " +
			"that close, so there is no row to reclaim",
	}

	for st := state(0); st < numStates; st++ {
		probe := newCreateFormHome(t)
		probe.state = st
		if probe.menuVisible() {
			continue // this state keeps the bar; its budget never changed
		}
		tc, ok := opens[st]
		if !ok {
			assert.NotEmptyf(t, exempt[st], "state %d hides the hint bar but is neither driven "+
				"nor exempted here — add it to opens (with the key that opens it) or to exempt "+
				"(with the reason its close cannot strand a row)", st)
			continue
		}

		for _, dim := range sizes {
			w, h := dim[0], dim[1]
			t.Run(tc.name, func(t *testing.T) {
				home := newFrameHome(t, w, h)
				before := strings.Split(home.View(), "\n")

				tc.open(home)
				require.Equal(t, st, home.state, "%s did not open", tc.name)
				// What the opener's tea.WindowSize() does once the runtime delivers it.
				home.recomputeLayout()
				require.Falsef(t, home.menuVisible(),
					"%s is meant to hide the hint bar; if it no longer does, this case proves nothing", tc.name)
				open := strings.Split(home.View(), "\n")
				assert.LessOrEqualf(t, len(open), h, "%s: the open frame already overflows %d rows", tc.name, h)

				home.Update(keyMsg("esc"))
				require.Equalf(t, stateDefault, home.state, "%s did not close on esc", tc.name)
				after := strings.Split(home.View(), "\n")

				assert.LessOrEqualf(t, len(after), h,
					"%s: after closing, View() emits %d lines into a %d-row terminal — the frame "+
						"scrolls and the alt-screen renderer never erases it", tc.name, len(after), h)
				assert.Lenf(t, after, len(before),
					"%s: the frame did not return to its pre-open height (%d → %d)", tc.name, len(before), len(after))
				for i, line := range after {
					if !assert.Equalf(t, w, ansi.PrintableRuneWidth(line),
						"%s: line %d is %d cells wide after closing", tc.name, i, ansi.PrintableRuneWidth(line)) {
						break
					}
				}
			})
		}
	}
}

// newFrameHome is a sized home with one ready session carrying a queued prompt, so
// every modal above has something to open against.
func newFrameHome(t *testing.T, w, h int) *home {
	t.Helper()
	home := newCreateFormHome(t)
	home.newSessionPath = t.TempDir()
	inst, err := session.NewInstance(session.InstanceOptions{
		Title: "s", Path: t.TempDir(), Program: "echo",
	})
	require.NoError(t, err)
	home.list.AddInstance(inst)()
	home.list.SetSelectedInstance(0)
	inst.SetStatus(session.Ready)
	inst.QueuePrompt("queued")
	home.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: w, Height: h})
	home.instanceChanged()
	return home
}
