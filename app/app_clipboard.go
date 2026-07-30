package app

import (
	"github.com/ZviBaratz/atrium/internal/actions"
	"github.com/ZviBaratz/atrium/log"

	tea "charm.land/bubbletea/v2"
)

// copyToClipboard puts text on the user's clipboard over two independent legs, so a
// copy lands whether the user is local or on the far side of an SSH session: the
// OSC 52 escape Bubble Tea emits — which crosses to the user's real terminal, where
// no clipboard binary is needed — and the OS copier (xclip/xsel/pbcopy) for
// terminals that ignore OSC 52.
//
// The OSC 52 leg is a command, not a write, which is what makes a missing xclip
// stop being an error: it is dispatched unconditionally and the renderer emits it,
// so there is no state in which neither leg ran. That also means it cannot report
// back, so the OS leg's failure is logged rather than surfaced — telling the user a
// copy failed when the escape went out would be wrong more often than right.
func (m *home) copyToClipboard(text string) tea.Cmd {
	if err := actions.CopyToClipboard(text); err != nil {
		log.WarningLog.Printf("clipboard: OS copier unavailable, OSC 52 leg still sent: %v", err)
	}
	return tea.SetClipboard(text)
}
