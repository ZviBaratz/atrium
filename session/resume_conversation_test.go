package session

import (
	"context"
	"fmt"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ZviBaratz/atrium/cmd/cmd_test"
	"github.com/ZviBaratz/atrium/session/tmux"
)

// The undo restore tells the user whether the agent came back into its old
// conversation or started over. Only startResuming knows: it asks the transcript
// adapter and then elects `--continue` or a blank launch, and until now it threw
// that answer away behind an `error` return. These pin the answer it records.

// relaunchableInstance is an instance whose tmux session is gone, so any resume
// path relaunches the agent through startResuming. cfgDir is claude's data root —
// write a transcript into it to make the conversation resumable, or leave it empty
// to make it not.
//
// goneFor is how many leading has-session probes must report the session as gone
// before it comes up. Each entry point asks a different number of times:
// recoverInPlace asks once (the duplicate-name guard in Start), Resume asks twice
// (its own "did the session survive the pause?" check, then that same guard). One
// too few and the launch is refused as a duplicate; the probe after them all has
// to see the session alive, or the launch is judged to have failed.
func relaunchableInstance(t *testing.T, program, cfgDir string, goneFor int) *Instance {
	t.Helper()
	wt := newTestWorktree(t)
	pty := newRecordingPtyFactory(t, nil)
	calls := 0
	liveExec := cmd_test.MockCmdExec{
		RunFunc: func(*exec.Cmd) error {
			calls++
			if calls <= goneFor {
				return fmt.Errorf("not yet")
			}
			return nil
		},
		OutputFunc: func(*exec.Cmd) ([]byte, error) { return nil, nil },
	}
	ts := tmux.NewSessionWithDeps(context.Background(), "sess", program, pty, liveExec)
	return &Instance{
		Title: "sess", status: Running, Program: program,
		claudeConfigDir: cfgDir, gitWorktree: wt, tmuxSession: ts,
	}
}

// TestResumedConversationRecordsThatTheConversationCameBack — a transcript exists,
// so startResuming elects `--continue` and the restore may say the conversation
// came back.
func TestResumedConversationRecordsThatTheConversationCameBack(t *testing.T) {
	cfgDir := t.TempDir()
	inst := relaunchableInstance(t, "claude", cfgDir, 1)
	writeClaudeTranscript(t, cfgDir, inst.gitWorktree.GetWorktreePath())

	inst.recoverInPlace()

	resumed, known := inst.ResumedConversation()
	require.True(t, known, "claude's transcript is locatable, so the answer is knowable")
	require.True(t, resumed, "a transcript existed, so the agent resumed it")
}

// TestResumedConversationRecordsThatTheAgentStartedFresh — no transcript, so
// startResuming launches blank. This is the case the notice exists to report: the
// session comes back but its conversation does not.
func TestResumedConversationRecordsThatTheAgentStartedFresh(t *testing.T) {
	inst := relaunchableInstance(t, "claude", t.TempDir(), 1) // deliberately no transcript

	inst.recoverInPlace()

	resumed, known := inst.ResumedConversation()
	require.True(t, known, "claude's transcript is locatable, so the answer is knowable")
	require.False(t, resumed, "with no transcript to continue, the agent started fresh")
}

// TestResumedConversationIsUnknownWithoutATranscriptAdapter. codex and gemini have
// no adapter, so startResuming defers to the agent's own resume probe and never
// learns which way it went. Reporting either answer would be a guess printed as a
// fact, so the restore must be able to tell "fresh" from "cannot tell".
func TestResumedConversationIsUnknownWithoutATranscriptAdapter(t *testing.T) {
	inst := relaunchableInstance(t, "codex", t.TempDir(), 1)

	inst.recoverInPlace()

	_, known := inst.ResumedConversation()
	require.False(t, known, "no adapter handles codex, so resumability is unknowable")
}

// TestResumeRecordsTheConversationOutcome is the link the undo restore actually
// walks. The tests above drive recoverInPlace; restoreOne calls Resume, which
// reaches startResuming by a different route (recreateSession, because a killed
// session's tmux session is gone). If that route ever stopped recording, the
// restore notice would go quiet and every test above would stay green.
func TestResumeRecordsTheConversationOutcome(t *testing.T) {
	inst := relaunchableInstance(t, "claude", t.TempDir(), 2) // no transcript: came back fresh
	inst.started = true
	inst.SetStatus(Paused)

	require.NoError(t, inst.Resume())

	resumed, known := inst.ResumedConversation()
	require.True(t, known, "a resume that relaunched the agent must record which way it went")
	require.False(t, resumed, "with no transcript to continue, the agent started fresh")
}

// TestResumedConversationIsUnknownBeforeAnyResume — an instance that has not been
// relaunched has nothing to report, so the notice must not claim it started fresh.
func TestResumedConversationIsUnknownBeforeAnyResume(t *testing.T) {
	inst := relaunchableInstance(t, "claude", t.TempDir(), 1)

	_, known := inst.ResumedConversation()
	require.False(t, known, "nothing has resumed yet, so there is no answer to report")
}
