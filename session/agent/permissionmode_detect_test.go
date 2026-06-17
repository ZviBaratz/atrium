package agent

import (
	"strings"
	"testing"
)

// pane wraps a footer status line in the structure claude renders — an input
// box (top border, "❯" prompt, bottom border) with the live footer below the
// last rule — so footerRegion picks up exactly footerLine. transcriptBody is
// placed above the box to prove detection is confined to the live footer.
func pane(transcriptBody, footerLine string) string {
	rule := strings.Repeat("─", 80)
	return strings.Join([]string{
		transcriptBody,
		rule,
		"❯ ",
		rule,
		footerLine,
	}, "\n")
}

// Footer status lines captured verbatim from a live claude 2.1.178 pane, one
// per --permission-mode (the "/rc active" right-aligned status is elided; only
// the left content matters to detection).
const (
	footerDefault     = "  ? for shortcuts · ← for agents"
	footerPlan        = "  ⏸ plan mode on (shift+tab to cycle) · ← for agents"
	footerAcceptEdits = "  ⏵⏵ accept edits on (shift+tab to cycle) · ← for agents"
	footerAuto        = "  ⏵⏵ auto mode on (shift+tab to cycle) · ← for agents"
	footerBypass      = "  ⏵⏵ bypass permissions on (shift+tab to cycle) · ← for agents"
	// A special mode persists its indicator while the turn is in flight, beside
	// the busy hint (registry_test.go fixture shape).
	footerAutoWorking = "  ⏵⏵ auto mode on (shift+tab to cycle) · esc to interrupt · ← for agents"
	// Default mode while working shows the busy hint and no mode indicator.
	footerBusyNoMode = "  ✻ Cogitating… (5s · esc to interrupt)"
)

func TestClaudePermissionMode(t *testing.T) {
	cases := []struct {
		name      string
		footer    string
		wantMode  string
		wantKnown bool
	}{
		{"default idle", footerDefault, "default", true},
		{"plan", footerPlan, "plan", true},
		{"accept edits", footerAcceptEdits, "acceptEdits", true},
		{"auto", footerAuto, "auto", true},
		{"bypass permissions", footerBypass, "bypassPermissions", true},
		{"special mode persists while working", footerAutoWorking, "auto", true},
		{"busy default footer is indeterminate", footerBusyNoMode, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mode, known := claudePermissionMode(pane("doing some work", c.footer))
			if mode != c.wantMode || known != c.wantKnown {
				t.Errorf("claudePermissionMode(%q) = (%q, %v), want (%q, %v)",
					c.footer, mode, known, c.wantMode, c.wantKnown)
			}
		})
	}
}

// A mode phrase quoted in the scrolled-back transcript (above the input box)
// must not be read as the live mode: only footerRegion, below the box, counts.
func TestClaudePermissionMode_ConfinedToFooter(t *testing.T) {
	// The conversation discusses "auto mode on", but the live footer is default.
	content := pane("Assistant: I'll switch to auto mode on the next turn.", footerDefault)
	if mode, known := claudePermissionMode(content); mode != "default" || !known {
		t.Errorf("transcript mention leaked into detection: got (%q, %v), want (default, true)", mode, known)
	}
	// A plan-mode phrase in the transcript with a busy (indeterminate) footer
	// must stay indeterminate, not report plan.
	content = pane("Assistant: leaving plan mode on hold.", footerBusyNoMode)
	if mode, known := claudePermissionMode(content); known {
		t.Errorf("transcript mention leaked into detection: got (%q, %v), want indeterminate", mode, known)
	}
}

// With no input-box border on screen — a busy frame whose box is hidden, or a
// pre-box startup capture — there is no anchor proving the bottom lines are live
// chrome, so detection must stay indeterminate even when those lines contain a
// verbatim mode token. Without the box gate footerRegion would fall back to the
// last few lines and read the transcript as the live mode, then persist it.
func TestClaudePermissionMode_NoBoxIsIndeterminate(t *testing.T) {
	for _, body := range []string{
		"Assistant: I'll switch to auto mode on the next turn.",
		"Run with ? for shortcuts to see the menu.",
		"⏸ plan mode on (shift+tab to cycle)", // a verbatim footer line, but unanchored
	} {
		if mode, known := claudePermissionMode(body); known {
			t.Errorf("unanchored content %q detected as (%q, %v), want indeterminate", body, mode, known)
		}
	}
}

// The Claude adapter is wired; non-claude adapters report indeterminate so the
// chip falls back to the (also-empty) pinned flag for them.
func TestDetectPermissionMode_AdapterWiring(t *testing.T) {
	claude := Resolve("claude")
	if mode, known := claude.DetectPermissionMode(pane("x", footerPlan)); mode != "plan" || !known {
		t.Errorf("claude adapter: got (%q, %v), want (plan, true)", mode, known)
	}
	for _, prog := range []string{"aider", "codex", "gemini"} {
		a := Resolve(prog)
		if a.PermissionMode != nil {
			t.Errorf("%s adapter unexpectedly has a PermissionMode detector", prog)
		}
		if mode, known := a.DetectPermissionMode(pane("x", footerAuto)); known || mode != "" {
			t.Errorf("%s adapter: got (%q, %v), want indeterminate", prog, mode, known)
		}
	}
}

// Every mode the detector can emit must be a value the CLI accepts, so the chip
// never renders a label the create form / flag composition would reject.
func TestClaudePermissionMode_EmitsValidEnum(t *testing.T) {
	for _, footer := range []string{footerPlan, footerAcceptEdits, footerAuto, footerBypass, footerDefault} {
		mode, known := claudePermissionMode(pane("x", footer))
		if !known {
			t.Fatalf("footer %q unexpectedly indeterminate", footer)
		}
		if !ValidPermissionMode(mode) {
			t.Errorf("detector emitted %q, not a valid permission mode", mode)
		}
	}
}
