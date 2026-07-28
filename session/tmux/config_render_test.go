package tmux

import (
	"fmt"
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
		`Ms=\033]52`,
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
	for _, want := range []string{"set-clipboard on", `Ms=\033]52`} {
		if !strings.Contains(offStr, want) {
			t.Errorf("disabled config missing %q (clipboard fix is unconditional)\n---\n%s", want, offStr)
		}
	}
}

// TestRenderManagedConfigScrollKeys pins the prefix-free scrollback chords. The
// isolated socket never reads the host ~/.tmux.conf, so whatever this file binds is
// the entire keyboard route into a session's history — and two halves have to agree
// for it to work: the root binding enters copy mode, and the copy-mode-vi entries
// are what page *further* once you are in it. A root-only config scrolls exactly one
// page and then goes dead, which is why both tables are asserted.
func TestRenderManagedConfigScrollKeys(t *testing.T) {
	for _, contextBar := range []bool{true, false} {
		rendered, err := renderManagedConfig(contextBar)
		if err != nil {
			t.Fatalf("renderManagedConfig(%v) error: %v", contextBar, err)
		}
		got := collapseWS(string(rendered))

		// Entry: -u lands a page back on the first press, -e drops back out at the
		// bottom so a scrolled pane can't swallow keys sent to it (autoyes' Enter, a
		// queued prompt). Both spellings enter; Shift is the terminal-native one.
		for _, want := range []string{
			// Every root chord is gated on alternate_on: an agent drawing on the
			// alternate screen (Claude Code's fullscreen renderer) keeps NO tmux
			// history, so entering copy mode there shows an empty [0/0] and eats the
			// key. Forwarding it lets the agent scroll its own view instead.
			`bind-key -n S-PPage if-shell -F '#{alternate_on}' 'send-keys S-PPage' { copy-mode -eu }`,
			`bind-key -n M-PPage if-shell -F '#{alternate_on}' 'send-keys M-PPage' { copy-mode -eu }`,
			"bind-key -T copy-mode-vi S-PPage send-keys -X page-up",
			"bind-key -T copy-mode-vi S-NPage send-keys -X page-down",
			"bind-key -T copy-mode-vi M-PPage send-keys -X page-up",
			"bind-key -T copy-mode-vi M-NPage send-keys -X page-down",
			// Line granularity is what makes scrolling feel smooth rather than
			// stepped: one line per press, continuous under key repeat.
			`bind-key -n S-Up if-shell -F '#{alternate_on}' 'send-keys S-Up' { copy-mode -e ; send-keys -X scroll-up }`,
			`bind-key -n S-Down if-shell -F '#{alternate_on}' 'send-keys S-Down' { copy-mode -e ; send-keys -X scroll-down }`,
			"bind-key -T copy-mode-vi S-Up send-keys -X scroll-up",
			"bind-key -T copy-mode-vi S-Down send-keys -X scroll-down",
			// A wheel notch matches the preview pane's, not tmux's 5. Built from the
			// shared constant, not a literal: a literal here would re-create the second
			// home the constant exists to remove, and would keep passing while the
			// template hardcoded a number the preview pane no longer uses.
			fmt.Sprintf(`bind-key -T copy-mode-vi WheelUpPane select-pane \; send-keys -X -N %d scroll-up`, WheelScrollLines),
			fmt.Sprintf(`bind-key -T copy-mode-vi WheelDownPane select-pane \; send-keys -X -N %d scroll-down`, WheelScrollLines),
			// The copy-mode-vi table is only reachable with mode-keys vi; without
			// this line every binding above lands in a table tmux never consults.
			"set-window-option -g mode-keys vi",
			// mode-keys vi alone maps v to rectangle-toggle and leaves y unbound, so
			// the habitual select-and-copy silently does the wrong thing.
			"bind-key -T copy-mode-vi v send-keys -X begin-selection",
			"bind-key -T copy-mode-vi y send-keys -X copy-selection-and-cancel",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("contextBar=%v: managed config missing %q (scroll keys are unconditional)\n---\n%s",
					contextBar, want, got)
			}
		}

		// The nudge keys must enter copy mode at the bottom (-e), not a page back
		// (-eu): with -u the first shift-up would jump a page, which is exactly the
		// stepped feel the line bindings exist to avoid.
		if strings.Contains(got, `'send-keys S-Up' { copy-mode -eu`) {
			t.Errorf("contextBar=%v: shift-up enters with -u, so its first press pages instead of nudging one line\n---\n%s",
				contextBar, got)
		}

		// Ctrl+PageUp/PageDown belong to the attach layer, which intercepts them for
		// sibling-session cycling (navReason) before tmux is handed the bytes. Binding
		// them here would register a key that can never fire.
		for _, forbidden := range []string{"C-PPage", "C-NPage"} {
			if strings.Contains(got, forbidden) {
				t.Errorf("contextBar=%v: managed config binds %s, which the attach layer intercepts for sibling cycling\n---\n%s",
					contextBar, forbidden, got)
			}
		}
	}
}
