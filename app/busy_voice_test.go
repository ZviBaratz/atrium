package app

import (
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/session"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// busyLabelVoice is every progress-row label the app can show, with the operation
// that produces it. It exists so the copy stays one voice as labels are added.
//
// What this table proves and does not:
//   - It proves CONSISTENCY. A drift to "Pushing..." fails both the exact match and
//     the shape rule below.
//   - It does NOT prove COVERAGE. Only labels someone remembered to add appear here.
//     TestEveryBusyCallDeclaresItsLabel is what catches a new call site with no
//     label at all; nothing proves a label describes the operation truthfully.
var busyLabelVoice = []struct {
	op    string
	label string
}{
	{"push", "pushing…"},
	{"merge PR", "merging PR #12…"},
	{"create PR", "creating PR…"},
	{"open PR", "opening PR #12…"},
	{"pause", "pausing…"},
	{"pause batch", "pausing 3 sessions…"},
	{"resume", "resuming…"},
	{"resume batch", "resuming 3 sessions…"},
	{"kill", "killing 'alpha'…"},
	{"kill batch", "killing 3 sessions…"},
	{"accept suggestion", "accepting suggestion…"},
	{"deep rename", "renaming to 'beta'…"},
	{"auto-name", "generating name…"},
}

// TestBusyLabels_Voice pins the shape every label shares, so a new one cannot
// arrive in a different register: lowercase, present participle, a real ellipsis
// (U+2026, not three periods), no glyph, no trailing period. A proper noun keeps
// its case — "PR" is the only one so far.
func TestBusyLabels_Voice(t *testing.T) {
	properNouns := []string{"PR"}
	for _, c := range busyLabelVoice {
		t.Run(c.op, func(t *testing.T) {
			assert.True(t, strings.HasSuffix(c.label, "…"),
				"a label must trail off with … while the work runs")
			assert.False(t, strings.HasSuffix(c.label, "..."),
				"the ellipsis must be U+2026, not three periods")
			assert.False(t, strings.HasSuffix(strings.TrimSuffix(c.label, "…"), "."),
				"a progress line is not a sentence")

			lowered := c.label
			for _, noun := range properNouns {
				lowered = strings.ReplaceAll(lowered, noun, strings.ToLower(noun))
			}
			assert.Equal(t, strings.ToLower(lowered), lowered,
				"labels are lowercase apart from proper nouns %v", properNouns)

			for _, r := range c.label {
				assert.False(t, r > 0x2100 && r != '…',
					"labels carry no glyphs — the row is text, and the one that had a ✨ "+
						"was the odd one out")
			}
		})
	}
}

// TestBusyLabels_KillNamesItsTarget is the asymmetry that will otherwise get
// "fixed" into consistency: kill names the session, pause and resume do not.
//
// Pause and resume always act on the highlighted row, which the user is looking
// at. Kill does not — confirmKill takes an explicit instance because the
// in-session chord and the auto-open path both target a specific session
// regardless of what is selected. There the object is load-bearing.
func TestBusyLabels_KillNamesItsTarget(t *testing.T) {
	h, inst := newKillHome(t)

	h.confirmKill(inst)
	require.Contains(t, h.pendingConfirmBusyLabel, inst.DisplayName(),
		"kill must name the session it will destroy")

	h.pendingConfirmBusyLabel = ""
	other, err := session.NewInstance(session.InstanceOptions{
		Title: "second", Path: t.TempDir(), Program: "echo", Direct: true,
	})
	require.NoError(t, err)
	h.list.AddInstance(other)()
	h.list.SetSelectedInstance(1)

	// Killing a session that is NOT selected is exactly why the object matters.
	h.confirmKill(inst)
	require.Contains(t, h.pendingConfirmBusyLabel, inst.DisplayName(),
		"the label must follow the kill's target, not the selection")
	require.NotContains(t, h.pendingConfirmBusyLabel, other.DisplayName())
}

// TestBusyRow_ActionOutranksBackground pins the two-tier row. Before it, two
// hand-rolled guards in two different files each protected one direction of the
// same collision.
func TestBusyRow_ActionOutranksBackground(t *testing.T) {
	h := newCreateFormHome(t)

	h.beginBackgroundAction("generating name…", nil)
	require.Equal(t, "generating name…", h.menu.BusyText())
	require.False(t, h.actionInFlight, "a background operation must not gate keys")

	h.beginAsyncAction("killing 'alpha'…", nil)
	require.Equal(t, "killing 'alpha'…", h.menu.BusyText(),
		"the row must name what the user is actually blocked on")
	require.True(t, h.actionInFlight)

	h.Update(asyncActionDoneMsg{})
	require.Equal(t, "generating name…", h.menu.BusyText(),
		"the still-running background operation gets its row back")
	require.False(t, h.actionInFlight)

	h.Update(backgroundActionDoneMsg{})
	require.Empty(t, h.menu.BusyText())
}

// TestInputGate_EmptyLabelStillReadsAsASentence: "busy — " with nothing after the
// dash was what an unlabelled action produced. The required label makes it
// unreachable; this keeps the sentence honest if it ever is.
func TestInputGate_EmptyLabelStillReadsAsASentence(t *testing.T) {
	h := newCreateFormHome(t)
	inst := newBranchInstance(t, "feat", "zvi/feat")
	inst.SetStatus(session.Running)
	h.list.AddInstance(inst)
	h.actionInFlight = true

	pressKey(h, 'p')

	require.True(t, h.menu.HasNotice())
	assert.Equal(t, "busy", h.menu.NoticeText())
	assert.NotContains(t, h.menu.NoticeText(), "— ", "no dangling dash")
}

// TestSetBusy_ClearsAStaleNotice: a notice is rendered ahead of every state, and
// toasts last 5s. Without this, a background toast landing just before an action
// armed would hide that action's label for its whole lifetime — the app looking
// frozen while refusing keys with an unrelated sentence.
func TestSetBusy_ClearsAStaleNotice(t *testing.T) {
	h := newCreateFormHome(t)
	h.menu.SetNotice("session 'x' terminal exited — parked as paused", 0)
	require.True(t, h.menu.HasNotice())

	h.beginAsyncAction("killing 'alpha'…", nil)

	require.False(t, h.menu.HasNotice(), "an armed progress row must not stay hidden behind a stale toast")
	require.Contains(t, h.menu.String(), "killing 'alpha'…")
}
