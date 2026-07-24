package overlay

import (
	"testing"

	"github.com/ZviBaratz/atrium/config"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
)

// claudeField is the shared surface of the three override fields the item-1
// hints exercise: focus + render.
type claudeField interface {
	Focus()
	Render() string
}

// claudeProfile builds a one-profile slice whose single claude program is
// program — the seed for the create-form-driven hint tests.
func claudeProfile(program string) []config.Profile {
	return []config.Profile{{Name: "c", Program: program}}
}

// fieldForStop returns the override field a claude override stop drives, so the
// hint tests can loop over the three uniformly.
func fieldForStop(ov *TextInputOverlay, stop focusStop) claudeField {
	switch stop {
	case stopModel:
		return ov.modelField
	case stopEffort:
		return ov.effortField
	case stopMode:
		return ov.modeField
	default:
		return nil
	}
}

// TestClaudeFields_NoOpChipLabeledInherit pins the settled call: the first chip
// of every claude override field reads "inherit", never "default" — the word
// collides with the permission-mode enum's real "default" and the list's
// no-chip filter vocabulary.
func TestClaudeFields_NoOpChipLabeledInherit(t *testing.T) {
	for _, tc := range []struct {
		name string
		f    claudeField
	}{
		{"model", NewModelField()},
		{"effort", NewEffortField()},
		{"mode", NewModeField()},
	} {
		out := xansi.Strip(tc.f.Render()) // unfocused: chips only, no hint
		assert.Contains(t, out, "inherit", "%s no-op chip must read 'inherit'", tc.name)
		assert.NotContains(t, out, "default", "%s must not label the no-op chip 'default'", tc.name)
	}
}

// TestModelField_InheritOrDefaultTypedMeansNoOverride locks the Value() contract:
// a user who types either "inherit" (the new chip word) or "default" (the old one,
// and never a valid --model) means "no override".
func TestModelField_InheritOrDefaultTypedMeansNoOverride(t *testing.T) {
	for _, word := range []string{"inherit", "default"} {
		o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "claude")
		o.focusStop(stopModel)
		o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(word)})
		assert.Equal(t, "", o.GetModel(), "typed %q must contribute no override", word)
	}
}

// TestClaudeFields_HintNoPin_ClaudesDefault: with a claude profile that pins
// nothing, the focused no-op-chip hint names the only source Atrium can see —
// claude's own resolved default.
func TestClaudeFields_HintNoPin_ClaudesDefault(t *testing.T) {
	for _, stop := range []focusStop{stopModel, stopEffort, stopMode} {
		ov := NewSessionCreateOverlay(claudeProfile("claude"), nil, []string{t.TempDir()}, "claude")
		ov.focusStop(stop)
		out := xansi.Strip(fieldForStop(ov, stop).Render())
		assert.Contains(t, out, "claude's default", "stop %v with no pin", stop)
	}
}

// TestModeField_HintSharedPin_EchoesLabel: an in-enum mode can be named, so the
// shared-pin hint echoes it — as its display label, never the raw enum value.
func TestModeField_HintSharedPin_EchoesLabel(t *testing.T) {
	ov := NewSessionCreateOverlay(claudeProfile("claude --permission-mode acceptEdits"), nil, []string{t.TempDir()}, "claude")
	ov.focusStop(stopMode)
	out := xansi.Strip(ov.modeField.Render())
	assert.Contains(t, out, "program pins accept-edits")
}

// TestEffortField_HintSharedPin_EchoesValue: an offered level can be named, so the
// shared-pin hint echoes it.
func TestEffortField_HintSharedPin_EchoesValue(t *testing.T) {
	ov := NewSessionCreateOverlay(claudeProfile("claude --effort high"), nil, []string{t.TempDir()}, "claude")
	ov.focusStop(stopEffort)
	out := xansi.Strip(ov.effortField.Render())
	assert.Contains(t, out, "program pins high")
}

// TestModelField_HintSharedPin_DoesNotEchoValue: model values are unbounded
// (64-char vendor IDs), so the shared-pin hint stays generic and never echoes the
// pinned value.
func TestModelField_HintSharedPin_DoesNotEchoValue(t *testing.T) {
	ov := NewSessionCreateOverlay(claudeProfile("claude --model claude-opus-4-6"), nil, []string{t.TempDir()}, "claude")
	ov.focusStop(stopModel)
	out := xansi.Strip(ov.modelField.Render())
	assert.Contains(t, out, "program pins it", "model names the source without the value")
	assert.NotContains(t, out, "claude-opus-4-6", "model must not echo the unbounded pinned value")
}

// TestClaudeFields_HintNoProfiles_SaysProgramNotProfile: with no profiles at all the
// pin lives in the configured default_program, and GetVariants falls back to it — so
// the hint must not credit a profile that does not exist. This is the whole reason
// the wording is "program pins", not "profile pins".
func TestClaudeFields_HintNoProfiles_SaysProgramNotProfile(t *testing.T) {
	ov := NewSessionCreateOverlay(nil, nil, []string{t.TempDir()}, "claude --effort high")
	ov.focusStop(stopEffort)
	out := xansi.Strip(ov.effortField.Render())
	assert.Contains(t, out, "program pins high")
	assert.NotContains(t, out, "profile", "no profile exists, so the hint must not name one")
}

// TestEffortField_HintUnvalidatedPin_DoesNotEchoValue: agent.EffortFlag is
// deliberately unvalidated, so a profile can pin a level Atrium does not offer, of
// any length. Echoing it would overflow the shared width budget, so such a pin is
// reported as a pin without being named — and must never read as "claude's default",
// which is the opposite of the truth.
func TestEffortField_HintUnvalidatedPin_DoesNotEchoValue(t *testing.T) {
	const bogus = "experimental-max-reasoning"
	ov := NewSessionCreateOverlay(claudeProfile("claude --effort "+bogus), nil, []string{t.TempDir()}, "claude")
	ov.focusStop(stopEffort)
	out := xansi.Strip(ov.effortField.Render())
	assert.Contains(t, out, "program pins it")
	assert.NotContains(t, out, bogus, "an out-of-enum level must not be echoed")
	assert.NotContains(t, out, "claude's default", "a pin Atrium cannot name is still a pin")
	assert.LessOrEqual(t, lipgloss.Width(ov.effortField.Render()), claudeFieldInnerWidth,
		"the unnamed fallback is what keeps an unbounded pin inside the width budget")
}

// TestModeField_HintOutOfEnumPin_StillReadsAsPinned: PermissionModeFlag drops a mode
// outside Atrium's snapshot, which would make a pinned field claim "claude's
// default". The hint reads the raw pin (agent.PermissionModePin) so it reports a pin
// it cannot name instead of asserting the opposite.
func TestModeField_HintOutOfEnumPin_StillReadsAsPinned(t *testing.T) {
	ov := NewSessionCreateOverlay(claudeProfile("claude --permission-mode reviewEdits"), nil, []string{t.TempDir()}, "claude")
	ov.focusStop(stopMode)
	out := xansi.Strip(ov.modeField.Render())
	assert.Contains(t, out, "program pins it")
	assert.NotContains(t, out, "claude's default", "a mode Atrium cannot name is still pinned")
	assert.NotContains(t, out, "reviewEdits", "an unrecognised mode must not be echoed")
}

// TestEffortField_HintMixedPins_Varies: two selected claude profiles pinning
// different levels can't be summarized by one value, so the hint says so.
func TestEffortField_HintMixedPins_Varies(t *testing.T) {
	profiles := []config.Profile{
		{Name: "a", Program: "claude --effort high"},
		{Name: "b", Program: "claude --effort low"},
	}
	ov := NewSessionCreateOverlay(profiles, nil, []string{t.TempDir()}, "claude")
	// Default counts select only profile "a" (×1); raise "b" to ×1 so both claude
	// variants are in the batch and their pins disagree.
	ov.focusStop(stopVariants)
	ov.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRight}) // cursor → profile "b"
	vpPlus(ov)                                        // "b" 0 → 1
	ov.focusStop(stopEffort)
	out := xansi.Strip(ov.effortField.Render())
	assert.Contains(t, out, "varies by profile")
}

// TestClaudeFields_HintOnlyOnNoOpChip: the hint explains the no-op chip, so it
// shows there and vanishes once the cursor moves onto a real value.
func TestClaudeFields_HintOnlyOnNoOpChip(t *testing.T) {
	for _, stop := range []focusStop{stopModel, stopEffort, stopMode} {
		ov := NewSessionCreateOverlay(claudeProfile("claude"), nil, []string{t.TempDir()}, "claude")
		ov.focusStop(stop)
		onNoOp := xansi.Strip(fieldForStop(ov, stop).Render())
		ov.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRight}) // move off the no-op chip
		offNoOp := xansi.Strip(fieldForStop(ov, stop).Render())
		assert.Contains(t, onNoOp, "claude's default", "stop %v: hint present on the no-op chip", stop)
		assert.NotContains(t, offNoOp, "claude's default", "stop %v: hint gone off the no-op chip", stop)
	}
}

// TestClaudeFields_HintHiddenWhenUnfocused: the hint is a focus affordance, so a
// blurred field shows none of it.
func TestClaudeFields_HintHiddenWhenUnfocused(t *testing.T) {
	for _, stop := range []focusStop{stopModel, stopEffort, stopMode} {
		ov := NewSessionCreateOverlay(claudeProfile("claude"), nil, []string{t.TempDir()}, "claude")
		// Do not focus the field: focus starts on the directory picker.
		out := xansi.Strip(fieldForStop(ov, stop).Render())
		assert.NotContains(t, out, "claude's default", "stop %v: no hint while unfocused", stop)
	}
}
