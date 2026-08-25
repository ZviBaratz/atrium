package overlay

// defaultPickerRows / defaultPromptRows are the preferred number of list rows the directory
// and branch pickers render and the preferred prompt-textarea height. They are also the
// upper bound: SetSize only ever shrinks below these to fit a short terminal, never grows
// past them. The chosen counts are fixed for a given window size (computed in SetSize, not
// per render) so the overlay's height never changes as focus moves between sections —
// otherwise the vertically centered overlay (see app View / PlaceOverlay) would jump on Tab.
const (
	defaultPickerRows = 3
	defaultPromptRows = 4
	// minPickerRows / minPromptRows are the floors the form collapses to on short terminals.
	minPickerRows = 1
	minPromptRows = 1
	// maxPickerRows caps how far the picker lists grow on tall terminals. With
	// background repo discovery the candidate list can hold hundreds of repos,
	// so extra vertical room goes to the lists first (each extra row costs 2
	// lines — the directory and branch pickers share the count).
	maxPickerRows = 6
	// formChromeLines is every create-form line that is neither a picker row nor a prompt
	// row: the rounded border (2) + vertical padding (2) + the overlay title, the Title
	// field and its divider, each picker's header/blank/divider, the prompt label and its
	// divider, the help line, and the Create button. Used to size the form to the terminal.
	formChromeLines = 18
	// accountSectionLines is the height the account section adds when present
	// (label + blank + options row + a divider).
	accountSectionLines = 4
	// variantSectionLines is the height the variant section adds when present (label +
	// blank + the profile-count row + a divider). The session total and any batch error
	// ride the label line, not a separate row (see VariantPicker.Render), so this matches
	// the other picker sections and the tallest claude form still fits 80×24.
	variantSectionLines = 4
	// modelSectionLines is the height the model section adds when present
	// (label + blank + input row + a divider).
	modelSectionLines = 4
	// modeSectionLines is the height the permission-mode section adds when present,
	// mirroring modelSectionLines (label + blank + chips row + a divider).
	modeSectionLines = 4
	// effortSectionLines is the height the effort section adds when present,
	// mirroring modeSectionLines (label + blank + chips row + a divider).
	effortSectionLines = 4
	// depsSectionLines is the height the Dependencies section adds when present: one
	// content row + a divider, HALF what every section above costs, because
	// DepsField.Render emits one line rather than three. The claude override fields
	// spend their extra lines on a constant-height hint row explaining their no-op
	// chip (see ModeField.Render); this field has neither a no-op chip nor a pin, so
	// it has nothing to put there.
	//
	// Like every constant here this only feeds fitRows, which picks the picker and
	// prompt row counts — it must agree with what the section actually renders, and
	// nothing checks that agreement. It is NOT what keeps the form inside the screen:
	// by the tightest configuration fitRows is already pinned at its floors, so the
	// real bound is the rendered line itself, shed by fitOverlay. That is where the
	// budget bites — see TestSessionCreateOverlay_ClaudeFormFitsShortTerminal and
	// TestFitOverlay_DropsTheHeadingBeforeTheCreateButton, which fail if this section
	// grows a second RENDERED row, and are indifferent to this number.
	depsSectionLines = 2
	// collapsedClaudeSectionLines is the height the ONE section a non-claude form
	// renders (see renderCollapsedClaudeFields) occupies. It is deliberately the sum
	// of the three it replaces, not the two rows it has anything to say in: the block
	// pads itself back out to this.
	//
	// Padding rather than shrinking, because this section sits ABOVE nothing and
	// BELOW the variant control that toggles it. The collapse flips under a ↑/↓ on
	// that control, and the app centres this overlay with PlaceOverlay, which
	// re-centres on every height change — so a section that shortened here would
	// move the very row the user is holding a key on, and move it back on the next
	// press. Handing the freed rows to the pickers and the prompt instead is WORSE,
	// not better: those render above the variant row, so the row is pushed down by
	// the reflow at the same time as the re-centre lifts the form, and the two add
	// (measured: 8 rows against 4 for a plain shrink, 0 for this).
	//
	// The rows are not wasted at the size where rows are scarce. fitOverlay drops
	// blank lines before anything else, so at the 80×24 floor this block's padding is
	// the first thing shed and the form is exactly what it would have been had the
	// block never padded — which is where #690 measured the defect and where
	// TestCreateForm_FloorGoldens renders it.
	//
	// Derived, not written down: a hand-written number here could drift from the
	// three constants it must equal, and the drift would be a silent re-centre.
	collapsedClaudeSectionLines = modelSectionLines + effortSectionLines + modeSectionLines
)

// TextInputSize gives the prompt/create box 60% of the terminal's width and
// the full terminal height: the create form sizes its own sections to fit
// (and the plain prompt overlay applies its own fraction), so it needs the
// real height rather than a pre-scaled slice of it.
var TextInputSize = SizeSpec{WFrac: 0.6, HFrac: 1}

// SetSize is given the full terminal dimensions. The create form keeps every section at a
// constant height so the centered overlay does not jump as focus moves, but it sizes those
// sections to fit the terminal (shrinking the pickers and prompt on short screens). The plain
// prompt overlay keeps its original behavior of a textarea ~40% of the screen tall.
func (t *TextInputOverlay) SetSize(width, height int) {
	t.width = width
	t.height = height
	if t.isCreateForm {
		pickerRows, promptRows := t.fitRows(height)
		t.textarea.SetHeight(promptRows)
		if t.directoryPicker != nil {
			t.directoryPicker.SetVisibleRows(pickerRows)
		}
		if t.branchPicker != nil {
			t.branchPicker.SetVisibleRows(pickerRows)
		}
	} else {
		t.textarea.SetHeight(int(float32(height) * 0.4))
	}
	t.titleInput.SetWidth(width - 6)
	if t.directoryPicker != nil {
		t.directoryPicker.SetWidth(width - 6)
	}
	if t.branchPicker != nil {
		t.branchPicker.SetWidth(width - 6)
	}
	if t.variantPicker != nil {
		t.variantPicker.SetWidth(width - 6)
	}
	if t.modelField != nil {
		t.modelField.SetWidth(width - 6)
	}
	if t.accountPicker != nil {
		t.accountPicker.SetWidth(width - 6)
	}
}

// fitRows chooses the picker-row and prompt-row counts that make the create form fit within
// a terminal of the given height. It starts from the preferred defaults and shrinks to fit —
// picker rows first (the windowed list degrades gracefully to a single scrolling row), then
// the prompt — but never below the floors. On terminals too short for even the floors it
// returns the floors and the overlay clips minimally rather than misbehaving.
func (t *TextInputOverlay) fitRows(height int) (pickerRows, promptRows int) {
	pickerRows, promptRows = defaultPickerRows, defaultPromptRows
	chrome := formChromeLines
	if t.variantPicker != nil {
		chrome += variantSectionLines
	}
	if t.modelField != nil {
		chrome += modelSectionLines
	}
	if t.modeField != nil {
		chrome += modeSectionLines
	}
	if t.effortField != nil {
		chrome += effortSectionLines
	}
	if t.hasAccountSection() {
		chrome += accountSectionLines
	}
	if t.depsField != nil {
		chrome += depsSectionLines
	}
	const margin = 2 // keep a row above and below so the overlay isn't flush to the edges
	total := func() int { return 2*pickerRows + promptRows + chrome }
	for total() > height-margin {
		switch {
		case pickerRows > minPickerRows:
			pickerRows--
		case promptRows > minPromptRows:
			promptRows--
		default:
			return pickerRows, promptRows
		}
	}
	// Spare room on tall terminals goes to the picker lists (each increment
	// costs 2 lines: the directory and branch pickers share the count) — with
	// repo discovery the candidate list is worth the rows. The prompt keeps its
	// preferred height: a taller textarea doesn't show more useful information.
	for pickerRows < maxPickerRows && total()+2 <= height-margin {
		pickerRows++
	}
	return pickerRows, promptRows
}
