package app

import (
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/git"

	xansi "github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The batch-pause question names the consequence the screen cannot show: pause
// commits WIP and then removes the worktree, taking every gitignored file in it
// (#399 item 4). This pins the literal only — that the consequence clause is present
// and identical for both kinds. Whether pause really removes the worktree is
// session/pause.go's to keep, and the reasoning for warning unconditionally (only the
// magnitude varies, and measuring it would mean a per-session `git status --ignored`
// on the UI thread) lives on pauseConfirmMessage.
func TestPauseConfirmMessage(t *testing.T) {
	require.Equal(t,
		"Pause 3 active sessions? (commits any work in progress, then removes each "+
			"worktree — gitignored files like .env or build caches are deleted for good)",
		pauseConfirmMessage("active", 3))
	require.Equal(t,
		"Pause 1 marked session? (commits any work in progress, then removes each "+
			"worktree — gitignored files like .env or build caches are deleted for good)",
		pauseConfirmMessage("marked", 1))
}

// The batch-resume question names what resume rebuilds. It says "reattaches" because
// pause only detaches tmux (session/pause.go): the agent process is normally still
// alive and keeps its conversation, so "restarts" would frighten the user off a
// non-destructive action. The worktree half is qualified ("each removed worktree") and
// the agent half is not, because a batch can hold sessions Resume rebuilds nothing for
// — a parked direct session, a commit-failure park — while every path reattaches.
func TestResumeConfirmMessage(t *testing.T) {
	require.Equal(t,
		"Resume 3 paused sessions? (rebuilds each removed worktree and reattaches every agent)",
		resumeConfirmMessage("paused", 3))
	require.Equal(t,
		"Resume 1 marked session? (rebuilds each removed worktree and reattaches every agent)",
		resumeConfirmMessage("marked", 1))
}

// The host-capacity fact is one sentence shared by the create confirmation and the
// resume clause, so the two cannot drift. It carries no noun ("with 2 already
// running") because the count is 0, 1 or many at different call sites and the
// earlier "%d sessions are already running" printed "1 sessions" for a fan-out batch
// over a cap of 2 with one session live. The limit comes first — a swapped pair
// reads as a plausible sentence, which is why this pins the order.
func TestHostCapacityLine(t *testing.T) {
	require.Equal(t, "Host capacity is 4, with 2 already running", hostCapacityLine(4, 2))
	require.Equal(t, "Host capacity is 2, with 1 already running", hostCapacityLine(2, 1))
	require.Equal(t, "Host capacity is 2, with 0 already running", hostCapacityLine(2, 0))
}

// The over-cap create confirmation states the capacity, the consequence, and the
// escape hatch, in that order — the one dialog that leads with facts rather than the
// verb (#399 left it as it was). Its tail used to send the user to a text editor;
// since PR C the panel owns max_sessions and ',' opens the dialog straight onto that
// row.
func TestOverCapMessage(t *testing.T) {
	require.Equal(t,
		"Host capacity is 2, with 1 already running.\n"+
			"Another will queue, not parallelize, and drive up load.\n"+
			"Create it anyway? (, to change the limit)",
		overCapMessage(2, 1, 1))
	require.Equal(t,
		"Host capacity is 2, with 1 already running.\n"+
			"Spawning 3 more will queue, not parallelize, and drive up load.\n"+
			"Create them anyway? (, to change the limit)",
		overCapMessage(2, 1, 3))
}

// The dialog wraps at 46 cells, so a message is priced in RENDERED lines, not characters.
// Teaching the key costs less than pointing at config.json did: the old tail wrapped onto a
// second line and the new one does not.
func TestOverCapMessageIsShorterThanThePathItReplaced(t *testing.T) {
	// confirmWidth's preferred 50 less Padding(1,2)'s four cells. Stated as a literal because
	// the dialog is only this wide on a terminal that can afford it; a narrower terminal wraps
	// harder, and this is the case the wording was chosen against.
	const wrap = 46
	for _, m := range []string{overCapMessage(2, 1, 1), overCapMessage(2, 1, 3)} {
		lines := 0
		for _, para := range strings.Split(m, "\n") {
			lines += len(strings.Split(xansi.Wrap(para, wrap, ""), "\n"))
		}
		assert.LessOrEqualf(t, lines, 4, "the confirmation must fit four rendered lines: %q", m)
	}
}

// The resume clause is the second paragraph of the resume confirmations: the same
// capacity sentence, then what resuming this many more costs. "queue rather than
// parallelize" is overCapMessage's point in fewer words — the resume question already
// occupies the first two rendered lines, and the dialog wraps at 46 cells.
func TestResumeCapClause(t *testing.T) {
	require.Equal(t,
		"Host capacity is 2, with 2 already running — 3 more will queue rather than parallelize.",
		resumeCapClause(2, 2, 3))
	require.Equal(t,
		"Host capacity is 2, with 2 already running — another will queue rather than parallelize.",
		resumeCapClause(2, 2, 1))
}

// Push already names its destination in the question, so it gains only the verb
// label: the hint says what y does instead of the generic "confirm".
func TestPushConfirm_VerbLabel(t *testing.T) {
	h := newCreateFormHome(t)
	inst := addActive(t, h, "alpha")
	h.list.SetSelectedInstance(0)
	_ = inst

	_, _ = h.pushSelected()

	require.Equal(t, stateConfirm, h.state)
	require.NotNil(t, h.confirmationOverlay)
	rendered := flattenOverlay(h.confirmationOverlay.Render())
	require.Contains(t, rendered, "Push changes from session 'alpha'?")
	require.Contains(t, rendered, "Press y to push, n or esc to cancel")
}

// Create-PR is gated on the branch already being pushed (CreateBlockedReason), so
// its label must not claim it pushes — it only opens the PR.
func TestCreatePRConfirm_VerbLabel(t *testing.T) {
	h := newCreateFormHome(t)
	inst := addActive(t, h, "alpha")
	inst.SetPRStatus(&git.PRStatus{Pushed: true})
	h.list.SetSelectedInstance(0)

	_, _ = h.createPRForSelected()

	require.Equal(t, stateConfirm, h.state)
	require.NotNil(t, h.confirmationOverlay)
	rendered := flattenOverlay(h.confirmationOverlay.Render())
	require.Contains(t, rendered, "Press y to create the PR, n or esc to cancel")
}

// Every batch dialog labels its confirm key with the verb and the count, from the
// shared core — so the all-sessions and marked-sessions entry points cannot drift.
func TestBatchConfirm_VerbLabels(t *testing.T) {
	t.Run("pause", func(t *testing.T) {
		h := newCreateFormHome(t)
		addActive(t, h, "alpha")
		addActive(t, h, "bravo")

		_ = h.pauseAll()

		require.Equal(t, stateConfirm, h.state)
		require.Contains(t, flattenOverlay(h.confirmationOverlay.Render()),
			"Press y to pause 2 sessions, n or esc to cancel")
	})

	t.Run("resume", func(t *testing.T) {
		h := newCreateFormHome(t)
		addPaused(t, h, "alpha")

		_ = h.resumeAll()

		require.Equal(t, stateConfirm, h.state)
		require.Contains(t, flattenOverlay(h.confirmationOverlay.Render()),
			"Press y to resume 1 session, n or esc to cancel")
	})
}

// The kill dialogs keep the generic hint: they are the shape the others adopted,
// and their alt-key slot (x / ctrl+x double-tap, #448) already carries the extra
// words. This pins the deliberate non-adoption so a later sweep doesn't "fix" it.
func TestKillConfirm_KeepsGenericHint(t *testing.T) {
	h := newCreateFormHome(t)
	inst := addActive(t, h, "alpha")
	inst.SetStatus(session.Running)

	_ = h.confirmKill(inst)

	require.Equal(t, stateConfirm, h.state)
	require.Contains(t, flattenOverlay(h.confirmationOverlay.Render()), "to confirm,")
}
