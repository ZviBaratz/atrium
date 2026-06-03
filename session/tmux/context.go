package tmux

import "fmt"

// context.go — feeding the in-session context bar. Atrium pushes each session's
// live, pre-rendered header string into tmux user options (@atrium_name /
// @atrium_left); the managed config's status-line format and set-titles-string read
// them. The string is composed by the ui layer (tmux #[...] markup), so this file
// stays presentation-agnostic.

// SetContext pushes the context-bar strings into this session's tmux user options
// in a single batched command, then refresh-client -S so the status line repaints.
// It is a no-op when the values are unchanged since the last push, so the
// per-second metadata tick costs a string comparison rather than a subprocess when
// nothing moved. name also drives the terminal title via set-titles-string.
func (t *Session) SetContext(name, left string) error {
	if t.ctxSet && t.ctxName == name && t.ctxLeft == left {
		return nil
	}
	target := t.snapshotName()
	cmd := tmuxCommand(
		"set-option", "-t", target, "@atrium_name", name, ";",
		"set-option", "-t", target, "@atrium_left", left, ";",
		"refresh-client", "-S",
	)
	if err := t.cmdExec.Run(cmd); err != nil {
		return fmt.Errorf("failed to set tmux session context: %w", err)
	}
	t.ctxName, t.ctxLeft, t.ctxSet = name, left, true
	return nil
}
