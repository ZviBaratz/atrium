package session

import "testing"

// Duplicate detection must compare derived names, not raw titles: two distinct
// titles that sanitize to the same tmux segment or the same branch slug would
// still collide at the tmux or git layer.
func TestDerivedNamesCollide(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
		why  string
	}{
		{"same", "same", true, "identical titles"},
		{"Fix Bug", "fixbug", true, "tmux strips whitespace; case-insensitively equal segments"},
		{"x-y", "x y", true, "branch slug lowercases and dashes spaces"},
		{"v1.2", "v1 2", false, "dots and spaces sanitize differently in both layers"},
		{"a.b", "a_b", true, "tmux maps dots to underscores; segments equal"},
		{"foo", "bar", false, "unrelated titles"},
		{"alpha", "alpha2", false, "prefix of the other is not a collision"},
		{"日本語", "中文", false, "distinct non-ASCII titles get distinct branch slugs (issue #187)"},
	}
	for _, c := range cases {
		if got := DerivedNamesCollide("zvi/", c.a, c.b); got != c.want {
			t.Errorf("DerivedNamesCollide(%q, %q) = %v, want %v (%s)", c.a, c.b, got, c.want, c.why)
		}
	}
}

// Every derived sibling is reserved in BOTH directions. Only one suffix was covered
// before the run command existed, and the direction that is easy to lose is the second:
// the candidate can be the sibling rather than the parent, because
// QualifiedSessionName maps a dot to an underscore.
func TestDerivedTmuxNameCollides(t *testing.T) {
	cases := []struct {
		cand, name string
		want       bool
		why        string
	}{
		{"atrium_g_foo", "atrium_g_foo", true, "the same name"},
		{"atrium_g_foo_term", "atrium_g_foo", true, "candidate is the terminal shell of an existing session"},
		{"atrium_g_foo_run", "atrium_g_foo", true, "candidate is the run command of an existing session"},
		{"atrium_g_foo", "atrium_g_foo_term", true, "an existing session IS a terminal-shell name"},
		{"atrium_g_foo", "atrium_g_foo_run", true, "an existing session IS a run-command name"},
		{"atrium_g_foo", "atrium_g_bar", false, "unrelated sessions"},
		{"atrium_g_foo", "atrium_g_foo2", false, "a prefix is not a derived sibling"},
		{"atrium_g_foo_runner", "atrium_g_foo", false, "a suffix must be the whole suffix"},
		{"atrium_g_foo", "", false, "an instance with no tmux name yet reserves nothing"},
	}
	for _, c := range cases {
		if got := DerivedTmuxNameCollides(c.cand, c.name); got != c.want {
			t.Errorf("DerivedTmuxNameCollides(%q, %q) = %v, want %v (%s)", c.cand, c.name, got, c.want, c.why)
		}
	}
}

// The sibling names an instance HOLDS are invisible to DerivedTmuxNameCollides, which can
// only derive them from the instance's current tmux name. Both are owned rather than
// derived, so a deep rename leaves them on the old name — and frees the old title for a new
// session to claim, which would mint straight onto a live shell or dev server.
func TestOwnedSiblingCollides(t *testing.T) {
	// A session renamed from "foo" to "bar" that still hosts both siblings under the
	// names it minted while it was "foo".
	renamed := &Instance{
		ident:    identity{title: "bar"},
		tmuxName: "atrium_g_bar",
		termName: "atrium_g_foo_term",
		runName:  "atrium_g_foo_run",
	}
	cases := []struct {
		cand string
		inst *Instance
		want bool
		why  string
	}{
		{"atrium_g_foo", renamed, true, "a new session with the freed title would mint onto both siblings"},
		{"atrium_g_foo_term", renamed, true, "a title sanitizing onto the held shell name itself"},
		{"atrium_g_foo_run", renamed, true, "a title sanitizing onto the held run-session name itself"},
		{"atrium_g_bar", renamed, false, "the instance's own current name is DerivedTmuxNameCollides' job"},
		{"atrium_g_baz", renamed, false, "an unrelated title"},
		{"atrium_g_foo2", renamed, false, "a prefix of the held name is not a collision"},
		{"", renamed, false, "no candidate, nothing to collide with"},
		{"atrium_g_foo", nil, false, "no instance, nothing held"},
		{"atrium_g_foo", &Instance{ident: identity{title: "foo"}, tmuxName: "atrium_g_foo"}, false,
			"an instance holding no siblings reserves nothing here"},
		{"atrium_g_foo", &Instance{termName: "atrium_g_foo_run"}, false,
			"the shell suffix is not matched against a run-session name"},
	}
	for _, c := range cases {
		if got := OwnedSiblingCollides(c.cand, c.inst); got != c.want {
			t.Errorf("OwnedSiblingCollides(%q, %v) = %v, want %v (%s)", c.cand, c.inst, got, c.want, c.why)
		}
	}
}
