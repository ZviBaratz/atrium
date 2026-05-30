package session

import "testing"

// A not-yet-started instance has no tmux session; AttachKillRequested must not
// panic and must report false.
func TestAttachKillRequestedFalseWithoutTmux(t *testing.T) {
	inst, err := NewInstance(InstanceOptions{Title: "kill-req", Path: t.TempDir(), Program: "echo"})
	if err != nil {
		t.Fatalf("failed to create instance: %v", err)
	}
	if inst.AttachKillRequested() {
		t.Fatal("expected AttachKillRequested to be false before any attach")
	}
}
