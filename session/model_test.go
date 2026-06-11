package session

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ZviBaratz/atrium/session/transcript"
)

func TestPinnedModelFlag(t *testing.T) {
	for _, tc := range []struct {
		program, want string
	}{
		{"claude --model fable", "fable"},
		{"claude --model=fable", "fable"},
		{"/home/zvi/.local/bin/claude --model opus", "opus"},
		{"claude --permission-mode plan --model opus --continue", "opus"},
		{"claude", ""},
		{"claude --model", ""},                   // trailing bare flag: no value
		{"aider --model ollama_chat/gemma3", ""}, // claude-only by design
		{"codex --model o3", ""},
		{"", ""},
	} {
		if got := pinnedModelFlag(tc.program); got != tc.want {
			t.Errorf("pinnedModelFlag(%q) = %q, want %q", tc.program, got, tc.want)
		}
	}
}

// TestModelInfo pins the chip-value rule: transcript truth wins when known,
// else the --model flag fills in before the first turn.
func TestModelInfo(t *testing.T) {
	inst, err := NewInstance(InstanceOptions{Title: "m", Path: ".", Program: "claude --model fable"})
	require.NoError(t, err)

	assert.Equal(t, "fable", inst.ModelInfo(), "flag fallback before any transcript truth")

	inst.SetModelMeta("claude-fable-5", transcript.Stamp{Path: "/x", Size: 1})
	assert.Equal(t, "claude-fable-5", inst.ModelInfo(), "transcript truth wins over the flag")

	bare, err := NewInstance(InstanceOptions{Title: "b", Path: ".", Program: "claude"})
	require.NoError(t, err)
	assert.Empty(t, bare.ModelInfo(), "no flag, no transcript: no chip")

	bare.SetModelMeta("claude-opus-4-7", transcript.Stamp{Path: "/y", Size: 1})
	assert.Equal(t, "claude-opus-4-7", bare.ModelInfo())
}

// TestSetModelMeta_EmptyAdvancesStampKeepsTruth pins the degradation contract:
// an extraction that parsed new bytes but found no assistant entry advances the
// stamp (no re-parse next tick) without clearing the last known model.
func TestSetModelMeta_EmptyAdvancesStampKeepsTruth(t *testing.T) {
	inst, err := NewInstance(InstanceOptions{Title: "m", Path: ".", Program: "claude"})
	require.NoError(t, err)

	first := transcript.Stamp{Path: "/t", ModTime: time.Now(), Size: 10}
	inst.SetModelMeta("claude-opus-4-7", first)
	later := transcript.Stamp{Path: "/t", ModTime: first.ModTime.Add(time.Second), Size: 20}
	inst.SetModelMeta("", later)

	assert.Equal(t, "claude-opus-4-7", inst.ModelInfo(), "empty extraction must not clear the last truth")
	assert.True(t, inst.modelStamp.Equal(later), "stamp must advance so the same bytes aren't re-parsed")
}

// TestComputeModel_DirectSession runs the full extraction path without tmux: a
// started direct session's WorkingDir is its Path, and claudeConfigDir routes
// the transcript root — the same wiring the poll loop uses.
func TestComputeModel_DirectSession(t *testing.T) {
	root := t.TempDir()
	workDir := t.TempDir()
	line := `{"type":"assistant","isSidechain":false,"message":{"model":"claude-opus-4-8","content":[{"type":"text","text":"hi"}]}}` + "\n"
	dest := filepath.Join(root, "projects", sanitizeCWDForTest(workDir), "s.jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(dest), 0o755))
	require.NoError(t, os.WriteFile(dest, []byte(line), 0o644))

	inst, err := NewInstance(InstanceOptions{Title: "d", Path: workDir, Program: "claude", Direct: true})
	require.NoError(t, err)
	inst.started = true
	inst.SetClaudeAccount("work", root, false)

	model, stamp, ok := inst.ComputeModel()
	require.True(t, ok)
	assert.Equal(t, "claude-opus-4-8", model)

	inst.SetModelMeta(model, stamp)
	_, _, ok = inst.ComputeModel()
	assert.False(t, ok, "unchanged transcript must short-circuit")

	inst.status = Paused
	_, _, ok = inst.ComputeModel()
	assert.False(t, ok, "paused instances are never extracted")
}

// sanitizeCWDForTest mirrors transcript.sanitizeCWD (unexported there): every
// rune outside [A-Za-z0-9] becomes '-'.
func sanitizeCWDForTest(cwd string) string {
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return '-'
	}, cwd)
}

// TestStorageRoundTrip_Model asserts the transcript-derived model survives a
// save/load cycle (paused sessions keep their chip), and that its absence in
// old state files deserializes to the quiet default.
func TestStorageRoundTrip_Model(t *testing.T) {
	store := newTestStorage(t)

	a := newPausedInstance(t, "alpha")
	a.SetModelMeta("claude-opus-4-7", transcript.Stamp{Path: "/t", Size: 1})
	b := newPausedInstance(t, "beta")
	require.NoError(t, store.SaveInstances([]*Instance{a, b}))

	got, err := store.LoadInstances(context.Background())
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "claude-opus-4-7", got[0].ModelInfo(), "persisted model must survive the round-trip")
	assert.Empty(t, got[1].ModelInfo(), "an instance without a model loads clean")
}
