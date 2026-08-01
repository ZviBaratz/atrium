package ui

import (
	"regexp"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui/theme"

	"charm.land/bubbles/v2/spinner"
	"github.com/stretchr/testify/require"
)

func mkInst(t *testing.T, title, path string) *session.Instance {
	t.Helper()
	i, err := session.NewInstance(session.InstanceOptions{Title: title, Path: path, Program: "echo"})
	require.NoError(t, err)
	return i
}

// siblingInGroup walks the repo group as a ring, skipping members the predicate
// rejects, confined to the group and wrapping at its ends. A lone member (or an
// all-rejected group) yields the input unchanged so an in-session jump is a no-op.
func TestSiblingInGroup_RingWalk(t *testing.T) {
	s := spinner.New()
	l := NewList(&s)
	a := mkInst(t, "a", "/tmp/repoA")
	b := mkInst(t, "b", "/tmp/repoA")
	c := mkInst(t, "c", "/tmp/repoA")
	d := mkInst(t, "d", "/tmp/repoB")
	for _, in := range []*session.Instance{a, b, c, d} {
		l.AddInstance(in)
	}

	yes := func(*session.Instance) bool { return true }

	require.Equal(t, b, l.siblingInGroup(a, +1, yes))
	require.Equal(t, c, l.siblingInGroup(b, +1, yes))
	require.Equal(t, a, l.siblingInGroup(c, +1, yes), "next wraps to group start")
	require.Equal(t, c, l.siblingInGroup(a, -1, yes), "prev wraps to group end")
	require.Equal(t, a, l.siblingInGroup(b, -1, yes))

	// d is alone in repoB → no sibling to move to.
	require.Equal(t, d, l.siblingInGroup(d, +1, yes))
	// Navigation never crosses a repo boundary.
	require.NotEqual(t, d, l.siblingInGroup(c, +1, yes))

	// Rejected members are skipped.
	notB := func(i *session.Instance) bool { return i != b }
	require.Equal(t, c, l.siblingInGroup(a, +1, notB), "skips b to reach c")

	// Whole group ineligible → stay put.
	none := func(*session.Instance) bool { return false }
	require.Equal(t, a, l.siblingInGroup(a, +1, none))

	// dir 0 and unknown instance are no-ops.
	require.Equal(t, a, l.siblingInGroup(a, 0, yes))
	require.Equal(t, a, l.siblingInGroup(a, 99, none))
}

func TestComposeSessionContext(t *testing.T) {
	a := mkInst(t, "alpha", "/tmp/repoA")

	name, left := ComposeSessionContext(a, "repoA")

	require.Equal(t, "alpha", name, "name drives the terminal title")
	// The header reads "<glyph> <repo> · <name>".
	require.Contains(t, left, "alpha")
	require.Contains(t, left, "repoA")
}

// With no repo (direct-mode sessions), the header collapses to "<glyph> <name>" with
// no repo field or separator.
func TestComposeSessionContext_NoRepo(t *testing.T) {
	a := mkInst(t, "alpha", "/tmp/repoA")

	_, left := ComposeSessionContext(a, "")

	require.Contains(t, left, "alpha")
	require.NotContains(t, left, "·", "no separator without a repo")
}

// Under NO_COLOR the pushed header must carry no tmux colour markup. tmux renders
// the status line itself, so Bubble Tea's colour profile never sees this string —
// it is the one surface where "the stack handles it" is false, and it is the most
// colour-saturated thing Atrium owns.
//
// #[bold] stays. Weight is not colour, and it is what keeps the session name
// findable once the tint is gone. #[default] stays too, for a less obvious reason:
// it resets ATTRIBUTES as well as colour, and the format relies on it to close each
// field back to the bar's own style.
func TestComposeSessionContextDropsColourUnderMono(t *testing.T) {
	defer theme.SetMono(true)()

	inst := mkInst(t, "alpha", "/tmp/repoA")
	_, left := ComposeSessionContext(inst, "myrepo")

	require.NotContains(t, left, "#[fg=", "no foreground markup under NO_COLOR")
	require.Contains(t, left, "#[bold]", "weight must survive: it replaces colour as the hierarchy")
	// Counted, not merely present: the three fields are the agent glyph, the state
	// glyph and the bold name, and a bare Contains here passes on the bold one alone
	// while both glyph fields have lost their terminator.
	require.Equal(t, 3, strings.Count(left, "#[default]"),
		"every field still terminates with #[default]: it resets attributes, not just colour")
	require.Contains(t, left, "myrepo", "the content itself is unchanged")
	require.Contains(t, left, "alpha")
}

// And with colour on, the markup is still there — the negative control. Without
// it, a ComposeSessionContext that emitted no colour at all would pass the test
// above, and the assertion would prove nothing.
func TestComposeSessionContextKeepsColourByDefault(t *testing.T) {
	inst := mkInst(t, "alpha", "/tmp/repoA")
	_, left := ComposeSessionContext(inst, "myrepo")

	require.Contains(t, left, "#[fg=", "colour markup is the default")
}

// Dropping the colour must not move anything else. The bar's spacing is carried by
// literal spaces around the markup, so a helper that swallowed one would shift every
// field without failing either test above.
func TestComposeSessionContextMonoDiffersOnlyByColour(t *testing.T) {
	inst := mkInst(t, "alpha", "/tmp/repoA")

	_, coloured := ComposeSessionContext(inst, "myrepo")

	restore := theme.SetMono(true)
	_, mono := ComposeSessionContext(inst, "myrepo")
	restore()

	stripped := regexp.MustCompile(`#\[fg=[^]]*\]`).ReplaceAllString(coloured, "")
	require.Equal(t, stripped, mono,
		"the mono header must be the coloured one with only the #[fg=…] wrappers removed")
}

// '#' in dynamic text must be escaped so tmux doesn't read it as a format directive.
func TestTmuxEsc(t *testing.T) {
	require.Equal(t, "a##b", tmuxEsc("a#b"))
	require.Equal(t, "plain", tmuxEsc("plain"))
}
