package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ZviBaratz/atrium/session/transcript"
)

// TestSetAskedMeta_FalseClears is the assertion that catches the copy-paste this file's
// shape invites. SetModelMeta, the function SetAskedMeta is modelled on, deliberately
// treats an empty value as "no information" and KEEPS the last known truth. Doing that
// here would latch the question flag on for the life of the session, holding every future
// queued prompt — so a false must clear a previous true.
func TestSetAskedMeta_FalseClears(t *testing.T) {
	inst, err := NewInstance(InstanceOptions{Title: "a", Path: ".", Program: "claude"})
	require.NoError(t, err)

	assert.False(t, inst.EndedAsking(), "a fresh instance has no question outstanding")

	first := transcript.Stamp{Path: "/t", ModTime: time.Now(), Size: 10}
	inst.SetAskedMeta(true, first)
	assert.True(t, inst.EndedAsking())

	later := transcript.Stamp{Path: "/t", ModTime: first.ModTime.Add(time.Second), Size: 20}
	inst.SetAskedMeta(false, later)
	assert.False(t, inst.EndedAsking(),
		"the next turn answers the question: false must CLEAR, not be read as 'no information'")
	assert.True(t, inst.askedStamp.Equal(later), "stamp must advance so the same bytes aren't re-parsed")
}

// TestComputeAsked_DirectSession runs the full derivation without tmux, mirroring
// TestComputeModel_DirectSession: a started direct session's WorkingDir is its Path and
// claudeConfigDir routes the transcript root — the same wiring the poll loop uses.
func TestComputeAsked_DirectSession(t *testing.T) {
	root := t.TempDir()
	workDir := t.TempDir()
	line := `{"type":"assistant","isSidechain":false,"message":{"model":"claude-opus-4-8",` +
		`"content":[{"type":"text","text":"Want me to open the PR, or will you?"}]}}` + "\n"
	dest := filepath.Join(root, "projects", sanitizeCWDForTest(workDir), "s.jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(dest), 0o755))
	require.NoError(t, os.WriteFile(dest, []byte(line), 0o644))

	inst, err := NewInstance(InstanceOptions{Title: "d", Path: workDir, Program: "claude", Direct: true})
	require.NoError(t, err)
	inst.started = true
	inst.SetClaudeAccount("work", root, false)

	asked, stamp, ok := inst.ComputeAsked()
	require.True(t, ok)
	assert.True(t, asked, "the turn ends with a question")

	inst.SetAskedMeta(asked, stamp)
	_, _, ok = inst.ComputeAsked()
	assert.False(t, ok, "unchanged transcript must short-circuit")
	assert.True(t, inst.EndedAsking(), "and the short-circuit must leave the memo standing")

	inst.status = Paused
	_, _, ok = inst.ComputeAsked()
	assert.False(t, ok, "paused instances are never re-derived")
}

// TestComputeAsked_NonClaude pins that agents without a transcript adapter never produce
// a verdict, so codex/gemini/aider sessions keep their existing delivery behaviour.
func TestComputeAsked_NonClaude(t *testing.T) {
	inst, err := NewInstance(InstanceOptions{Title: "c", Path: t.TempDir(), Program: "codex", Direct: true})
	require.NoError(t, err)
	inst.started = true

	_, _, ok := inst.ComputeAsked()
	assert.False(t, ok, "no transcript adapter: nothing to apply")
	assert.False(t, inst.EndedAsking())
}
