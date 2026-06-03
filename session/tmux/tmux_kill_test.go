package tmux

import "testing"

// A fresh session has no pending kill request; the flag only flips when the
// in-session Ctrl+X is intercepted during an attach.
func TestKillRequestedDefaultsFalse(t *testing.T) {
	ts := NewSession("kill-flag-test", "echo")
	if ts.KillRequested() {
		t.Fatal("expected KillRequested to be false on a fresh session")
	}
}

// classifyAttachInput is the decision the attach stdin reader makes for every
// chunk it reads. These cases lock in the control-byte gating that the
// real-PTY/stdin goroutine cannot be unit-tested against directly.
func TestClassifyAttachInput(t *testing.T) {
	cases := []struct {
		name      string
		in        []byte
		allowKill bool
		want      attachInputAction
	}{
		{"ctrl-q detaches regardless of allowKill", []byte{ctrlQ}, false, attachDetach},
		{"ctrl-q detaches when allowKill", []byte{ctrlQ}, true, attachDetach},
		{"ctrl-x kills when allowKill", []byte{ctrlX}, true, attachKill},
		{"ctrl-x is forwarded when not allowKill", []byte{ctrlX}, false, attachForward},
		{"regular byte is forwarded", []byte{'a'}, true, attachForward},
		{"ctrl-x within a longer read is forwarded", []byte{ctrlX, 'a'}, true, attachForward},
		{"ctrl-q within a longer read is forwarded", []byte{ctrlQ, 'b'}, true, attachForward},
		{"empty read is forwarded", []byte{}, true, attachForward},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyAttachInput(tc.in, tc.allowKill); got != tc.want {
				t.Fatalf("classifyAttachInput(%v, allowKill=%v) = %d, want %d", tc.in, tc.allowKill, got, tc.want)
			}
		})
	}
}
