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

// Every row the field can render must fit the 42 inner cells an 80-col terminal
// yields — and unlike the claude fields, this one shares that budget with its own
// label, because it renders on a single line (see depsField.go). Driven through
// SetTarget, which is the only thing that picks between the two placeholders, so
// adding a third verdict cannot skip the measurement.
func TestDepsFieldRowWidths(t *testing.T) {
	f := NewDepsField()
	f.Focus()
	assert.LessOrEqual(t, lipgloss.Width(f.Render()), claudeFieldInnerWidth,
		"the chip row must fit beside its label")

	for _, k := range []targetKind{targetDirect, targetInvalid} {
		f.SetTarget(k)
		assert.LessOrEqual(t, lipgloss.Width(f.Render()), claudeFieldInnerWidth,
			"the inert placeholder for target %d must fit beside its label", k)
	}
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

// ...and it is scoped to the DEFAULT heading, which is the part that is easy to get
// wrong: openForkForm builds an ordinary create overlay and overwrites Title with
// "Fork from checkpoint · <stamp>", which its own comment calls the only thing on
// screen saying this submit forks rather than creates — and which checkpoint it forks
// from. Shedding it as decoration hands the user a form indistinguishable from a plain
// create that quietly forks on submit.
//
// Same fixture and same size as the drop test above, so the two differ in exactly the
// thing under test: at 80×24 this form is over budget either way.
func TestFitOverlay_KeepsAnOverriddenHeading(t *testing.T) {
	o := NewSessionCreateOverlay(mixedProfiles, twoAccounts, []string{"/repo/a"}, "claude", linkedPaths)
	o.SetBranchResults([]string{"main", "develop", "feature/x"}, o.BranchFilterVersion())
	o.Title = "Fork from checkpoint · 14:32"
	o.SetSize(80, 24)

	out := o.Render()
	assert.LessOrEqual(t, strings.Count(out, "\n")+1, 24, "still bounded by the terminal")
	assert.Contains(t, out, "Fork from checkpoint",
		"an overridden heading carries what nothing else on the form says")
	assert.Contains(t, out, "14:32", "including which checkpoint, not just that it forks")
	assert.Contains(t, out, "Create", "and the submit control still survives")
}

// The clip of last resort takes the tail, so on its own it takes the submit button —
// the one control the overlay exists for. Driven below the 24-row floor, where the
// heading stage has already been spent and cannot buy another row.
func TestFitOverlay_ClipKeepsTheSubmitButton(t *testing.T) {
	t.Run("create form", func(t *testing.T) {
		o := NewSessionCreateOverlay(mixedProfiles, twoAccounts, []string{"/repo/a"}, "claude", linkedPaths)
		o.SetSize(80, 12)
		out := o.Render()
		assert.LessOrEqual(t, strings.Count(out, "\n")+1, 12)
		assert.Contains(t, out, "Create")
	})
	t.Run("plain overlay", func(t *testing.T) {
		o := NewTextInputOverlay("Send to session", "")
		o.SetSize(80, 6)
		out := o.Render()
		assert.LessOrEqual(t, strings.Count(out, "\n")+1, 6)
		assert.Contains(t, out, "Enter")
	})
}

// #545, applied to this field: the two inert targets are not the same fact, and the
// wrong one tells the user they have a session they can create. The branch picker had
// to grow this distinction; a field that borrows its vocabulary must borrow the half
// that fixed it, or the two rows sit one section apart contradicting each other.
func TestDepsField_InertNoteNamesWhichNonGitTarget(t *testing.T) {
	o := NewSessionCreateOverlay(nil, nil, []string{"/repo/a"}, "claude", linkedPaths)
	o.SetSize(80, 40)

	o.SetTargetValidity(true, true, "") // a directory, but not a repo
	assert.Contains(t, o.Render(), "direct session")

	o.SetTargetValidity(false, false, "") // not a directory at all
	out := o.Render()
	assert.Contains(t, out, "not a directory")
	assert.NotContains(t, out, "n/a — direct session",
		"a path that is not a directory must not be reported as a creatable direct session")
}

// What the clip costs, pinned — because the render is the only place it is visible
// and no assertion above could see it. The fork form keeps its heading and so runs
// one row over budget at 80×24; the row the clip takes is the one above the button,
// which is the hint footer. That is a defensible trade (Enter and Esc still work,
// and #466 makes the footer the only owner of ⌃S/⌃R, but the heading is the only
// statement anywhere that this submit forks) — and it is one row from being a much
// worse one, because the next line up is a real field.
//
// So this asserts the boundary rather than the trade: every field survives, and the
// hint is what pays. A failure here means the form grew and the clip has started
// eating content the user has to fill in.
func TestFitOverlay_ClipTakesTheHintBeforeAnyField(t *testing.T) {
	o := NewSessionCreateOverlay(mixedProfiles, twoAccounts, []string{"/repo/a"}, "claude", linkedPaths)
	o.SetBranchResults([]string{"main", "develop", "feature/x"}, o.BranchFilterVersion())
	o.Title = "Fork from checkpoint · 14:32"
	o.SetSize(80, 24)
	out := o.Render()

	for _, field := range []string{"Project", "Title", "Prompt", "Model", "Effort", "Permissions", "Account", "Dependencies"} {
		assert.Contains(t, out, field, "the clip must not reach a field the user has to fill in")
	}
	assert.Contains(t, out, "Create", "nor the control the form exists for")
	assert.NotContains(t, out, "⌃R clear",
		"the hint footer is the designated sacrifice; if it now survives, the budget moved — re-read fitOverlay")
}
