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
// else the pinned flag; pinned reflects the flag either way.
func TestModelInfo(t *testing.T) {
	inst, err := NewInstance(InstanceOptions{Title: "m", Path: ".", Program: "claude --model fable"})
	require.NoError(t, err)

	model, pinned := inst.ModelInfo()
	assert.Equal(t, "fable", model, "flag fallback before any transcript truth")
	assert.True(t, pinned)

	inst.SetModelMeta("claude-fable-5", transcript.Stamp{Path: "/x", Size: 1})
	model, pinned = inst.ModelInfo()
	assert.Equal(t, "claude-fable-5", model, "transcript truth wins over the flag")
	assert.True(t, pinned, "an in-session switch keeps the pinned styling")

	bare, err := NewInstance(InstanceOptions{Title: "b", Path: ".", Program: "claude"})
	require.NoError(t, err)
	model, pinned = bare.ModelInfo()
	assert.Empty(t, model)
	assert.False(t, pinned)

	bare.SetModelMeta("claude-opus-4-7", transcript.Stamp{Path: "/y", Size: 1})
	model, pinned = bare.ModelInfo()
	assert.Equal(t, "claude-opus-4-7", model)
	assert.False(t, pinned, "transcript-known but unpinned renders dim")
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

	model, _ := inst.ModelInfo()
	assert.Equal(t, "claude-opus-4-7", model, "empty extraction must not clear the last truth")
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
	model, _ := got[0].ModelInfo()
	assert.Equal(t, "claude-opus-4-7", model, "persisted model must survive the round-trip")
	model, _ = got[1].ModelInfo()
	assert.Empty(t, model, "an instance without a model loads clean")
}
