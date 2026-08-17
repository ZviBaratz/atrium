package tmux

import (
	"context"
	"fmt"
	"math/rand"
	"testing"

	"github.com/ZviBaratz/atrium/internal/testutil"
	"github.com/stretchr/testify/require"
)

// TestCloseSucceedsAfterRebindFromCancelledContext proves the tmux half of the
// #282 rebind: a Close issued under a cancelled lifecycle context can't kill the
// session (kill-session is insta-killed by the cancellation), leaving an orphaned
// tmux session on the dedicated socket; rebinding to a context.WithoutCancel
// context lets Close actually terminate it.
//
// Drives a REAL tmux server, so it self-skips when tmux is unavailable. Unlike
// TestSessionDeathStopsProbing it runs in CI — every workflow command that runs
// the suite passes `-skip` for that one and none names this one, so nothing in
// the workflows keeps this test off a real server under ATRIUM_CI_REQUIRE_TMUX=1.
// TestEveryWorkflowGoTestSkipsTheRealTmuxTest holds both halves of that to the
// workflow files, because the pair of line numbers this sentence used to carry
// was wrong on the day it merged and short of the skip sites it pointed at.
func TestCloseSucceedsAfterRebindFromCancelledContext(t *testing.T) {
	testutil.RequireTmux(t)

	ctx, cancel := context.WithCancel(context.Background())
	name := fmt.Sprintf("rebind-%s-%d", t.Name(), rand.Int31())
	session := NewSession(ctx, name, "sleep 300")
	require.NoError(t, session.Start(t.TempDir()))
	// Belt-and-suspenders teardown on a live context so a mid-test failure can't
	// leave a real session behind on the socket.
	t.Cleanup(func() {
		session.SetBaseContext(context.Background())
		_ = session.Close()
	})

	// Probe liveness on a fresh context — the session's own DoesSessionExist runs
	// on baseCtx, which we deliberately cancel below.
	alive := func() bool {
		return tmuxCommand(context.Background(), "has-session", fmt.Sprintf("-t=%s", session.sanitizedName)).Run() == nil
	}
	require.True(t, alive(), "session should be alive after Start")

	// Signal shutdown cancels the lifecycle context. Close's kill-session runs on
	// the cancelled ctx and is insta-killed, so the session survives — the orphan.
	cancel()
	_ = session.Close()
	require.True(t, alive(), "precondition for #282: Close under a cancelled ctx should leave the session alive")

	// Rebind to a survivable context; Close now actually kills the session.
	session.SetBaseContext(context.WithoutCancel(ctx))
	require.NoError(t, session.Close())
	require.False(t, alive(), "Close after rebind should kill the session")
}
