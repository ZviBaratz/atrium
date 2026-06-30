package tmux

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ZviBaratz/atrium/cmd/cmd_test"

	"github.com/stretchr/testify/require"
)

// hookPollSession is like pollSession but names the session after the test, so each test
// gets a unique sanitizedName (and thus its own hook dir under the sandbox HOME) — no
// cross-test leakage through the shared state file path.
func hookPollSession(t *testing.T, program string, content *string) *Session {
	t.Helper()
	cmdExec := cmd_test.MockCmdExec{
		RunFunc:    func(cmd *exec.Cmd) error { return nil },
		OutputFunc: func(cmd *exec.Cmd) ([]byte, error) { return []byte(*content), nil },
	}
	return NewSessionWithDeps(context.Background(), t.Name(), program, NewMockPtyFactory(t), cmdExec)
}

func writeHookState(t *testing.T, s *Session, word string) {
	t.Helper()
	dir, err := hookSessionDir(s.snapshotName())
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "state"), []byte(word), 0o644))
	// The sandbox HOME is shared across a `go test -count=N` run, so a state file left
	// here would leak into the next iteration — e.g. TestReadHookState's opening
	// "absent file" assertion would then see a stale file and fail. Remove the dir at
	// test end so each iteration starts clean.
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
}

// forceSettingsFlag overrides the --settings capability probe for the duration of a test so
// it never execs the real claude binary and is order-independent.
func forceSettingsFlag(t *testing.T, supported bool) {
	t.Helper()
	prev := settingsFlagOverride
	settingsFlagOverride = &supported
	t.Cleanup(func() { settingsFlagOverride = prev })
}

func TestBuildHookSettings(t *testing.T) {
	data, err := buildHookSettings("/abs/dir/state")
	require.NoError(t, err)

	var parsed struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	require.NoError(t, json.Unmarshal(data, &parsed), "settings must be valid JSON")

	for _, ev := range []string{"UserPromptSubmit", "PreToolUse", "Stop", "StopFailure"} {
		require.Len(t, parsed.Hooks[ev], 1, "event %s has one matcher group", ev)
		require.Len(t, parsed.Hooks[ev][0].Hooks, 1, "event %s has one command", ev)
		require.Equal(t, "command", parsed.Hooks[ev][0].Hooks[0].Type)
		require.Contains(t, parsed.Hooks[ev][0].Hooks[0].Command, "/abs/dir/state",
			"the absolute state path is baked into the command for %s", ev)
	}
	// PreToolUse matches all tools; the matcher-less events omit it.
	require.Equal(t, "*", parsed.Hooks["PreToolUse"][0].Matcher)
	require.Empty(t, parsed.Hooks["UserPromptSubmit"][0].Matcher)
	require.Empty(t, parsed.Hooks["Stop"][0].Matcher)
	require.Empty(t, parsed.Hooks["StopFailure"][0].Matcher)
	// The working/ready words land on the right events. Stop fires at a clean turn-end and
	// StopFailure at an API-error turn-end; both mean "no longer working", so both write ready.
	require.Contains(t, parsed.Hooks["UserPromptSubmit"][0].Hooks[0].Command, hookStateWorking)
	require.Contains(t, parsed.Hooks["PreToolUse"][0].Hooks[0].Command, hookStateWorking)
	require.Contains(t, parsed.Hooks["Stop"][0].Hooks[0].Command, hookStateReady)
	require.Contains(t, parsed.Hooks["StopFailure"][0].Hooks[0].Command, hookStateReady)
}

// The hook write must use a per-invocation temp file: agent teams fire many PreToolUse hooks
// concurrently (the parent session's injected --settings applies to every tool call), and a
// single shared temp made them race — the first mv won and the rest failed with "cannot stat
// …/state.tmp", losing state writes (including the final "ready") and stranding the file on
// "working". A "$$"-suffixed temp is unique per hook process, so the writes can't collide.
func TestHookWriteCmdUsesUniqueTemp(t *testing.T) {
	cmd := hookWriteCmd("/abs/dir/state", hookStateReady)
	require.Contains(t, cmd, "$$",
		"temp file must be per-process unique (shell PID) so concurrent hooks don't clobber a shared temp")
	require.Contains(t, cmd, "/abs/dir/state", "the final destination is still the state file")
	require.Contains(t, cmd, hookStateReady, "the word being written is baked in")
}

func TestEnsureHookSettingsClaude(t *testing.T) {
	forceSettingsFlag(t, true)
	name := "claudesquad_" + t.Name()
	dir, err := hookSessionDir(name)
	require.NoError(t, err)
	// A stale state from a prior incarnation must be cleared on (re)launch.
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "state"), []byte(hookStateWorking), 0o644))

	settingsPath, err := ensureHookSettings(name, "claude")
	require.NoError(t, err)
	require.Equal(t, filepath.Join(dir, "settings.json"), settingsPath)
	require.FileExists(t, settingsPath)
	_, statErr := os.Stat(filepath.Join(dir, "state"))
	require.True(t, os.IsNotExist(statErr), "stale state file is cleared")

	data, err := os.ReadFile(settingsPath)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &map[string]any{}), "written settings is valid JSON")
}

func TestEnsureHookSettingsSkips(t *testing.T) {
	forceSettingsFlag(t, true)
	p, err := ensureHookSettings("claudesquad_"+t.Name()+"_aider", "aider")
	require.NoError(t, err)
	require.Empty(t, p, "non-claude program gets no hooks")

	forceSettingsFlag(t, false)
	p, err = ensureHookSettings("claudesquad_"+t.Name()+"_unsupported", "claude")
	require.NoError(t, err)
	require.Empty(t, p, "no --settings support → no hooks")
}

func TestReadHookState(t *testing.T) {
	c := "x"
	s := hookPollSession(t, "claude", &c)

	_, ok := s.readHookState()
	require.False(t, ok, "absent file → no signal")

	writeHookState(t, s, hookStateWorking)
	st, ok := s.readHookState()
	require.True(t, ok)
	require.Equal(t, hookStateWorking, st)

	writeHookState(t, s, "  "+hookStateReady+"\n") // surrounding whitespace tolerated
	st, ok = s.readHookState()
	require.True(t, ok)
	require.Equal(t, hookStateReady, st)

	writeHookState(t, s, "garbage")
	_, ok = s.readHookState()
	require.False(t, ok, "unknown word → no signal")

	// A non-claude program never consults a hook file even if one is present.
	c2 := "x"
	a := hookPollSession(t, "aider", &c2)
	writeHookState(t, a, hookStateWorking)
	_, ok = a.readHookState()
	require.False(t, ok)
}

// Regression guard for the running↔ready flicker (RC1+RC3): a hook file stuck on "working"
// — a missed Stop, or the lost-write race before Fix 1 — must NOT re-raise working once the
// indicator has settled to idle. The marker is the only signal that raises working; the
// stuck file is never trusted as a latch. After a marker-absent grace the indicator commits
// idle and STAYS idle across pane repaints (which used to reset the churn gate and re-raise
// "working", producing the blink). Only a returning marker flips it back.
func TestPollStuckWorkingFileDoesNotFlicker(t *testing.T) {
	busy := "✻ Cogitating… (1s · esc to interrupt)"
	idle := "❯ \n⏵⏵ auto mode on (shift+tab to cycle) · ← for agents"
	c := busy
	s := hookPollSession(t, "claude", &c)
	writeHookState(t, s, hookStateWorking) // Stop never fires — file is stuck on "working".

	require.Equal(t, PaneWorking, s.Poll(), "marker present → working")

	// Marker gone but the file still says working. Held by the marker-absent grace…
	c = idle
	for i := 1; i < idleConfirmTicks; i++ {
		require.Equal(t, PaneWorking, s.Poll(), "marker-absent grace holds working (tick %d)", i)
	}
	require.Equal(t, PaneIdle, s.Poll(), "commits idle at the grace cap despite the stuck 'working' file")

	// The idle pane keeps repainting (cursor blink, selector redraw). Each repaint is a
	// content change, which previously reset the churn gate and re-raised "working".
	for i := 0; i < 5; i++ {
		c = idle + "\n· redraw " + itoa(i)
		require.Equal(t, PaneIdle, s.Poll(), "stays idle across repaints — no flicker (repaint %d)", i)
	}

	// Only a returning marker raises working again.
	c = busy
	require.Equal(t, PaneWorking, s.Poll(), "a returning marker re-raises working")
}

func TestPollHookReadyIsIdle(t *testing.T) {
	idle := "❯ \n⏵⏵ auto mode on (shift+tab to cycle) · ← for agents"
	c := idle
	s := hookPollSession(t, "claude", &c)
	writeHookState(t, s, hookStateReady)
	require.Equal(t, PaneIdle, s.Poll(), "hook 'ready' with no marker is idle")
}

// A live busy marker positively proves work and overrides a stale/missed hook "ready".
func TestPollMarkerOverridesHookReady(t *testing.T) {
	c := "✻ Cogitating… (5s · esc to interrupt)"
	s := hookPollSession(t, "claude", &c)
	writeHookState(t, s, hookStateReady)
	require.Equal(t, PaneWorking, s.Poll())
}

// With no hook file the marker drives the state: present → working, and once it is gone the
// marker-absent grace holds working until the idleConfirmTicks cap, then commits idle.
func TestPollNoHookFileUsesScrape(t *testing.T) {
	c := "✻ Cogitating… (5s · esc to interrupt)"
	s := hookPollSession(t, "claude", &c)
	require.Equal(t, PaneWorking, s.Poll(), "marker present → working")
	// Marker gone, no hook file: held by the marker-absent grace up to the cap.
	c = "❯ \n⏵⏵ auto mode on (shift+tab to cycle) · ← for agents"
	for i := 1; i < idleConfirmTicks; i++ {
		require.Equal(t, PaneWorking, s.Poll(), "held by the marker-absent grace (tick %d)", i)
	}
	require.Equal(t, PaneIdle, s.Poll(), "commits idle at the marker-absent cap")
}

// PollNow (detach/switch refresh) reads the latch at face value for claude.
func TestPollNowReadsHookState(t *testing.T) {
	idle := "❯ \n⏵⏵ auto mode on (shift+tab to cycle) · ← for agents"
	c := idle
	s := hookPollSession(t, "claude", &c)
	writeHookState(t, s, hookStateWorking)
	require.Equal(t, PaneWorking, s.PollNow(), "marker-absent but latch says working")
	writeHookState(t, s, hookStateReady)
	require.Equal(t, PaneIdle, s.PollNow(), "latch says ready")
}

func TestCleanupHookSession(t *testing.T) {
	c := "x"
	s := hookPollSession(t, "claude", &c)
	writeHookState(t, s, hookStateReady)
	dir, err := hookSessionDir(s.snapshotName())
	require.NoError(t, err)
	require.DirExists(t, dir)
	require.NoError(t, s.Close())
	require.NoDirExists(t, dir, "Close removes the per-session hook dir")
}

// itoa avoids importing strconv just for the churn-gap fixture.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
