package overlay

import (
	"strings"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/session/agent"
)

// NewSessionCreateOverlay creates the unified new-session form: a title field, a prompt
// textarea, a project (directory) picker, an optional profile picker (only when more than
// one profile exists), an optional Claude model override (only when a selectable program
// resolves to claude), and a branch picker. Focus starts on the project picker (the `N`
// flow); the quick flow (`n`) moves it to the title via FocusTitle. Every section renders
// at a constant height so the centered overlay does not jump as focus moves. dirCandidates
// is the ordered list of candidate repo paths with the default/contextual target first.
// defaultProgram is the program used when no profile is selected; with profiles present
// the selected profile's program always wins (see createSessionFromForm), so the model
// field keys its visibility and enabled state off the profiles instead.
func NewSessionCreateOverlay(profiles []config.Profile, accounts []config.ClaudeAccount, dirCandidates []string, defaultProgram string) *TextInputOverlay {
	ti := newTextarea("")
	// The prompt is optional and auto-sent to the agent once the session boots, so say so.
	ti.Placeholder = "Optional — sent to the agent once it starts (Enter or Tab to skip)"
	bp := NewBranchPicker()
	dp := NewDirectoryPicker(dirCandidates)

	var pp *ProfilePicker
	if len(profiles) > 0 {
		pp = NewProfilePicker(profiles)
	}

	var ap *AccountPicker
	if len(accounts) > 0 {
		ap = NewAccountPicker(accounts)
	}

	// The model and permission-mode fields exist only when some selectable program
	// resolves to claude (the candidates are the profiles when any exist — a profile's
	// program always overrides the default — else the default program). Their *enabled*
	// state then tracks the effective program: present-but-inert while a non-claude
	// profile is selected, so a typed model is visibly n/a rather than silently dropped.
	candidates := []string{defaultProgram}
	if len(profiles) > 0 {
		candidates = candidates[:0]
		for _, p := range profiles {
			candidates = append(candidates, p.Program)
		}
	}
	var mf *ModelField
	var pmf *ModeField
	var ef *EffortField
	for _, c := range candidates {
		if agent.Resolve(c).Key == agent.KeyClaude {
			mf = NewModelField()
			pmf = NewModeField()
			ef = NewEffortField()
			break
		}
	}

	// Project first (where focus starts), immediately followed by the base branch — the two
	// form one repo-context unit, since branches are scoped to the chosen project. Then the
	// title and the prompt — the input that distinguishes this flow from the inline `n` flow
	// (which jumps straight to the title via FocusTitle) — then the optional profile with its
	// dependent claude overrides (model, effort, permission mode, in that order), and finally
	// the optional Claude-account override.
	stops := []focusStop{stopDirectory, stopBranch, stopTitle, stopTextarea}
	if pp != nil && pp.HasMultiple() {
		stops = append(stops, stopProfile)
	}
	if mf != nil {
		stops = append(stops, stopModel)
	}
	if ef != nil {
		stops = append(stops, stopEffort)
	}
	if pmf != nil {
		stops = append(stops, stopMode)
	}
	if ap != nil && ap.HasMultiple() {
		stops = append(stops, stopAccount)
	}
	stops = append(stops, stopEnter)

	overlay := &TextInputOverlay{
		textarea:        ti,
		titleInput:      newTitleInput(),
		Title:           "New session",
		directoryPicker: dp,
		profilePicker:   pp,
		modelField:      mf,
		modeField:       pmf,
		effortField:     ef,
		accountPicker:   ap,
		branchPicker:    bp,
		focus:           focusRing{stops: stops},
		isCreateForm:    true,
		defaultProgram:  defaultProgram,
	}
	overlay.syncClaudeFieldsEnabled()
	overlay.focusStop(stopDirectory)
	return overlay
}

// syncClaudeFieldsEnabled re-derives the model, effort, and permission-mode fields'
// enabled state from the effective program (the selected profile's program when a
// picker exists, else the configured default). Called at construction and after
// every profile-picker keypress.
func (t *TextInputOverlay) syncClaudeFieldsEnabled() {
	// The three fields are created together or not at all (see NewSessionCreateOverlay),
	// so one presence check covers all of them.
	if t.modelField == nil {
		return
	}
	program := t.defaultProgram
	if t.profilePicker != nil {
		program = t.profilePicker.GetSelectedProfile().Program
	}
	disabled := agent.Resolve(program).Key != agent.KeyClaude
	t.modelField.SetDisabled(disabled)
	t.modeField.SetDisabled(disabled)
	t.effortField.SetDisabled(disabled)
}

// FocusTitle moves focus to the title field. The quick-create flow (`n`) calls
// it right after building the form so typing a name is immediate; the full flow
// (`N`) keeps the default project-picker focus.
func (t *TextInputOverlay) FocusTitle() { t.focusStop(stopTitle) }

// FocusMode moves focus to the Permissions (mode) chip when it can take focus,
// falling back to the Create button otherwise (the mode field is absent for a
// non-claude program and disabled while a non-claude profile is selected). Smart
// dispatch uses this on a confident match so the one decision it defers — the
// permission mode — is the active field, a ←/→ away from plan and ⌃S from create.
func (t *TextInputOverlay) FocusMode() {
	if i := t.indexOfStop(stopMode); i >= 0 && t.stopEnabled(stopMode) {
		t.setFocusIndex(i)
		return
	}
	t.focusStop(stopEnter)
}

// SetPrompt sets the prompt textarea's contents (used to pre-fill the create form).
func (t *TextInputOverlay) SetPrompt(s string) {
	t.textarea.SetValue(s)
}

// SetTitleValue sets the title field's text (create form only). It is distinct from
// the Title struct field, which is the overlay's header caption.
func (t *TextInputOverlay) SetTitleValue(s string) {
	t.titleInput.SetValue(s)
}

// IsDirty reports whether the create form holds user-entered free text (title or
// prompt). The draft-stash logic uses it so an untouched form is still discarded
// on Escape; picker-only changes do not count as dirty.
func (t *TextInputOverlay) IsDirty() bool {
	return strings.TrimSpace(t.titleInput.Value()) != "" ||
		strings.TrimSpace(t.textarea.Value()) != ""
}

// SelectPath pre-selects path in the project picker, returning false when path is not
// a candidate. No-op (false) without a directory picker.
func (t *TextInputOverlay) SelectPath(path string) bool {
	if t.directoryPicker == nil {
		return false
	}
	return t.directoryPicker.SelectPath(path)
}

// SetProjectHint sets (or, with "", clears) the transient note rendered beside the
// project picker — e.g. "detecting…" while smart dispatch routes in the background.
func (t *TextInputOverlay) SetProjectHint(s string) {
	t.projectHint = s
}

// GetTitle returns the trimmed session title from the title field (create form only).
func (t *TextInputOverlay) GetTitle() string {
	return strings.TrimSpace(t.titleInput.Value())
}

// IsCreateForm reports whether this overlay is the new-session creation form (as opposed
// to the plain prompt overlay used to send a prompt to a running session).
func (t *TextInputOverlay) IsCreateForm() bool {
	return t.isCreateForm
}

// ClearRequested reports whether a confirmed double-tap Ctrl+R has asked to reset
// the create form. The app consumes it by rebuilding a fresh overlay.
func (t *TextInputOverlay) ClearRequested() bool { return t.clearRequested }

// DisarmClear drops a half-completed double-tap Ctrl+R (the "⌃R again" arm).
// HandleKeyPress disarms on any other key, but a Ctrl+C cancel is intercepted by
// the app before it reaches the overlay; stashing this form as a draft calls this
// so the arm can't ride into the stash and turn the next single Ctrl+R into a wipe.
func (t *TextInputOverlay) DisarmClear() { t.clearArmed = false }

// SetTitleError sets (or, with "", clears) the inline validation message shown
// on the title label. The error never disables submit — the app layer blocks a
// conflicting submit itself and re-focuses the title.
func (t *TextInputOverlay) SetTitleError(msg string) {
	t.titleError = msg
}

// TitleError returns the current inline title validation message ("" = none).
func (t *TextInputOverlay) TitleError() string {
	return t.titleError
}

// GetSelectedPath returns the selected target directory from the directory picker.
// Returns empty string if no directory picker is present.
func (t *TextInputOverlay) GetSelectedPath() string {
	if t.directoryPicker == nil {
		return ""
	}
	return t.directoryPicker.GetSelectedPath()
}

// UpdateDirCandidates refreshes the project picker's candidate list, preserving
// the user's typed filter and selection — used when a background repo scan
// completes while the form is open. No-op without a directory picker.
func (t *TextInputOverlay) UpdateDirCandidates(paths []string) {
	if t.directoryPicker == nil {
		return
	}
	t.directoryPicker.UpdateCandidates(paths)
}

// SetTargetValidity marks the currently selected target directory's state so the picker
// can surface an inline indicator while the user chooses: valid means it exists and is a
// directory; direct means it is a directory but not a git repo (a direct session).
// It also enables/disables the branch picker — a non-git (or invalid) target has no
// branches to base on, so the section goes inert and is skipped by navigation. If the
// verdict lands while the branch picker holds focus, focus moves to the next enabled
// stop rather than stranding the user on an inert field. headBranch (the resolved name
// of the branch HEAD points at, "" when unknown) labels the picker's default base option.
// No-op when there is no directory picker.
func (t *TextInputOverlay) SetTargetValidity(valid, direct bool, headBranch string) {
	if t.directoryPicker == nil {
		return
	}
	t.directoryPicker.SetSelectionState(valid, direct)
	if t.branchPicker == nil {
		return
	}
	t.branchPicker.SetHeadLabel(headBranch)
	t.branchPicker.SetDisabled(!valid || direct)
	if t.isBranchPicker() && !t.stopEnabled(stopBranch) {
		t.setFocusIndex(t.nextEnabledIndex(1))
	}
}

// ClearTargetValidity resets the target-directory state indicator to "unknown", so no
// hint is shown until a fresh check resolves. The branch picker's disabled state is
// deliberately left untouched — flipping it during the debounce window would flicker the
// section on every path keystroke; the fresh verdict re-sets it via SetTargetValidity.
// No-op when there is no directory picker.
func (t *TextInputOverlay) ClearTargetValidity() {
	if t.directoryPicker == nil {
		return
	}
	t.directoryPicker.ClearSelectionState()
}

// GetSelectedBranch returns the selected branch name from the branch picker.
// Returns empty string if no branch picker is present or "New branch" is selected.
func (t *TextInputOverlay) GetSelectedBranch() string {
	if t.branchPicker == nil {
		return ""
	}
	return t.branchPicker.GetSelectedBranch()
}

// GetSelectedProgram returns the program string from the selected profile.
// Returns empty string if no profile picker is present.
func (t *TextInputOverlay) GetSelectedProgram() string {
	if t.profilePicker == nil {
		return ""
	}
	return t.profilePicker.GetSelectedProfile().Program
}

// GetModel returns the Claude model override typed into the model field, or ""
// when no flag should be composed: the form has no model field, the field is
// inert (non-claude profile selected), or it was left empty / "default".
func (t *TextInputOverlay) GetModel() string {
	if t.modelField == nil {
		return ""
	}
	return t.modelField.Value()
}

// GetPermissionMode returns the selected Claude permission-mode override, or
// "" when no flag should be composed: no mode field, the field is inert
// (non-claude profile selected), or it sits on the default chip.
func (t *TextInputOverlay) GetPermissionMode() string {
	if t.modeField == nil {
		return ""
	}
	return t.modeField.Value()
}

// GetEffort returns the selected Claude effort-level override, or "" when no
// flag should be composed: no effort field, the field is inert (non-claude
// profile selected), or it sits on the default chip.
func (t *TextInputOverlay) GetEffort() string {
	if t.effortField == nil {
		return ""
	}
	return t.effortField.Value()
}

// GetSelectedAccount returns the chosen account and true only when the user has
// deliberately driven the picker, i.e. an override. Otherwise it returns
// (zero, false) so the caller keeps the freshly-resolved auto-route — whether the
// form has no picker, or has one the user never touched (its selection is just the
// auto-routed preselection, which the caller already computes itself).
func (t *TextInputOverlay) GetSelectedAccount() (config.ClaudeAccount, bool) {
	if t.accountPicker == nil || !t.accountPicker.Touched() {
		return config.ClaudeAccount{}, false
	}
	return t.accountPicker.GetSelectedAccount(), true
}

// SelectedAccountName returns the name of the account the picker is currently pointing
// at — the auto-routed preselection or a manual choice — regardless of whether the user
// has driven it. Unlike GetSelectedAccount, which reports a value only after a deliberate
// override (the submit contract), this exposes the displayed selection. "" when there is
// no account picker.
func (t *TextInputOverlay) SelectedAccountName() string {
	if t.accountPicker == nil {
		return ""
	}
	return t.accountPicker.GetSelectedAccount().Name
}

// PreselectAccount points the picker at the auto-routed account name. It is a no-op
// once the user has taken manual control (see AccountPicker.SelectByName), so the
// form can re-preselect as the target project changes without clobbering a choice.
func (t *TextInputOverlay) PreselectAccount(name string) {
	if t.accountPicker != nil {
		t.accountPicker.SelectByName(name)
	}
}

// BranchFilterVersion returns the current filter version from the branch picker.
// Returns 0 if no branch picker is present.
func (t *TextInputOverlay) BranchFilterVersion() uint64 {
	if t.branchPicker == nil {
		return 0
	}
	return t.branchPicker.GetFilterVersion()
}

// BranchFilter returns the current filter text from the branch picker.
func (t *TextInputOverlay) BranchFilter() string {
	if t.branchPicker == nil {
		return ""
	}
	return t.branchPicker.GetFilter()
}

// InvalidateBranchSearch bumps the branch filter version and clears stale results,
// returning the new version. Used when the target directory changes so in-flight
// searches for the previous repo are rejected. Returns 0 if no branch picker.
func (t *TextInputOverlay) InvalidateBranchSearch() uint64 {
	if t.branchPicker == nil {
		return 0
	}
	return t.branchPicker.Invalidate()
}

// SetBranchResults updates the branch picker with search results.
// version must match the picker's current filterVersion to be accepted.
func (t *TextInputOverlay) SetBranchResults(branches []string, version uint64) {
	if t.branchPicker == nil {
		return
	}
	t.branchPicker.SetResults(branches, version)
}

// SetBranchSearchError marks the branch search for the given version as failed, so the
// picker stops showing "searching…" and surfaces an error hint instead.
func (t *TextInputOverlay) SetBranchSearchError(version uint64) {
	if t.branchPicker == nil {
		return
	}
	t.branchPicker.SetError(version)
}
