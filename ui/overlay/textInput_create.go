package overlay

import (
	"strings"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/session/agent"
)

// createFormTitle is the create form's default heading, and the value fitOverlay
// compares Title against to decide the heading is generic enough to shed on a short
// terminal. A caller that overrides Title (openForkForm, whose heading names the
// checkpoint being forked) keeps it, so this is the seam between "decoration" and
// "the only thing on screen saying what this submit does" — not a bare string.
const createFormTitle = "New session"

// NewSessionCreateOverlay creates the unified new-session form: a title field, a prompt
// textarea, a project (directory) picker, a variant (fan-out) control, an optional Claude
// model override (only when a selectable program resolves to claude), and a branch picker.
// Focus starts on the project picker (the `N` flow); the quick flow (`n`) moves it to the
// title via FocusTitle. Every section renders at a constant height so the centered overlay
// does not jump as focus moves. dirCandidates is the ordered list of candidate repo paths
// with the default/contextual target first. defaultProgram is the fallback program when no
// profiles are configured; with profiles present the variant control's selected programs
// win (see createSessionFromForm), so the model field keys its visibility off the profiles.
//
// linkPaths is the configured link_paths list, and only its emptiness is read: a non-empty
// one adds the Dependencies field (#481). It is a parameter rather than a config.LoadConfig()
// here for two reasons. That call seeds and writes the file and sweeps the data dir, on a
// path that runs for every `n`/`N`, every ⌃R rebuild and every crash-draft restore; and it
// would make this form — and therefore app/testdata/frames/prompt.txt and both colour
// fingerprints — a function of the developer's own config.json. The app layer holds the
// live config the settings panel edits and pushes it in, which is this overlay's standing
// contract (see SetTargetValidity).
func NewSessionCreateOverlay(profiles []config.Profile, accounts []config.ClaudeAccount, dirCandidates []string, defaultProgram string, linkPaths []string) *TextInputOverlay {
	ti := newTextarea("")
	// The prompt is optional and auto-sent to the agent once the session boots, so say so.
	ti.Placeholder = PromptPlaceholderOptional
	bp := NewBranchPicker()
	dp := NewDirectoryPicker(dirCandidates)

	// The variant control is the #387 fan-out stepper: one count per profile, defaulting
	// to a single session of the default profile. Always present (even with one profile,
	// so claude ×N works) whenever there is a profile to spawn.
	var vp *VariantPicker
	if len(profiles) > 0 {
		vp = NewVariantPicker(profiles)
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

	// The dependency-isolation field exists only when link_paths names something to
	// isolate. The other half of its gate — that the target is a git repo at all — is
	// dynamic and arrives via SetTargetValidity, which makes the field inert rather
	// than removing it, so the form does not reflow while the user edits the path.
	var df *DepsField
	if len(linkPaths) > 0 {
		df = NewDepsField()
	}

	// Project first (where focus starts), immediately followed by the base branch — the two
	// form one repo-context unit, since branches are scoped to the chosen project. Then the
	// title and the prompt — the input that distinguishes this flow from the inline `n` flow
	// (which jumps straight to the title via FocusTitle) — then the optional profile with its
	// dependent claude overrides (model, effort, permission mode, in that order), then the
	// optional Claude-account override, and last the optional dependency-isolation choice.
	// Appending that one keeps every existing relative order untouched, and it belongs in
	// the same category as the overrides above it: a per-session departure from a global
	// default.
	stops := []focusStop{stopDirectory, stopBranch, stopTitle, stopTextarea}
	if vp != nil {
		// Always focusable (unlike the old single-select profile picker, which was
		// skipped for a lone profile): the count stepper is useful even with one
		// profile — that is how claude ×N is dialed with no profiles configured.
		stops = append(stops, stopVariants)
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
	if df != nil {
		stops = append(stops, stopDeps)
	}
	stops = append(stops, stopEnter)

	overlay := &TextInputOverlay{
		textarea:        ti,
		titleInput:      newTitleInput(),
		Title:           createFormTitle,
		directoryPicker: dp,
		variantPicker:   vp,
		modelField:      mf,
		modeField:       pmf,
		effortField:     ef,
		depsField:       df,
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
// enabled state from the variant selection: the overrides apply only where a session's
// program is claude, so they stay live as long as the batch includes at least one claude
// variant, and go inert (present-but-disabled) once it does not. It also refreshes each
// field's no-op-chip hint from the pins the selected claude programs carry (see
// SetProfilePin). Called at construction and after every variant-control keypress.
func (t *TextInputOverlay) syncClaudeFieldsEnabled() {
	// The three fields are created together or not at all (see NewSessionCreateOverlay),
	// so one presence check covers all of them.
	if t.modelField == nil {
		return
	}
	includesClaude := agent.Resolve(t.defaultProgram).Key == agent.KeyClaude
	if t.variantPicker != nil {
		includesClaude = t.variantPicker.SelectedIncludesClaude()
	}
	t.modelField.SetDisabled(!includesClaude)
	t.modeField.SetDisabled(!includesClaude)
	t.effortField.SetDisabled(!includesClaude)

	// Push the pin state each field's no-op-chip hint names. Every field reports
	// whether a pin exists separately from what to call it: the raw pin decides
	// "pinned", and a label is supplied only when Atrium can name the value. All
	// three extractors read the raw flag (ModelFlag and EffortFlag are unvalidated by
	// design, and PermissionModePin is the unvalidated counterpart of
	// PermissionModeFlag), so a pin Atrium does not recognise still reads as a pin
	// rather than as "claude's default" — and withholding its label is what keeps an
	// arbitrarily long profile token from overflowing the row.
	progs := t.claudePrograms()
	mv, mMixed := profilePin(progs, agent.ModelFlag)
	t.modelField.SetProfilePin("", mv != "", mMixed) // model never echoes its value
	ev, eMixed := profilePin(progs, agent.EffortFlag)
	t.effortField.SetProfilePin(effortPinLabel(ev), ev != "", eMixed)
	pv, pMixed := profilePin(progs, agent.PermissionModePin)
	t.modeField.SetProfilePin(permissionModePinLabel(pv), pv != "", pMixed)
}

// effortPinLabel returns the display label for a pinned --effort level, or "" when
// the level is outside the set Atrium offers. agent.EffortFlag is deliberately
// unvalidated — a newer CLI may resolve a level this list has not caught up to — so
// the raw token can be any length; withholding the label leaves the hint saying
// "program pins it" instead of echoing something that would truncate the row.
func effortPinLabel(level string) string {
	if !agent.ValidEffort(level) {
		return ""
	}
	return agent.ClaudeEffortLabel(level)
}

// permissionModePinLabel returns the display label for a pinned --permission-mode
// value, or "" when the value falls outside Atrium's enum snapshot. Paired with
// agent.PermissionModePin (the raw read), this is what lets the hint distinguish "no
// flag" from "a flag Atrium cannot name" — the latter must not read as "claude's
// default", which would tell the user the opposite of the truth.
func permissionModePinLabel(mode string) string {
	if !agent.ValidPermissionMode(mode) {
		return ""
	}
	return agent.ClaudePermissionModeLabel(mode)
}

// claudePrograms returns the selected variant programs that resolve to claude —
// the set the model/effort/mode overrides apply to, and whose pins the focused
// no-op-chip hints describe. GetVariants already falls back to the configured
// defaultProgram when there is no variant picker, so a pin in that program counts
// too — which is why the hints say "program pins …" rather than naming a profile
// that, in exactly that case, does not exist (see chipRow.noOverrideHint).
func (t *TextInputOverlay) claudePrograms() []string {
	var out []string
	for _, p := range t.GetVariants() {
		if agent.Resolve(p).Key == agent.KeyClaude {
			out = append(out, p)
		}
	}
	return out
}

// profilePin folds the per-program value of one flag across programs into the
// (value, mixed) pair the hint needs: value is the common value when every program
// pins the flag to it ("" when none does), and mixed is true when the programs
// disagree or only some pin — the case no single value can summarize. flag extracts
// the raw pin from one program (agent.ModelFlag / EffortFlag / PermissionModePin);
// it must be the *unvalidated* read, so that a value Atrium cannot name still folds
// as a pin.
func profilePin(programs []string, flag func(string) string) (value string, mixed bool) {
	for i, p := range programs {
		v := flag(p)
		if i == 0 {
			value = v
			continue
		}
		if v != value {
			return "", true
		}
	}
	return value, false
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

// PromptFocusedAndEmpty reports whether the prompt textarea currently holds focus
// and is empty. It gates the up-arrow prompt-history trigger (#388): up on an
// empty prompt has nothing to move to, so it is free to open the history picker,
// in both the create form and the single-field quick-send overlay.
func (t *TextInputOverlay) PromptFocusedAndEmpty() bool {
	return t.isTextarea() && strings.TrimSpace(t.textarea.Value()) == ""
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

// PromptPlaceholderOptional and PromptPlaceholderFork are the two things the prompt
// field can truthfully say about itself, and which one is showing is a fact about
// the form rather than a decoration.
//
// An ordinary session's prompt really is optional: it is queued and typed into the
// pane once the agent is up, and skipping it just leaves the agent idle. A fork's is
// not. The print run that materializes the truncated conversation will not run
// without one — claude exits 1 — so the submit is refused, and a field that says
// "Optional" while the submit refuses an empty value is the form contradicting
// itself at the moment the user is deciding whether to type.
//
// The field soft-wraps rather than truncating at 80 columns, so the leading word is
// the one guaranteed to be read. Both start with it.
const (
	PromptPlaceholderOptional = "Optional — sent to the agent once it starts (Enter or Tab to skip)"
	PromptPlaceholderFork     = "Required — the fork's first turn, asked as it is created"
)

// SetPromptPlaceholder replaces what the empty prompt field says about itself. Used
// by the fork form, whose prompt is required (see PromptPlaceholderFork).
func (t *TextInputOverlay) SetPromptPlaceholder(s string) { t.textarea.Placeholder = s }

// PromptPlaceholder is what the empty prompt field currently says about itself.
func (t *TextInputOverlay) PromptPlaceholder() string { return t.textarea.Placeholder }

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

	// Order matters: an invalid path is reported as invalid even though the caller
	// also passes direct=false for it. The dependent sections are inert either way,
	// but their placeholders name which one, so the two must not collapse (#545).
	//
	// Derived before either section is touched, and each section guarded on its own
	// nil. An early return on a missing branch picker would make the deps field's
	// inertness — and so whether a stale "isolated" survives a retarget — contingent
	// on an unrelated widget being present.
	kind := targetGit
	switch {
	case !valid:
		kind = targetInvalid
	case direct:
		kind = targetDirect
	}
	if t.branchPicker != nil {
		t.branchPicker.SetHeadLabel(headBranch)
		t.branchPicker.SetTarget(kind)
		if t.isBranchPicker() && !t.stopEnabled(stopBranch) {
			t.setFocusIndex(t.nextEnabledIndex(1))
		}
	}
	// The dependency field rides the same verdict: only a git target has a worktree
	// to seed, so there is nothing to isolate for the other two kinds.
	if t.depsField != nil {
		t.depsField.SetTarget(kind)
		if t.isDepsField() && !t.stopEnabled(stopDeps) {
			t.setFocusIndex(t.nextEnabledIndex(1))
		}
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

// PreferBranch points the branch picker at name once its next results arrive, and
// seeds the filter that produces them.
//
// The filter is not decoration here, it is what makes the preference reachable:
// SearchBranches returns at most MaxBranchSearchResults, ordered by most recent
// commit, so an unfiltered search in a busy repo need not contain the branch at all.
// Filtering to its name guarantees it is in the set the preference is applied to —
// and shows the user why that base is selected.
//
// Returns the filter version the caller must run its search under; a search issued
// under an older version is rejected on arrival.
func (t *TextInputOverlay) PreferBranch(name string) uint64 {
	if t.branchPicker == nil || name == "" {
		return t.BranchFilterVersion()
	}
	t.branchPicker.SetFilter(name)
	t.branchPicker.PreferBranch(name)
	return t.branchPicker.filterVersion
}

// GetSelectedBranch returns the selected branch name from the branch picker.
// Returns empty string if no branch picker is present or "New branch" is selected.
func (t *TextInputOverlay) GetSelectedBranch() string {
	if t.branchPicker == nil {
		return ""
	}
	return t.branchPicker.GetSelectedBranch()
}

// GetVariants returns the flattened list of programs to spawn — one entry per
// session, each profile's program repeated by its count in the variant control
// (create form only). Falls back to a single default-program session when there is
// no variant picker. The list is never empty (the stepper keeps a floor of one).
func (t *TextInputOverlay) GetVariants() []string {
	if t.variantPicker == nil {
		return []string{t.defaultProgram}
	}
	return t.variantPicker.Variants()
}

// SetVariantError sets (or, with "", clears) the inline batch-validation message on
// the variant control — used to refuse an over-cap or over-large batch while keeping
// the form open, where a toast would be swallowed behind the modal. No-op without a
// variant picker.
func (t *TextInputOverlay) SetVariantError(msg string) {
	if t.variantPicker != nil {
		t.variantPicker.SetError(msg)
	}
}

// VariantError returns the current inline batch-validation message on the variant
// control ("" = none) — the counterpart to SetVariantError, mirroring TitleError.
func (t *TextInputOverlay) VariantError() string {
	if t.variantPicker == nil {
		return ""
	}
	return t.variantPicker.errMsg
}

// FocusVariants moves focus to the variant (fan-out) control when present, mirroring
// FocusTitle/FocusMode.
func (t *TextInputOverlay) FocusVariants() { t.focusStop(stopVariants) }

// FocusDeps moves focus to the Dependencies chips when the form has them and they can
// take focus (link_paths names something to isolate, and the target is a git repo).
// No-op otherwise, like FocusTitle/FocusVariants.
func (t *TextInputOverlay) FocusDeps() {
	if i := t.indexOfStop(stopDeps); i >= 0 && t.stopEnabled(stopDeps) {
		t.setFocusIndex(i)
	}
}

// GetModel returns the Claude model override typed into the model field, or ""
// when no flag should be composed: the form has no model field, the field is
// inert (non-claude profile selected), or it was left empty or typed as the no-op
// word ("inherit", or the "default" it replaced — see ModelField.Value).
func (t *TextInputOverlay) GetModel() string {
	if t.modelField == nil {
		return ""
	}
	return t.modelField.Value()
}

// GetPermissionMode returns the selected Claude permission-mode override, or
// "" when no flag should be composed: no mode field, the field is inert
// (non-claude profile selected), or it sits on the no-op ("inherit") chip.
func (t *TextInputOverlay) GetPermissionMode() string {
	if t.modeField == nil {
		return ""
	}
	return t.modeField.Value()
}

// GetEffort returns the selected Claude effort-level override, or "" when no
// flag should be composed: no effort field, the field is inert (non-claude
// profile selected), or it sits on the no-op ("inherit") chip.
func (t *TextInputOverlay) GetEffort() string {
	if t.effortField == nil {
		return ""
	}
	return t.effortField.Value()
}

// GetIsolateDeps reports whether the session should be created dependency-isolating:
// its worktree gets none of the configured link_paths symlinks, so what it installs
// stays private to it (#481). False when there is no deps field (link_paths names
// nothing to isolate), when it is inert (the target is not a git repo, so there is no
// worktree), or when it sits on "shared".
func (t *TextInputOverlay) GetIsolateDeps() bool {
	return t.depsField != nil && t.depsField.Isolate()
}

// GetSelectedAccount returns the deliberate account choice, or nil when the user
// never touched the picker. A pool ⇄ entry rotates (Member nil); a member entry
// pins (Member set); Pool is the cluster key in both cases.
func (t *TextInputOverlay) GetSelectedAccount() *AccountSelection {
	if t.accountPicker == nil || !t.accountPicker.Touched() {
		return nil
	}
	e := t.accountPicker.Selected()
	if e.member == nil && e.pool == "" {
		return nil
	}
	return &AccountSelection{Pool: e.pool, Member: e.member}
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
