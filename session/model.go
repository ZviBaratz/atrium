package session

// Per-session model identity: which Claude model a session is using. Two
// sources feed it — the transcript (ground truth, survives in-session /model
// switches; see transcript.LatestModel) and a --model flag pinned in Program
// (intent, available before the first turn). ModelInfo arbitrates for the UI.

import (
	"github.com/ZviBaratz/atrium/session/agent"
	"github.com/ZviBaratz/atrium/session/transcript"
)

// pinnedModelFlag returns the value of a --model flag in program ("" = none).
// Claude-only by design: aider takes --model with provider/model syntax that
// would render as a noisy chip, and other agents' flags are unvetted. The
// extraction itself is agent.ModelFlag, next to WithModelFlag which composes
// the same pin at session creation.
func pinnedModelFlag(program string) string {
	if agent.Resolve(program).Key != agent.KeyClaude {
		return ""
	}
	return agent.ModelFlag(program)
}

// PinnedModel returns the --model flag value parsed from Program ("" = none).
// Derived on demand — Program is immutable and already persisted.
func (i *Instance) PinnedModel() string { return pinnedModelFlag(i.Program) }

// ModelInfo returns the model chip's value — transcript truth when known, else
// the pinned flag — and whether the model is pinned via flag (accent vs dim).
// When an in-session /model switch diverges from the pin, the chip keeps the
// pinned styling but shows the true model: truth wins over the flag.
func (i *Instance) ModelInfo() (model string, pinned bool) {
	flag := i.PinnedModel()
	if i.modelID != "" {
		return i.modelID, flag != ""
	}
	return flag, flag != ""
}

// SetModelMeta records a model-extraction result. Main thread only (like
// SetDiffStats). model == "" advances the stamp — so the parsed-but-empty
// window isn't re-read next tick — without clearing the last known truth.
func (i *Instance) SetModelMeta(model string, stamp transcript.Stamp) {
	if model != "" {
		i.modelID = model
	}
	i.modelStamp = stamp
}

// ComputeModel re-extracts the transcript model off the main thread (the
// metadata-poll goroutine), gated by the memoized stamp so an idle session
// costs one ReadDir + Stat per tick. ok=false means nothing to apply:
// unstarted/paused, non-claude program, transcript unavailable, or unchanged.
func (i *Instance) ComputeModel() (model string, stamp transcript.Stamp, ok bool) {
	if !i.isStarted() || i.Paused() {
		return "", transcript.Stamp{}, false
	}
	m, s, err := transcript.LatestModel(i.Program, i.WorkingDir(), i.modelStamp,
		transcript.Options{Root: i.claudeConfigDir})
	if err != nil || s.Equal(i.modelStamp) {
		return "", transcript.Stamp{}, false
	}
	return m, s, true
}
