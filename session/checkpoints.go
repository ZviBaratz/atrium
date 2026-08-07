package session

// Per-session checkpoint enumeration: the native file-history checkpoints Claude
// Code records in its transcript before each prompt. Read-only — Atrium lists
// them and leaves restoring to Claude's own Esc-Esc, which is the only surface
// that can rewind code and conversation together (see #385).

import (
	"context"

	"github.com/ZviBaratz/atrium/session/agent"
	"github.com/ZviBaratz/atrium/session/transcript"
)

// SupportsCheckpoints reports whether this session's agent keeps native
// checkpoints at all. Claude-only, derived from Program on demand like every
// other claude-gated helper here (see pinnedModelFlag, PermissionMode, Effort) —
// Program is immutable and already persisted, so there is nothing to store.
func (i *Instance) SupportsCheckpoints() bool {
	return agent.Resolve(i.Program).Key == agent.KeyClaude
}

// LoadCheckpoints enumerates the session's native checkpoints.
//
// Unlike ComputeModel/ComputeUsage this is not a poll-path reader: it reads the
// whole transcript rather than a stamp-gated tail, so it runs once per overlay
// open from a tea.Cmd goroutine. That is also why it takes ctx explicitly
// instead of deriving one from i.baseContext() — the lifetime belongs to the
// overlay that asked, not to the session.
//
// Paused sessions are deliberately included. Pause removes the worktree but not
// the transcripts it produced, and WorkingDir() keeps returning the worktree
// path across a pause, so the reading stays correct (the same property
// ContextSourceKey relies on).
func (i *Instance) LoadCheckpoints(ctx context.Context) (transcript.Checkpoints, error) {
	if !i.Started() {
		return transcript.Checkpoints{}, nil
	}
	return transcript.LoadCheckpoints(ctx, i.Program, i.WorkingDir(),
		transcript.Options{Root: i.claudeConfigDir})
}
