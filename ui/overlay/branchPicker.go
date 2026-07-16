package overlay

import (
	"github.com/ZviBaratz/atrium/ui/theme"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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
	// disabled marks the picker inert because the target is not a git repo (a direct
	// session has no branches). The form skips it in Tab order; here it renders an
	// explanatory placeholder, ignores input, and reports no selection — so a branch
	// chosen for a previous git target can't leak into a direct session's submit.
	disabled bool
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

// SetDisabled marks the picker inert (true when the target is not a git repo). The
// selection state is retained, so flipping back to a git target restores it.
func (bp *BranchPicker) SetDisabled(d bool) {
	bp.disabled = d
}

// Disabled reports whether the picker is inert (non-git target).
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
func (bp *BranchPicker) HandleKeyPress(msg tea.KeyMsg) (consumed bool, filterChanged bool) {
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
		// Inert placeholder for a non-git target, at the exact unfocused shape (header,
		// blank, visibleRows blank rows) so the form's height is unaffected.
		s.WriteString(bpLabelStyle().Render("Base: "))
		s.WriteString(bpDimStyle().Italic(true).Render("(direct session — no git branching)"))
		s.WriteString("\n\n")
		s.WriteString(renderPickerRows(nil, 0, bp.visibleRows, false, "", bpSelectedStyle(), bpDimStyle()))
		return s.String()
	}

	if !bp.focused {
		s.WriteString(bpLabelStyle().Render("Base: "))
		if sel := bp.selectedLabel(); sel != "" {
			s.WriteString(sel)
		} else {
			s.WriteString(bpDimStyle().Render("(none)"))
		}
		if bp.errored {
			// A failure that lands while blurred must still be visible — the selection
			// (typically the HEAD-base default) stays usable, but the list behind it isn't.
			s.WriteString(bpDimStyle().Render("  couldn't list branches"))
		}
		s.WriteString("\n\n")
		s.WriteString(renderPickerRows(nil, 0, bp.visibleRows, false, "", bpSelectedStyle(), bpDimStyle()))
		return s.String()
	}

	s.WriteString(bpLabelStyle().Render("Base branch"))
	s.WriteString(bpFilterStyle().Render(" (filter: " + bp.filter + theme.Current().Glyphs.TextCursor + ")"))
	switch {
	case bp.loading:
		s.WriteString(bpDimStyle().Render("  searching…"))
	case bp.errored:
		s.WriteString(bpDimStyle().Render("  couldn't list branches"))
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
