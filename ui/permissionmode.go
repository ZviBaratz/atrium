package ui

// permissionModeLabel returns the display string for a --permission-mode
// value, using kebab-case for camelCase modes to match the create form's
// ClaudePermissionModeLabels convention.
func permissionModeLabel(mode string) string {
	if mode == "acceptEdits" {
		return "accept-edits"
	}
	return mode
}
