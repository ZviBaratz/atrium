package app

import (
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui"
	"github.com/ZviBaratz/atrium/ui/overlay"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"
)

// testDiff is a minimal unified diff with annotatable code lines: one context
// row, one addition, and one deletion — enough for the comment cursor to land.
const testDiff = "diff --git a/foo.go b/foo.go\n" +
	"@@ -1,3 +1,3 @@\n" +
	" ctx\n" +
	"+add\n" +
	"-del\n"

// newDiffCommentHome builds a home that is already in stateDiffComment with the
// diff pane frozen on testDiff and the cursor on the first annotatable row. It
// mirrors the shape newSmokeHome targets — one selected instance, layout sized —
// so assertions on overlay, notice, and queue state work without further setup.
func newDiffCommentHome(t *testing.T) *home {
	t.Helper()

	inst, err := session.NewInstance(session.InstanceOptions{
		Title: "dc", Path: t.TempDir(), Program: "echo",
	})
	require.NoError(t, err)

	h := newHintsHome(t, inst)
	h.list.SelectInstance(inst)

	// Populate the diff pane with rows from raw diff content so EnterDiffComment
	// succeeds without a live git worktree.
	h.tabbedWindow.SetDiffContent(testDiff)
	ok := h.tabbedWindow.EnterDiffComment()
	require.True(t, ok, "testDiff must contain at least one annotatable row")

	h.state = stateDiffComment
	h.menu.SetState(ui.StateDiffComment)
	return h
}

// --- Gap 1: stateDiffComment in the background-message panic sweep ---
//
// TestStateMachine_DiffComment_BackgroundNeverPanics is a targeted extension of
// TestStateMachine_BackgroundMessagesNeverPanic: it feeds every async background
// message through Update while h.state == stateDiffComment and the diff pane is
// genuinely frozen (commenting == true), asserting neither panics nor nil-derefs.
// Background messages bypass handleDiffCommentState and should be invisible to the
// cursor state, but a misrouted previewTick or a nil-deref on the selected instance
// could slip through — this sweep guards the whole cross-product.
func TestStateMachine_DiffComment_BackgroundNeverPanics(t *testing.T) {
	messages := []struct {
		name string
		msg  tea.Msg
	}{
		{"WindowSizeMsg", tea.WindowSizeMsg{Width: 100, Height: 40}},
		{"previewTickMsg", previewTickMsg{}},
		{"metadataUpdateDoneMsg", metadataUpdateDoneMsg{}},
		{"metadataSweepDoneMsg", metadataSweepDoneMsg{}},
		{"smartDispatchDoneMsg", smartDispatchDoneMsg{}},
	}

	for _, mc := range messages {
		t.Run(mc.name, func(t *testing.T) {
			h := newDiffCommentHome(t)

			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("Update panicked in stateDiffComment on %s: %v", mc.name, r)
				}
			}()

			model, _ := h.Update(mc.msg)
			require.NotNil(t, model, "Update must always return a model")
		})
	}
}

// --- Gap 2: enterDiffComment guard — no session selected ---

// TestEnterDiffComment_NoSession_ShowsNotice checks the guard at the top of
// enterDiffComment: when no instance is selected, the function shows an info
// notice and stays in stateDefault rather than attempting to freeze an empty pane.
func TestEnterDiffComment_NoSession_ShowsNotice(t *testing.T) {
	h := newHintsHome(t) // no instances → GetSelectedInstance() == nil

	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'C'}})

	require.Equal(t, stateDefault, h.state)
	require.True(t, h.menu.HasNotice())
}

// --- Gap 3: enterDiffComment guard — session selected but no annotatable rows ---

// TestEnterDiffComment_NoDiffLines_ShowsNotice checks the second guard in
// enterDiffComment: when the selected session's diff has no code lines to anchor
// a comment to (the pane is still loading, empty, or chrome-only), EnterDiffComment
// returns false and the app shows a "no diff lines" notice without entering the mode.
// An unstarted instance triggers the same code path: UpdateDiff is a no-op and
// the pane's row slice stays nil, so EnterDiffComment declines.
func TestEnterDiffComment_NoDiffLines_ShowsNotice(t *testing.T) {
	inst := newBranchInstance(t, "s", "b") // not started → no diff rows in pane
	h := newHintsHome(t, inst)
	h.list.SelectInstance(inst)

	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'C'}})

	require.Equal(t, stateDefault, h.state)
	require.True(t, h.menu.HasNotice())
}

// --- Gap 6: cancelDiffComment — returns to stateDiffComment, not stateDefault ---

// TestDiffComment_Cancel_ReturnsToCommentCursor is the regression guard for the
// cancel invariant: pressing esc in the diff-comment composer must bring the user
// back to the line cursor (stateDiffComment), not all the way to the session list
// (stateDefault). The most likely regression would be accidentally routing cancel
// through the generic cancelPromptOverlay, which resets to stateDefault.
func TestDiffComment_Cancel_ReturnsToCommentCursor(t *testing.T) {
	h := newDiffCommentHome(t)

	// Open the composer.
	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	require.Equal(t, statePrompt, h.state)
	require.True(t, h.composingDiffComment, "composingDiffComment must be set when the overlay opens")

	// Cancel via esc.
	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyEsc})

	require.Equal(t, stateDiffComment, h.state, "cancel must return to the cursor, not the list")
	require.False(t, h.composingDiffComment)
	require.Nil(t, h.textInputOverlay)
}

// TestDiffComment_CtrlC_ReturnsToCommentCursor mirrors TestDiffComment_Cancel for
// the ctrl+c cancel path, which has a separate fast-exit in handleDiffCommentComposer.
func TestDiffComment_CtrlC_ReturnsToCommentCursor(t *testing.T) {
	h := newDiffCommentHome(t)

	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	require.Equal(t, statePrompt, h.state)

	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyCtrlC})

	require.Equal(t, stateDiffComment, h.state, "ctrl+c cancel must return to the cursor")
	require.False(t, h.composingDiffComment)
	require.Nil(t, h.textInputOverlay)
}

// --- Gap 4: submitDiffComment — valid note queues a follow-up and returns to cursor ---

// TestDiffComment_Submit_QueuesFollowupAndReturnsToCommentMode is the integration
// contract for the diff-comment submit path: a non-empty note must be queued on the
// selected instance (reusing the verified-delivery queue, same as quick-send), the
// state must return to stateDiffComment so the user can annotate the next line, and
// an ack notice must flash. It mirrors TestQuickSendQueuesPromptForVerifiedDelivery
// for the diff-comment-specific flow.
func TestDiffComment_Submit_QueuesFollowupAndReturnsToCommentMode(t *testing.T) {
	h := newDiffCommentHome(t)

	st, err := session.NewStorage(config.DefaultState())
	require.NoError(t, err)
	h.storage = st

	// Open the composer on the cursor's current line.
	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	require.Equal(t, statePrompt, h.state)
	require.True(t, h.composingDiffComment)

	// Pre-fill a note and submit.
	h.textInputOverlay.SetPrompt("handle the nil case")
	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})

	inst := h.list.GetSelectedInstance()
	require.NotEmpty(t, inst.Prompt(), "the note must be queued for agent delivery")
	require.Equal(t, stateDiffComment, h.state, "submit must return to the cursor, not the session list")
	require.False(t, h.composingDiffComment)
	require.True(t, h.menu.HasNotice(), "a successful submit must flash an ack notice")
	require.Nil(t, h.textInputOverlay, "the composer overlay must close on submit")
}

// --- Gap 5: submitDiffComment — empty note queues nothing ---

// TestDiffComment_EmptyNote_QueuesNothing guards the empty-note guard in
// submitDiffComment: a blank note (whitespace-only or zero-length) must not call
// QueueFollowupPrompt, and must instead show an "empty comment — nothing queued"
// notice so the user knows the submission was a no-op.
func TestDiffComment_EmptyNote_QueuesNothing(t *testing.T) {
	h := newDiffCommentHome(t)

	// Wire in an overlay the way openDiffCommentComposer would: submitOnEnter=true
	// so a bare Enter submits even with empty content.
	h.textInputOverlay = overlay.NewQuickSendOverlay("Comment on foo.go:1")
	h.composingDiffComment = true
	h.state = statePrompt

	// Submit without typing anything.
	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})

	inst := h.list.GetSelectedInstance()
	require.Empty(t, inst.Prompt(), "an empty note must not be queued")
	require.Equal(t, stateDiffComment, h.state, "empty-note submit still returns to the cursor")
	require.True(t, h.menu.HasNotice(), "empty submit must show an explanatory notice")
}
