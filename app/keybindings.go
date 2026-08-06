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
// It runs after keys.Apply, and re-derives the chords rather than being told
// them, so the two layers cannot disagree about which key is which. A chord that
// will not encode is impossible here — keys.Validate refuses one for these two
// actions — so a failure is a logic error, logged and then left on the previous
// (default) bytes rather than installed half-applied: a detach byte from one
// keymap beside a kill byte from another is the one state worse than either.
func installAttachChords() {
	detach, err := keys.ControlByte(keys.PrimaryKey(keys.KeyAttachToggle))
	if err != nil {
		log.ErrorLog.Printf("attach chords: detach key is not encodable, keeping the defaults: %v", err)
		return
	}
	kill, err := keys.ControlByte(keys.KillKey())
	if err != nil {
		log.ErrorLog.Printf("attach chords: kill key is not encodable, keeping the defaults: %v", err)
		return
	}
	tmux.SetAttachChords(detach, kill)
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
	var b strings.Builder
	fmt.Fprintf(&b, "%d keybinding%s in config.json %s not applied:\n",
		len(problems), plural(len(problems)), wereOrWas(len(problems)))
	for i, p := range problems {
		if i == keybindingProblemsShown {
			fmt.Fprintf(&b, "  … and %d more\n", len(problems)-keybindingProblemsShown)
			break
		}
		fmt.Fprintf(&b, "  %s\n", clipReportLine(p.Error()))
	}
	b.WriteString("\nThose actions keep their default keys. " +
		"`atrium doctor` reports the same list.")
	return b.String()
}
