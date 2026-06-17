package ui

import "testing"

func TestPermissionModeLabel(t *testing.T) {
	cases := []struct{ mode, want string }{
		{"plan", "plan"},
		{"auto", "auto"},
		{"acceptEdits", "accept-edits"},
		// Not an offered create-form chip, but live footer detection surfaces it;
		// shortened to a clean chip rather than the raw enum.
		{"bypassPermissions", "bypass"},
		{"dontAsk", "dontAsk"}, // undetected/unlabeled mode falls through verbatim
	}
	for _, c := range cases {
		if got := permissionModeLabel(c.mode); got != c.want {
			t.Errorf("permissionModeLabel(%q) = %q, want %q", c.mode, got, c.want)
		}
	}
}
