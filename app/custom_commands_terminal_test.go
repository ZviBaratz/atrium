package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/cmdlog"
	"github.com/ZviBaratz/atrium/config"

	tea "charm.land/bubbletea/v2"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/muesli/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// terminalRun is one call the terminal exec seam received.
type terminalRun struct{ spec customCommandSpec }

// stubTerminalRunner replaces the terminal exec seam and returns the calls it saw.
// outcome is what the run reports once it "exits"; the returned channel is closed
// immediately, so attachCommand.Run does not block.
//
// It is the seam that makes "refused" testable for this mode too. Terminal mode's spawn
// happens inside attachCommand.Run, which a test can call directly — so without the seam
// a gate test would either spawn a real `sh -c` or assert nothing about the spawn at all.
func stubTerminalRunner(t *testing.T, outcome error) *[]terminalRun {
	t.Helper()
	var calls []terminalRun
	prev := runTerminalCustomCommand
	runTerminalCustomCommand = func(_ context.Context, spec customCommandSpec) (chan struct{}, func() error, error) {
		calls = append(calls, terminalRun{spec: spec})
		ch := make(chan struct{})
		close(ch)
		return ch, func() error { return outcome }, nil
	}
	t.Cleanup(func() { runTerminalCustomCommand = prev })
	return &calls
}

// terminalEntry is the wire fixture for a repo-context terminal command, which is live
// for any selection (see newCustomCommandHome on why session context needs real tmux).
func terminalEntry(key, desc, command string) config.CustomCommand {
	return config.CustomCommand{
		Key: key, Description: desc, Context: "repo", Command: command, Output: "terminal",
	}
}

// TestCustomCommandTerminalWiresEveryAttachObligation is the reason this stage was split
// out of #375's UI stage.
//
// A tea.ExecProcess would run the command and look correct. What it would not do is any
// of the three things suspending the event loop obliges a caller to do — and each failure
// is invisible on the screen the user is looking at:
//
//   - no attachGen bump: a pane capture taken before a three-minute command is applied
//     after it, replaying a stale PanePrompt as a TapEnter into whatever dialog is up now.
//   - no keeper: every autoyes session in the fleet sits on an unanswered permission
//     dialog for the whole command (#264, reintroduced).
//   - excluded set to anything but nil: the keeper skips a session for no reason — unlike
//     an attach, no session is holding the terminal here.
func TestCustomCommandTerminalWiresEveryAttachObligation(t *testing.T) {
	h, inst := newCustomCommandHome(t, nil)
	other := newGateInstance(t, h, "other")
	spec := customCommandSpec{key: "t", desc: "take the terminal", script: "true", dir: t.TempDir()}

	cmd, callback := h.terminalCustomCommandExec(spec)

	require.NotNil(t, cmd.keeper, "a suspended loop stops prompt delivery fleet-wide (#264)")
	assert.Nil(t, cmd.keeper.excluded,
		"no session is attached, so every session is the keeper's business")
	assert.Contains(t, cmd.keeper.instances, inst, "the keeper must see the whole fleet")
	assert.Contains(t, cmd.keeper.instances, other)
	assert.False(t, cmd.raw,
		"a `sh -c` child must run cooked — raw mode clears OPOST and staircases its output")
	require.NotNil(t, cmd.onAttached, "the generation bump is not optional")
	require.NotNil(t, callback, "every path must yield a done message")

	before := h.attachGen
	cmd.onAttached()
	assert.Equal(t, before+1, h.attachGen,
		"pane captures taken before the takeover must be retired, or one replays a "+
			"TapEnter onto whatever dialog is on screen when the loop resumes")
}

// The bump has to happen for a run that STARTED and not for one that failed to, and
// Run is what decides that. Driven through Run rather than by calling onAttached, because
// the ordering is the property: a bump before the spawn would retire captures for a
// command that never ran.
func TestCustomCommandTerminalBumpsGenerationOnlyOnAStartedRun(t *testing.T) {
	h, _ := newCustomCommandHome(t, nil)
	spec := customCommandSpec{key: "t", desc: "take the terminal", script: "true", dir: t.TempDir()}

	t.Run("started", func(t *testing.T) {
		stubTerminalRunner(t, nil)
		before := h.attachGen
		cmd, _ := h.terminalCustomCommandExec(spec)
		require.NoError(t, cmd.Run())
		assert.Equal(t, before+1, h.attachGen)
	})

	t.Run("failed to start", func(t *testing.T) {
		prev := runTerminalCustomCommand
		runTerminalCustomCommand = func(context.Context, customCommandSpec) (chan struct{}, func() error, error) {
			return nil, nil, errors.New("fork/exec: no such file or directory")
		}
		t.Cleanup(func() { runTerminalCustomCommand = prev })

		before := h.attachGen
		cmd, callback := h.terminalCustomCommandExec(spec)
		err := cmd.Run()
		require.Error(t, err)
		assert.Equal(t, before, h.attachGen,
			"nothing ran and no keeper moved a pane, so pre-existing captures are still valid")

		// And the failure still becomes a done message, or the single-flight slot stays
		// claimed for the rest of the process.
		done, ok := callback(err).(customCommandTerminalDoneMsg)
		require.True(t, ok)
		assert.Error(t, done.err)
	})
}

// The exit code is the whole of AC5 for this mode. stderr went to the tty, but the status
// did not: bubbletea hands Run's error to the callback verbatim, so a non-zero exit
// arrives as an *exec.ExitError that the user — who watched a screenful scroll past —
// has no other way to learn.
func TestCustomCommandTerminalSurfacesTheExitCode(t *testing.T) {
	h, _ := newCustomCommandHome(t, nil)

	_, cmd := h.handleCustomCommandTerminalDone(customCommandTerminalDoneMsg{
		key: "c", desc: "just ci", err: exitErrorWithCode(t, 2),
	})
	require.NotNil(t, cmd, "the return must at least repaint")

	// The hint bar, not the screen: handleError has already placed the message, and the
	// cmd it returns is only the toast's expiry timer.
	require.True(t, h.menu.HasNotice(), "a failed run must reach the user")
	notice := xansi.Strip(h.menu.String())
	assert.Contains(t, notice, "just ci", "the notice must name which command failed")
	assert.Contains(t, notice, "exited 2", "and what its status was")
	assert.Equal(t, stateDefault, h.state,
		"a bounded notice stays a toast rather than taking the screen back")
	assert.Empty(t, h.runningCustomCommand, "a failure still releases the slot")
}

// A command that ran fine says nothing, matching background mode. The user watched it
// happen; a toast confirming it is noise, and the log record is there either way.
func TestCustomCommandTerminalSuccessIsQuiet(t *testing.T) {
	h, _ := newCustomCommandHome(t, nil)
	h.runningCustomCommand = "t"

	_, _ = h.handleCustomCommandTerminalDone(customCommandTerminalDoneMsg{key: "t", desc: "lazygit"})

	assert.False(t, h.menu.HasNotice(), "a clean run must not raise a notice")
	assert.Empty(t, h.runningCustomCommand)
}

// TestCustomCommandExitTailNamesWhatItCanAndNothingElse pins the fallback, which is the
// part of this that looks like defensive padding and is not.
//
// ExitCode() returns -1 for a process killed by a signal, and a signal death is the
// ORDINARY outcome here: Ctrl+C is how a user stops a command that owns their terminal.
// " exited -1" reads as a bug in Atrium rather than as "you stopped it".
func TestCustomCommandExitTailNamesWhatItCanAndNothingElse(t *testing.T) {
	assert.Equal(t, " exited 1", customCommandExitTail(exitErrorWithCode(t, 1)))
	assert.Equal(t, " exited 255", customCommandExitTail(exitErrorWithCode(t, 255)))

	killed := signalDeathError(t)
	require.Equal(t, -1, killed.ExitCode(), "the fixture must be a signal death for this to test anything")
	assert.Equal(t, customCommandDiedTail, customCommandExitTail(killed),
		`a signal death has no exit code; " exited -1" reads as a bug`)

	assert.Equal(t, customCommandDiedTail, customCommandExitTail(errors.New("fork/exec: no such file")),
		"a failure to start has no status either")
}

// TestCustomCommandTerminalNoticeFitsARow: terminal mode's notice obeys the same two
// rules every other one-row message about a custom command does — bounded through
// customCommandLabel, and the reason LAST so the hint bar's right-hand truncation cannot
// delete it. The tails themselves join the iterated set in
// TestCustomCommandRefusalsFitARow; this is the composed message.
func TestCustomCommandTerminalNoticeFitsARow(t *testing.T) {
	long := strings.Repeat("an unbounded user-authored description ", 20)
	wide := strings.Repeat("日本語", 200)

	for _, err := range []error{exitErrorWithCode(t, 255), signalDeathError(t), errors.New("nope")} {
		for _, desc := range []string{"short", long, wide} {
			msg := customCommandExitNotice(desc, err)
			assert.LessOrEqualf(t, ansi.PrintableRuneWidth(msg), customCommandNoticeWidth,
				"the notice must fit its declared bound: %q", msg)
			assert.Truef(t, strings.HasSuffix(msg, customCommandExitTail(err)),
				"the reason must come last: %q", msg)
		}
	}
}

// Keeper losses must reach the user AND be persisted, exactly as they are after a tmux
// detach — the keeper cleared those prompts and cannot persist, because persistence is
// main-loop-owned. A run whose command also failed gets both facts in ONE error:
// handleError writes a single notice, so two calls would silently drop the first.
func TestCustomCommandTerminalKeeperLossesAreSurfaced(t *testing.T) {
	h, _ := newCustomCommandHome(t, nil)

	_, _ = h.handleCustomCommandTerminalDone(customCommandTerminalDoneMsg{
		key: "c", desc: "just ci", err: exitErrorWithCode(t, 1),
		keeperErrs: []string{`failed to deliver prompt to "b": send-keys failed`},
	})

	// Both halves, wherever handleError routed them: joined, they are wider than a row,
	// so this becomes the persistent modal rather than a toast.
	surfaced := xansi.Strip(h.menu.String())
	if h.textOverlay != nil {
		surfaced += xansi.Strip(h.textOverlay.Render())
	}
	assert.Contains(t, surfaced, "exited 1", "the command's own outcome must survive")
	assert.Contains(t, surfaced, "failed to deliver prompt",
		"a prompt the keeper lost must not be log-only — the session sits Ready and idle")
}

// TestResumeAfterSuspendedLoopServesBothSuspensions is the guard for the extraction.
//
// The tail (sweep the stale fleet, pin the poll tracker, hard repaint) is shared between
// the tmux attach and terminal mode because both suspend the loop and both leave every
// row stale — polling stopped for the whole duration. Nothing asserted it for the attach
// path before, so extracting it had no safety net; this is that net, in both directions.
//
// The pin is asserted through selectedSince rather than through lastStatusPollSelection,
// which would be vacuous: instanceChanged assigns that field itself on a changed
// selection. What the pin actually buys is that instanceChanged's changed-selection
// BRANCH does not run — the branch that fires a second poll of the instance the sweep is
// already polling — and restamping selectedSince is that branch's visible mark.
func TestResumeAfterSuspendedLoopServesBothSuspensions(t *testing.T) {
	for _, tc := range []struct {
		name string
		msg  tea.Msg
	}{
		{"tmux attach", attachFinishedMsg{}},
		{"terminal custom command", customCommandTerminalDoneMsg{key: "t", desc: "lazygit"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, inst := newCustomCommandHome(t, nil)
			// What a detach leaves behind: the tracker reset, so a resume that does not
			// pin it looks to instanceChanged like a fresh selection.
			h.lastStatusPollSelection = nil
			stamp := time.Now().Add(-time.Hour)
			h.selectedSince = stamp

			_, cmd := h.Update(tc.msg)

			require.NotNil(t, cmd, "the frame must be repainted — RestoreTerminal only soft-repaints")
			assert.Equal(t, inst, h.lastStatusPollSelection)
			assert.True(t, h.selectedSince.Equal(stamp),
				"the pin must come BEFORE instanceChanged, or the selection is polled twice: "+
					"once by the sweep and once by instanceChanged's changed-selection branch")
		})
	}
}

// The keeper cleared prompts while the loop was suspended and cannot persist that —
// persistence is main-loop-owned. Without this, state.json resurrects a prompt the keeper
// already delivered, or one it abandoned with its retry budget spent, on the next launch.
// Exactly the attach path's obligation, which terminal mode inherits whole.
func TestCustomCommandTerminalPersistsWhatTheKeeperDid(t *testing.T) {
	for _, tc := range []struct {
		name string
		msg  customCommandTerminalDoneMsg
	}{
		{"delivered", customCommandTerminalDoneMsg{key: "t", desc: "lazygit", keeperDelivered: true}},
		{"abandoned", customCommandTerminalDoneMsg{key: "t", desc: "lazygit", keeperErrs: []string{"lost"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := newCustomCommandHome(t, nil)
			statePath := filepath.Join(mustConfigDir(t), "state.json")
			require.NoError(t, os.RemoveAll(statePath))

			_, _ = h.handleCustomCommandTerminalDone(tc.msg)

			_, err := os.Stat(statePath)
			require.NoError(t, err,
				"what the keeper cleared must be persisted on return, or state.json "+
					"resurrects it — the keeper cannot persist it itself")
		})
	}
}

// A clean run with an idle keeper must NOT write state.json. The over-persist direction
// matters because the write is not free: it serializes the whole instance list, and doing
// it after every lazygit would put a file write on a path the user takes constantly.
func TestCustomCommandTerminalDoesNotPersistWhenTheKeeperDidNothing(t *testing.T) {
	h, _ := newCustomCommandHome(t, nil)
	statePath := filepath.Join(mustConfigDir(t), "state.json")
	require.NoError(t, os.RemoveAll(statePath))

	_, _ = h.handleCustomCommandTerminalDone(customCommandTerminalDoneMsg{key: "t", desc: "lazygit"})

	_, err := os.Stat(statePath)
	assert.ErrorIs(t, err, os.ErrNotExist,
		"an idle keeper cleared nothing, so there is nothing to persist")
}

// TestCustomCommandTerminalRoutesOnTheOutputMode: pressing a terminal row's key must
// reach the terminal seam and NOT the background one. The two are separate code paths
// from the same menu, and a spec that lost its mode would run a screen-owning command
// into a buffer nobody reads (or, worse, the reverse).
func TestCustomCommandTerminalRoutesOnTheOutputMode(t *testing.T) {
	cmds := validCommands(t,
		terminalEntry("t", "lazygit here", "true"),
		config.CustomCommand{Key: "b", Description: "background one", Context: "repo",
			Command: "true", Output: "background"},
	)
	h, _ := newCustomCommandHome(t, cmds)
	background := stubRunner(t, nil)
	terminal := stubTerminalRunner(t, nil)

	_, _ = h.handleKeyPress(runeKey("!"))
	require.Equal(t, stateCustomCommands, h.state)
	_, cmd := h.handleKeyPress(runeKey("t"))
	require.NotNil(t, cmd, "running must return work to do")
	// Drained, which is the whole assertion. A terminal command's cmd is a tea.Exec that
	// spawns nothing until the runtime processes it, so an undrained one keeps this test
	// green even if the routing sent it down the background path instead — the background
	// seam is only reached when its cmd is invoked.
	drain(t, h, cmd)

	assert.Empty(t, *background, "a terminal command must not run through the background seam")
	assert.Equal(t, "t", h.runningCustomCommand, "and it claims the single-flight slot")

	// tea.Exec's message is unexported, so drive the suspension the way the runtime
	// would: the constructor is the seam split out for exactly this.
	execCmd, callback := h.terminalCustomCommandExec(customCommandSpec{
		key: "t", desc: "lazygit here", script: "true", dir: t.TempDir(),
	})
	require.NoError(t, execCmd.Run())
	require.Len(t, *terminal, 1, "the terminal seam is what spawns the child")
	_, _ = h.Update(callback(nil))
	assert.Empty(t, h.runningCustomCommand, "and the done message releases the slot")
}

// TestCustomCommandTerminalRefusedWhileBackgroundRuns is why both modes share the slot.
//
// The README promises "one custom command runs at a time", and that promise is what makes
// the two modes non-interleavable: the slot is released only by a done handler, so a
// terminal takeover cannot begin while a background run is live — and therefore a
// background done message can never arrive holding a terminal run's key and clear the
// wrong latch.
func TestCustomCommandTerminalRefusedWhileBackgroundRuns(t *testing.T) {
	cmds := validCommands(t,
		config.CustomCommand{Key: "b", Description: "slow build", Context: "repo",
			Command: "true", Output: "background"},
		terminalEntry("t", "lazygit here", "true"),
	)
	h, _ := newCustomCommandHome(t, cmds)
	// A background run that never reports, so the slot stays claimed.
	stubRunner(t, func(customCommandSpec) tea.Msg { return nil })
	terminal := stubTerminalRunner(t, nil)

	_, _ = h.handleKeyPress(runeKey("!"))
	_, _ = h.handleKeyPress(runeKey("b"))
	require.Equal(t, "b", h.runningCustomCommand, "the background run must hold the slot")

	_, _ = h.handleKeyPress(runeKey("!"))
	_, _ = h.handleKeyPress(runeKey("t"))

	assert.Empty(t, *terminal, "a terminal takeover must not start over a running command")
	assert.Equal(t, "b", h.runningCustomCommand, "the slot still belongs to the first")
	require.True(t, h.menu.HasNotice(), "the user must be told why nothing happened")
	assert.Contains(t, xansi.Strip(h.menu.String()), "one custom command at a time")
}

// TestCustomCommandTerminalDrivesTheRealSeam: the working directory, the $ATRIUM_*
// environment and the command log, through a real `sh -c` on the real seam.
//
// The record is the part that needs driving rather than reasoning about. It happens in
// the waiting goroutine, after the outcome is known and before the channel closes — so a
// record written on the wrong side of that would be racy rather than absent, and only an
// end-to-end run shows it.
func TestCustomCommandTerminalDrivesTheRealSeam(t *testing.T) {
	cmdlog.Reset()
	t.Cleanup(cmdlog.Reset)

	dir := t.TempDir()
	const secret = "ghp_averyrealsecrettoken"
	spec := customCommandSpec{
		key: "w", desc: "write", session: "live",
		script: `printf '%s\n%s\n' "$PWD" "$ATRIUM_TITLE" > out.txt; ` +
			`echo "Authorization: token ` + secret + `"; exit 4`,
		dir:  dir,
		env:  []string{"ATRIUM_TITLE=from-the-env"},
		argv: []string{"atrium", "custom-command", "w", "write"},
	}

	done, outcome, err := execTerminalCustomCommand(context.Background(), spec)
	require.NoError(t, err, "the command must have started")
	<-done
	require.Error(t, outcome(), "exit 4 is a failure")

	body, err := os.ReadFile(filepath.Join(dir, "out.txt"))
	require.NoError(t, err, "the command must have run in spec.dir")
	lines := strings.Fields(string(body))
	require.Len(t, lines, 2)
	// macOS resolves TMPDIR through /private, so compare the resolved paths.
	wantDir, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	gotDir, err := filepath.EvalSymlinks(lines[0])
	require.NoError(t, err)
	assert.Equal(t, wantDir, gotDir)
	assert.Equal(t, "from-the-env", lines[1], "$ATRIUM_* must reach the shell")

	recs := cmdlog.Snapshot()
	require.Len(t, recs, 1, "every execution is recorded, including this mode's")
	rec := recs[0]
	assert.NotContains(t, rec.Argv, secret,
		"the rendered script must never reach the log — Redact cannot defend a single token")
	assert.Contains(t, rec.Argv, "custom-command")
	assert.Contains(t, rec.Argv, "write")
	assert.Equal(t, 4, rec.Exit, "the exit code must survive the argv swap")
	assert.Equal(t, "live", rec.Session)
	assert.Empty(t, rec.Stderr,
		"terminal mode captures nothing: the output went to the terminal the user was watching")
}

// A command bound to a cancelled context must not leave the caller waiting: quitting
// Atrium mid-command has to end the suspension, not park the exec goroutine forever.
func TestCustomCommandTerminalHonoursACancelledContext(t *testing.T) {
	cmdlog.Reset()
	t.Cleanup(cmdlog.Reset)

	ctx, cancel := context.WithCancel(context.Background())
	spec := customCommandSpec{
		key: "s", desc: "sleep", script: "sleep 60", dir: t.TempDir(),
		argv: []string{"atrium", "custom-command", "s", "sleep"},
	}
	done, outcome, err := execTerminalCustomCommand(ctx, spec)
	require.NoError(t, err)

	cancel()
	<-done // the assertion: this returns rather than hanging
	assert.Error(t, outcome(), "a cancelled command did not finish")
}

// exitErrorWithCode produces a real *exec.ExitError with the given status, by running a
// shell that exits with it — the only way to get one whose ProcessState is populated.
func exitErrorWithCode(t *testing.T, code int) *exec.ExitError {
	t.Helper()
	err := exec.CommandContext(t.Context(), "sh", "-c", fmt.Sprintf("exit %d", code)).Run()
	var exitErr *exec.ExitError
	require.ErrorAs(t, err, &exitErr)
	require.Equal(t, code, exitErr.ExitCode())
	return exitErr
}

// signalDeathError produces a real *exec.ExitError for a process killed by a signal,
// which is what makes ExitCode() report -1.
func signalDeathError(t *testing.T) *exec.ExitError {
	t.Helper()
	err := exec.CommandContext(t.Context(), "sh", "-c", "kill -TERM $$").Run()
	var exitErr *exec.ExitError
	require.ErrorAs(t, err, &exitErr)
	return exitErr
}
