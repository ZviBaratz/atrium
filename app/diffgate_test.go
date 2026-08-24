package app

import (
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/git"

	"github.com/stretchr/testify/require"
)

// diffContentDue decides whether a background session pays for `git ls-files
// --others` plus `git diff --numstat` this sweep — measured at ~16ms of CPU per
// session per sweep, which at a 14-session fleet was the single largest subprocess
// cost (#546).
//
// Every row here is a claim about when a tree can have changed. The "false" rows
// are the whole point of the change; the "true" rows are what keeps it honest.
//
// The floor is a parameter since #799 (config.GetDiffRefreshSeconds). This table drives
// the built-in default so its rows keep saying what they said; that the parameter is
// honoured at all is TestDiffContentDueHonoursTheConfiguredFloor's claim.
func TestDiffContentDue(t *testing.T) {
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	floor := time.Duration(config.DefaultDiffRefreshSeconds()) * time.Second

	for _, tc := range []struct {
		name            string
		status          session.Status
		statusChangedAt time.Time
		contentAt       time.Time
		want            bool
	}{
		{
			name: "never computed", status: session.Ready,
			statusChangedAt: ago(time.Minute), contentAt: time.Time{}, want: true,
		},
		{
			name: "running agent may be writing right now", status: session.Running,
			statusChangedAt: ago(time.Minute), contentAt: ago(time.Second), want: true,
		},
		{
			name: "loading session is still being set up", status: session.Loading,
			statusChangedAt: ago(time.Minute), contentAt: ago(time.Second), want: true,
		},
		{
			// #290: the main turn ended but a background sub-agent is still working,
			// so the tree is still moving even though the row reads "not running".
			name: "pending sub-agent is still autonomous work", status: session.Pending,
			statusChangedAt: ago(time.Minute), contentAt: ago(time.Second), want: true,
		},
		{
			// The Running→Ready edge: the agent's final write just landed and the
			// chip matters most here.
			name: "status changed since the last computation", status: session.Ready,
			statusChangedAt: ago(time.Second), contentAt: ago(2 * time.Second), want: true,
		},
		{
			name: "restored instance has never stamped a status", status: session.Ready,
			statusChangedAt: time.Time{}, contentAt: ago(time.Second), want: true,
		},
		{
			name: "idle beyond the floor", status: session.Ready,
			statusChangedAt: ago(time.Hour), contentAt: ago(floor + time.Second), want: true,
		},
		{
			// The win: a settled session polled again a moment later.
			name: "idle within the floor", status: session.Ready,
			statusChangedAt: ago(time.Hour), contentAt: ago(time.Second), want: false,
		},
		{
			name: "blocked on the user within the floor", status: session.NeedsInput,
			statusChangedAt: ago(time.Hour), contentAt: ago(time.Second), want: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := diffContentDue(tc.status, tc.statusChangedAt, tc.contentAt, now, floor)
			require.Equal(t, tc.want, got)
		})
	}
}

// A skipped result must not blank the row's chip.
//
// The stats a gated sweep returns carry zero line counts because they were never
// asked for, not because the tree is clean. Storing them verbatim would flip every
// idle session's "+42 −7" to nothing on the very next tick — a visible lie, and the
// defect this carry-forward exists to prevent.
func TestApplyDiffStats_SkippedContentKeepsThePreviousCounts(t *testing.T) {
	inst := newDiffGateInstance(t)
	inst.SetDiffStats(&git.DiffStats{Added: 42, Removed: 7, FilesChanged: 3, Content: "diff --git a/x b/x"})

	applyDiffStats(inst, &git.DiffStats{Dirty: true, Unpushed: 2}, true)

	got := inst.GetDiffStats()
	require.Equal(t, 42, got.Added, "line counts must survive a skipped sweep")
	require.Equal(t, 7, got.Removed)
	require.Equal(t, 3, got.FilesChanged)
	require.Equal(t, "diff --git a/x b/x", got.Content)
}

// ...and it must still apply the numbers it DID compute.
//
// This is the safety half of the split: Dirty and Unpushed are what the kill
// confirmation turns into "has uncommitted changes and N unpushed commits" before
// deleting a branch. They are recomputed on every sweep precisely so gating the
// diff cannot make a branch look safe to delete.
func TestApplyDiffStats_SkippedContentStillRefreshesTheKillWarningNumbers(t *testing.T) {
	inst := newDiffGateInstance(t)
	inst.SetDiffStats(&git.DiffStats{Added: 42, Dirty: false, Unpushed: 0})

	applyDiffStats(inst, &git.DiffStats{Dirty: true, Unpushed: 2}, true)

	got := inst.GetDiffStats()
	require.True(t, got.Dirty, "Dirty reaches a destructive prompt and must never be stale")
	require.Equal(t, 2, got.Unpushed, "Unpushed reaches a destructive prompt and must never be stale")
}

// The content clock advances only when content was actually computed — that is what
// closes the loop, since diffContentDue reads it to decide the next sweep.
func TestApplyDiffStats_StampsTheContentClockOnlyWhenContentWasComputed(t *testing.T) {
	t.Run("a full result stamps it", func(t *testing.T) {
		inst := newDiffGateInstance(t)
		require.True(t, inst.DiffContentAt().IsZero(), "precondition: never computed")

		applyDiffStats(inst, &git.DiffStats{Added: 1}, false)

		require.False(t, inst.DiffContentAt().IsZero(), "a computed diff must stamp the clock")
	})

	t.Run("a skipped result leaves it where it was", func(t *testing.T) {
		inst := newDiffGateInstance(t)
		applyDiffStats(inst, &git.DiffStats{Added: 1}, false)
		stamped := inst.DiffContentAt()

		applyDiffStats(inst, &git.DiffStats{Dirty: true}, true)

		require.Equal(t, stamped, inst.DiffContentAt(),
			"a skipped sweep must not advance the clock, or the floor would never lapse")
	})
}

// An errored result still nils the stats, skipped or not — the pre-existing
// contract that a row shows no numbers rather than wrong ones.
func TestApplyDiffStats_ErrorStillNilsEvenWhenSkipped(t *testing.T) {
	inst := newDiffGateInstance(t)
	inst.SetDiffStats(&git.DiffStats{Added: 42})

	applyDiffStats(inst, &git.DiffStats{Error: errBoom}, true)

	require.Nil(t, inst.GetDiffStats())
}

var errBoom = &diffGateError{}

type diffGateError struct{}

func (*diffGateError) Error() string { return "boom" }

func newDiffGateInstance(t *testing.T) *session.Instance {
	t.Helper()
	inst, err := session.NewInstance(session.InstanceOptions{
		Title: "gated", Path: t.TempDir(), Program: "echo",
	})
	require.NoError(t, err)
	return inst
}
