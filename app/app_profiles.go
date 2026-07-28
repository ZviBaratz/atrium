package app

import (
	"strings"

	"github.com/ZviBaratz/atrium/config"
	tea "github.com/charmbracelet/bubbletea"
)

// profilesDetectedMsg carries a completed agent detection back to the settings panel's Profiles
// editor.
type profilesDetectedMsg struct {
	detected []config.Profile
}

// detectProfilesCmd probes for installed agent CLIs off the update loop.
//
// It goes through the same detectAgents seam the startup agent check uses, so the panel's D and
// `atrium profiles detect` can never probe differently; the merge half is
// config.MergeDetectedProfiles, which runs when the result lands. Running it inline would block
// every session's poll for the claude probe's ten-second shell timeout.
func (m *home) detectProfilesCmd() tea.Cmd {
	return func() tea.Msg {
		return profilesDetectedMsg{detected: detectAgents()}
	}
}

// profilesDetectedText is the outcome wording for a completed detection, deliberately mirroring
// `atrium profiles detect`'s own output (main.go) so the two surfaces read the same.
func profilesDetectedText(added []string) string {
	if len(added) == 0 {
		return "no new agents detected; profiles unchanged"
	}
	return "added profiles: " + strings.Join(added, ", ")
}
