package session

// Per-session reasoning effort: which effort level a claude session is actually
// running at. Two sources feed it, the same intent-then-truth split the model and
// permission-mode chips use — an --effort flag pinned in Program (intent,
// available before the first turn) and the level claude's hooks report for a
// resolved turn (truth, survives an in-session /effort switch; see
// tmux.Session.RuntimeEffort). EffortInfo arbitrates for the UI.

import "github.com/ZviBaratz/atrium/session/agent"

// PinnedEffort returns the --effort flag value parsed from Program ("" = none).
// Claude-only: other agents don't use this flag. Derived on demand — Program is
// immutable and already persisted.
func (i *Instance) PinnedEffort() string {
	if agent.Resolve(i.Program).Key != agent.KeyClaude {
		return ""
	}
	return agent.EffortFlag(i.Program)
}

// EffortInfo returns the effort chip's value: the level claude's hooks reported for the
// last resolved turn when known, else the --effort flag ("" = unknown). Mirrors ModelInfo /
// PermissionModeInfo — the flag fills the gap before the first turn, then runtime truth
// wins. Truth is strictly better than the flag here, and in two ways the flag can never
// match: it survives an in-session /effort switch, and it knows the level a session with no
// --effort inherited from settings.json or the model default.
//
// Only PinnedEffort gates on claude; the runtime path needs no gate, because effort reaches
// runtimeEffort solely through the hook record and hook settings are written for claude
// sessions alone (tmux.ensureHookSettings). A non-claude session therefore has no runtime
// level to prefer, and falls through to the gated flag.
func (i *Instance) EffortInfo() string {
	if i.runtimeEffort != "" {
		return i.runtimeEffort
	}
	return i.PinnedEffort()
}

// ComputeEffort reads the effort level the tmux Poll lifted off the hook record, off the
// main thread (the metadata-poll goroutine). ok=false means nothing to apply:
// unstarted/paused, nothing reported yet, or unchanged from the cached value.
func (i *Instance) ComputeEffort() (effort string, ok bool) {
	ts := i.tmux()
	if !i.isStarted() || i.Paused() || ts == nil {
		return "", false
	}
	effort = ts.RuntimeEffort()
	if effort == "" || effort == i.runtimeEffort {
		return "", false
	}
	return effort, true
}

// SetEffortMeta records a hook-reported effort level. Main thread only (like SetModeMeta).
func (i *Instance) SetEffortMeta(effort string) { i.runtimeEffort = effort }
