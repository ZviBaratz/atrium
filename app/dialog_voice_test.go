package app

import (
	"fmt"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/keys"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/git"

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
	// The property the second size is here for is that the height does not move with the
	// count, and it is asserted rather than written down: each case returns the height it
	// measured per dialog, and the two are required to agree. An absolute number in a
	// comment would be a claim no test reads — and would be wrong per-dialog anyway, since
	// the two dialogs are not the same height as each other.
	heights := map[string]map[string]int{}
	for _, f := range []struct {
		name           string
		limit, live, n int
	}{
		{"small fleet", 4, 2, 3},
		{"large fleet", 128, 99, 112},
	} {
		t.Run(f.name, func(t *testing.T) {
			heights[f.name] = runConfirmationHeightCase(t, minWidth, minHeight, f.limit, f.live, f.n)
		})
	}
	require.Len(t, heights, 2, "both fleet sizes must have measured, or the comparison below is vacuous")
	for dialog, small := range heights["small fleet"] {
		assert.Equal(t, small, heights["large fleet"][dialog],
			"the %s dialog's height moved with the fleet size: a three-digit count crossed a wrap "+
				"boundary, so the box this test measures at one size is not the one users see at the other", dialog)
	}
}

// runConfirmationHeightCase renders both confirmations at one fleet size and returns the
// line count of each, keyed by dialog, so the caller can compare across sizes.
//
// The dialog is DRIVEN, not assembled: the fleet is arranged and the opening key is
// pressed, so what gets measured is the box pauseAll and resumeAll actually build. It
// used to be composed here out of the same functions the app calls, and that is a
// weaker thing than it looks — it measures the parts an author remembered. #798 added
// a part nobody remembered (the double-tap key's "(or ctrl+p)", eight cells into a
// hint that was already at the wrap column), and the composed fixture went on
// reporting a box one line shorter than the screen for as long as it took to render
// the real one and look at it. A drive cannot fall behind the code that way.
func runConfirmationHeightCase(t *testing.T, minWidth, minHeight, limit, live, n int) map[string]int {
	t.Helper()
	measured := map[string]int{}
	// live and n mean different things per dialog, which is why the arrangement is per
	// case rather than shared: pause acts on the n ACTIVE sessions in view, while
	// resume acts on n PAUSED ones and weighs them against the live population — so the
	// resume fleet is live + n sessions and the pause fleet is n.
	cases := map[string]func(h *home){
		"pause": func(h *home) {
			for i := 0; i < n; i++ {
				addActive(t, h, fmt.Sprintf("active-%d", i))
			}
			_, _ = h.handleKeyPress(keyMsg(keys.PrimaryKey(keys.KeyPauseAll)))
		},
		"resume": func(h *home) {
			h.hostCap = limit
			h.appConfig.MaxSessions = nil // unset → the host-derived soft cap
			for i := 0; i < live; i++ {
				addActive(t, h, fmt.Sprintf("live-%d", i))
			}
			for i := 0; i < n; i++ {
				addPaused(t, h, fmt.Sprintf("parked-%d", i))
			}
			_, _ = h.handleKeyPress(keyMsg(keys.PrimaryKey(keys.KeyResumeAll)))
		},
	}
	for name, arrange := range cases {
		t.Run(name, func(t *testing.T) {
			h := newCreateFormHome(t)
			h.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: minWidth, Height: minHeight})
			arrange(h)
			require.Equal(t, stateConfirm, h.state, "the %s dialog never opened", name)
			require.NotNil(t, h.confirmationOverlay)

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
			measured[name] = len(box)
		})
	}
	return measured
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
	// ConfirmSize's preferred 52 outer less the border and Padding(1,2) — six cells. Stated as a literal because
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

// The push question names the session it pushes FROM; the parenthetical names the two
// things confirming does that no row on screen shows. One of them is conditional, so
// the clause has two spellings and both are pinned here — the literal has one home
// (pushConsequenceClause) and this is it.
//
// The clean spelling is not the dirty one minus a phrase: a push always opens the
// branch in a browser, so the parenthetical is never empty. That is the difference
// from killDataWarning, whose whole clause disappears when nothing is at risk, and it
// is why "no clause on a clean worktree" would be the wrong assertion to write here.
func TestPushConsequenceClause(t *testing.T) {
	require.Equal(t,
		" (commits your uncommitted work first, then opens the branch in your browser)",
		pushConsequenceClause(true))
	require.Equal(t, " (opens the branch in your browser)", pushConsequenceClause(false))
}

// A copy change is a height change, and #469 grew this dialog from one line to three.
// PlaceOverlay CLIPS rather than overflows, so the cost of one line too many is the
// bottom border and the "Press y …" line under it — the only thing telling the user
// how to answer the box.
//
// Both spellings, because the conditional clause is the taller one and a guard that
// only ever renders the clean case would prove nothing about the case the change was
// made for.
//
// What it does NOT claim is a worst case. The session name is user-authored and
// nothing caps it, so the message has no provable ceiling and no fixture can reach the
// width that breaks it — this measures a long realistic name, which is the discipline
// the surrounding dialogs already accept (kill names a session too). The property
// asserted is that the box the app renders TODAY fits the smallest supported terminal.
//
// Fitting 24 rows is a loose bound on its own — this box is nine — so the two heights
// are also compared against each other. The design claim being pinned is that the
// conditional half costs AT MOST ONE rendered line, which is what lets the dirty case
// share the clean case's geometry instead of needing a width ladder of its own. A
// reworded clause that quietly costs three is the realistic regression, and the
// absolute bound alone would not notice it.
func TestPushConfirmationFitsTheSmallestSupportedTerminal(t *testing.T) {
	const minWidth, minHeight = 80, 24
	heights := map[bool]int{}
	for _, dirty := range []bool{true, false} {
		t.Run(fmt.Sprintf("dirty=%v", dirty), func(t *testing.T) {
			h := newCreateFormHome(t)
			inst := addActive(t, h, "refactor-the-notice-ladder")
			inst.SetDiffStats(&git.DiffStats{Dirty: dirty})
			h.list.SetSelectedInstance(0)
			h.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: minWidth, Height: minHeight})

			_, _ = h.pushSelected()
			require.NotNil(t, h.confirmationOverlay)

			box := strings.Split(xansi.Strip(h.confirmationOverlay.Render()), "\n")
			bottom := box[len(box)-1]
			// Contains("") is true of every string, so pin the line to the shape of a
			// real bottom border first — otherwise an overlay that rendered nothing at
			// all would satisfy the assertions below while measuring nothing.
			require.NotEmpty(t, strings.TrimSpace(bottom), "the overlay rendered no box to measure")
			require.Greater(t, lipgloss.Width(bottom), minWidth/2,
				"the last line is too narrow to be the box's bottom border: %q", bottom)

			assert.Contains(t, xansi.Strip(h.View().Content), bottom,
				"the bottom of the push confirmation was clipped away, taking the line that says how to answer it")
			assert.LessOrEqual(t, len(box), minHeight,
				"the push confirmation is taller than the smallest supported terminal")
			heights[dirty] = len(box)
		})
	}
	require.Len(t, heights, 2, "both spellings must have measured, or the comparison below is vacuous")
	assert.LessOrEqual(t, heights[true], heights[false]+1,
		"the conditional clause costs more than one rendered line, so the dirty push dialog "+
			"is no longer the same box shape as the clean one")
}

// The dirty half is conditional on the CACHED diff stats, which is what makes it free
// on the UI thread (#469): the same poll-maintained snapshot killDataWarning reads.
// Three states, because the third is the one an if-nil-then-dirty slip would get
// wrong: dirty, clean, and never-polled — and a session Atrium has not measured must
// not be told its work is about to be committed.
func TestPushConfirm_NamesTheCommitOnlyWhenThereIsOneToMake(t *testing.T) {
	const commitClause = "commits your uncommitted work first"
	const browserClause = "opens the branch in your browser"

	open := func(t *testing.T, stats *git.DiffStats) string {
		t.Helper()
		h := newCreateFormHome(t)
		inst := addActive(t, h, "alpha")
		if stats != nil {
			inst.SetDiffStats(stats)
		}
		h.list.SetSelectedInstance(0)

		_, _ = h.pushSelected()

		require.Equal(t, stateConfirm, h.state)
		require.NotNil(t, h.confirmationOverlay)
		rendered := flattenOverlay(h.confirmationOverlay.Render())
		require.Contains(t, rendered, "Push changes from session 'alpha'?")
		// The verb label, unchanged by #469: the hint says what y does rather than the
		// generic "confirm".
		require.Contains(t, rendered, "to push, n or esc to cancel")
		return rendered
	}

	t.Run("uncommitted work names the commit", func(t *testing.T) {
		rendered := open(t, &git.DiffStats{Dirty: true})
		assert.Contains(t, rendered, commitClause)
		assert.Contains(t, rendered, browserClause)
	})

	t.Run("a clean worktree does not", func(t *testing.T) {
		rendered := open(t, &git.DiffStats{Dirty: false})
		assert.NotContains(t, rendered, commitClause,
			"a push that commits nothing must not warn about a commit")
		assert.Contains(t, rendered, browserClause,
			"the browser tab opens either way, so it is named either way")
	})

	t.Run("an unpolled session does not", func(t *testing.T) {
		rendered := open(t, nil)
		assert.NotContains(t, rendered, commitClause,
			"never measured is not the same as dirty")
		assert.Contains(t, rendered, browserClause)
	})
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
	require.Contains(t, rendered, "Press y (or c) to create the PR, n or esc to cancel")
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
			"Press y (or ctrl+p) to pause 2 sessions, n or esc to cancel")
	})

	t.Run("resume", func(t *testing.T) {
		h := newCreateFormHome(t)
		addPaused(t, h, "alpha")

		_ = h.resumeAll()

		require.Equal(t, stateConfirm, h.state)
		require.Contains(t, flattenOverlay(h.confirmationOverlay.Render()),
			"Press y (or ctrl+r) to resume 1 session, n or esc to cancel")
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
