package tmux

import "testing"

// navReason maps the Ctrl+PageUp/PageDown escape sequences to sibling-cycle detach
// reasons and leaves everything else (plain keys, Ctrl+Q, pasted text) alone, so a
// stray byte run can't trigger an accidental jump.
func TestNavReason(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		want   DetachReason
		wantOk bool
	}{
		{"ctrl+pageup → prev", "\x1b[5;5~", DetachPrev, true},
		{"ctrl+pagedown → next", "\x1b[6;5~", DetachNext, true},
		{"plain pageup ignored", "\x1b[5~", DetachQuit, false},
		{"ctrl+q ignored here", "\x11", DetachQuit, false},
		{"empty ignored", "", DetachQuit, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := navReason([]byte(tc.in))
			if ok != tc.wantOk || got != tc.want {
				t.Fatalf("navReason(%q) = (%v, %v), want (%v, %v)", tc.in, got, ok, tc.want, tc.wantOk)
			}
		})
	}
}
