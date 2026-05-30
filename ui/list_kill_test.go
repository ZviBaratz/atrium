package ui

import "testing"

// KillInstance must remove the *targeted* instance even when it is not the
// selected one. Killing an instance positioned before the selection keeps the
// same logical instance selected (its index shifts down by one).
func TestKillInstanceRemovesTargetBeforeSelection(t *testing.T) {
	l := newTestList("a", "b", "c")
	l.SetSelectedInstance(2) // select "c"

	l.KillInstance(l.GetInstances()[0]) // kill "a", before the selection

	if got := l.NumInstances(); got != 2 {
		t.Fatalf("expected 2 instances after kill, got %d", got)
	}
	if got := l.GetSelectedInstance().Title; got != "c" {
		t.Fatalf("expected selection to remain on 'c', got %q", got)
	}
}

// Killing a target positioned after the selection leaves the selection untouched.
func TestKillInstanceRemovesTargetAfterSelection(t *testing.T) {
	l := newTestList("a", "b", "c")
	l.SetSelectedInstance(1) // select "b"

	l.KillInstance(l.GetInstances()[2]) // kill "c", after the selection

	if got := l.NumInstances(); got != 2 {
		t.Fatalf("expected 2 instances after kill, got %d", got)
	}
	if got := l.GetSelectedInstance().Title; got != "b" {
		t.Fatalf("expected selection to remain on 'b', got %q", got)
	}
}
