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

func TestPermissionModeFlag(t *testing.T) {
	cases := []struct {
		name, program, want string
	}{
		{"no flag", "claude", ""},
		{"plan flag", "claude --permission-mode plan", "plan"},
		{"acceptEdits flag", "claude --permission-mode acceptEdits", "acceptEdits"},
		{"auto flag", "claude --permission-mode auto", "auto"},
		{"combined form", "claude --permission-mode=plan", "plan"},
		{"last wins", "claude --permission-mode plan --permission-mode auto", "auto"},
		{"invalid value returns empty", "claude --permission-mode yolo", ""},
		// "default" IS a valid mode; the extractor returns it verbatim. The
		// renderer filters mode == "default" separately so the chip stays
		// hidden for explicit default pins — same as the no-flag case.
		{"explicit default pin returns 'default'", "claude --permission-mode default", "default"},
		{"non-claude program returns empty via caller gate — extractor sees it",
			"aider --permission-mode plan", "plan"},
		{"flag alongside model", "claude --model opus --permission-mode acceptEdits", "acceptEdits"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := PermissionModeFlag(c.program); got != c.want {
				t.Errorf("PermissionModeFlag(%q) = %q, want %q", c.program, got, c.want)
			}
		})
	}
}
