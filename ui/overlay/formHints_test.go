package overlay

import (
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/config"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
)

// navKeys is the glyph pair the create form's footer owns (createFormHelp's
// "↑↓ select"). The rule the guard below enforces is stated there; this const is
// the string form of it.
const navKeys = "↑↓"

// createFormWithEveryField builds the widest create form there is — two claude
// profiles (so the variant control and all three claude override fields exist) and
// a two-member account pool (so the account section is present, focusable, and in
// its glossed state) — sized to a terminal large enough that fitOverlay neither
// truncates a line nor drops one. That matters: the guard counts a glyph in the
// rendered form, so any clipping would make it measure layout instead of copy.
func createFormWithEveryField(t *testing.T) *TextInputOverlay {
	t.Helper()
	profiles := []config.Profile{{Name: "one", Program: "claude"}, {Name: "two", Program: "claude"}}
	accounts := []config.ClaudeAccount{{Name: "personal", Pool: "work"}, {Name: "team", Pool: "work"}}
	ov := NewSessionCreateOverlay(profiles, accounts, []string{t.TempDir()}, "claude")
	ov.SetSize(200, 60)
	return ov
}

// TestCreateFormHints_FooterOwnsTheNavKeys is the rule #466 settled, as a test: the
// form footer names the navigation keys once, and a per-field hint earns its place on
// a label line only by saying something the footer does not. So the whole rendered
// form contains "↑↓" exactly once — the footer — no matter which field holds focus.
//
// Both departures from that count are asserted rather than assumed, because either
// direction is a real regression:
//
//   - Variants is the one field where "↑↓ select" is actively wrong (there ↑↓ steps a
//     count and ←→ moves between profiles), so its hint names both axes and the count
//     is 2. Pinning it is what stops a later consistency sweep from "fixing" the
//     exception away.
//   - The prompt swaps the footer for promptFocusHelp, which names no arrow keys at
//     all, so the count is 0.
//
// Before #466 this failed on stopAccount, whose hint still opened with "↑↓ to change"
// — a verbatim restatement of the footer — while its Effort and Permissions siblings
// had dropped theirs in #460.
func TestCreateFormHints_FooterOwnsTheNavKeys(t *testing.T) {
	for _, tc := range []struct {
		name string
		stop focusStop
		want int
		why  string
	}{
		{"directory", stopDirectory, 1, "the project picker adds no hint of its own"},
		{"branch", stopBranch, 1, "the branch picker adds no hint of its own"},
		{"title", stopTitle, 1, "the title's suffix is a validity marker, not a key hint"},
		{"variants", stopVariants, 2, "the variant hint names two axes the footer gets wrong"},
		{"model", stopModel, 1, "the model hint is a custom-entry affordance, not a nav key"},
		{"effort", stopEffort, 1, "the effort hint names the pin behind the inherit chip"},
		{"mode", stopMode, 1, "the permissions hint names the pin behind the inherit chip"},
		{"account", stopAccount, 1, "the account hint glosses pool vs member, not the arrows"},
		{"enter", stopEnter, 1, "the Create button adds no hint of its own"},
		{"prompt", stopTextarea, 0, "the prompt swaps in promptFocusHelp, which names no arrows"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ov := createFormWithEveryField(t)
			ov.focusStop(tc.stop)
			out := xansi.Strip(ov.Render())
			assert.Equal(t, tc.want, strings.Count(out, navKeys), "%s focused: %s", tc.name, tc.why)
		})
	}
}

// TestCreateFormHints_ModelKeepsTypeAName pins the other deliberate exception. The
// model field is the only override field whose chips are not the whole input surface
// — typing a rune enters free-text mode — and nothing else in the form advertises
// that. A sweep applying the "footer owns the keys" rule must leave it alone, so the
// affordance is asserted rather than left to the rule's silence.
func TestCreateFormHints_ModelKeepsTypeAName(t *testing.T) {
	ov := createFormWithEveryField(t)
	ov.focusStop(stopModel)
	assert.Contains(t, xansi.Strip(ov.modelField.Render()), "type a name")
}

// TestAccountPicker_HintGlossesPoolsOnly checks what survived the restatement's
// removal: with pools configured the hint still distinguishes the rotating ⇄ entry
// from a pinned member — information the footer does not carry — and with no pools
// there is nothing left to say, so the focused label line carries nothing, exactly
// like a focused Effort row sitting on a real chip.
func TestAccountPicker_HintGlossesPoolsOnly(t *testing.T) {
	pooled := NewAccountPicker([]config.ClaudeAccount{{Name: "personal", Pool: "work"}, {Name: "team", Pool: "work"}})
	pooled.Focus()
	rotate := xansi.Strip(pooled.Render())
	assert.Contains(t, rotate, "⇄ rotates the pool", "the cursor starts on the pool's ⇄ entry")
	assert.NotContains(t, rotate, navKeys, "the footer owns the arrows")

	pooled.HandleKeyPress(keyMsg("down")) // onto the first member
	member := xansi.Strip(pooled.Render())
	assert.Contains(t, member, "pins this member")
	assert.NotContains(t, member, navKeys, "the footer owns the arrows")

	flat := NewAccountPicker([]config.ClaudeAccount{{Name: "personal"}, {Name: "team"}})
	flat.Focus()
	label, _, _ := strings.Cut(xansi.Strip(flat.Render()), "\n")
	assert.Equal(t, "Account", strings.TrimRight(label, " "),
		"with no pools the focused label carries no hint at all")
}

// TestCreateFormHelp_EveryRungKeepsTheNavKeys extends the rule above across width.
// TestCreateFormHints_FooterOwnsTheNavKeys renders at one wide size, where fitHint
// always returns the first rung, so it can only ever see createFormHelp[0]. The
// narrow rungs exist precisely to shed cells, and "↑↓ select" is a tempting nine to
// shed — but dropping it would leave the footer not owning the nav keys on exactly
// the 80-col terminal where a per-field restatement is least affordable, and the
// guard above would stay green throughout.
func TestCreateFormHelp_EveryRungKeepsTheNavKeys(t *testing.T) {
	for i, rung := range createFormHelp {
		assert.Equalf(t, 1, strings.Count(rung, navKeys),
			"rung %d must name the nav keys exactly once: %q", i, rung)
	}
}

// TestPromptFocusHelp_NoRungNamesTheNavKeys is the same assertion from the other
// side, and it exists because the count TestCreateFormHints_FooterOwnsTheNavKeys
// pins for the prompt is *zero*: with the textarea focused the footer is swapped for
// promptFocusHelp, which names no arrow keys, so a per-field hint has nothing to
// restate. That zero is width-indexed too, and the wide render only ever reaches
// rung 0.
//
// Guarding one ladder and not the other is the shape this repo has hit before (a
// ladder extracted to stop one hint lying, leaving its sibling lying): a rung like
// "↑↓ next · ⌃S create" is 20 cells, so it fits every width guard here and would
// have broken #466's rule with the whole suite green.
// Both ladders are swept, though the second is redundant while
// promptFocusHelpLegacy is promptFocusHelp[1:] — every legacy rung is already a
// rung of its parent. It is named anyway so the sweep keeps covering what is
// rendered rather than what happens to be a slice of what is rendered, which is
// the property that would quietly lapse if the two ladders ever stopped sharing a
// literal.
func TestPromptFocusHelp_NoRungNamesTheNavKeys(t *testing.T) {
	for name, ladder := range map[string][]string{
		"promptFocusHelp":       promptFocusHelp,
		"promptFocusHelpLegacy": promptFocusHelpLegacy,
	} {
		for i, rung := range ladder {
			assert.NotContainsf(t, rung, navKeys,
				"%s rung %d must leave the arrows to the fields that correct them: %q", name, i, rung)
		}
	}
}
