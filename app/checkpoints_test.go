package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/internal/testutil"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/transcript"

	xansi "github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// checkpointHome is a sized home whose selected session looks like claude, which is
// the only kind the timeline opens for.
func checkpointHome(t *testing.T) (*home, *session.Instance) {
	t.Helper()
	h := newFrameHome(t, 120, 40)
	inst := h.list.GetSelectedInstance()
	require.NotNil(t, inst)
	inst.Program = "claude"
	return h, inst
}

// The surface is invisible for agents that keep no checkpoints: no overlay, no
// state change, and — the part a screen assertion would miss — no read either.
func TestOpenCheckpoints_NonClaudeOpensNothing(t *testing.T) {
	h := newFrameHome(t, 120, 40) // its session runs "echo"

	_, cmd := h.openCheckpoints()

	assert.Equal(t, stateDefault, h.state, "a non-claude session must not open the timeline")
	assert.Nil(t, h.checkpointOverlay)
	assert.Nil(t, h.checkpointTarget)

	// A dead key is worse than a refusal: nothing on screen says which agent a row
	// runs, so the press has to answer. flashNotice sets the notice inline and
	// returns only the command that later hides it again, so this reads the bar as
	// it is now rather than running that command — which would expire it.
	require.NotNil(t, cmd, "it should still schedule the notice's expiry")
	require.True(t, h.menu.HasNotice(), "pressing H on a non-claude session must say something")
	assert.Contains(t, xansi.Strip(h.menu.String()), "Claude Code feature")
}

func TestOpenCheckpoints_OpensLoadingForClaude(t *testing.T) {
	h, inst := checkpointHome(t)

	_, cmd := h.openCheckpoints()

	assert.Equal(t, stateCheckpoints, h.state)
	require.NotNil(t, h.checkpointOverlay)
	assert.Same(t, inst, h.checkpointTarget)
	require.NotNil(t, cmd, "opening must start the read")
	assert.Contains(t, xansi.Strip(h.checkpointOverlay.Render()), "reading transcript",
		"the box opens in its loading state — the read is async, so there is never data yet")
}

// A result for a session the user has navigated away from is dropped. The read is a
// whole-transcript scan, so a slow one landing late is the ordinary case, not an
// exotic one.
func TestHandleCheckpointsLoaded_DropsAStaleResult(t *testing.T) {
	h, _ := checkpointHome(t)
	h.openCheckpoints()

	other, err := session.NewInstance(session.InstanceOptions{
		Title: "other", Path: t.TempDir(), Program: "claude",
	})
	require.NoError(t, err)

	h.handleCheckpointsLoaded(checkpointsLoadedMsg{
		target: other,
		result: transcript.Checkpoints{List: []transcript.Checkpoint{{MessageID: "x", Label: "not mine"}}},
	})

	assert.NotContains(t, xansi.Strip(h.checkpointOverlay.Render()), "not mine",
		"a result for another session must not land in this timeline")
}

// The two failure kinds must not share a sentence: telling the user a transcript
// does not exist when the read of it failed sends them looking for the wrong thing,
// and `r` repeats it.
func TestHandleCheckpointsLoaded_SeparatesAbsenceFromFailure(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"absent", fmt.Errorf("%w: nothing there", transcript.ErrNoTranscript), "no transcript for this session yet"},
		{"failed", errors.New("bufio.Scanner: token too long"), "could not read"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, inst := checkpointHome(t)
			h.openCheckpoints()

			h.handleCheckpointsLoaded(checkpointsLoadedMsg{target: inst, err: tc.err})

			assert.Contains(t, xansi.Strip(h.checkpointOverlay.Render()), tc.want)
		})
	}
}

// An empty enumeration is a legitimate answer — an older claude, checkpointing
// switched off, or a session that has not been checkpointed yet — so it reads as a
// statement rather than an error.
func TestHandleCheckpointsLoaded_EmptyListExplainsItself(t *testing.T) {
	h, inst := checkpointHome(t)
	h.openCheckpoints()

	h.handleCheckpointsLoaded(checkpointsLoadedMsg{target: inst, result: transcript.Checkpoints{Blobs: true}})

	assert.Contains(t, xansi.Strip(h.checkpointOverlay.Render()), "no checkpoints recorded")
}

// Rows are pushed newest-first, because the checkpoint a user wants is nearly
// always a recent one and the cursor opens on the first row.
func TestHandleCheckpointsLoaded_NewestRowFirst(t *testing.T) {
	h, inst := checkpointHome(t)
	h.openCheckpoints()
	at := func(minute int) time.Time { return time.Date(2026, 8, 5, 10, minute, 0, 0, time.UTC) }

	h.handleCheckpointsLoaded(checkpointsLoadedMsg{target: inst, result: transcript.Checkpoints{
		Blobs: true,
		List: []transcript.Checkpoint{ // reader order: oldest first
			{MessageID: "a", At: at(0), Label: "oldest prompt"},
			{MessageID: "b", At: at(30), Label: "newest prompt"},
		},
	}})

	out := xansi.Strip(h.checkpointOverlay.Render())
	newest := strings.Index(out, "newest prompt")
	oldest := strings.Index(out, "oldest prompt")
	require.Positive(t, newest)
	require.Positive(t, oldest)
	assert.Less(t, newest, oldest, "the newest checkpoint must be the first row")
	assert.Equal(t, 0, h.checkpointOverlay.SelectedIndex(), "and the cursor opens on it")
}

// The retention caveat is attached only when claude has actually swept the backups,
// so the common case carries no standing warning.
func TestHandleCheckpointsLoaded_NoteOnlyWhenBackupsAreGone(t *testing.T) {
	rows := []transcript.Checkpoint{{MessageID: "a", Label: "a prompt", Files: 2}}

	h, inst := checkpointHome(t)
	h.openCheckpoints()
	h.handleCheckpointsLoaded(checkpointsLoadedMsg{target: inst, result: transcript.Checkpoints{Blobs: true, List: rows}})
	assert.NotContains(t, xansi.Strip(h.checkpointOverlay.Render()), "swept")

	h2, inst2 := checkpointHome(t)
	h2.openCheckpoints()
	h2.handleCheckpointsLoaded(checkpointsLoadedMsg{target: inst2, result: transcript.Checkpoints{Blobs: false, List: rows}})
	assert.Contains(t, xansi.Strip(h2.checkpointOverlay.Render()), "swept",
		"a listed checkpoint whose backups are gone must say so")
}

// esc returns to the list and drops the target with the overlay.
func TestHandleCheckpointsState_EscDismisses(t *testing.T) {
	h, _ := checkpointHome(t)
	h.openCheckpoints()

	h.handleCheckpointsState(keyMsg("esc"))

	assert.Equal(t, stateDefault, h.state)
	assert.Nil(t, h.checkpointOverlay)
	assert.Nil(t, h.checkpointTarget)
}

// r re-reads rather than resuming a paused session, which is what `r` does in
// the default state — stateCheckpoints' keys entry in surfaceSpecs routing the
// press here before the global dispatch is what makes that true.
// TestCheckpointsKeyRoutesThroughUpdate below is the test that proves the
// routing; this one pins the handler's own behavior.
func TestHandleCheckpointsState_RReloads(t *testing.T) {
	h, inst := checkpointHome(t)
	h.openCheckpoints()
	h.handleCheckpointsLoaded(checkpointsLoadedMsg{target: inst, result: transcript.Checkpoints{
		Blobs: true, List: []transcript.Checkpoint{{MessageID: "a", Label: "a prompt"}},
	}})

	_, cmd := h.handleCheckpointsState(keyMsg("r"))

	assert.Equal(t, stateCheckpoints, h.state, "r must not leave the timeline")
	require.NotNil(t, cmd, "r must start another read")
	assert.Contains(t, xansi.Strip(h.checkpointOverlay.Render()), "reading transcript",
		"and put the box back into its loading state")
}

// TestCheckpointsKeyRoutesThroughUpdate presses r through home.Update where the
// sibling tests above call handleCheckpointsState directly. The direct calls
// cannot see the routing: stateCheckpoints' keys entry in surfaceSpecs is the
// only thing sending a keypress to this surface, and a re-route can survive
// every other suite — a cross-wire to a handler that dereferences its own
// overlay on entry panics in the frame-restore walk, but
// handleImagePreviewState touches no overlay and its one gesture closes
// nil-tolerantly, so wiring the timeline's keys to it keeps that walk green
// with the timeline dead.
//
// The state assertion discriminates neither mutation — r leaves the state in
// stateCheckpoints under both, since a swallow changes nothing and the global
// dispatch's resume refuses a running session with a notice. Mutation-verified,
// one killing assertion each: with keys pointed at handleImagePreviewState, r
// returns no cmd and the require below aborts there; with keys nil, the
// dispatch's notice is a cmd, so what fails is the render assertion — the box
// never re-enters its loading state (the frame-restore walk fails that mutant
// independently, on esc no longer closing the timeline).
func TestCheckpointsKeyRoutesThroughUpdate(t *testing.T) {
	h, inst := checkpointHome(t)
	h.openCheckpoints()
	h.handleCheckpointsLoaded(checkpointsLoadedMsg{target: inst, result: transcript.Checkpoints{
		Blobs: true, List: []transcript.Checkpoint{{MessageID: "a", Label: "a prompt"}},
	}})

	_, cmd := h.Update(keyMsg("r"))

	assert.Equal(t, stateCheckpoints, h.state, "r must not leave the timeline")
	require.NotNil(t, cmd, "r must start another read")
	assert.Contains(t, xansi.Strip(h.checkpointOverlay.Render()), "reading transcript",
		"and put the box back into its loading state")
}

// startedClaudeSession adds a session to the list that Started() reports true for,
// which needs a real tmux session: NewInstance alone cannot reach it, and
// ContextSourceKey — the whole basis of the ambiguity check — returns "" until it
// does, so an unstarted fixture makes that check a no-op rather than a test of it.
//
// It starts a harmless program and relabels it claude afterwards, the same trick
// checkpointHome uses: the checkpoint surface reads Program (for the claude gate and
// for the project-dir derivation) and never runs it, so launching a real agent would
// buy nothing and cost a billable turn.
func startedClaudeSession(t *testing.T, h *home, title, path string) *session.Instance {
	t.Helper()
	inst, err := session.NewInstance(session.InstanceOptions{
		Title: title, Path: path, Program: "sleep 300", Direct: true,
	})
	require.NoError(t, err)
	inst.SetBaseContext(context.Background())
	require.NoError(t, inst.Start(true))
	t.Cleanup(func() {
		inst.RebindBaseContext(context.Background())
		_ = inst.Kill()
	})
	require.True(t, inst.Started(), "the fixture must be started for ContextSourceKey to resolve")
	inst.Program = "claude"
	h.list.AddInstance(inst)()
	return inst
}

// Two sessions whose claude project directory is the same one cannot have their
// checkpoints told apart: the enumeration resolves by newest mtime within that
// directory, so the timeline would head one session's name over another's history —
// and its one action walks the user into that session to rewind files off the list.
//
// This is the only test that exercises the check at all. ContextSourceKey is empty
// for an unstarted instance, so every cheaper fixture in this file takes the
// early return and never reaches the fleet scan.
func TestOpenCheckpoints_RefusesAnAmbiguousProjectDirectory(t *testing.T) {
	testutil.RequireTmux(t)
	h := newFrameHome(t, 120, 40)
	shared := t.TempDir()

	first := startedClaudeSession(t, h, "shared-one", shared)
	startedClaudeSession(t, h, "shared-two", shared)
	// NotEmpty as well as Equal: two unstarted instances both key "" and compare
	// equal, which is exactly the vacuous fixture this test exists to avoid. Without
	// it the collision assertion would pass while the guard was never reached.
	require.NotEmpty(t, first.ContextSourceKey(), "the fixture must resolve a project dir")
	require.Equal(t, first.ContextSourceKey(), h.list.GetInstances()[2].ContextSourceKey(),
		"the fixture must actually collide, or this asserts nothing")
	h.list.SetSelectedInstance(1)
	require.Same(t, first, h.list.GetSelectedInstance())

	_, cmd := h.openCheckpoints()

	assert.Equal(t, stateDefault, h.state, "an ambiguous source must not open a timeline")
	assert.Nil(t, h.checkpointOverlay)
	assert.Nil(t, h.checkpointTarget)
	require.NotNil(t, cmd, "and it must say why rather than dying quietly")
	require.True(t, h.menu.HasNotice())
	assert.Contains(t, xansi.Strip(h.menu.String()), "shares this one's claude project directory")

	// The control, and the reason the check is a fleet scan of keys rather than a
	// count of sessions: a session on its own directory opens as normal even with
	// the colliding pair still in the list.
	own := startedClaudeSession(t, h, "alone", t.TempDir())
	h.list.SetSelectedInstance(3)
	require.Same(t, own, h.list.GetSelectedInstance())

	_, cmd = h.openCheckpoints()

	assert.Equal(t, stateCheckpoints, h.state, "an unshared source must still open")
	require.NotNil(t, h.checkpointOverlay)
	assert.Same(t, own, h.checkpointTarget)
	require.NotNil(t, cmd)
}

// Blobs is the existence of the file-history directory, and Claude creates it on
// the first file it backs up — so a session that has touched no files has no
// directory and never had one. Reading that as a sweep would put a standing
// data-loss warning on a session whose checkpointing is working fine.
func TestHandleCheckpointsLoaded_NoSweepNoticeForASessionThatTouchedNoFiles(t *testing.T) {
	h, inst := checkpointHome(t)
	h.openCheckpoints()
	h.handleCheckpointsLoaded(checkpointsLoadedMsg{target: inst, result: transcript.Checkpoints{
		Blobs: false,
		List:  []transcript.Checkpoint{{MessageID: "a", Label: "a prompt", Files: 0}},
	}})

	assert.NotContains(t, xansi.Strip(h.checkpointOverlay.Render()), "swept",
		"nothing was backed up, so nothing was swept")
}

// A refused attach leaves the timeline standing. Tearing it down first would cost
// the user the list they were reading — and a second whole-transcript scan to get
// it back — for an action that never happened. attachSelected orders it the same
// way: every precondition before anything irreversible.
func TestHandleCheckpointsState_RefusedAttachKeepsTheTimeline(t *testing.T) {
	h, inst := checkpointHome(t)
	h.openCheckpoints()
	h.handleCheckpointsLoaded(checkpointsLoadedMsg{target: inst, result: transcript.Checkpoints{
		Blobs: true, List: []transcript.Checkpoint{{MessageID: "a", Label: "a prompt"}},
	}})
	// A session whose pane is gone: the attach cannot happen, and openCheckpoints
	// deliberately admits it, since the transcript outlives the terminal.
	inst.SetStatus(session.Paused)

	h.handleCheckpointsState(keyMsg("enter"))

	assert.Equal(t, stateCheckpoints, h.state, "a refused attach must not close the timeline")
	require.NotNil(t, h.checkpointOverlay)
	assert.Same(t, inst, h.checkpointTarget)
	// Behind an overlay a notice falls back to the errBox row, which the centred
	// box does not cover — so the refusal is both spoken and visible.
	require.True(t, h.errBox.HasContent(), "and it must say why")
	assert.Contains(t, xansi.Strip(h.errBox.String()), "paused")
}

// Closing the box cancels the read it started. The enumeration is an unbounded
// whole-file scan, so its lifetime belongs to the overlay that asked for it — the
// app context would keep it decoding JSON for a result nothing will ever use.
func TestCheckpointRead_IsCancelledWithTheOverlay(t *testing.T) {
	h, _ := checkpointHome(t)
	h.openCheckpoints()
	require.NotNil(t, h.checkpointCancel, "opening must own a cancellable read")

	h.handleCheckpointsState(keyMsg("esc"))

	assert.Nil(t, h.checkpointCancel, "dismissing must cancel and drop it")
}
