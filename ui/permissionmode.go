package ui

import "github.com/ZviBaratz/atrium/session/agent"

// permissionModeLabel returns the display string for a --permission-mode value.
// Delegates to ClaudePermissionModeLabels as the single source of truth so
// future mode additions stay consistent automatically.
func permissionModeLabel(mode string) string {
	for i, m := range agent.ClaudePermissionModes {
		if m == mode && i < len(agent.ClaudePermissionModeLabels) {
			return agent.ClaudePermissionModeLabels[i]
		}
	}
	return mode
}
