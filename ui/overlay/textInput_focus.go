package overlay

// focusStop identifies a focusable component in the overlay. The overlay holds an
// ordered slice of the stops that are actually present, so adding or removing a
// component is a matter of editing that slice rather than juggling hardcoded indices.
type focusStop int

const (
	stopTitle focusStop = iota
	stopDirectory
	stopProfile
	stopModel
	stopMode
	stopAccount
	stopTextarea
	stopBranch
	stopEnter
)

// focusRing is the overlay's focus cursor: the ordered list of focus stops actually
// present plus the index of the focused one. It owns navigation — looking a stop up,
// moving the cursor, and the wrap-around traversal that skips disabled stops — while the
// overlay keeps the pieces that must read concrete widgets: stopEnabled (the
// state-dependent predicate) and updateFocusState (the focus/blur fan-out).
type focusRing struct {
	stops []focusStop
	index int
}

// current returns the focusStop the cursor points at (stopEnter when out of range).
func (r *focusRing) current() focusStop {
	if r.index < 0 || r.index >= len(r.stops) {
		return stopEnter
	}
	return r.stops[r.index]
}

// indexOf returns the position of a stop kind in the ring, or -1 if absent.
func (r *focusRing) indexOf(kind focusStop) int {
	for i, s := range r.stops {
		if s == kind {
			return i
		}
	}
	return -1
}

// set moves the cursor to index i.
func (r *focusRing) set(i int) { r.index = i }

// nextEnabled returns the first enabled stop index reached from the current cursor by
// repeatedly stepping `delta` (+1 forward, -1 backward), wrapping around the stop list. The
// loop visits each stop at most once, so it terminates even with several stops disabled; the
// caller keeps one always-enabled stop (the Create button), so an enabled stop always exists.
func (r *focusRing) nextEnabled(delta int, enabled func(focusStop) bool) int {
	n := len(r.stops)
	i := r.index
	for range r.stops {
		i = (i + delta + n) % n
		if enabled(r.stops[i]) {
			return i
		}
	}
	return r.index
}

// currentStop returns the focusStop the cursor currently points at.
func (t *TextInputOverlay) currentStop() focusStop { return t.focus.current() }

// ModeFocused reports whether the Permissions (mode) chip currently has focus.
func (t *TextInputOverlay) ModeFocused() bool { return t.isModeField() }

// TitleFocused reports whether the title field currently has focus.
func (t *TextInputOverlay) TitleFocused() bool { return t.isTitle() }

func (t *TextInputOverlay) isTitle() bool           { return t.currentStop() == stopTitle }
func (t *TextInputOverlay) isDirectoryPicker() bool { return t.currentStop() == stopDirectory }
func (t *TextInputOverlay) isProfilePicker() bool   { return t.currentStop() == stopProfile }
func (t *TextInputOverlay) isModelField() bool      { return t.currentStop() == stopModel }
func (t *TextInputOverlay) isModeField() bool       { return t.currentStop() == stopMode }
func (t *TextInputOverlay) isAccountPicker() bool   { return t.currentStop() == stopAccount }
func (t *TextInputOverlay) isTextarea() bool        { return t.currentStop() == stopTextarea }
func (t *TextInputOverlay) isBranchPicker() bool    { return t.currentStop() == stopBranch }
func (t *TextInputOverlay) isEnterButton() bool     { return t.currentStop() == stopEnter }

// hasAccountSection reports whether the form shows the Account picker. It requires
// ≥2 accounts: a lone account offers no choice, so rendering it would be a dead,
// unfocusable row (and stopAccount is likewise only added when HasMultiple).
func (t *TextInputOverlay) hasAccountSection() bool {
	return t.accountPicker != nil && t.accountPicker.HasMultiple()
}

// indexOfStop returns the focus index of a stop kind, or -1 if absent.
func (t *TextInputOverlay) indexOfStop(kind focusStop) int { return t.focus.indexOf(kind) }

// focusStop moves focus to the given stop kind (if present) and syncs focus state.
func (t *TextInputOverlay) focusStop(kind focusStop) {
	if i := t.indexOfStop(kind); i >= 0 {
		t.setFocusIndex(i)
	}
}

// setFocusIndex sets the focus index and syncs focus state.
func (t *TextInputOverlay) setFocusIndex(i int) {
	t.focus.set(i)
	t.updateFocusState()
}

// stopEnabled reports whether a stop can take focus: the branch picker is disabled when
// the target is not a git repo, the model field when the effective program is not claude;
// every other stop is always enabled.
func (t *TextInputOverlay) stopEnabled(kind focusStop) bool {
	if kind == stopBranch && t.branchPicker != nil && t.branchPicker.Disabled() {
		return false
	}
	if kind == stopMode && t.modeField != nil && t.modeField.Disabled() {
		return false
	}
	if kind == stopModel && t.modelField != nil && t.modelField.Disabled() {
		return false
	}
	return true
}

// nextEnabledIndex returns the first enabled stop index reached from the current cursor by
// stepping `delta` (+1 forward, -1 backward), wrapping around the stop list.
func (t *TextInputOverlay) nextEnabledIndex(delta int) int {
	return t.focus.nextEnabled(delta, t.stopEnabled)
}

// updateFocusState syncs each component's focus/blur state to the current stop.
func (t *TextInputOverlay) updateFocusState() {
	if t.isTitle() {
		t.titleInput.Focus()
	} else {
		t.titleInput.Blur()
	}
	if t.isTextarea() {
		t.textarea.Focus()
	} else {
		t.textarea.Blur()
	}
	if t.directoryPicker != nil {
		if t.isDirectoryPicker() {
			t.directoryPicker.Focus()
		} else {
			t.directoryPicker.Blur()
		}
	}
	if t.branchPicker != nil {
		if t.isBranchPicker() {
			t.branchPicker.Focus()
		} else {
			t.branchPicker.Blur()
		}
	}
	if t.profilePicker != nil {
		if t.isProfilePicker() {
			t.profilePicker.Focus()
		} else {
			t.profilePicker.Blur()
		}
	}
	if t.modelField != nil {
		if t.isModelField() {
			t.modelField.Focus()
		} else {
			t.modelField.Blur()
		}
	}
	if t.modeField != nil {
		if t.isModeField() {
			t.modeField.Focus()
		} else {
			t.modeField.Blur()
		}
	}
	if t.accountPicker != nil {
		if t.isAccountPicker() {
			t.accountPicker.Focus()
		} else {
			t.accountPicker.Blur()
		}
	}
}
