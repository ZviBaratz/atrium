package tmux

import "testing"

// A fresh session has no pending kill request; the flag only flips when the
// in-session Ctrl+X is intercepted during an attach.
func TestKillRequestedDefaultsFalse(t *testing.T) {
	ts := NewTmuxSession("kill-flag-test", "echo")
	if ts.KillRequested() {
		t.Fatal("expected KillRequested to be false on a fresh session")
	}
}
