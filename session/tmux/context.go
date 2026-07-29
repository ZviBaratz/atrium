package tmux

import "fmt"

// context.go — feeding the in-session context bar. Atrium pushes each session's
// live, pre-rendered header string into tmux user options (@atrium_name /
// @atrium_left); the managed config's status-line format and set-titles-string read
// them. The string is composed by the ui layer (tmux #[...] markup), so this file
// stays presentation-agnostic.
//
// The push is split in two so it can leave the update thread (#380). ArmContext
// owns the cache — ctxName/ctxLeft/ctxSet are main-thread-only fields, documented
// as such where they are declared — and PushContext owns the subprocess, touching
// no shared state, so it is safe on a background goroutine.

// ArmContext records the context strings this session should be showing and
// reports whether they differ from the last successful push. Main-thread only,
// like the fields it writes.
//
// It arms optimistically: the cache is updated before the push is known to have
// succeeded, so an unchanged tick costs a string comparison rather than a
// subprocess. A failed push calls ClearContextCache to undo that, which is what
// makes the next tick retry instead of believing a push that never landed.
func (t *Session) ArmContext(name, left string) bool {
	if t.ctxSet && t.ctxName == name && t.ctxLeft == left {
		return false
	}
	t.ctxName, t.ctxLeft, t.ctxSet = name, left, true
	return true
}

// ClearContextCache un-arms the cache after a failed push, so the next tick tries
// again rather than short-circuiting on a value that never reached tmux.
// Main-thread only, like ArmContext.
func (t *Session) ClearContextCache() { t.ctxSet = false }

// PushContext writes the context-bar strings into this session's tmux user options
// in a single batched command, then refresh-client -S so the status line repaints.
// name also drives the terminal title via set-titles-string.
//
// It reads no cached state and writes none, so it is safe to call from a
// background goroutine — which is the point: this used to run on the update thread
// once per changed session.
func (t *Session) PushContext(name, left string) error {
	target := t.snapshotName()
	ctx, cancel := t.opContext()
	defer cancel()
	cmd := tmuxCommand(ctx,
		"set-option", "-t", target, "@atrium_name", name, ";",
		"set-option", "-t", target, "@atrium_left", left, ";",
		"refresh-client", "-S",
	)
	if err := t.cmdExec.Run(cmd); err != nil {
		return fmt.Errorf("failed to set tmux session context: %w", err)
	}
	return nil
}

// SetContext arms and pushes in one call, for the synchronous callers that need
// the bar to land before they hand the terminal over (the attach cycle). Every
// periodic caller should use ArmContext + PushContext so the subprocess runs off
// the update thread.
func (t *Session) SetContext(name, left string) error {
	if !t.ArmContext(name, left) {
		return nil
	}
	if err := t.PushContext(name, left); err != nil {
		t.ClearContextCache()
		return err
	}
	return nil
}
