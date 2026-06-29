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
	// accountSectionLines is the height the account section adds when present, mirroring
	// profileSectionLines (label + blank + options row + spacing).
	accountSectionLines = 4
	// profileSectionLines is the height the profile section adds when present (label + blank
	// + the names row + a divider).
	profileSectionLines = 4
	// modelSectionLines is the height the model section adds when present, mirroring
	// profileSectionLines (label + blank + input row + a divider).
	modelSectionLines = 4
	// modeSectionLines is the height the permission-mode section adds when present,
	// mirroring modelSectionLines (label + blank + chips row + a divider).
	modeSectionLines = 4
)

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
	t.titleInput.Width = width - 6
	if t.directoryPicker != nil {
		t.directoryPicker.SetWidth(width - 6)
	}
	if t.branchPicker != nil {
		t.branchPicker.SetWidth(width - 6)
	}
	if t.profilePicker != nil {
		t.profilePicker.SetWidth(width - 6)
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
	if t.profilePicker != nil {
		chrome += profileSectionLines
	}
	if t.modelField != nil {
		chrome += modelSectionLines
	}
	if t.modeField != nil {
		chrome += modeSectionLines
	}
	if t.hasAccountSection() {
		chrome += accountSectionLines
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
