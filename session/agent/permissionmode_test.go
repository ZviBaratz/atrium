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

func TestPermissionModeLabels_LenMatchesModes(t *testing.T) {
	if len(ClaudePermissionModeLabels) != len(ClaudePermissionModes) {
		t.Errorf("ClaudePermissionModeLabels has %d entries, ClaudePermissionModes has %d — they must match",
			len(ClaudePermissionModeLabels), len(ClaudePermissionModes))
	}
}

// Every chip the form offers must validate, or createSessionFromForm would
// reject a mode the UI itself offered (the two lists are maintained by hand;
// the enum is deliberately a superset, so it cannot be derived from the chips).
func TestValidPermissionMode_CoversOfferedChips(t *testing.T) {
	for _, m := range ClaudePermissionModes {
		if !ValidPermissionMode(m) {
			t.Errorf("offered chip %q is not in the permission-mode enum", m)
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
		// A flag-lookalike token inside a quoted argument must not trigger the
		// replace path — strings.Fields can't see quoting, so quoted programs
		// append instead (argv last-wins keeps the override effective).
		{"flag token inside a quoted argument appends",
			`claude --append-system-prompt "never use --permission-mode plan"`, "acceptEdits",
			`claude --append-system-prompt "never use --permission-mode plan" --permission-mode acceptEdits`},
		{"real pin alongside quoting appends last-wins",
			`claude --permission-mode plan --append-system-prompt 'be nice'`, "auto",
			`claude --permission-mode plan --append-system-prompt 'be nice' --permission-mode auto`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := WithPermissionModeFlag(c.program, c.mode); got != c.want {
				t.Errorf("WithPermissionModeFlag(%q, %q) = %q, want %q", c.program, c.mode, got, c.want)
			}
		})
	}
}
