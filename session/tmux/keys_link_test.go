package tmux

import (
	"testing"

	"github.com/ZviBaratz/atrium/keys"
)

// The keymap registry declares ctrl+q and ctrl+x LayerBoth: TUI actions this
// layer mirrors as raw control bytes while attached. The byte IS the chord's
// letter (& 0x1f), so tie the two spellings together — a change to either side
// without the other is the drift the layer tags exist to prevent.
func TestAttachControlBytes_MatchRegistryChords(t *testing.T) {
	chordByte := func(chord string) byte { return chord[len(chord)-1] & 0x1f }

	if got := chordByte(keys.KillKey); got != ctrlX {
		t.Errorf("keys.KillKey %q encodes byte %d; the attach layer kills on %d", keys.KillKey, got, ctrlX)
	}
	detach := keys.GlobalKeyBindings[keys.KeyAttachToggle].Keys()[0]
	if got := chordByte(detach); got != ctrlQ {
		t.Errorf("KeyAttachToggle %q encodes byte %d; the attach layer detaches on %d", detach, got, ctrlQ)
	}
}
