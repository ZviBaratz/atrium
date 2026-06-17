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
		{"codex-cli 0.12\n", "0.12", true},
		{"no version here", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		got, ok := parseVersion(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("parseVersion(%q) = (%q,%v), want (%q,%v)", c.in, got, ok, c.want, c.ok)
		}
	}
}
