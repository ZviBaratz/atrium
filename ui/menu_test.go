package ui

import (
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/git"
	"github.com/charmbracelet/lipgloss"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// The three mode bars teach modal gesture vocabularies. Exact-text pins: the
// bars render from the registry's mode hint tables through the same path as
// renderHintLine, and these froze the text across that refactor — any change
// to the words or separators is a deliberate UX decision, not a side effect.
func TestMenu_ModeBarsExactText(t *testing.T) {
	for state, want := range map[MenuState]string{
		StateFilter: "enter accept · esc clear · filter: status: dirty behind pr: account: note:",
		StateHints:  "a–z copy · A–Z copy + open · esc cancel",
		StateVisual: "space mark · p/r/x pause/resume/kill marked · esc exit",
	} {
		m := NewMenu()
		m.SetSize(200, 3)
		m.SetState(state)
		got := strings.TrimSpace(xansi.Strip(m.String()))
		require.Equal(t, want, got, "state %v bar text", state)
	}
}

// The default bar is a short, fixed line of high-value keys — a reminder that
// keys exist (with ? as the door to the full list), not a reference card.
func TestMenu_DefaultHintLine(t *testing.T) {
	inst, err := session.NewInstance(session.InstanceOptions{Title: "t", Path: t.TempDir(), Program: "echo"})
	require.NoError(t, err)
	m := NewMenu()
	m.SetSize(200, 3)
	m.SetInstance(inst)

	out := m.String()
	for _, want := range []string{"open", "new", "send", "kill", "help"} {
		require.Contains(t, out, want)
	}
	require.NotContains(t, out, "scroll", "the scroll hint is reserved for the scrolling tabs")

	// The diff/terminal tabs add the scroll hint.
	m.SetActiveTab(DiffTab)
	require.Contains(t, m.String(), "scroll")
	m.SetActiveTab(PreviewTab)
	require.NotContains(t, m.String(), "scroll")
}

// With no sessions, the bar surfaces the create/help/quit keys instead.
func TestMenu_EmptyHintLine(t *testing.T) {
	m := NewMenu()
	m.SetSize(200, 3)
	m.SetInstance(nil)

	out := m.String()
	for _, want := range []string{"new", "help", "quit"} {
		require.Contains(t, out, want)
	}
	require.NotContains(t, out, "kill", "no session to kill in the empty state")
	require.NotContains(t, out, "pick project", "the n/N distinction is noise with zero sessions")
}

// A paused session can't be opened or sent to — the bar must advertise what
// actually works (resume, kill) instead of actions that silently no-op.
func TestMenu_PausedHintLine(t *testing.T) {
	inst, err := session.NewInstance(session.InstanceOptions{Title: "t", Path: t.TempDir(), Program: "echo"})
	require.NoError(t, err)
	inst.SetStatus(session.Paused)

	m := NewMenu()
	m.SetSize(200, 3)
	m.SetInstance(inst)

	out := m.String()
	require.Contains(t, out, "resume")
	require.Contains(t, out, "kill")
	require.Contains(t, out, "copy branch",
		"a paused session's branch is the thing you came to pick up elsewhere — y works here")
	require.NotContains(t, out, "open", "a paused session cannot be attached")
	require.NotContains(t, out, "send", "a paused session cannot receive messages")
}

// A direct (non-git) session has no worktree and no branch, so y would report
// "no branch to copy yet". Direct sessions still reach Paused — not through
// Pause(), which refuses them, but through RecoverLostSession when their pane
// dies — and hintsFor tests Paused() before IsDirect(), so the paused set has
// to carve them out or the bar advertises a dead key.
func TestMenu_PausedDirectHintLineOmitsCopyBranch(t *testing.T) {
	inst, err := session.NewInstance(session.InstanceOptions{
		Title: "t", Path: t.TempDir(), Program: "echo", Direct: true,
	})
	require.NoError(t, err)
	inst.SetStatus(session.Paused)

	m := NewMenu()
	m.SetSize(200, 3)
	m.SetInstance(inst)

	out := m.String()
	require.Contains(t, out, "resume", "a parked direct session can still be resumed")
	require.NotContains(t, out, "copy branch", "a direct session has no branch to copy")
}

// A session with work on its branch surfaces the pause/push pair; a clean one
// keeps the bar short.
func TestMenu_DirtyHintLineAddsPausePush(t *testing.T) {
	inst, err := session.NewInstance(session.InstanceOptions{Title: "t", Path: t.TempDir(), Program: "echo"})
	require.NoError(t, err)
	inst.SetStatus(session.Running)
	inst.SetDiffStats(&git.DiffStats{Added: 3, Removed: 1, Content: "x"})

	m := NewMenu()
	m.SetSize(200, 3)
	m.SetInstance(inst)

	out := m.String()
	require.Contains(t, out, "pause")
	require.Contains(t, out, "push")

	inst.SetDiffStats(&git.DiffStats{}) // clean
	m.SetInstance(inst)
	out = m.String()
	require.NotContains(t, out, "pause", "a clean session has nothing to pause")
	require.NotContains(t, out, "push", "a clean session has nothing to push")
}

// A session whose PR is ready to merge surfaces both the merge and open-PR keys.
// A blocked PR (e.g. conflicting) drops merge but still advertises open-PR — the
// case where going to look at the PR on GitHub matters most.
func TestMenu_MergeableHintSurfacesMerge(t *testing.T) {
	inst, err := session.NewInstance(session.InstanceOptions{Title: "t", Path: t.TempDir(), Program: "echo"})
	require.NoError(t, err)
	inst.SetStatus(session.Running)
	inst.SetPRStatus(&git.PRStatus{HasPR: true, State: "OPEN", CI: git.CIPassing, Mergeable: "MERGEABLE"})

	m := NewMenu()
	m.SetSize(200, 3)
	m.SetInstance(inst)
	require.Contains(t, m.String(), "merge")
	require.Contains(t, m.String(), "open PR", "a mergeable PR also advertises open-PR")

	inst.SetPRStatus(&git.PRStatus{HasPR: true, State: "OPEN", Mergeable: "CONFLICTING"})
	m.SetInstance(inst)
	require.NotContains(t, m.String(), "merge", "a blocked PR must not advertise merge")
	require.Contains(t, m.String(), "open PR", "a blocked PR still advertises open-PR")
}

// A pushed session with no PR yet surfaces the create key; an unpushed session
// (no remote ref) does not, and a session whose PR already exists shows merge
// rather than create.
func TestMenu_CreatableHintSurfacesCreate(t *testing.T) {
	inst, err := session.NewInstance(session.InstanceOptions{Title: "t", Path: t.TempDir(), Program: "echo"})
	require.NoError(t, err)
	inst.SetStatus(session.Running)

	// Pushed, no PR yet => create is the headline action.
	inst.SetPRStatus(&git.PRStatus{Pushed: true, HasPR: false})
	m := NewMenu()
	m.SetSize(200, 3)
	m.SetInstance(inst)
	require.Contains(t, m.String(), "create")

	// Not pushed yet => create must not be advertised (push first).
	inst.SetPRStatus(&git.PRStatus{Pushed: false, HasPR: false})
	m.SetInstance(inst)
	require.NotContains(t, m.String(), "create", "an unpushed branch must not advertise create")

	// An open, mergeable PR already exists => surface merge, not create.
	inst.SetPRStatus(&git.PRStatus{Pushed: true, HasPR: true, State: "OPEN", CI: git.CIPassing, Mergeable: "MERGEABLE"})
	m.SetInstance(inst)
	out := m.String()
	require.Contains(t, out, "merge")
	require.NotContains(t, out, "create", "a branch with a PR hands off to merge, not create")
}

// On a terminal narrower than the hint line, the bar truncates with an
// ellipsis instead of overflowing and wrapping — wrapping would grow the row
// and break the one-row layout contract.
func TestMenu_HintLineTruncatesOnNarrowWidth(t *testing.T) {
	inst, err := session.NewInstance(session.InstanceOptions{Title: "t", Path: t.TempDir(), Program: "echo"})
	require.NoError(t, err)
	m := NewMenu()
	m.SetSize(30, 1)
	m.SetInstance(inst)

	out := m.String()
	require.Equal(t, 1, lipgloss.Height(out), "hint bar must stay a single row")
	require.LessOrEqual(t, lipgloss.Width(out), 30)
	require.Contains(t, out, "…")
}

// The filter state shows its own accept/clear cue.
func TestMenu_FilterHintLine(t *testing.T) {
	m := NewMenu()
	m.SetSize(200, 3)
	m.SetState(StateFilter)

	out := m.String()
	require.Contains(t, out, "accept")
	require.Contains(t, out, "clear")
}

// While the agent is blocked on a prompt, answering it is the headline action:
// the bar must surface approve, and drop it again once the agent moves on.
func TestMenu_NeedsInputHintSurfacesApprove(t *testing.T) {
	inst, err := session.NewInstance(session.InstanceOptions{Title: "t", Path: t.TempDir(), Program: "echo"})
	require.NoError(t, err)
	inst.SetStatus(session.NeedsInput)

	m := NewMenu()
	m.SetSize(200, 3)
	m.SetInstance(inst)
	require.Contains(t, m.String(), "approve")

	inst.SetStatus(session.Running)
	m.SetInstance(inst)
	require.NotContains(t, m.String(), "approve", "a working agent has nothing to approve")
}

// A blocked prompt outranks PR state: merge/push are moot until the agent is
// unblocked, so the needs-input set wins over the mergeable set.
func TestMenu_NeedsInputHintOutranksPR(t *testing.T) {
	inst, err := session.NewInstance(session.InstanceOptions{Title: "t", Path: t.TempDir(), Program: "echo"})
	require.NoError(t, err)
	inst.SetStatus(session.NeedsInput)
	inst.SetPRStatus(&git.PRStatus{HasPR: true, State: "OPEN", CI: git.CIPassing, Mergeable: "MERGEABLE"})

	m := NewMenu()
	m.SetSize(200, 3)
	m.SetInstance(inst)

	out := m.String()
	require.Contains(t, out, "approve")
	require.NotContains(t, out, "merge", "PR actions are moot while the agent waits on a prompt")
}
