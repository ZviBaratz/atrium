package agent

import "testing"

func TestValidPermissionMode(t *testing.T) {
	// The full CLI enum (claude 2.1.172 --help) — including the two modes the
	// picker doesn't offer, so a profile-pinned value still validates.
	valid := []string{"acceptEdits", "auto", "bypassPermissions", "default", "dontAsk", "plan"}
	for _, s := range valid {
		if !ValidPermissionMode(s) {
			t.Errorf("ValidPermissionMode(%q) = false, want true", s)
		}
	}
	invalid := []string{"", "Plan", "accept-edits", "yolo", "plan; rm -rf"}
	for _, s := range invalid {
		if ValidPermissionMode(s) {
			t.Errorf("ValidPermissionMode(%q) = true, want false", s)
		}
	}
}

func TestWithPermissionModeFlag(t *testing.T) {
	cases := []struct {
		name, program, mode, want string
	}{
		{"append to bare program", "claude", "plan", "claude --permission-mode plan"},
		{"append preserves existing flags",
			"claude --model opus", "acceptEdits",
			"claude --model opus --permission-mode acceptEdits"},
		{"replace separate-form pin",
			"claude --permission-mode acceptEdits", "plan", "claude --permission-mode plan"},
		{"replace combined-form pin",
			"claude --permission-mode=acceptEdits", "plan", "claude --permission-mode plan"},
		{"replace keeps trailing flags",
			"claude --permission-mode plan --model opus", "auto",
			"claude --model opus --permission-mode auto"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := WithPermissionModeFlag(c.program, c.mode); got != c.want {
				t.Errorf("WithPermissionModeFlag(%q, %q) = %q, want %q", c.program, c.mode, got, c.want)
			}
		})
	}
}
