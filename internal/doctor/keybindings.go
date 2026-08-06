package doctor

import (
	"fmt"
	"strings"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/keys"
)

// CheckKeybindings reports the keybindings overrides validation refuses to apply
// (#376).
//
// A refused override leaves its action on the default key, which is the right
// runtime behaviour — one typo must not cost the user their other rebinds — but
// it is also indistinguishable from Atrium never having read the config at all.
// This is the answer to "why is my key still the old one?" outside the TUI; the
// startup modal is the same answer inside it. Both read the same validation
// pass, so the two can never disagree.
func CheckKeybindings(cfg *config.Config) []keys.Problem {
	if cfg == nil {
		return nil
	}
	return keys.Validate(cfg.KeybindingOverrides())
}

// RenderKeybindings formats refused overrides for `atrium doctor` (empty string
// when none), in the section shape RenderPools established.
func RenderKeybindings(problems []keys.Problem) string {
	if len(problems) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Keybindings:\n")
	for _, p := range problems {
		// A warning was applied and a refusal was not, so they cannot share a
		// marker: printing both as ⚠ under a heading about what was refused told
		// the user their key had been ignored when it was in fact live.
		marker := "⚠"
		if p.Warning {
			marker = "·"
		}
		fmt.Fprintf(&b, "  %s %s\n", marker, p.Error())
	}
	return b.String()
}
