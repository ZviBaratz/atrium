package overlay

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// linkedPaths is a non-empty link_paths list, which is the whole of what the create
// form reads from that config key: something to isolate exists.
var linkedPaths = []string{"node_modules"}

// Both rows the field can render must fit the 42 inner cells an 80-col terminal
// yields — and unlike the claude fields, this one shares that budget with its own
// label, because it renders on a single line (see depsField.go).
func TestDepsFieldRowWidths(t *testing.T) {
	f := NewDepsField()
	f.Focus()
	assert.LessOrEqual(t, lipgloss.Width(f.Render()), claudeFieldInnerWidth,
		"the chip row must fit beside its label")

	f.SetDisabled(true)
	assert.LessOrEqual(t, lipgloss.Width(f.Render()), claudeFieldInnerWidth,
		"the inert placeholder must fit beside its label")
}

// The section exists only when link_paths names something to isolate. With an empty
// list the choice has nothing to act on, and the form is height-budgeted enough that a
// permanently dead row is a real cost (see TestSessionCreateOverlay_ClaudeFormFitsShortTerminal).
func TestSessionCreateOverlay_DepsSectionOnlyWithLinkPaths(t *testing.T) {
	off := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "claude", nil)
	off.SetSize(80, 40)
	assert.NotContains(t, off.Render(), "Dependencies",
		"no link_paths means nothing to isolate, so the section must not render")
	assert.False(t, off.GetIsolateDeps())

	on := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "claude", linkedPaths)
	on.SetSize(80, 40)
	assert.Contains(t, on.Render(), "Dependencies")
}

// Selecting "isolated" must reach the accessor the submit path reads — the whole
// point of the field. Driven through the overlay's real key handling rather than the
// widget's, because a registered focus stop with no routing arm is exactly the kind of
// dead control a rendering assertion cannot see.
func TestSessionCreateOverlay_DepsSelectionReachesAccessor(t *testing.T) {
	o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "claude", linkedPaths)
	o.SetSize(80, 40)
	o.SetTargetValidity(true, false, "main")
	o.focusStop(stopDeps)
	require.True(t, o.isDepsField(), "the deps field must be a focus stop when present")

	assert.False(t, o.GetIsolateDeps(), "the form must default to today's behavior: shared")
	o.HandleKeyPress(keyMsg("right"))
	assert.True(t, o.GetIsolateDeps(), "→ must select isolated")
	o.HandleKeyPress(keyMsg("left"))
	assert.False(t, o.GetIsolateDeps(), "← must return to shared")
}

// A direct (or invalid) target has no worktree, so there is nothing to seed and
// nothing to isolate. The field goes inert rather than absent — the verdict changes as
// the user retypes the path, and a section appearing and vanishing mid-edit reflows
// the form under them.
//
// The second half is the one that matters: a choice made while the target was a git
// repo must not survive a retarget to one that is not.
func TestSessionCreateOverlay_DepsFieldInertForNonGitTarget(t *testing.T) {
	o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "claude", linkedPaths)
	o.SetSize(80, 40)
	o.SetTargetValidity(true, false, "main")
	o.focusStop(stopDeps)
	o.HandleKeyPress(keyMsg("right"))
	require.True(t, o.GetIsolateDeps())

	o.SetTargetValidity(true, true, "") // retargeted at a non-git directory
	assert.Contains(t, o.Render(), "Dependencies", "the section stays, inert")
	assert.False(t, o.stopEnabled(stopDeps), "an inert field must be skipped by navigation")
	assert.False(t, o.GetIsolateDeps(),
		"a choice made for a git target must not leak into a direct session's submit")
	assert.False(t, o.isDepsField(), "focus must be evicted off the field it just disabled")

	o.SetTargetValidity(false, false, "") // and for a path that is not a directory
	assert.False(t, o.stopEnabled(stopDeps))
}

// The form's constant-height invariant: the section is the same number of lines
// however it is rendered, so the vertically centered overlay does not jump.
func TestSessionCreateOverlay_DepsSectionHeightConstant(t *testing.T) {
	o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "claude", linkedPaths)
	o.SetSize(80, 40)
	o.SetTargetValidity(true, false, "main")

	o.focusStop(stopDeps)
	focused := strings.Count(o.Render(), "\n")
	o.focusStop(stopTitle)
	blurred := strings.Count(o.Render(), "\n")
	assert.Equal(t, focused, blurred, "overlay height must not change with deps focus")

	o.SetTargetValidity(true, true, "")
	inert := strings.Count(o.Render(), "\n")
	assert.Equal(t, blurred, inert, "overlay height must not change when the deps field goes inert")
}

// The third compaction stage, added with this section (#481). fitOverlay sheds blank
// lines, then dividers, and then — for a create form only — its own heading, because
// the line the hard clip below it would take is the Create button.
//
// The tallest form there is exercises the whole ladder at 80×24; the assertion is that
// the control the form exists for survives, and the heading is what paid for it.
func TestFitOverlay_DropsTheHeadingBeforeTheCreateButton(t *testing.T) {
	o := NewSessionCreateOverlay(mixedProfiles, twoAccounts, []string{"/repo/a"}, "claude", linkedPaths)
	o.SetBranchResults([]string{"main", "develop", "feature/x"}, o.BranchFilterVersion())
	o.SetSize(80, 24)

	out := o.Render()
	assert.LessOrEqual(t, strings.Count(out, "\n")+1, 24)
	assert.Contains(t, out, "Create", "the Create button must survive compaction")
	assert.Contains(t, out, "Dependencies", "and so must the section that made it tight")
	assert.NotContains(t, out, "New session",
		"the heading is what pays for them at this size")

	// It is a last resort, not a default: with room to spare the heading stays.
	o.SetSize(80, 40)
	assert.Contains(t, o.Render(), "New session")
}

// The stage is scoped to the create form. The plain prompt overlay has no Create
// button to protect and its heading names which session it is composing to, so
// dropping that would be a loss with nothing bought.
func TestFitOverlay_KeepsTheHeadingOnAPlainPrompt(t *testing.T) {
	o := NewTextInputOverlay("Send to session", "")
	o.SetSize(80, 8)
	assert.Contains(t, o.Render(), "Send to session")
}
