package session

import "github.com/ZviBaratz/atrium/session/agent"

// PinnedPermissionMode returns the --permission-mode flag value parsed from
// Program ("" = none). Claude-only: other agents don't use this flag.
// Derived on demand — Program is immutable and already persisted.
func (i *Instance) PinnedPermissionMode() string {
	if agent.Resolve(i.Program).Key != agent.KeyClaude {
		return ""
	}
	return agent.PermissionModeFlag(i.Program)
}

// PermissionModeInfo returns the permission-mode chip's value: the live mode
// detected from the footer when known, else the --permission-mode flag ("" =
// unknown). Mirrors ModelInfo — the flag fills the gap before the first
// detection, then footer truth wins, including over an in-session switch away
// from the flag (the bug this fixes). A detected "default" is a real value: the
// renderer hides the chip for it, so switching back to normal clears the chip.
func (i *Instance) PermissionModeInfo() string {
	if i.runtimeMode != "" {
		return i.runtimeMode
	}
	return i.PinnedPermissionMode()
}

// ComputeMode reads the live permission mode the tmux Poll detected, off the
// main thread (the metadata-poll goroutine). ok=false means nothing to apply:
// unstarted/paused, nothing detected yet, or unchanged from the cached value.
func (i *Instance) ComputeMode() (mode string, ok bool) {
	ts := i.tmux()
	if !i.isStarted() || i.Paused() || ts == nil {
		return "", false
	}
	mode = ts.RuntimePermissionMode()
	if mode == "" || mode == i.runtimeMode {
		return "", false
	}
	return mode, true
}

// SetModeMeta records a detected permission mode. Main thread only (like
// SetModelMeta).
func (i *Instance) SetModeMeta(mode string) { i.runtimeMode = mode }
