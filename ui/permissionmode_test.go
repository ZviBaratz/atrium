package ui

import "testing"

func TestPermissionModeLabel(t *testing.T) {
	cases := []struct{ mode, want string }{
		{"plan", "plan"},
		{"auto", "auto"},
		{"acceptEdits", "accept-edits"},
		{"bypassPermissions", "bypassPermissions"}, // not a displayed chip; pass-through
	}
	for _, c := range cases {
		if got := permissionModeLabel(c.mode); got != c.want {
			t.Errorf("permissionModeLabel(%q) = %q, want %q", c.mode, got, c.want)
		}
	}
}
