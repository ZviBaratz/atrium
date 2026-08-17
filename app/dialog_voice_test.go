package app

import (
	"fmt"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/git"
	"github.com/ZviBaratz/atrium/ui/overlay"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
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
		"Pause 3 active sessions? (stops each agent, commits any work in progress, then removes each "+
			"worktree — gitignored files like .env or build caches are deleted for good)",
		pauseConfirmMessage("active", 3))
	require.Equal(t,
		"Pause 1 marked session? (stops each agent, commits any work in progress, then removes each "+
			"worktree — gitignored files like .env or build caches are deleted for good)",
		pauseConfirmMessage("marked", 1))
}

// The batch-resume question names what resume rebuilds. It says "relaunches" because
// pause closes the tmux session (session/pause.go), ending the agent with it, so there
// is nothing left to reattach — and it names the conversation because that is what the
// old "reattaches" was really reassuring the user about.
//
// The worktree half is qualified ("each removed worktree") because a batch can hold
// sessions Resume rebuilds nothing for — a parked direct session, a commit-failure
// park. The conversation half is qualified for a different reason: only claude has a
// transcript adapter, so for every other agent Atrium genuinely does not know, and a
// flat promise would be a guess printed as a fact.
func TestResumeConfirmMessage(t *testing.T) {
	require.Equal(t,
		"Resume 3 paused sessions? (rebuilds each removed worktree and relaunches each agent, "+
			"resuming its conversation where the agent supports it)",
		resumeConfirmMessage("paused", 3))
	require.Equal(t,
		"Resume 1 marked session? (rebuilds each removed worktree and relaunches each agent, "+
			"resuming its conversation where the agent supports it)",
		resumeConfirmMessage("marked", 1))
}

// A copy change is a height change, and this is the one box where nothing else would
// notice. PlaceOverlay CLIPS rather than overflows, so a confirmation that outgrows the
// terminal still renders exactly 24×80 and TestViewFitsTerminalBounds stays green while
// the user loses the bottom border and the "Press y …" line — the only thing in the box
// telling them how to answer it. These are the app's longest confirmations, and they are
// hand-written prose, so the height is whatever the last edit made it.
func TestConfirmationsFitTheSmallestSupportedTerminal(t *testing.T) {
	// The narrowest size TestViewFitsTerminalBounds sweeps, and the smallest terminal
	// anything here is written for.
	const minWidth, minHeight = 80, 24
	// Both fields come from the functions that build the real dialog, composed the way
	// the real dialog composes them — a fixture assembled out of the same words would
	// measure a box the app never renders, and the pieces are shorter than the whole in
	// both directions here: resumeInstances appends resumeCapClause (not the bare
	// hostCapacityLine it wraps) to this same box rather than opening a second one
	// (#463), and both dialogs set a counted confirm label, which is longer than any
	// stand-in and is itself part of what has to fit.
	//
	// Two fleet sizes, because every one of those parts is count-dependent and the dialog
	// wraps: the message, the cap clause and the label all carry numbers that grow, so a
	// single small n measures the shortest form of a box whose length is the subject. The
	// large case is a three-digit host running a batch of the same order — the widest
	// these numbers realistically print.
	//
	// Measured, both currently render 13 lines: three digits do not push this copy over a
	// wrap boundary. The case is here so that stays a measurement rather than an
	// assumption, since the next edit to any of the three parts can change it.
	for _, f := range []struct {
		name           string
		limit, live, n int
	}{
		{"small fleet", 4, 2, 3},
		{"large fleet", 128, 99, 112},
	} {
		t.Run(f.name, func(t *testing.T) {
			runConfirmationHeightCase(t, minWidth, minHeight, f.limit, f.live, f.n)
		})
	}
}

func runConfirmationHeightCase(t *testing.T, minWidth, minHeight, limit, live, n int) {
	t.Helper()
	cases := map[string]struct{ msg, label string }{
		"pause": {
			pauseConfirmMessage("active", n),
			fmt.Sprintf("pause %d session%s", n, plural(n)),
		},
		"resume": {
			resumeConfirmMessage("paused", n) + "\n" + resumeCapClause(limit, live, n),
			fmt.Sprintf("resume %d session%s", n, plural(n)),
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			h := newCreateFormHome(t)
			h.state = stateConfirm
			h.confirmationOverlay = overlay.NewConfirmationOverlay(tc.msg)
			h.confirmationOverlay.SetConfirmLabel(tc.label)
			h.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: minWidth, Height: minHeight})

			// The consequence first, then the mechanism, and neither fatal: a require
			// on the height would abort the subtest and the failure output would never
			// name what the user actually loses.
			//
			// The box's own bottom border, not the hint text: at this width the hint
			// wraps mid-phrase ("…n or esc to" / "cancel"), so a contiguous match for it
			// reports a clip on a box that fits perfectly well — which is what the real
			// confirm label, longer than any stand-in, turned up. The border is the last
			// line of the box either way, and the bottom is the end PlaceOverlay clips,
			// so it goes before the hint does.
			box := strings.Split(xansi.Strip(h.confirmationOverlay.Render()), "\n")
			bottom := box[len(box)-1]
			// Contains("") is true of every string, so a render that came back empty —
			// a zero width reaching SetWidth, an overlay left unarmed — would satisfy
			// both assertions below while measuring nothing at all. Pin the line to the
			// shape of a real bottom border first, so this subtest cannot pass vacuously.
			require.NotEmpty(t, strings.TrimSpace(bottom), "the overlay rendered no box to measure")
			require.Greater(t, lipgloss.Width(bottom), minWidth/2,
				"the last line is too narrow to be the box's bottom border: %q", bottom)

			assert.Contains(t, xansi.Strip(h.View().Content), bottom,
				"the bottom of the confirmation was clipped away, taking the line that says how to answer it")
			assert.LessOrEqual(t, len(box), minHeight,
				"the confirmation box is taller than the smallest supported terminal, so it will be clipped")
		})
	}
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
