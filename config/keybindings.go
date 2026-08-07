package config

import (
	"encoding/json"
	"fmt"

	"github.com/ZviBaratz/atrium/keys"
)

// KeySpec is what config.json's keybindings section names for one action: the
// key it should answer to, several keys, or the string "disabled" to unbind it.
//
//	"keybindings": {
//	  "attach_toggle": "ctrl+g",
//	  "up":            ["up", "w"],
//	  "pause_all":     "disabled"
//	}
//
// The three forms exist because the common case is one key and writing
// ["ctrl+g"] for it is noise, while an action with aliases has no other way to
// say so. Marshalling reproduces the form it was written in, because SaveConfig
// rewrites the whole file on every settings keystroke: rewriting a user's
// "ctrl+g" as ["ctrl+g"] behind their back would be a silent edit to a file they
// hand-authored.
type KeySpec struct {
	// Keys are the key strings the action should answer to. Empty when Disabled,
	// or when the value could not be understood at all.
	Keys []string
	// Disabled is the "disabled" sentinel: the action answers to no key, and
	// disappears from the hint bar and cheatsheet while staying reachable from the
	// command palette.
	Disabled bool
	// Malformed carries the reason a value could not be read as a key spec, for
	// validation to report. It is a field rather than an unmarshalling error
	// because LoadConfig cannot fail: it is called from a dozen-odd non-TUI sites,
	// including the daemon and the worktree hooks, so a mistyped keybinding must
	// not be something that stops a session from starting.
	Malformed string
	// wasArray records which of the two shapes the value was written in, so
	// marshalling can put it back the way the user wrote it.
	wasArray bool
	// raw is the bytes a malformed value was written as, kept verbatim so the
	// next save puts back exactly what the user typed. Without it a value Atrium
	// could not parse marshalled as [], so the first settings keystroke destroyed
	// the line and the doctor message naming it lost its evidence — the same
	// failure the unknown-action case is careful to avoid, one level down.
	raw json.RawMessage
}

// DisabledSpec is the sentinel value that unbinds an action.
const DisabledSpec = "disabled"

// UnmarshalJSON reads the string, array or sentinel form. It never returns an
// error: an unreadable value becomes a Malformed spec that validation reports by
// name, which is the difference between "your keybinding is wrong" and "Atrium
// would not start".
func (k *KeySpec) UnmarshalJSON(b []byte) error {
	*k = KeySpec{}

	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		if one == DisabledSpec {
			k.Disabled = true
			return nil
		}
		k.Keys = []string{one}
		return nil
	}

	var many []string
	if err := json.Unmarshal(b, &many); err == nil {
		k.Keys = many
		k.wasArray = true
		return nil
	}

	k.Malformed = fmt.Sprintf("value %s is neither a key, a list of keys, nor %q",
		string(b), DisabledSpec)
	k.raw = append(json.RawMessage(nil), b...)
	return nil
}

// MarshalJSON writes the spec back in the shape it was read in.
func (k KeySpec) MarshalJSON() ([]byte, error) {
	switch {
	case k.raw != nil:
		return k.raw, nil
	case k.Disabled:
		return json.Marshal(DisabledSpec)
	case !k.wasArray && len(k.Keys) == 1:
		return json.Marshal(k.Keys[0])
	default:
		// A nil slice would marshal as null and read back as a malformed spec, so
		// an empty list stays an empty list — round-tripping a value the user wrote
		// matters more than tidying it.
		if k.Keys == nil {
			return json.Marshal([]string{})
		}
		return json.Marshal(k.Keys)
	}
}

// KeybindingOverrides is the keybindings section in the shape the keys package
// validates, so the wire type and the rules that judge it stay in their own
// packages: config knows the three JSON shapes, keys knows what a legal key is,
// and neither has to learn the other's job.
func (c *Config) KeybindingOverrides() map[string]keys.Spec {
	if c == nil || len(c.Keybindings) == 0 {
		return nil
	}
	out := make(map[string]keys.Spec, len(c.Keybindings))
	for action, spec := range c.Keybindings {
		out[action] = keys.Spec{
			Keys:      spec.Keys,
			Disabled:  spec.Disabled,
			Malformed: spec.Malformed,
		}
	}
	return out
}
