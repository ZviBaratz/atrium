package app

import (
	"testing"

	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui/overlay"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"
)

// newSmokeHome builds a fully-wired home parked in state st, with one selected
// instance and its panes already sized. The optional wire callback attaches the
// overlay a given state expects, so each state is exercised in a *valid* shape
// (overlay present where production keeps one) rather than a half-constructed one.
func newSmokeHome(t *testing.T, st state, wire func(h *home, inst *session.Instance)) *home {
	t.Helper()
	h := newCreateFormHome(t)
	h.spinner = spinner.New()
	h.lostStrikes = map[*session.Instance]int{}

	inst, err := session.NewInstance(session.InstanceOptions{
		Title: "s", Path: t.TempDir(), Program: "echo",
	})
	require.NoError(t, err)
	h.list.AddInstance(inst)()
	h.list.SetSelectedInstance(0)

	// Size the components in the default state so layout recomputation (which several
	// background handlers trigger) exercises the real path under every state below.
	h.Update(tea.WindowSizeMsg{Width: 120, Height: 40})

	h.state = st
	if wire != nil {
		wire(h, inst)
	}
	return h
}

// TestStateMachine_BackgroundMessagesNeverPanic is a robustness sweep: for every UI
// state, feed each background/async message — the ones emitted by timers and
// goroutines independently of the current state, so they can genuinely arrive in any
// of them — through Update *and the View that follows it* and assert neither panics
// nor nil-derefs, and that Update always returns a model. Rendering is what makes the
// per-state overlay wiring load-bearing: the overlay dereferences live in View.
// State-routed *key* handling is covered per-feature elsewhere; this guards the
// cross-product that those targeted tests don't.
func TestStateMachine_BackgroundMessagesNeverPanic(t *testing.T) {
	states := []struct {
		name string
		st   state
		wire func(h *home, inst *session.Instance)
	}{
		{"default", stateDefault, nil},
		{"prompt", statePrompt, func(h *home, _ *session.Instance) {
			h.textInputOverlay = overlay.NewSessionCreateOverlay(
				h.appConfig.GetProfiles(), h.appConfig.ClaudeAccounts, nil, h.program)
		}},
		{"help", stateHelp, func(h *home, _ *session.Instance) {
			h.textOverlay = overlay.NewTextOverlay("help")
		}},
		{"info", stateInfo, func(h *home, _ *session.Instance) {
			h.textOverlay = overlay.NewTextOverlay("info")
		}},
		{"confirm", stateConfirm, func(h *home, _ *session.Instance) {
			h.confirmationOverlay = overlay.NewConfirmationOverlay("sure?")
		}},
		{"rename", stateRename, func(h *home, inst *session.Instance) {
			h.renameTarget = inst
			h.renameOverlay = overlay.NewRenameOverlay("label", "", false)
		}},
		{"settings", stateSettings, func(h *home, _ *session.Instance) {
			h.settingsOverlay = overlay.NewSettingsOverlay(h.appConfig)
		}},
		{"accounts", stateAccounts, func(h *home, _ *session.Instance) {
			h.accountsOverlay = overlay.NewAccountsOverlay(h.appConfig)
		}},
		{"filter", stateFilter, nil},
		{"hints", stateHints, nil},
		{"visual", stateVisual, nil},
		{"queue", stateQueue, func(h *home, inst *session.Instance) {
			h.queueOverlay = overlay.NewQueueOverlay(inst.DisplayName())
		}},
		{"cmdlog", stateCmdLog, func(h *home, _ *session.Instance) {
			h.cmdLogOverlay = overlay.NewCmdLogOverlay("test-session")
		}},
		{"welcome", stateWelcome, func(h *home, _ *session.Instance) {
			h.welcomeOverlay = overlay.NewWelcomeOverlay()
		}},
		{"history", stateHistory, func(h *home, _ *session.Instance) {
			h.promptHistoryOverlay = overlay.NewPromptHistoryOverlay([]string{"remembered"})
		}},
	}

	// Each factory takes the home's selected instance so payload-bearing messages
	// (autoNameDoneMsg dereferences it) carry a live target.
	messages := []struct {
		name string
		make func(inst *session.Instance) tea.Msg
	}{
		{"WindowSizeMsg", func(*session.Instance) tea.Msg { return tea.WindowSizeMsg{Width: 100, Height: 40} }},
		{"previewTickMsg", func(*session.Instance) tea.Msg { return previewTickMsg{} }},
		{"metadataUpdateDoneMsg", func(*session.Instance) tea.Msg { return metadataUpdateDoneMsg{} }},
		{"metadataSweepDoneMsg", func(*session.Instance) tea.Msg { return metadataSweepDoneMsg{} }},
		{"autoNameDoneMsg", func(inst *session.Instance) tea.Msg { return autoNameDoneMsg{instance: inst, name: "x"} }},
		{"smartDispatchDoneMsg", func(*session.Instance) tea.Msg { return smartDispatchDoneMsg{} }},
		{"spinnerTickMsg", func(*session.Instance) tea.Msg { return spinner.TickMsg{} }},
		{"mousePress", func(*session.Instance) tea.Msg {
			return tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft}
		}},
	}

	for _, sc := range states {
		for _, mc := range messages {
			t.Run(sc.name+"/"+mc.name, func(t *testing.T) {
				h := newSmokeHome(t, sc.st, sc.wire)
				msg := mc.make(h.list.GetSelectedInstance())

				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("Update/View panicked in state %q on %s: %v", sc.name, mc.name, r)
					}
				}()

				model, _ := h.Update(msg)
				require.NotNil(t, model, "Update must always return a model")

				// Render too: the per-state overlay dereferences live in View, so an
				// Update-only sweep never touches the fields each wire callback arms.
				// Bubble Tea always renders after Update, so a state whose overlay the
				// message invalidated panics here, not above.
				require.NotEmpty(t, model.View(), "View must render in every state")
			})
		}
	}
}
