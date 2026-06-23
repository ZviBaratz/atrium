package tmux

import (
	"regexp"
	"strings"
	"testing"
)

var wsRun = regexp.MustCompile(`[ \t]+`)

// collapseWS squeezes runs of spaces/tabs to a single space so assertions don't
// depend on the template's column alignment.
func collapseWS(s string) string { return wsRun.ReplaceAllString(s, " ") }

// The managed config is rendered from a template gated on the context bar. With the
// bar on, the status line is enabled and references the @atrium_* options Atrium
// pushes; with it off, the file collapses to the chrome-free `status off` that
// shipped before the feature. The terminal-title fix is unconditional in both.
func TestRenderManagedConfig(t *testing.T) {
	on, err := renderManagedConfig(true)
	if err != nil {
		t.Fatalf("renderManagedConfig(true) error: %v", err)
	}
	onStr := collapseWS(string(on))
	// Identity rides a single top status line (@atrium_left); there is no bottom strip
	// (pane-border-status off). Assert the header-only layout is locked.
	for _, want := range []string{
		"status on",
		"status-position top",
		"@atrium_left",
		"pane-border-status off",
		"set-titles on",
		// Copy must reach the OS clipboard: set-clipboard on + an OSC 52 Ms
		// override (tmux-256color has no Ms), or in-pane copies never leave tmux.
		"set-clipboard on",
		`Ms=\E]52`,
	} {
		if !strings.Contains(onStr, want) {
			t.Errorf("context-bar config missing %q\n---\n%s", want, onStr)
		}
	}
	// The chip footer is gone, so its option must not be referenced.
	if strings.Contains(onStr, "@atrium_right") {
		t.Errorf("context-bar config should not reference the dropped chip option\n---\n%s", onStr)
	}
	// The header must carry a real background fill (the theme's elevated surface) so it
	// reads as a band, not text floating over the pane. A truecolor "bg=#…" proves the
	// theme color substituted; "bg=default" would be the regression that blends in.
	if !strings.Contains(onStr, `status-style "bg=#`) {
		t.Errorf("header status-style should fill with a theme color, not bg=default\n---\n%s", onStr)
	}

	off, err := renderManagedConfig(false)
	if err != nil {
		t.Fatalf("renderManagedConfig(false) error: %v", err)
	}
	offStr := collapseWS(string(off))
	if !strings.Contains(offStr, "status off") {
		t.Errorf("disabled config should set status off\n---\n%s", offStr)
	}
	if strings.Contains(offStr, "@atrium_left") {
		t.Errorf("disabled config should not reference the context bar options\n---\n%s", offStr)
	}
	// The terminal-title fix is independent of the bar toggle.
	if !strings.Contains(offStr, "set-titles on") {
		t.Errorf("set-titles should be on regardless of the bar\n---\n%s", offStr)
	}
	// Clipboard fix is likewise unconditional.
	for _, want := range []string{"set-clipboard on", `Ms=\E]52`} {
		if !strings.Contains(offStr, want) {
			t.Errorf("disabled config missing %q (clipboard fix is unconditional)\n---\n%s", want, offStr)
		}
	}
}
