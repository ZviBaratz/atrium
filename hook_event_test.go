package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/session/tmux"

	"github.com/stretchr/testify/require"
)

// TestParseHookPayload pulls agent_id and permission_mode out of a hook payload, degrading
// to the zero value for the absent/empty/garbage cases (which applyHookEvent then skips
// rather than corrupting the set or blanking the chip).
func TestParseHookPayload(t *testing.T) {
	// A sub-agent's tool edge: both fields present (verified shape, claude 2.1.209).
	require.Equal(t, tmux.HookPayload{AgentID: "aa", PermissionMode: "plan"},
		parseHookPayload(strings.NewReader(`{"agent_id":"aa","agent_type":"x","permission_mode":"plan"}`)))
	// The main loop's own edge: no agent_id, a real mode.
	require.Equal(t, tmux.HookPayload{PermissionMode: "default"},
		parseHookPayload(strings.NewReader(`{"session_id":"s","permission_mode":"default"}`)))
	// SubagentStart's shape: an agent_id but no permission_mode at all.
	require.Equal(t, tmux.HookPayload{AgentID: "aa"},
		parseHookPayload(strings.NewReader(`{"agent_id":"aa","agent_type":"general-purpose"}`)))
	require.Equal(t, tmux.HookPayload{}, parseHookPayload(strings.NewReader(`{}`)))
	require.Equal(t, tmux.HookPayload{}, parseHookPayload(strings.NewReader("")))
	require.Equal(t, tmux.HookPayload{}, parseHookPayload(strings.NewReader("not json")))
}

// TestParseHookPayloadBounded: an oversized payload is truncated by the LimitReader, which
// fails the decode and degrades to the zero value rather than buffering it all.
func TestParseHookPayloadBounded(t *testing.T) {
	huge := `{"agent_id":"aa","pad":"` + strings.Repeat("x", hookPayloadLimit) + `"}`
	require.Equal(t, tmux.HookPayload{}, parseHookPayload(strings.NewReader(huge)))
}

// TestParseHookPayloadNeverBlocks pins the property hookstate.go used to buy by abstention:
// a stdin that is never written and never closed must not stall the hook. This is what makes
// it safe for the once-per-turn edges to read stdin at all — claude WAITS for its hooks, so a
// blocking read here would stall the agent, not just atrium.
func TestParseHookPayloadNeverBlocks(t *testing.T) {
	prev := hookPayloadTimeout
	hookPayloadTimeout = 50 * time.Millisecond
	t.Cleanup(func() { hookPayloadTimeout = prev })

	r, w := io.Pipe()
	t.Cleanup(func() { _ = w.Close() }) // never written, never closed during the read

	done := make(chan tmux.HookPayload, 1)
	go func() { done <- parseHookPayload(r) }()
	select {
	case p := <-done:
		require.Equal(t, tmux.HookPayload{}, p, "a blocked stdin degrades to the zero payload")
	case <-time.After(5 * time.Second):
		t.Fatal("parseHookPayload blocked on a never-closed stdin")
	}
}

// TestRunHookEvent drives the hidden `hook` subcommand's body across a realistic turn —
// prompt-submit (mode from stdin), a per-tool working edge, a sub-agent start (agent_id from
// stdin), ready, then the matched stop — and asserts the on-disk record the poller reads. It
// also confirms the subcommand ignores stdin for the per-tool edge and no-ops on missing args.
func TestRunHookEvent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state")

	runHookEvent(path, tmux.HookEventPromptSubmit, strings.NewReader(`{"permission_mode":"plan"}`))
	require.Equal(t, "working", readState(t, path).State)
	require.Equal(t, "plan", readState(t, path).PermissionMode, "prompt-submit records the mode")

	// The per-tool edge never reads stdin: a mode here must not land, even if one is piped.
	runHookEvent(path, tmux.HookEventWorking, strings.NewReader(`{"permission_mode":"auto"}`))
	require.Equal(t, "plan", readState(t, path).PermissionMode, "the working edge ignores stdin")

	runHookEvent(path, tmux.HookEventSubagentStart, strings.NewReader(`{"agent_id":"aa"}`))
	runHookEvent(path, tmux.HookEventReady, strings.NewReader(`{"permission_mode":"plan"}`))

	require.Equal(t, "ready", readState(t, path).State)
	require.Equal(t, []string{"aa"}, readState(t, path).Inflight)

	runHookEvent(path, tmux.HookEventSubagentStop, strings.NewReader(`{"agent_id":"aa"}`))
	require.Empty(t, readState(t, path).Inflight, "the matched stop drains the set")
	require.Equal(t, "ready", readState(t, path).State, "the latch survives the stop")

	// StopFailure's shape: ready with no permission_mode at all must leave the mode standing.
	runHookEvent(path, tmux.HookEventReady, strings.NewReader(`{"session_id":"s"}`))
	require.Equal(t, "plan", readState(t, path).PermissionMode, "an absent mode never blanks the chip")

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
	State          string   `json:"state"`
	Inflight       []string `json:"inflight"`
	Effort         string   `json:"effort"`
	PermissionMode string   `json:"permission_mode"`
}

func readState(t *testing.T, path string) stateFileView {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	var v stateFileView
	require.NoError(t, json.Unmarshal(b, &v))
	return v
}
