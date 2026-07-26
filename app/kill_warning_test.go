package app

import (
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/git"

	xansi "github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// flattenOverlay strips the confirmation overlay's styling, box-drawing and line
// wrapping so a test can assert on the logical message — or on the key hint, which
// is assembled from separately styled fragments — regardless of where Render wraps.
func flattenOverlay(s string) string {
	r := strings.NewReplacer("│", " ", "╭", " ", "╮", " ", "╰", " ", "╯", " ", "─", " ")
	return strings.Join(strings.Fields(r.Replace(xansi.Strip(s))), " ")
}

// TestKillDataWarning covers the kill-confirmation suffix across the four states of
// the branch: clean, dirty-only, unpushed-only (the paused auto-WIP case reads here,
// Dirty=false), and both. The count is pluralized and the consequence is spelled out
// so the danger is explicit. The count is the *unpushed* one — kill runs
// `git branch -D`, which only destroys commits that exist nowhere else.
func TestKillDataWarning(t *testing.T) {
	require.Equal(t, "", killDataWarning(false, 0))
	require.Equal(t, " (has uncommitted changes — deleting discards this work)", killDataWarning(true, 0))
	require.Equal(t, " (has 1 unpushed commit — deleting discards this work)", killDataWarning(false, 1))
	require.Equal(t, " (has 3 unpushed commits — deleting discards this work)", killDataWarning(false, 3))
	require.Equal(t,
		" (has uncommitted changes and 2 unpushed commits — deleting discards this work)",
		killDataWarning(true, 2))
}

// TestSessionsWithUnpushedWork verifies the batch-kill aggregate counts a session
// once whether it is dirty, holds unpushed commits, or both, and ignores clean,
// fully-pushed, and never-polled (nil-stats) sessions.
func TestSessionsWithUnpushedWork(t *testing.T) {
	mk := func(t *testing.T, stats *git.DiffStats) *session.Instance {
		inst, err := session.NewInstance(session.InstanceOptions{Title: "t", Path: t.TempDir(), Program: "echo"})
		require.NoError(t, err)
		if stats != nil {
			inst.SetDiffStats(stats)
		}
		return inst
	}

	insts := []*session.Instance{
		mk(t, &git.DiffStats{Dirty: true}),                          // dirty only
		mk(t, &git.DiffStats{Commits: 2, Unpushed: 2}),              // unpushed commits (e.g. paused WIP)
		mk(t, &git.DiffStats{Dirty: true, Commits: 1, Unpushed: 1}), // both — still one session
		mk(t, &git.DiffStats{Commits: 2, Unpushed: 0}),              // fully pushed — nothing at risk
		mk(t, &git.DiffStats{}),                                     // clean
		mk(t, nil),                                                  // never polled
	}

	require.Equal(t, 3, sessionsWithUnpushedWork(insts))
	require.Equal(t, 0, sessionsWithUnpushedWork(nil))
}

// TestConfirmKill_WarnsUnpushedCommits verifies the single-kill confirmation
// actually renders the at-risk warning, and that it names the *unpushed* count
// rather than the ahead-of-base one: a branch 3 commits ahead of base with only 1
// commit not yet on origin stands to lose exactly that 1.
func TestConfirmKill_WarnsUnpushedCommits(t *testing.T) {
	h := newCreateFormHome(t)
	h.windowWidth = 120
	inst := addActive(t, h, "alpha")
	inst.SetDiffStats(&git.DiffStats{Commits: 3, Unpushed: 1})

	h.confirmKill(inst)

	require.Equal(t, stateConfirm, h.state)
	require.NotNil(t, h.confirmationOverlay)
	require.Contains(t, flattenOverlay(h.confirmationOverlay.Render()), "has 1 unpushed commit — deleting discards this work")
}

// TestConfirmKill_SilentWhenAllCommitsPushed is the regression test for the bug
// this warning had: a session whose commits are all on origin (typically sitting in
// an open PR) was told its work would be discarded. Kill only runs `git branch -D`,
// which cannot touch a pushed commit, so the dialog must say nothing at all.
func TestConfirmKill_SilentWhenAllCommitsPushed(t *testing.T) {
	h := newCreateFormHome(t)
	h.windowWidth = 120
	inst := addActive(t, h, "alpha")
	// 2 commits ahead of base, both pushed — the real CORE-280 shape.
	inst.SetDiffStats(&git.DiffStats{Commits: 2, Unpushed: 0})

	h.confirmKill(inst)

	require.Equal(t, stateConfirm, h.state)
	require.NotNil(t, h.confirmationOverlay)
	rendered := flattenOverlay(h.confirmationOverlay.Render())
	require.Contains(t, rendered, "Kill session 'alpha'?")
	require.NotContains(t, rendered, "discards")
	require.NotContains(t, rendered, "unpushed")
}

// TestKillMarked_AggregatesUnpushedWork drives the real x-over-marked path and
// verifies the batch confirmation names how many of the marked sessions carry work
// that a delete would actually destroy (dirty or unpushed), counting each such
// session once. A marked session whose commits are all pushed must not be counted.
func TestKillMarked_AggregatesUnpushedWork(t *testing.T) {
	h := newCreateFormHome(t)
	h.windowWidth = 120
	a := addActive(t, h, "alpha")
	b := addActive(t, h, "bravo")
	c := addActive(t, h, "charlie")
	a.SetDiffStats(&git.DiffStats{Commits: 1, Unpushed: 1})
	b.SetDiffStats(&git.DiffStats{Dirty: true})
	// charlie (c) is 2 commits ahead of base but fully pushed — it is marked below
	// but has nothing at risk, so it must not be counted.
	c.SetDiffStats(&git.DiffStats{Commits: 2, Unpushed: 0})

	pressRune(h, 'v')
	h.list.ToggleMark(a)
	h.list.ToggleMark(b)
	h.list.ToggleMark(c)

	pressRune(h, 'x')

	require.Equal(t, stateConfirm, h.state)
	require.NotNil(t, h.confirmationOverlay)
	require.Contains(t, flattenOverlay(h.confirmationOverlay.Render()), "2 of 3 have unpushed work")
}
