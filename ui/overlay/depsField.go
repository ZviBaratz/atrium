package overlay

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

// depsFieldNA is the dim placeholder the field renders while it is inert. The reason
// is always the same one — the target has no worktree to seed, so there is nothing to
// isolate — and it borrows the branch picker's vocabulary for the same verdict
// ("direct session — no git branching").
//
// Its budget is tighter than the claude fields' claudeFieldNA, and the difference is
// structural rather than stylistic: those render their label on a line of its own, so
// the placeholder gets all 42 cells an 80-col terminal yields, while this field is one
// line and must share them with the 12-cell "Dependencies" label.
//
// The widths are asserted rather than stated here — TestDepsFieldRowWidths measures
// both rows against claudeFieldInnerWidth, and TestCreateForm_ComposesWithinInnerWidth
// sweeps the whole composed form via its "link paths, direct target" fixture.
const depsFieldNA = "  n/a — direct session"

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
}

// NewDepsField builds the field, starting on "shared" — today's behaviour, so an
// untouched form creates exactly the session it created before.
func NewDepsField() *DepsField {
	return &DepsField{chipRow{options: []string{"shared", "isolated"}}}
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

// Render renders the field on a single line: label, then the chip row (or the dim
// placeholder when inert). The height is the same either way, so the form does not
// jump when the target changes.
func (f *DepsField) Render() string {
	var s strings.Builder
	s.WriteString(mfLabelStyle().Render("Dependencies"))
	if f.disabled {
		s.WriteString(mfDimStyle().Render(depsFieldNA))
		return s.String()
	}
	s.WriteString(" ")
	s.WriteString(f.render())
	return s.String()
}
