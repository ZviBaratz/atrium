package app

import (
	"context"
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/session"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"
)

// TestPollTargets covers the metadata-poll throttle decision: a full sweep polls every
// active session, while a light tick polls only the selected session and any session
// with a queued prompt (so prompt delivery stays responsive). Throttling the rest is
// what keeps the shared tmux server from being hammered every 500ms.
func TestPollTargets(t *testing.T) {
	newInst := func(prompt string) *session.Instance {
		inst, err := session.NewInstance(session.InstanceOptions{
			Title: "s", Path: t.TempDir(), Program: "claude",
		})
		require.NoError(t, err)
		inst.Prompt = prompt
		return inst
	}

	plain := newInst("")
	selected := newInst("")
	queued := newInst("do the thing")
	active := []*session.Instance{plain, selected, queued}

	t.Run("full sweep returns every active session", func(t *testing.T) {
		require.Equal(t, active, pollTargets(active, selected, true))
	})

	t.Run("light tick polls only selected and queued-prompt sessions", func(t *testing.T) {
		got := pollTargets(active, selected, false)
		require.ElementsMatch(t, []*session.Instance{selected, queued}, got)
		require.NotContains(t, got, plain, "a stable non-selected session is skipped on light ticks")
	})

	t.Run("light tick with no selection still polls queued prompts", func(t *testing.T) {
		require.Equal(t, []*session.Instance{queued}, pollTargets(active, nil, false))
	})
}

// TestSweepMetadataNowCmdGuards pins the cheap exit: the one-shot detach sweep is skipped
// only when there is no active session to refresh. The sweep now covers the selected row
// too (polled face-value), so a lone selected session still yields a sweep.
func TestSweepMetadataNowCmdGuards(t *testing.T) {
	newInst := func() *session.Instance {
		inst, err := session.NewInstance(session.InstanceOptions{
			Title: "s", Path: t.TempDir(), Program: "claude",
		})
		require.NoError(t, err)
		return inst
	}
	selected, other := newInst(), newInst()

	require.Nil(t, sweepMetadataNowCmd(context.Background(), nil, selected),
		"no active sessions yields no sweep")
	require.NotNil(t, sweepMetadataNowCmd(context.Background(), []*session.Instance{selected}, selected),
		"the selected row alone still yields a sweep (it is refreshed face-value)")
	require.NotNil(t, sweepMetadataNowCmd(context.Background(), []*session.Instance{selected, other}, selected),
		"active rows present yield a sweep")
}

// TestTickUpdateMetadataCmdHonorsContextCancellation pins the shutdown contract: the
// self-chaining metadata tick selects on ctx.Done() during its 500ms inter-tick wait, so
// a cancelled app context unblocks the in-flight poll Cmd promptly instead of leaving its
// goroutine parked for up to half a second. (The Cmd wg.Wait()s its per-instance fan-out,
// so its prompt return also proves those children unwound.)
func TestTickUpdateMetadataCmdHonorsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	// Empty active slice: the tick still runs its inter-tick wait before the len check,
	// which is exactly the cancellable sleep under test.
	cmd := tickUpdateMetadataCmd(ctx, nil, nil, true)

	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()

	cancel()

	select {
	case msg := <-done:
		require.IsType(t, metadataUpdateDoneMsg{}, msg)
	case <-time.After(200 * time.Millisecond):
		t.Fatal("tick did not unblock within 200ms of cancellation (sleep ignored ctx)")
	}
}
