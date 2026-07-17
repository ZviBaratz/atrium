package overlay

import (
	"strings"
	"testing"
)

func TestPromptHistoryOverlay_SelectInsertsCursorText(t *testing.T) {
	p := NewPromptHistoryOverlay([]string{"newest", "middle", "oldest"})
	// Default cursor on the newest (index 0).
	if got := p.SelectedText(); got != "newest" {
		t.Fatalf("default SelectedText = %q, want newest", got)
	}
	// Move down twice → oldest, then pick.
	p.HandleKeyPress(key("down"))
	p.HandleKeyPress(key("down"))
	if !p.HandleKeyPress(key("enter")) {
		t.Fatal("enter should close the overlay")
	}
	if !p.Selected() {
		t.Error("enter must mark the overlay selected")
	}
	if got := p.SelectedText(); got != "oldest" {
		t.Errorf("SelectedText after two downs = %q, want oldest", got)
	}
}

func TestPromptHistoryOverlay_EscCancelsWithoutSelecting(t *testing.T) {
	p := NewPromptHistoryOverlay([]string{"a", "b"})
	if !p.HandleKeyPress(key("esc")) {
		t.Fatal("esc should close")
	}
	if p.Selected() {
		t.Error("esc must not select")
	}
}

func TestPromptHistoryOverlay_CursorClamps(t *testing.T) {
	p := NewPromptHistoryOverlay([]string{"a", "b"})
	p.HandleKeyPress(key("up")) // already at top
	if got := p.SelectedText(); got != "a" {
		t.Errorf("up at top = %q, want a", got)
	}
	p.HandleKeyPress(key("down"))
	p.HandleKeyPress(key("down")) // past the end
	if got := p.SelectedText(); got != "b" {
		t.Errorf("down past end = %q, want b", got)
	}
}

func TestPromptHistoryOverlay_ClearArmsReadOnce(t *testing.T) {
	p := NewPromptHistoryOverlay([]string{"a"})
	if p.HandleKeyPress(key("x")) {
		t.Fatal("x must not close the overlay (it arms a clear)")
	}
	if !p.ClearRequested() {
		t.Error("x must arm ClearRequested")
	}
	if p.ClearRequested() {
		t.Error("ClearRequested must be read-once")
	}
}

func TestPromptHistoryOverlay_EmptyEnterIsNoop(t *testing.T) {
	p := NewPromptHistoryOverlay(nil)
	if p.HandleKeyPress(key("enter")) {
		t.Error("enter on empty history must not close")
	}
	if p.Selected() {
		t.Error("enter on empty history must not select")
	}
	if !strings.Contains(p.Render(), "no prompts yet") {
		t.Error("empty history should render the empty hint")
	}
}
