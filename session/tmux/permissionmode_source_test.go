package tmux

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Poll resolves the permission mode from two sources, and this file pins the arbitration
// between them (#324). One file per signal, mirroring heartbeat_poll_test.go.

// modePane renders the claude pane shape footerBelowBox anchors on: an input box whose
// bottom border is the last horizontal rule, with footerLine as the live chrome below it.
func modePane(footerLine string) string {
	rule := strings.Repeat("─", 80)
	return strings.Join([]string{"Assistant: done.", rule, "❯ ", rule, footerLine}, "\n")
}

// statusLinePane renders the shape that defeats footer detection outright: a custom
// statusLine drawing its own ─── divider BELOW the mode line, so that divider — not the input
// box's border — is the last rule footerBelowBox anchors on. The mode line then sits above the
// anchor and is invisible to the scrape, which reports known=false forever. This is the
// concrete case the hook fallback exists for (chrome.go documents the same mechanism for the
// sibling segment scan).
func statusLinePane(footerLine string) string {
	rule := strings.Repeat("─", 80)
	return strings.Join([]string{
		"Assistant: done.", rule, "❯ ", rule,
		footerLine,
		rule, // the statusLine's own divider becomes the last rule
		" main | ctx 42% | $0.12",
	}, "\n")
}

// seedModeRecord writes a hook record carrying mode for s's session. A thin wrapper over
// seedHookRecord (hookstate_test.go) — which writes through the real writeHookRecordAtomic
// and handles the -count=N cleanup — for the mode-only cases this file cares about.
func seedModeRecord(t *testing.T, s *Session, mode string) {
	t.Helper()
	seedHookRecord(t, s, hookRecord{State: hookStateReady, PermissionMode: mode})
}

// TestRuntimePermissionModeFooterWins is the anti-regression test for the #324 precedence
// decision: the footer refreshes every 500ms whereas no hook fires on a mode switch at all,
// so preferring the record would leave a Shift+Tab-then-detach stale until the next turn.
// Flipping the arbitration to hook-primary must fail here.
func TestRuntimePermissionModeFooterWins(t *testing.T) {
	content := modePane("  ⏵⏵ auto mode on (shift+tab to cycle) · ← for agents")
	s := hookPollSession(t, "claude", &content)
	seedModeRecord(t, s, "plan") // a stale record from an earlier turn

	s.Poll()
	require.Equal(t, "auto", s.RuntimePermissionMode(), "a readable footer outranks the hook record")
}

// TestRuntimePermissionModeHookFillsFooterGap is the fix: with the footer unanchored by a
// statusLine's divider, the scrape is silent forever and the chip used to fall back to the
// stale launch-time flag. The record answers with claude's own enum instead.
func TestRuntimePermissionModeHookFillsFooterGap(t *testing.T) {
	content := statusLinePane("  ⏸ plan mode on (shift+tab to cycle) · ← for agents")
	s := hookPollSession(t, "claude", &content)

	// Precondition: the scrape really is blind to this pane, so the test proves the fallback
	// rather than accidentally passing on a footer read.
	_, known := s.adapter.DetectPermissionMode(content)
	require.False(t, known, "a statusLine divider must defeat the footer anchor")

	s.Poll()
	require.Empty(t, s.RuntimePermissionMode(), "no footer and no record leaves the chip unknown")

	seedModeRecord(t, s, "plan")
	s.Poll()
	require.Equal(t, "plan", s.RuntimePermissionMode(), "the record fills the footer's gap")
}

// TestRuntimePermissionModeFooterOvertakesHook: the fallback is transient, not
// sticky-in-preference — once the footer can speak again it resumes driving the chip.
func TestRuntimePermissionModeFooterOvertakesHook(t *testing.T) {
	content := statusLinePane("  ⏸ plan mode on (shift+tab to cycle) · ← for agents")
	s := hookPollSession(t, "claude", &content)
	seedModeRecord(t, s, "plan")

	s.Poll()
	require.Equal(t, "plan", s.RuntimePermissionMode())

	// The user drops the statusLine; the footer is anchored again and reports a fresh switch
	// the record has not seen yet.
	content = modePane("  ⏵⏵ accept edits on (shift+tab to cycle) · ← for agents")
	s.Poll()
	require.Equal(t, "acceptEdits", s.RuntimePermissionMode(), "the footer resumes primacy")
}

// TestRuntimePermissionModeStickyWhenBothSilent: a busy frame whose box is hidden has no
// footer anchor, and a record with no mode says nothing — the last known value must stand
// rather than flicker the chip off.
func TestRuntimePermissionModeStickyWhenBothSilent(t *testing.T) {
	content := modePane("  ⏸ plan mode on (shift+tab to cycle) · ← for agents")
	s := hookPollSession(t, "claude", &content)
	s.Poll()
	require.Equal(t, "plan", s.RuntimePermissionMode())

	content = "Assistant: working…\n  ✻ Cogitating… (5s · esc to interrupt)" // no box, no rule
	s.Poll()
	require.Equal(t, "plan", s.RuntimePermissionMode(), "an indeterminate frame keeps the last mode")
}

// TestRuntimePermissionModeNonClaudeIgnoresRecord: readHookRecord gates on HookSupport, so a
// non-claude session never consults the record even if one is somehow present.
func TestRuntimePermissionModeNonClaudeIgnoresRecord(t *testing.T) {
	content := modePane("  ⏸ plan mode on (shift+tab to cycle) · ← for agents")
	s := hookPollSession(t, "aider", &content)
	seedModeRecord(t, s, "plan")

	s.Poll()
	require.Empty(t, s.RuntimePermissionMode(), "aider has no hook channel and no mode footer")
}
