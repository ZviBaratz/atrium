package overlay

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/ZviBaratz/atrium/config"
	"github.com/stretchr/testify/assert"
)

// lastLine returns the final line of a rendered block — the chip row, below the label
// line and the blank separator.
func lastLine(s string) string {
	lines := strings.Split(s, "\n")
	return lines[len(lines)-1]
}

func vpProfiles() []config.Profile {
	return []config.Profile{
		{Name: "claude", Program: "claude"},
		{Name: "codex", Program: "codex"},
		{Name: "aider", Program: "aider"},
	}
}

// An untouched picker spawns exactly one session of the default (first) profile —
// byte-identical to the pre-#387 single-session create.
func TestVariantPicker_DefaultsToOneOfDefault(t *testing.T) {
	vp := NewVariantPicker(vpProfiles())
	assert.Equal(t, 1, vp.Total())
	assert.Equal(t, []string{"claude"}, vp.Variants())
}

// Up/+ raise the focused count; Right moves between profiles; Variants flattens in
// picker order with each profile repeated by its count.
func TestVariantPicker_IncrementAndOrder(t *testing.T) {
	vp := NewVariantPicker(vpProfiles())
	vp.HandleKeyPress(key("up")) // claude 1 -> 2
	vp.HandleKeyPress(key("right"))
	vp.HandleKeyPress(key("+")) // codex 0 -> 1
	assert.Equal(t, 3, vp.Total())
	assert.Equal(t, []string{"claude", "claude", "codex"}, vp.Variants())
}

// The unshifted "=" is accepted as an alias for "+".
func TestVariantPicker_EqualsAliasesPlus(t *testing.T) {
	vp := NewVariantPicker(vpProfiles())
	vp.HandleKeyPress(key("="))
	assert.Equal(t, 2, vp.Total())
}

// The total can never drop to zero: decrementing the sole remaining session is
// refused, so a submit always has at least one session to create.
func TestVariantPicker_FloorOfOne(t *testing.T) {
	vp := NewVariantPicker(vpProfiles())
	vp.HandleKeyPress(key("down")) // claude 1 -> refused (would zero the batch)
	assert.Equal(t, 1, vp.Total())
	assert.Equal(t, []string{"claude"}, vp.Variants())
}

// Decrement below a profile with siblings works; the floor only guards the global zero.
func TestVariantPicker_DecrementWithSiblings(t *testing.T) {
	vp := NewVariantPicker(vpProfiles())
	vp.HandleKeyPress(key("right"))
	vp.HandleKeyPress(key("+")) // codex 0 -> 1 (total 2: claude1 codex1)
	vp.HandleKeyPress(key("left"))
	vp.HandleKeyPress(key("down")) // claude 1 -> 0 (allowed, codex keeps total at 1)
	assert.Equal(t, 1, vp.Total())
	assert.Equal(t, []string{"codex"}, vp.Variants())
}

// The per-profile count is capped so key-repeat cannot produce an absurd row.
func TestVariantPicker_PerProfileCap(t *testing.T) {
	vp := NewVariantPicker(vpProfiles())
	for i := 0; i < variantCountMax+5; i++ {
		vp.HandleKeyPress(key("+"))
	}
	assert.Equal(t, variantCountMax, vp.Total())
}

// The claude-only overrides apply iff some selected variant is claude.
func TestVariantPicker_SelectedIncludesClaude(t *testing.T) {
	vp := NewVariantPicker(vpProfiles())
	assert.True(t, vp.SelectedIncludesClaude(), "default is claude x1")

	// Move to codex, +1; move back to claude, -1: claude0 codex1 -> no claude.
	vp.HandleKeyPress(key("right"))
	vp.HandleKeyPress(key("+"))
	vp.HandleKeyPress(key("left"))
	vp.HandleKeyPress(key("down"))
	assert.False(t, vp.SelectedIncludesClaude(), "claude 0, codex 1 -> no claude selected")
}

// A count change clears a stale batch error so the message does not linger after
// the user reacts to it.
func TestVariantPicker_CountChangeClearsError(t *testing.T) {
	vp := NewVariantPicker(vpProfiles())
	vp.SetError("too many")
	vp.HandleKeyPress(key("+"))
	assert.NotContains(t, vp.Render(), "too many")
}

// A generous width renders every chip on one line with no ellipsis marker.
func TestVariantPicker_ChipRowFullWhenFits(t *testing.T) {
	vp := NewVariantPicker(vpProfiles())
	vp.SetWidth(200)
	row := lastLine(vp.Render())
	for _, name := range []string{"claude", "codex", "aider"} {
		assert.Contains(t, row, name, "every chip is shown when the row fits")
	}
	assert.NotContains(t, row, "…", "no marker when nothing is clipped")
}

// When the width is too small for every chip, the row is windowed around the cursor:
// it stays within the width, keeps the focused chip visible, and marks the clipped
// ends with "…" — so a many-profile bake-off never overflows or hides the chip the
// count keys act on.
func TestVariantPicker_ChipRowWindowsAroundCursor(t *testing.T) {
	vp := NewVariantPicker(vpProfiles()) // claude, codex, aider
	vp.HandleKeyPress(key("right"))      // cursor -> codex (the middle chip)

	// Just enough width for the cursor chip plus both "…" markers, but not a neighbor.
	width := lipgloss.Width(vp.chipLabel(1)) + 8 // + two ellipses (1 each) + two separators (3 each)
	vp.SetWidth(width)

	row := lastLine(vp.Render())
	assert.LessOrEqual(t, lipgloss.Width(row), width, "the chip row must fit the width")
	assert.Contains(t, row, "codex", "the focused chip stays visible")
	assert.Contains(t, row, "…", "clipped ends are marked with an ellipsis")
	assert.NotContains(t, row, "claude", "an unaffordable neighbor is clipped")
	assert.NotContains(t, row, "aider", "an unaffordable neighbor is clipped")
}

// Non-navigation keys are not consumed, so they fall through to the form.
func TestVariantPicker_IgnoresOtherKeys(t *testing.T) {
	vp := NewVariantPicker(vpProfiles())
	assert.False(t, vp.HandleKeyPress(textMsg("x")))
	assert.Equal(t, 1, vp.Total(), "an unrelated key must not change counts")
}
