package overlay

import (
	"testing"

	"github.com/ZviBaratz/atrium/keys"

	"charm.land/lipgloss/v2"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// shiftEnterGlyph and ctrlJGlyph are the two clauses under test. Spelled once so a
// footer reworded in one place and asserted in another cannot pass by accident.
const (
	shiftEnterGlyph = "⇧↵"
	ctrlJGlyph      = "⌃J"
)

// TestComposerFooters_AreCapabilityHonest is #396's third acceptance criterion:
// ⇧↵ is shown only when the terminal has confirmed it will work, and otherwise the
// hint leads with ⌃J.
//
// Both directions are asserted, and that is what makes it a guard rather than a
// formality. Only checking the legacy branch would be satisfied by a footer that
// never names ⇧↵ at all — which is honest but throws away the whole point of the
// change — and only checking the capable branch would be satisfied by today's
// unconditional string.
//
// The renders are ANSI-stripped: the footers are styled, so a substring assertion
// over the raw output would be measuring the escape codes as much as the text.
func TestComposerFooters_AreCapabilityHonest(t *testing.T) {
	for _, tc := range []struct {
		name          string
		disambiguates bool
	}{
		{"terminal answered the query", true},
		{"terminal never answered", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Cleanup(keys.SetTerminalDisambiguates(tc.disambiguates))

			for _, composer := range []struct {
				name string
				make func(t *testing.T) *TextInputOverlay
			}{
				{"create form prompt", func(t *testing.T) *TextInputOverlay {
					o := promptFocusedForm(t)
					// Wide enough that the ladder is never truncated for width, so the
					// only thing that can drop the clause is the capability gate.
					o.SetSize(200, 60)
					return o
				}},
				{"quick send", func(*testing.T) *TextInputOverlay {
					o := NewQuickSendOverlay("Send to foo")
					o.SetSize(200, 60)
					return o
				}},
			} {
				t.Run(composer.name, func(t *testing.T) {
					out := xansi.Strip(composer.make(t).Render())

					assert.Contains(t, out, ctrlJGlyph,
						"⌃J works on every terminal, so it is named in both branches")
					if tc.disambiguates {
						assert.Contains(t, out, shiftEnterGlyph,
							"a terminal that disambiguates modified keys really does send ⇧↵")
					} else {
						assert.NotContains(t, out, shiftEnterGlyph,
							"without disambiguation ⇧↵ is byte-identical to ↵, so naming it is a lie")
					}
				})
			}
		})
	}
}

// TestPromptFocusHelpLegacy_IsDerivedFromTheOptimisticLadder pins the relationship
// rather than the strings, because the strings are the thing most likely to be
// edited. If a later change re-authors the legacy ladder as its own literal, the
// two can drift — one gains a clause the other does not — and every other guard
// here would stay green.
func TestPromptFocusHelpLegacy_IsDerivedFromTheOptimisticLadder(t *testing.T) {
	require.Equal(t, promptFocusHelp[1:], promptFocusHelpLegacy,
		"the legacy ladder is the optimistic one minus its ⇧↵ rung, by construction")
	require.Contains(t, promptFocusHelp[0], shiftEnterGlyph,
		"and the rung it drops must be the one that names ⇧↵ — otherwise the slice "+
			"is dropping a rung for a reason nothing states")
	for i, rung := range promptFocusHelpLegacy {
		assert.NotContainsf(t, rung, shiftEnterGlyph,
			"legacy rung %d must not name a key the terminal cannot send: %q", i, rung)
	}
}

// TestQuickSendFooter_FitsTheFloor gives that one-line footer the width bound the
// laddered ones get from TestHintLadders_NarrowestRungFitsTheFloor. It never goes
// through fitHint — it is a bare string — so until now nothing measured it at all,
// and adding a second spelling is the moment that gap starts to matter.
//
// Both spellings are checked even though the legacy one is strictly narrower: it is
// the shorter string *today*, and a guard that assumes that stops holding the moment
// someone lengthens it.
func TestQuickSendFooter_FitsTheFloor(t *testing.T) {
	for name, hint := range map[string]string{
		"quickSendHelp":       quickSendHelp,
		"quickSendHelpLegacy": quickSendHelpLegacy,
	} {
		assert.LessOrEqualf(t, lipgloss.Width(hint), claudeFieldInnerWidth,
			"%s is %d cells, past the %d an 80-col terminal gives: %q",
			name, lipgloss.Width(hint), claudeFieldInnerWidth, hint)
	}
}
