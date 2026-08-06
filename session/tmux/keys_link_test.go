package tmux

import (
	"testing"

	"github.com/ZviBaratz/atrium/keys"
)

// The keymap registry declares detach and kill LayerBoth: TUI actions this layer
// mirrors as raw control bytes while attached. The bytes are derived from the
// chords rather than declared beside them, so the two cannot drift — but the
// defaults in attach.go are still a second spelling of the default keymap, and
// this is what ties them.
func TestAttachControlBytes_MatchRegistryChords(t *testing.T) {
	kill, err := keys.ControlByte(keys.KillKey())
	if err != nil {
		t.Fatalf("the kill chord %q must be encodable as a control byte: %v", keys.KillKey(), err)
	}
	if kill != ctrlX {
		t.Errorf("keys.KillKey() %q encodes byte %d; this layer's default kill byte is %d",
			keys.KillKey(), kill, ctrlX)
	}

	chord := keys.PrimaryKey(keys.KeyAttachToggle)
	detach, err := keys.ControlByte(chord)
	if err != nil {
		t.Fatalf("the detach chord %q must be encodable as a control byte: %v", chord, err)
	}
	if detach != ctrlQ {
		t.Errorf("attach_toggle %q encodes byte %d; this layer's default detach byte is %d",
			chord, detach, ctrlQ)
	}
}

// The bytes have to actually follow a rebind, or #376's headline case — a user
// whose terminal eats ctrl+q — is only fixed on the list and not inside a pane,
// which is the half that matters.
func TestAttachControlBytes_FollowARebind(t *testing.T) {
	t.Cleanup(func() { attachChords.Store(nil) })

	problems, restore := keys.Apply(map[string]keys.Spec{
		"attach_toggle": {Keys: []string{"ctrl+g"}},
	})
	defer restore()
	if len(problems) != 0 {
		t.Fatalf("rebinding detach to ctrl+g must be legal: %v", problems)
	}

	detach, err := keys.ControlByte(keys.PrimaryKey(keys.KeyAttachToggle))
	if err != nil {
		t.Fatalf("ControlByte: %v", err)
	}
	kill, err := keys.ControlByte(keys.KillKey())
	if err != nil {
		t.Fatalf("ControlByte: %v", err)
	}
	SetAttachChords(detach, kill)

	if got := classifyAttachInput([]byte{detach}, true); got != attachDetach {
		t.Errorf("the rebound chord ctrl+g classified as %v, want a detach", got)
	}
	if got := classifyAttachInput([]byte{ctrlQ}, true); got != attachForward {
		t.Errorf("ctrl+q classified as %v after being rebound away; it must reach the agent", got)
	}
}

// Nothing installed means the defaults, which is the daemon's state and every
// test's — a nil pointer here would be a panic on the attach path.
func TestAttachControlBytes_DefaultWithoutApply(t *testing.T) {
	t.Cleanup(func() { attachChords.Store(nil) })
	attachChords.Store(nil)

	if got := classifyAttachInput([]byte{ctrlQ}, true); got != attachDetach {
		t.Errorf("with no chords installed ctrl+q classified as %v, want a detach", got)
	}
	if got := classifyAttachInput([]byte{ctrlX}, true); got != attachKill {
		t.Errorf("with no chords installed ctrl+x classified as %v, want a kill", got)
	}
}
