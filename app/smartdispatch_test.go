package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui"
	"github.com/ZviBaratz/atrium/ui/overlay"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/stretchr/testify/require"
)

// newSmartHome builds a home wired enough to drive the smart-dispatch flow and open
// the create form, with no tmux/git/HOME reach (direct sessions on temp dirs).
func newSmartHome(t *testing.T) *home {
	t.Helper()
	s := spinner.New()
	l := ui.NewList(&s)
	st, err := session.NewStorage(config.DefaultState())
	require.NoError(t, err)
	return &home{
		ctx:          context.Background(),
		state:        stateDefault,
		list:         l,
		menu:         ui.NewMenu(),
		tabbedWindow: ui.NewTabbedWindow(ui.NewPreviewPane(), ui.NewDiffPane(), ui.NewTerminalPane(context.Background())),
		errBox:       ui.NewErrBox(),
		appConfig:    config.DefaultConfig(),
		appState:     config.DefaultState(),
		storage:      st,
		program:      "echo",
	}
}

// mkNamedDir creates a real directory whose basename is name, so it becomes a
// candidate the matcher can route to.
func mkNamedDir(t *testing.T, name string) string {
	t.Helper()
	d := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.MkdirAll(d, 0o755))
	return d
}

func TestSmartDispatch_ConfidentMatchSeedsForm(t *testing.T) {
	h := newSmartHome(t)
	neutral := mkNamedDir(t, "workspace")
	box := mkNamedDir(t, "box")
	addDirectInstance(t, h, "neutral", neutral) // selected → contextual default
	addDirectInstance(t, h, "other", box)       // a candidate the matcher can pick

	h.handleSmartDispatchSubmit("Review box#123")

	require.Equal(t, statePrompt, h.state)
	require.NotNil(t, h.textInputOverlay)
	require.True(t, h.textInputOverlay.IsCreateForm())
	require.Equal(t, "Review box#123", h.textInputOverlay.GetValue(), "the line seeds the prompt")
	require.Equal(t, "Review #123", h.textInputOverlay.GetTitle(), "the redundant project name is dropped, '#' kept")
	require.Equal(t, box, h.textInputOverlay.GetSelectedPath(), "the matched project is pre-selected")
	require.NotContains(t, h.textInputOverlay.Render(), "detecting", "a confident local match needs no async routing")
	require.NotContains(t, h.textInputOverlay.Render(), "refining", "a clean short title needs no LLM upgrade")
}

func TestSmartDispatch_ConfidentMatchFocusesPermissions(t *testing.T) {
	h := newSmartHome(t)
	h.program = "claude" // the Permissions field exists only for a claude program
	neutral := mkNamedDir(t, "workspace")
	box := mkNamedDir(t, "box")
	addDirectInstance(t, h, "neutral", neutral)
	addDirectInstance(t, h, "other", box)

	h.handleSmartDispatchSubmit("Review box#123")

	require.Equal(t, statePrompt, h.state)
	require.True(t, h.textInputOverlay.ModeFocused(),
		"a confident match lands focus on the Permissions chip, the one decision smart dispatch defers")
}

func TestSmartDispatch_ConfidentRoughMatchUpgradesTitleAsync(t *testing.T) {
	h := newSmartHome(t)
	neutral := mkNamedDir(t, "workspace")
	box := mkNamedDir(t, "box")
	addDirectInstance(t, h, "neutral", neutral) // selected → contextual default
	addDirectInstance(t, h, "other", box)       // the confident match

	cmd := h.handleSmartDispatchSubmit("box keeps crashing on startup unexpectedly")

	require.Equal(t, statePrompt, h.state)
	require.NotNil(t, cmd, "a confident but prose-y title still routes for an LLM title upgrade")
	require.Equal(t, box, h.textInputOverlay.GetSelectedPath(), "the confident project stays pre-selected")
	require.Equal(t, "keeps crashing on startup", h.textInputOverlay.GetTitle(), "the stripped slug seeds the title")
	h.textInputOverlay.SetSize(100, 40)
	render := h.textInputOverlay.Render()
	require.Contains(t, render, "refining", "a title-only upgrade is in flight")
	require.NotContains(t, render, "detecting", "the project is already known — not a routing call")
}

func TestSmartDispatch_EmptyProjectResultStillUpgradesTitle(t *testing.T) {
	h := newSmartHome(t)
	neutral := mkNamedDir(t, "workspace")
	box := mkNamedDir(t, "box")
	addDirectInstance(t, h, "neutral", neutral)
	addDirectInstance(t, h, "other", box)

	h.handleSmartDispatchSubmit("the crash in the dashboard") // unmatched → async, title seeded

	// The router returned a usable title but no project: the title must still land,
	// independent of routing, while the picker stays on the contextual default.
	h.Update(smartDispatchDoneMsg{form: h.textInputOverlay, project: "", title: "Dashboard crash"})

	require.Equal(t, "Dashboard crash", h.textInputOverlay.GetTitle(), "a title lands even without a routed project")
	require.Equal(t, neutral, h.textInputOverlay.GetSelectedPath(), "no project means the picker stays on the default")
}

func TestSmartDispatch_NoMatchOpensFormAndRoutesAsync(t *testing.T) {
	h := newSmartHome(t)
	neutral := mkNamedDir(t, "workspace")
	box := mkNamedDir(t, "box")
	addDirectInstance(t, h, "neutral", neutral)
	addDirectInstance(t, h, "other", box)

	cmd := h.handleSmartDispatchSubmit("fix the dashboard crash")

	require.Equal(t, statePrompt, h.state)
	require.Equal(t, "fix the dashboard crash", h.textInputOverlay.GetValue())
	require.Equal(t, neutral, h.textInputOverlay.GetSelectedPath(), "unmatched → contextual default")
	h.textInputOverlay.SetSize(100, 40)
	require.Contains(t, h.textInputOverlay.Render(), "detecting", "no local match → routing in flight")
	require.NotNil(t, cmd)
}

func TestSmartDispatch_AutoCreatesOnConfidentMatch(t *testing.T) {
	h := newSmartHome(t)
	on := true
	h.appConfig.SmartDispatchAuto = &on
	box := mkNamedDir(t, "box")
	addDirectInstance(t, h, "other", box)
	before := h.list.NumInstances()

	h.handleSmartDispatchSubmit("Review box#123")

	require.Equal(t, stateDefault, h.state, "auto-dispatch skips the form")
	require.Nil(t, h.textInputOverlay)
	require.Equal(t, before+1, h.list.NumInstances(), "a session was created directly")
}

func TestSmartDispatch_AutoFallsBackToFormOnTitleConflict(t *testing.T) {
	h := newSmartHome(t)
	on := true
	h.appConfig.SmartDispatchAuto = &on
	box := mkNamedDir(t, "box")
	// An existing session whose derived title collides with the one we'd mint
	// ("Review box#123" → "Review #123").
	addDirectInstance(t, h, "Review #123", box)

	h.handleSmartDispatchSubmit("Review box#123")

	require.Equal(t, statePrompt, h.state, "a conflict falls back to the form rather than erroring")
	require.NotNil(t, h.textInputOverlay)
}

func TestSmartDispatch_RoutingResultFillsForm(t *testing.T) {
	h := newSmartHome(t)
	neutral := mkNamedDir(t, "workspace")
	box := mkNamedDir(t, "box")
	addDirectInstance(t, h, "neutral", neutral)
	addDirectInstance(t, h, "other", box)

	h.handleSmartDispatchSubmit("the crash in the dashboard") // unmatched → async

	h.Update(smartDispatchDoneMsg{form: h.textInputOverlay, project: "box", title: "Dashboard crash"})

	require.Equal(t, box, h.textInputOverlay.GetSelectedPath(), "routed project is selected")
	require.Equal(t, "Dashboard crash", h.textInputOverlay.GetTitle(), "routed title replaces the placeholder")
	require.NotContains(t, h.textInputOverlay.Render(), "detecting", "the detecting hint is cleared")
}

func TestSmartDispatch_RoutingDoesNotClobberEditedTitle(t *testing.T) {
	h := newSmartHome(t)
	neutral := mkNamedDir(t, "workspace")
	box := mkNamedDir(t, "box")
	addDirectInstance(t, h, "neutral", neutral)
	addDirectInstance(t, h, "other", box)

	h.handleSmartDispatchSubmit("the crash in the dashboard")
	h.textInputOverlay.SetTitleValue("My own title") // user edits the title

	h.Update(smartDispatchDoneMsg{form: h.textInputOverlay, project: "box", title: "Dashboard crash"})

	require.Equal(t, "My own title", h.textInputOverlay.GetTitle(), "a user-edited title is preserved")
}

func TestSmartDispatch_StaleRoutingResultIgnored(t *testing.T) {
	h := newSmartHome(t)
	neutral := mkNamedDir(t, "workspace")
	box := mkNamedDir(t, "box")
	addDirectInstance(t, h, "neutral", neutral)
	addDirectInstance(t, h, "other", box)

	h.handleSmartDispatchSubmit("fix the dashboard crash") // opens form, routing in flight

	// A result tagged with a different (superseded) form — e.g. the user cancelled and
	// opened another — must not touch the current form.
	stale := overlay.NewSmartDispatchOverlay("other")
	h.Update(smartDispatchDoneMsg{form: stale, project: "box", title: "Stale"})

	require.Equal(t, neutral, h.textInputOverlay.GetSelectedPath(), "stale result did not re-point the project")
	require.NotEqual(t, "Stale", h.textInputOverlay.GetTitle())
	h.textInputOverlay.SetSize(100, 40)
	require.Contains(t, h.textInputOverlay.Render(), "detecting", "the live request's hint is untouched")
}
