package overlay

import (
	"testing"

	"github.com/ZviBaratz/atrium/config"
)

func claudeProfiles() []config.Profile {
	return []config.Profile{{Name: "claude", Program: "claude"}}
}

func TestCreateForm_EffortField_PresentForClaude(t *testing.T) {
	ov := NewSessionCreateOverlay(claudeProfiles(), nil, []string{t.TempDir()}, "claude", nil)
	if ov.effortField == nil {
		t.Fatal("effort field should exist for a claude program")
	}
	if ov.GetEffort() != "" {
		t.Errorf("fresh form GetEffort() = %q, want \"\"", ov.GetEffort())
	}
	if ov.indexOfStop(stopEffort) < 0 {
		t.Error("stopEffort should be in the focus ring for a claude profile")
	}
}

func TestCreateForm_EffortField_SelectionReadOut(t *testing.T) {
	ov := NewSessionCreateOverlay(claudeProfiles(), nil, []string{t.TempDir()}, "claude", nil)
	ov.focusStop(stopEffort)
	ov.HandleKeyPress(keyMsg("right")) // default -> low
	if got := ov.GetEffort(); got != "low" {
		t.Errorf("GetEffort() after Right = %q, want \"low\"", got)
	}
}

func TestCreateForm_EffortField_AbsentForNonClaude(t *testing.T) {
	profiles := []config.Profile{{Name: "aider", Program: "aider"}}
	ov := NewSessionCreateOverlay(profiles, nil, []string{t.TempDir()}, "aider", nil)
	if ov.effortField != nil {
		t.Error("effort field should be nil when no candidate program is claude")
	}
	if ov.GetEffort() != "" {
		t.Errorf("GetEffort() with no field = %q, want \"\"", ov.GetEffort())
	}
	if ov.indexOfStop(stopEffort) >= 0 {
		t.Error("stopEffort should not be in the focus ring for a non-claude form")
	}
}

func TestCreateForm_EffortField_DisabledForNonClaudeProfile(t *testing.T) {
	profiles := []config.Profile{
		{Name: "claude", Program: "claude"},
		{Name: "aider", Program: "aider"},
	}
	ov := NewSessionCreateOverlay(profiles, nil, []string{t.TempDir()}, "claude", nil)
	selectOnlyNonClaude(ov) // Claude ×0, Aider ×1 → no claude variant selected
	if !ov.effortField.Disabled() {
		t.Error("effort field should be disabled when no claude variant is selected")
	}
	if ov.GetEffort() != "" {
		t.Errorf("disabled GetEffort() = %q, want \"\"", ov.GetEffort())
	}
}
