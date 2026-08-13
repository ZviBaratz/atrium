package overlay

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

// The dim placeholders the field renders while it is inert. The field is equally inert
// for both, but they are not the same fact and must not collapse into one sentence:
// reporting a path that is not a directory at all as a "direct session" tells the user
// they have a creatable session when they do not, which is the defect #545 was filed
// for and removed from the branch picker's placeholder. This borrows that picker's
// vocabulary — including the half it had to grow.
//
// Their budget is tighter than the claude fields' claudeFieldNA, and the difference is
// structural rather than stylistic: those render their label on a line of its own, so
// the placeholder gets all 42 cells an 80-col terminal yields, while this field is one
// line and must share them with the 12-cell "Dependencies" label.
//
// The widths are asserted rather than stated here — TestDepsFieldRowWidths measures
// every row against claudeFieldInnerWidth, and TestCreateForm_ComposesWithinInnerWidth
// sweeps the whole composed form via its "link paths, direct target" fixture.
const (
	depsFieldNADirect  = "  n/a — direct session"
	depsFieldNAInvalid = "  n/a — not a directory"
)

// Chip indices. Chip 0 is "shared", a REAL default rather than the no-op chip every
// claude override field starts with, which is why this field exposes Isolate()
// rather than chipRow.selected() — the latter reports "" for chip 0.
const (
	depsShared = iota
	depsIsolated
)

// DepsField is the create form's per-session link_paths write-direction choice
// (#481): "shared" gives the session the configured symlinks into the origin
// checkout, "isolated" gives it none, so an `npm install` it runs cannot reach the
// user's own tree or any sibling session's.
//
// The field exists only when link_paths configures something to isolate — with an
// empty list the choice has nothing to act on, and a permanently-inert section would
// cost every user a row of a height-budgeted form for a feature they do not use. It
// goes inert (rather than absent) when the target is not a git repository, because
// that verdict changes as the user retypes the path and a section appearing and
// vanishing mid-edit reflows the form under them.
//
// One line, not two. The claude override fields spend a second line on a
// constant-height hint row explaining their no-op chip (see ModeField.Render); this
// field has no no-op chip and no pin to explain, and the form's height budget at
// 80x24 has no room to spare once profiles, the three claude fields and an account
// picker are all present.
type DepsField struct {
	chipRow
	// target is what the selected directory turned out to be, and so which inert
	// placeholder to render. Only targetGit leaves the field live; the embedded
	// chipRow's disabled flag is derived from it in SetTarget rather than set
	// independently, so the two can never disagree about whether the field is inert.
	target targetKind
}

// NewDepsField builds the field, starting on "shared" — today's behaviour, so an
// untouched form creates exactly the session it created before.
func NewDepsField() *DepsField {
	return &DepsField{chipRow: chipRow{options: []string{"shared", "isolated"}}}
}

// SetTarget records what the selected target is, marking the field inert for anything
// but a git repo — only a git target gets a worktree, and seeding is what there is to
// isolate. The chip selection is retained, so flipping back to a git target restores
// it (the branch picker retains its selection for the same reason).
func (f *DepsField) SetTarget(k targetKind) {
	f.target = k
	f.SetDisabled(k != targetGit)
}

// HandleKeyPress cycles the chips with the arrow keys; every other key is a no-op
// (see chipRow.moveCursor).
func (f *DepsField) HandleKeyPress(msg tea.KeyPressMsg) {
	if f.disabled {
		return
	}
	f.moveCursor(msg)
}

// Isolate reports whether the session should be created dependency-isolating.
//
// The disabled guard matters as much as the cursor check: a form pointed at a git
// repo, switched to "isolated", and then retargeted at a directory that is not a
// repo must not carry that choice into the submit. BranchPicker.GetSelectedBranch
// refuses a stale selection the same way and for the same reason.
func (f *DepsField) Isolate() bool {
	return !f.disabled && f.cursor == depsIsolated
}

// inertNote is the placeholder for the current inert target: which of the two
// non-git verdicts the path landed on. Only reached while disabled, so the git
// case returns the direct wording it can never render.
func (f *DepsField) inertNote() string {
	if f.target == targetInvalid {
		return depsFieldNAInvalid
	}
	return depsFieldNADirect
}

// Render renders the field on a single line: label, then the chip row (or the dim
// placeholder when inert). The height is the same either way, so the form does not
// jump when the target changes.
func (f *DepsField) Render() string {
	var s strings.Builder
	s.WriteString(mfLabelStyle().Render("Dependencies"))
	if f.disabled {
		s.WriteString(mfDimStyle().Render(f.inertNote()))
		return s.String()
	}
	s.WriteString(" ")
	s.WriteString(f.render())
	return s.String()
}
