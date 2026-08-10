package overlay

import (
	"github.com/ZviBaratz/atrium/ui/theme"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// The default base choice: the session branches off the repo's current HEAD. Every
// session gets its own fresh branch regardless (see the branch-off worktree model); this
// picker only chooses the base, so selecting this option means "base = HEAD". Its label
// names the actual branch once resolved (SetHeadLabel); these are the fallbacks.
const (
	headBaseUnresolved = "HEAD (current branch)"
	headBaseDetached   = "HEAD (detached)"
)

// BranchPicker is an embeddable component for selecting a branch.
// It does not hold the full branch list — results are provided asynchronously
// via SetResults after each debounced search.
type BranchPicker struct {
	Picker // shared filter/cursor/key-grammar; async source — filter edits bump filterVersion

	results      []string // current search results (from git)
	showHeadBase bool     // whether to offer the default "HEAD (current branch)" base option
	loading      bool     // a search is in flight (results not yet authoritative)
	errored      bool     // the last search failed (cleared by any filter edit or fresh results)
	// headBranch is the resolved name of the branch HEAD points at in the target repo
	// ("" until the async validity check resolves it; "HEAD" for a detached HEAD). It
	// only affects the default option's label — selection is positional (see
	// GetSelectedBranch), so the label can change freely without breaking identity.
	headBranch string
	// disabled marks the picker inert because the target is anything but a git repo —
	// a directory without one, or not a directory at all (target says which). The form
	// skips it in Tab order; here it renders an explanatory placeholder, ignores input,
	// and reports no selection — so a branch chosen for a previous git target can't
	// leak into a direct session's submit.
	disabled bool
	// target is what the selected directory turned out to be, and so which
	// inertNote explains the absent base (see targetKind).
	target targetKind
	// preferred is a branch to select as soon as results arrive, used by the fork
	// form to point a new session at the conversation's own branch (#657).
	//
	// It applies to the FIRST result set only, matched or not, and is then dropped.
	// That bound is the whole design: selection here is positional and the list is
	// re-delivered on every debounced filter edit, so a preference that outlived its
	// first delivery would drag the cursor back each time the user typed — a picker
	// that fights the person using it. One delivery is also exactly what the seeding
	// caller needs, since it seeds the filter that produces that delivery.
	preferred string
	// preferenceArmed distinguishes "no preference" from "preferred was applied and
	// cleared", so the one-shot cannot be re-armed by an empty name.
	preferenceArmed bool
}

// NewBranchPicker creates a new empty branch picker. It starts in the loading state
// because the caller kicks off an initial search as soon as the overlay opens.
func NewBranchPicker() *BranchPicker {
	return &BranchPicker{
		Picker:       newPicker(true),
		showHeadBase: true,
		loading:      true,
	}
}

// (Focus/Blur/IsFocused/SetWidth/SetVisibleRows are provided by the embedded Picker.)

// targetKind is what the create form's target directory turned out to be, which is
// both whether this picker is inert and *why*.
//
// It replaced a bare disabled bool because the two inert cases are not the same fact:
// the placeholder said "direct session — no git branching" for either, so a path that
// was not a directory at all was reported to the user as a direct session (#545). The
// picker is equally inert in both; only one of them is a session you can create.
type targetKind int

const (
	targetGit     targetKind = iota // a git repo — the picker is live
	targetDirect                    // a directory, but not a git repo
	targetInvalid                   // not a directory at all
)

// SetTarget records what the selected target is, marking the picker inert for anything
// but a git repo. The selection state is retained, so flipping back to a git target
// restores it.
func (bp *BranchPicker) SetTarget(k targetKind) {
	bp.target = k
	bp.disabled = k != targetGit
}

// searchFailedNote is the header's marker for a branch search that failed, and it is
// short because the focused header has almost no room for it: "Base branch (filter: ▌)"
// is 23 of the 42 cells an 80-col terminal gives the form even before a character is
// typed, leaving 19. It read "  couldn't list branches" (24), so the focused header was
// 47 cells and the note was cut in that state *always* — no user content required (#557).
//
// It drops "branches" rather than the verb: the label two cells to its left already says
// what is being listed, and "couldn't" is the half that distinguishes a failed search
// from an empty result.
const searchFailedNote = "  couldn't list"

// searchingNote is the in-flight marker. It always fitted, but it shares the
// header with the same unbounded filter, so it is carved out the same way.
const searchingNote = "  searching…"

// fitHeaderLabel bounds the header's variable middle — the typed filter when focused,
// the selected base when not — to what is left after the fixed chrome and a trailing
// note. It is DirectoryPicker.fitHeaderBody's counterpart, and exists for the same
// reason: a note appended past the row's budget lands beyond fitOverlay's edge and is
// never seen, so its columns are carved out up front. Width 0 (unsized) bounds nothing.
func (bp *BranchPicker) fitHeaderLabel(label string, chrome, note int) string {
	if bp.width <= 0 {
		return label
	}
	return truncTail(label, bp.width-chrome-note)
}

// inertNote explains why there is no base to choose. Both spellings are constants
// bounded well inside the row's budget; "Base: " plus the longer one is 41 of the 42
// cells an 80-col terminal gives the form.
func (bp *BranchPicker) inertNote() string {
	if bp.target == targetInvalid {
		return "(not a directory)"
	}
	return "(direct session — no git branching)"
}

// Disabled reports whether the picker is inert, which is every target but a git repo.
// It does not say which of the two inert cases holds — see target/inertNote for that.
func (bp *BranchPicker) Disabled() bool {
	return bp.disabled
}

// SetHeadLabel records the resolved name of the target repo's current branch, shown in
// the default base option ("HEAD (main)"). Pass "" to fall back to the generic label.
func (bp *BranchPicker) SetHeadLabel(branch string) {
	bp.headBranch = branch
}

// headOptionLabel returns the display label of the default HEAD-base option.
func (bp *BranchPicker) headOptionLabel() string {
	switch bp.headBranch {
	case "":
		return headBaseUnresolved
	case "HEAD": // git rev-parse --abbrev-ref HEAD yields literal "HEAD" when detached
		return headBaseDetached
	default:
		return "HEAD (" + bp.headBranch + ")"
	}
}

// GetFilter returns the current filter text.
func (bp *BranchPicker) GetFilter() string {
	return bp.filter
}

// GetFilterVersion returns a monotonically increasing version that changes on every filter edit.
func (bp *BranchPicker) GetFilterVersion() uint64 {
	return bp.filterVersion
}

// Invalidate bumps the filter version and clears stale results, returning the new
// version. Used when the target repo changes so in-flight searches for the previous
// repo are rejected by SetResults' version check. It enters the loading state rather
// than rendering an empty list, so the picker keeps its height and shows "searching…"
// until the fresh results arrive.
func (bp *BranchPicker) Invalidate() uint64 {
	bp.filterVersion++
	bp.results = nil
	bp.cursor = 0
	bp.loading = true
	bp.errored = false
	return bp.filterVersion
}

// HandleKeyPress processes a key event. Returns (consumed, filterChanged). The
// shared Picker owns the key grammar; as an async source it bumps filterVersion on
// each edit so in-flight results are rejected on arrival. On an edit we also enter
// the loading state and clear any previous error (it described the previous
// search), reproducing the old beginSearch step.
func (bp *BranchPicker) HandleKeyPress(msg tea.KeyPressMsg) (consumed bool, filterChanged bool) {
	if bp.disabled {
		// Unreachable through normal navigation (the form skips a disabled picker), but
		// guard anyway so no input path can mutate an inert picker.
		return false, false
	}
	consumed, filterChanged, _ = bp.handleKey(msg, len(bp.visibleItems()))
	if filterChanged {
		bp.loading = true
		bp.errored = false
	}
	return consumed, filterChanged
}

// HandlePaste appends pasted text to the filter, taking the same loading/error
// step an edit takes in HandleKeyPress so the debounced search the app schedules
// off filterChanged is not left describing the previous query.
func (bp *BranchPicker) HandlePaste(text string) (consumed bool, filterChanged bool) {
	if bp.disabled {
		return false, false
	}
	consumed, filterChanged = bp.handlePaste(text)
	if filterChanged {
		bp.loading = true
		bp.errored = false
	}
	return consumed, filterChanged
}

// SetError marks the current search as failed, clearing the loading state so the picker
// shows an error hint instead of spinning on "searching…" forever. version must match
// filterVersion (a stale error for an abandoned search is dropped, like stale results).
func (bp *BranchPicker) SetError(version uint64) {
	if version != bp.filterVersion {
		return // stale error
	}
	bp.results = nil
	bp.loading = false
	bp.errored = true
}

// PreferBranch asks the picker to select name when the next result set arrives.
// One-shot — see the field comment on preferred. An empty name is a no-op rather
// than an arm, so a caller with nothing to prefer changes nothing.
func (bp *BranchPicker) PreferBranch(name string) {
	if name == "" {
		return
	}
	bp.preferred = name
	bp.preferenceArmed = true
}

// SetResults updates the branch list with search results.
// version must match filterVersion for the results to be accepted (prevents stale updates).
func (bp *BranchPicker) SetResults(branches []string, version uint64) {
	if version != bp.filterVersion {
		return // stale results
	}
	bp.results = branches
	bp.loading = false
	bp.errored = false

	// Hide the default HEAD-base option when the filter exactly matches a branch name: the
	// user is clearly homing in on that branch as the base.
	bp.showHeadBase = true
	if bp.filter != "" {
		lower := strings.ToLower(bp.filter)
		for _, b := range branches {
			if strings.ToLower(b) == lower {
				bp.showHeadBase = false
				break
			}
		}
	}

	// Clamp the cursor to the freshly delivered result set.
	bp.clampCursor(len(bp.visibleItems()))

	// Apply a one-shot preference, then drop it whether or not it matched. Exact
	// comparison, not the case-insensitive containment the search uses: a preference
	// for "main" must never land on "maintenance", and the caller knows the branch's
	// real name because it read it off a live session.
	//
	// After the clamp, because this sets a deliberate position the clamp would
	// otherwise be free to move.
	if bp.preferenceArmed {
		want := bp.preferred
		bp.preferred, bp.preferenceArmed = "", false
		for i, b := range branches {
			if b == want {
				idx := i
				if bp.showHeadBase {
					idx++ // item 0 is the HEAD option
				}
				bp.cursor = idx
				break
			}
		}
	}
}

// visibleItems returns the list of items to display. When showHeadBase is set, the
// HEAD-base option is always item 0 — GetSelectedBranch relies on that position.
func (bp *BranchPicker) visibleItems() []string {
	var items []string
	if bp.showHeadBase {
		items = append(items, bp.headOptionLabel())
	}
	items = append(items, bp.results...)
	return items
}

// GetSelectedBranch returns the selected base branch name, or empty string for the default
// HEAD-base option (which means "branch off the current HEAD, no explicit base"). The HEAD
// option is identified by its position (item 0 when shown), not its label — the label is
// dynamic (SetHeadLabel) and may collide with nothing. A disabled picker always reports no
// selection — direct sessions never branch.
func (bp *BranchPicker) GetSelectedBranch() string {
	if bp.disabled {
		return ""
	}
	idx := bp.cursor
	if bp.showHeadBase {
		idx-- // item 0 is the HEAD option → "no explicit base"
	}
	if idx < 0 || idx >= len(bp.results) {
		return ""
	}
	return bp.results[idx]
}

func bpLabelStyle() lipgloss.Style    { return overlayLabelStyle() }
func bpFilterStyle() lipgloss.Style   { return overlayFilterStyle() }
func bpSelectedStyle() lipgloss.Style { return overlaySelectedStyle() }
func bpDimStyle() lipgloss.Style      { return overlayDimStyle() }

// Render renders the branch picker at a constant height (one header line, a blank line,
// then visibleRows item rows) so the surrounding overlay never changes size as
// focus moves or results load. When unfocused it shows the chosen branch on the header
// line and leaves the rows blank; when focused it shows the filter and the list, with a
// "searching…" hint while results are in flight rather than blanking the list.
func (bp *BranchPicker) Render() string {
	var s strings.Builder

	if bp.disabled {
		// Inert placeholder for any target but a git repo — inertNote says which of the
		// two it is — at the exact unfocused shape (header, blank, visibleRows blank
		// rows) so the form's height is unaffected.
		s.WriteString(bpLabelStyle().Render("Base: "))
		s.WriteString(bpDimStyle().Italic(true).Render(bp.inertNote()))
		s.WriteString("\n\n")
		s.WriteString(renderPickerRows(nil, 0, bp.visibleRows, false, "", bpSelectedStyle(), bpDimStyle()))
		return s.String()
	}

	if !bp.focused {
		const prefix = "Base: "
		// A failure that lands while blurred must still be visible — the selection
		// (typically the HEAD-base default) stays usable, but the list behind it isn't.
		// Its cells come out of the base label, which is unbounded: a branch name is the
		// user's to choose, and "Base: HEAD (develop)  couldn't list branches" was 44
		// cells, so every branch but one named "main" pushed the note off the row (#557).
		note := 0
		if bp.errored {
			note = lipgloss.Width(searchFailedNote)
		}
		s.WriteString(bpLabelStyle().Render(prefix))
		if sel := bp.selectedLabel(); sel != "" {
			s.WriteString(bp.fitHeaderLabel(sel, lipgloss.Width(prefix), note))
		} else {
			s.WriteString(bpDimStyle().Render("(none)"))
		}
		if bp.errored {
			s.WriteString(bpDimStyle().Render(searchFailedNote))
		}
		s.WriteString("\n\n")
		s.WriteString(renderPickerRows(nil, 0, bp.visibleRows, false, "", bpSelectedStyle(), bpDimStyle()))
		return s.String()
	}

	const label, filterOpen, filterClose = "Base branch", " (filter: ", ")"
	note := 0
	switch {
	case bp.loading:
		note = lipgloss.Width(searchingNote)
	case bp.errored:
		note = lipgloss.Width(searchFailedNote)
	}
	chrome := lipgloss.Width(label + filterOpen + filterClose + theme.Current().Glyphs.TextCursor)
	s.WriteString(bpLabelStyle().Render(label))
	s.WriteString(bpFilterStyle().Render(
		filterOpen + bp.fitHeaderLabel(bp.filter, chrome, note) + theme.Current().Glyphs.TextCursor + filterClose))
	switch {
	case bp.loading:
		s.WriteString(bpDimStyle().Render(searchingNote))
	case bp.errored:
		s.WriteString(bpDimStyle().Render(searchFailedNote))
	}
	s.WriteString("\n\n")

	s.WriteString(renderPickerRows(bp.visibleItems(), bp.cursor, bp.visibleRows, true, "no matching branches", bpSelectedStyle(), bpDimStyle()))
	return s.String()
}

// selectedLabel returns the label of the current selection (including the "New branch"
// option), or empty if there is nothing to select.
func (bp *BranchPicker) selectedLabel() string {
	items := bp.visibleItems()
	if bp.cursor < 0 || bp.cursor >= len(items) {
		return ""
	}
	return items[bp.cursor]
}
