package ui

import "github.com/ZviBaratz/atrium/session/agent"

// permissionModeLabel returns the display string for a --permission-mode value.
// Delegates to agent.ClaudePermissionModeLabel, the single source of truth for
// mode→label, so the list chip and the create form's chip row stay consistent
// (including modes only live detection surfaces, like bypassPermissions).
func permissionModeLabel(mode string) string {
	return agent.ClaudePermissionModeLabel(mode)
}
