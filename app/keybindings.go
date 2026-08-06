package app

import (
	"fmt"
	"strings"

	"github.com/ZviBaratz/atrium/keys"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/session/tmux"

	tea "charm.land/bubbletea/v2"
)

// keybindingProblemsShown caps the startup report's list, as
// customCommandProblemsShown does for its own.
const keybindingProblemsShown = 5

// installAttachChords hands the attach layer the bytes the applied keymap
// detaches and kills on.
//
// It runs after keys.Apply and re-derives the chords rather than being told
// them, so the two layers cannot disagree about which key is which.
//
// Detach and kill are resolved together and installed in one call. Installing
// them one at a time, or bailing after the first failure, is what produced the
// split-brain this comment used to deny: unbinding kill made KillKey() empty, the
// early return skipped the install entirely, and a session rebound to ctrl+g on
// the list went on detaching the pane with ctrl+q — while ctrl+x, the key the
// user had just unbound, still killed it.
//
// An unbound kill is a legitimate thing to want and is carried through as such.
// An unencodable detach is not reachable — keys.Validate refuses one — so it is
// logged and the defaults are left whole rather than half-replaced.
func installAttachChords() {
	detach, err := keys.ControlByte(keys.PrimaryKey(keys.KeyAttachToggle))
	if err != nil {
		log.ErrorLog.Printf("attach chords: detach key is not encodable, keeping the defaults: %v", err)
		return
	}
	// An unbound kill leaves the attach layer with no kill byte at all, which is
	// the whole point of unbinding it.
	var kill byte
	killBound := false
	if chord := keys.KillKey(); chord != "" {
		kill, err = keys.ControlByte(chord)
		if err != nil {
			log.ErrorLog.Printf("attach chords: kill key is not encodable, keeping the defaults: %v", err)
			return
		}
		killBound = true
	}
	tmux.SetAttachChords(detach, kill, killBound)
}

// flushKeybindingProblems opens the startup report once the screen is free, in
// the shape flushCustomCommandProblems uses — nil while an overlay owns the
// screen, and the buffer is cleared as it fires so the preview tick cannot
// reopen it forever.
func (m *home) flushKeybindingProblems() tea.Cmd {
	if len(m.pendingKeybindingProblems) == 0 || m.state != stateDefault {
		return nil
	}
	problems := m.pendingKeybindingProblems
	m.pendingKeybindingProblems = nil
	return m.showInfo(keybindingProblemsReport(problems))
}

// keybindingProblemsReport is the modal's text: what was refused, and what that
// costs. The consequence line matters more here than for the other two reports —
// a dropped override leaves the action on its default key, which is
// indistinguishable from Atrium not having read the config at all.
func keybindingProblemsReport(problems []keys.Problem) string {
	var refused, warnings []keys.Problem
	for _, p := range problems {
		if p.Warning {
			warnings = append(warnings, p)
			continue
		}
		refused = append(refused, p)
	}

	var b strings.Builder
	section := func(items []keys.Problem, headingFmt, tail string) {
		if len(items) == 0 {
			return
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, headingFmt, len(items), plural(len(items)), wereOrWas(len(items)))
		for i, p := range items {
			if i == keybindingProblemsShown {
				fmt.Fprintf(&b, "  … and %d more\n", len(items)-keybindingProblemsShown)
				break
			}
			fmt.Fprintf(&b, "  %s\n", clipReportLine(p.Error()))
		}
		b.WriteString(tail)
	}

	// Two sections, because the two outcomes are opposite. A refused override did
	// not take effect; a warned one did. Listing them together under "not applied"
	// told the user their key had been ignored when it was in fact live — worse
	// than saying nothing at all.
	section(refused, "%d keybinding%s in config.json %s not applied:\n",
		"\nThose actions keep their default keys.\n")
	section(warnings, "%d keybinding%s %s applied with a caveat:\n", "")
	b.WriteString("\n`atrium doctor` reports the same list.")
	return b.String()
}
