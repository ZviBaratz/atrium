package app

import (
	"context"
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"
)

// newChromeHome builds a minimal home holding instances at the given statuses, with
// the OS chrome switch on. It renders nothing until refreshOSChrome runs, which is
// the point: the derivation is the unit under test.
func newChromeHome(t *testing.T, instances ...*session.Instance) *home {
	t.Helper()
	s := spinner.New()
	l := ui.NewList(&s)
	for _, inst := range instances {
		l.AddInstance(inst)
	}
	return &home{
		ctx:          context.Background(),
		state:        stateDefault,
		list:         l,
		menu:         ui.NewMenu(),
		tabbedWindow: ui.NewTabbedWindow(ui.NewPreviewPane(), ui.NewDiffPane(), ui.NewTerminalPane(context.Background())),
		errBox:       ui.NewErrBox(),
		appConfig:    config.DefaultConfig(),
		appState:     config.DefaultState(),
	}
}

// chromeInstance makes an unstarted instance at the given status.
func chromeInstance(t *testing.T, title string, st session.Status) *session.Instance {
	t.Helper()
	inst, err := session.NewInstance(session.InstanceOptions{
		Title: title, Path: t.TempDir(), Program: "echo",
	})
	require.NoError(t, err)
	inst.SetStatus(st)
	return inst
}

// The title is the fleet's whole presence in a terminal tab bar, so it has to name
// both populations and the counts have to be right.
func TestRefreshOSChrome_TitleReflectsFleet(t *testing.T) {
	h := newChromeHome(t,
		chromeInstance(t, "a", session.NeedsInput),
		chromeInstance(t, "b", session.NeedsInput),
		chromeInstance(t, "c", session.Running),
	)

	h.refreshOSChrome(false)

	require.Equal(t, "atrium · 2 need you · 1 running", h.osChromeTitle)
}

// The count derivation is the part with no other coverage: Loading counts as work
// in flight, a paused session counts as nothing at all, and a finished turn nobody
// has looked at is still asking for the user.
func TestRefreshOSChrome_CountDerivation(t *testing.T) {
	loading := chromeInstance(t, "loading", session.Loading)
	// A new instance is already Ready, so the unread bit is set by driving the real
	// non-Ready→Ready edge — the same one a finished turn produces in the poll loop.
	unread := chromeInstance(t, "unread", session.Running)
	unread.SetStatus(session.Ready)
	read := chromeInstance(t, "read", session.Ready)
	read.MarkSeen()

	h := newChromeHome(t, loading, unread, read)
	h.refreshOSChrome(false)

	require.True(t, unread.Unread(), "fixture precondition: the unread session is unread")
	require.False(t, read.Unread(), "fixture precondition: the read session is not")
	require.Equal(t, "atrium · 1 need you · 1 running", h.osChromeTitle,
		"Loading counts as running; an unread finished turn counts as needing you; a read one counts as neither")
}

// A paused session is not part of the fleet's live state — its worktree is gone —
// so it must not appear in either count regardless of the status it froze at.
func TestRefreshOSChrome_PausedSessionsNeverCount(t *testing.T) {
	h := newChromeHome(t, chromeInstance(t, "a", session.Running))
	h.refreshOSChrome(false)
	require.Equal(t, "atrium · 1 running", h.osChromeTitle, "precondition: it counts while live")

	h.list.GetInstances()[0].SetStatus(session.Paused)
	h.refreshOSChrome(false)

	require.Equal(t, "atrium", h.osChromeTitle, "a paused session drops out of the title")
	require.Equal(t, tea.ProgressBarNone, h.osChromeProgress, "and out of the progress bar")
}

// The three progress states, including both cases where an error this tick outranks
// what the fleet is otherwise doing.
func TestRefreshOSChrome_ProgressStates(t *testing.T) {
	idle := newChromeHome(t, chromeInstance(t, "a", session.Ready))
	idle.list.GetInstances()[0].MarkSeen()
	idle.refreshOSChrome(false)
	require.Equal(t, tea.ProgressBarNone, idle.osChromeProgress)

	busy := newChromeHome(t, chromeInstance(t, "a", session.Running))
	busy.refreshOSChrome(false)
	require.Equal(t, tea.ProgressBarIndeterminate, busy.osChromeProgress)

	busy.refreshOSChrome(true)
	require.Equal(t, tea.ProgressBarError, busy.osChromeProgress, "an error outranks a working session")

	idle.refreshOSChrome(true)
	require.Equal(t, tea.ProgressBarError, idle.osChromeProgress, "and an idle one")
}

// errored is a per-tick observation, not a latched state: the bar clears itself on
// the next healthy tick rather than staying red until something resets it.
func TestRefreshOSChrome_ErrorClearsOnNextTick(t *testing.T) {
	h := newChromeHome(t, chromeInstance(t, "a", session.Running))

	h.refreshOSChrome(true)
	require.Equal(t, tea.ProgressBarError, h.osChromeProgress)

	h.refreshOSChrome(false)
	require.Equal(t, tea.ProgressBarIndeterminate, h.osChromeProgress,
		"the next healthy tick must drop the error state")
}

// os_chrome off means the user's shell owns the title: Atrium contributes nothing,
// and the renderer turns the empty title and cleared bar into an actual clear.
func TestRefreshOSChrome_DisabledContributesNothing(t *testing.T) {
	h := newChromeHome(t, chromeInstance(t, "a", session.Running))
	h.refreshOSChrome(false)
	require.NotEmpty(t, h.osChromeTitle, "precondition: it contributes while enabled")

	off := false
	h.appConfig.OSChrome = &off
	h.refreshOSChrome(false)

	require.Empty(t, h.osChromeTitle)
	require.Equal(t, tea.ProgressBarNone, h.osChromeProgress)
}

// View carries the derived chrome, which is what actually reaches the terminal.
func TestView_CarriesOSChrome(t *testing.T) {
	h := newChromeHome(t, chromeInstance(t, "a", session.Running))
	h.windowWidth, h.windowHeight = 80, 24
	h.refreshOSChrome(false)

	v := h.View()

	require.Equal(t, "atrium · 1 running", v.WindowTitle)
	require.NotNil(t, v.ProgressBar)
	require.Equal(t, tea.ProgressBarIndeterminate, v.ProgressBar.State)
}

// View must hand back a fresh *ProgressBar every frame. Bubble Tea's renderer keeps
// the pointer it was given as "the last view" and diffs the value behind it, so a
// shared pointer mutated in place compares equal to itself: the bar would freeze at
// whatever it showed when the pointer last changed, its reset would be skipped at
// exit, and the whole-view equality check — which reads through the same pointer —
// would start dropping frames whose content happened not to move.
//
// Nothing else can see this. Every state assertion above passes with a shared
// pointer, and so does every golden frame.
func TestView_ProgressBarPointerIsFreshEachFrame(t *testing.T) {
	h := newChromeHome(t, chromeInstance(t, "a", session.Running))
	h.windowWidth, h.windowHeight = 80, 24
	h.refreshOSChrome(false)

	first, second := h.View().ProgressBar, h.View().ProgressBar

	require.NotNil(t, first)
	require.NotNil(t, second)
	require.NotSame(t, first, second,
		"two frames must not share a ProgressBar — the renderer diffs through the pointer")
	require.Equal(t, *first, *second, "and the value they carry is still the same state")
}
