package doctor

import "testing"

func TestParseVersion(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"2.1.179 (Claude Code)\n", "2.1.179", true},
		{"0.45.1\n", "0.45.1", true},
		{"aider 0.64.1\n", "0.64.1", true},
		{"codex-cli 0.12\n", "0.12", true}, // two-component: no full semver to prefer
		{"no version here", "", false},
		{"", "", false},
		// A full MAJOR.MINOR.PATCH wins over a bare MAJOR.MINOR printed first, so a
		// toolchain banner before the real version doesn't mislead.
		{"built with Go 1.21\nmytool 2.3.4\n", "2.3.4", true},
		{"v1.2.3\n", "1.2.3", true},                // leading "v" prefix
		{"tool 2.1.179-beta.1\n", "2.1.179", true}, // prerelease suffix trimmed by token match
		{"only minor 0.27 here\n", "0.27", true},   // bare MAJOR.MINOR fallback
	}
	for _, c := range cases {
		got, ok := parseVersion(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("parseVersion(%q) = (%q,%v), want (%q,%v)", c.in, got, ok, c.want, c.ok)
		}
	}
}
