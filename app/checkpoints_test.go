package app

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

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

// r re-reads rather than resuming a paused session, which is what `r` does in the
// default state — the prelude branch ordering is what makes that true.
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
