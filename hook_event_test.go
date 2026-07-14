package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/session/tmux"

	"github.com/stretchr/testify/require"
)

// TestParseSubagentID pulls agent_id out of a hook payload, degrading to "" for the
// absent/empty/garbage cases (which the set logic then skips rather than corrupting).
func TestParseSubagentID(t *testing.T) {
	require.Equal(t, "aa", parseSubagentID(strings.NewReader(`{"agent_id":"aa","agent_type":"x"}`)))
	require.Equal(t, "", parseSubagentID(strings.NewReader(`{}`)))
	require.Equal(t, "", parseSubagentID(strings.NewReader("")))
	require.Equal(t, "", parseSubagentID(strings.NewReader("not json")))
}

// TestRunHookEvent drives the hidden `hook` subcommand's body across a realistic sequence
// — working, a sub-agent start (agent_id from stdin), ready, then the matched stop — and
// asserts the on-disk record the poller reads. It also confirms the subcommand reads stdin
// only for the sub-agent events and no-ops on missing args.
func TestRunHookEvent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state")

	runHookEvent(path, tmux.HookEventWorking, strings.NewReader("")) // no stdin needed
	runHookEvent(path, tmux.HookEventSubagentStart, strings.NewReader(`{"agent_id":"aa"}`))
	runHookEvent(path, tmux.HookEventReady, strings.NewReader(""))

	require.Equal(t, "ready", readState(t, path).State)
	require.Equal(t, []string{"aa"}, readState(t, path).Inflight)

	runHookEvent(path, tmux.HookEventSubagentStop, strings.NewReader(`{"agent_id":"aa"}`))
	require.Empty(t, readState(t, path).Inflight, "the matched stop drains the set")
	require.Equal(t, "ready", readState(t, path).State, "the latch survives the stop")

	// A missing state-file or event is a silent no-op (must not panic or write).
	runHookEvent("", tmux.HookEventWorking, strings.NewReader(""))
	runHookEvent(path, "", strings.NewReader(""))
	require.Equal(t, "ready", readState(t, path).State, "no-op events leave the record untouched")
}

// TestRunHookEventEffort pins the carrier: the subcommand takes the turn's effort from its
// own $CLAUDE_EFFORT environment (claude exports it to every hook subprocess), never from
// stdin — which is what lets the payload-free working/ready latches carry it without ever
// touching stdin. The write rule itself is tmux's (TestApplyHookEventEffort); this covers
// the main-package seam that feeds it, including the prompt-submit exemption end to end.
func TestRunHookEventEffort(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state")

	t.Setenv("CLAUDE_EFFORT", "max")
	runHookEvent(path, tmux.HookEventWorking, strings.NewReader(""))
	require.Equal(t, "max", readState(t, path).Effort, "effort comes from the env, with no stdin read")

	// UserPromptSubmit's env value is a stale model default; it must not clobber the truth.
	t.Setenv("CLAUDE_EFFORT", "high")
	runHookEvent(path, tmux.HookEventPromptSubmit, strings.NewReader(""))
	require.Equal(t, "max", readState(t, path).Effort, "prompt-submit's stale value is ignored")
	require.Equal(t, "working", readState(t, path).State, "but it still latches working")

	// A model without effort support exports nothing — the last known level must survive.
	t.Setenv("CLAUDE_EFFORT", "")
	runHookEvent(path, tmux.HookEventWorking, strings.NewReader(""))
	require.Equal(t, "max", readState(t, path).Effort, "an empty env must not clear a known effort")
}

type stateFileView struct {
	State    string   `json:"state"`
	Inflight []string `json:"inflight"`
	Effort   string   `json:"effort"`
}

func readState(t *testing.T, path string) stateFileView {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	var v stateFileView
	require.NoError(t, json.Unmarshal(b, &v))
	return v
}
