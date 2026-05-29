package overlay

import (
	"github.com/ZviBaratz/atrium/ui/theme"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// headBaseOption is the default base choice: the session branches off the repo's current
// HEAD. Every session gets its own fresh branch regardless (see the branch-off worktree
// model); this picker only chooses the base, so selecting this option means "base = HEAD".
const headBaseOption = "HEAD (current branch)"

// BranchPicker is an embeddable component for selecting a branch.
// It does not hold the full branch list — results are provided asynchronously
// via SetResults after each debounced search.
type BranchPicker struct {
	results       []string // current search results (from git)
	filter        string   // current filter text
	filterVersion uint64   // incremented on each filter change
	cursor        int      // index into visibleItems()
	focused       bool
	width         int
	visibleRows   int  // number of result rows to render (kept constant across focus)
	showHeadBase  bool // whether to offer the default "HEAD (current branch)" base option
	loading       bool // a search is in flight (results not yet authoritative)
}

// NewBranchPicker creates a new empty branch picker. It starts in the loading state
// because the caller kicks off an initial search as soon as the overlay opens.
func NewBranchPicker() *BranchPicker {
	return &BranchPicker{
		showHeadBase: true,
		loading:      true,
		visibleRows:  defaultPickerRows,
	}
}

// SetWidth sets the width of the branch picker.
func (bp *BranchPicker) SetWidth(w int) {
	bp.width = w
}

// SetVisibleRows sets how many result rows the picker renders (floored at 1). Driven by the
// overlay so the form can shrink to fit short terminals.
func (bp *BranchPicker) SetVisibleRows(n int) {
	if n < 1 {
		n = 1
	}
	bp.visibleRows = n
}

// Focus gives the branch picker focus.
func (bp *BranchPicker) Focus() {
	bp.focused = true
}

// Blur removes focus from the branch picker.
func (bp *BranchPicker) Blur() {
	bp.focused = false
}

// IsFocused returns whether the branch picker is focused.
func (bp *BranchPicker) IsFocused() bool {
	return bp.focused
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
	return bp.filterVersion
}

// HandleKeyPress processes a key event. Returns (consumed, filterChanged).
func (bp *BranchPicker) HandleKeyPress(msg tea.KeyMsg) (consumed bool, filterChanged bool) {
	switch msg.Type {
	case tea.KeyUp:
		if bp.cursor > 0 {
			bp.cursor--
		}
		return true, false
	case tea.KeyDown:
		items := bp.visibleItems()
		if bp.cursor < len(items)-1 {
			bp.cursor++
		}
		return true, false
	case tea.KeyBackspace:
		if len(bp.filter) > 0 {
			runes := []rune(bp.filter)
			bp.filter = string(runes[:len(runes)-1])
			bp.filterVersion++
			bp.loading = true
			return true, true
		}
		return true, false
	case tea.KeyRunes:
		bp.filter += string(msg.Runes)
		bp.filterVersion++
		bp.loading = true
		return true, true
	case tea.KeySpace:
		bp.filter += " "
		bp.filterVersion++
		bp.loading = true
		return true, true
	}
	return false, false
}

// SetResults updates the branch list with search results.
// version must match filterVersion for the results to be accepted (prevents stale updates).
func (bp *BranchPicker) SetResults(branches []string, version uint64) {
	if version != bp.filterVersion {
		return // stale results
	}
	bp.results = branches
	bp.loading = false

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

	// Clamp cursor
	items := bp.visibleItems()
	if bp.cursor >= len(items) {
		if len(items) > 0 {
			bp.cursor = len(items) - 1
		} else {
			bp.cursor = 0
		}
	}
}

// visibleItems returns the list of items to display.
func (bp *BranchPicker) visibleItems() []string {
	var items []string
	if bp.showHeadBase {
		items = append(items, headBaseOption)
	}
	items = append(items, bp.results...)
	return items
}

// GetSelectedBranch returns the selected base branch name, or empty string for the default
// HEAD-base option (which means "branch off the current HEAD, no explicit base").
func (bp *BranchPicker) GetSelectedBranch() string {
	items := bp.visibleItems()
	if bp.cursor < 0 || bp.cursor >= len(items) {
		return ""
	}
	selected := items[bp.cursor]
	if selected == headBaseOption {
		return ""
	}
	return selected
}

func bpLabelStyle() lipgloss.Style  { return theme.Current().AccentStyle().Bold(true) }
func bpFilterStyle() lipgloss.Style { return theme.Current().FgStyle() }
func bpSelectedStyle() lipgloss.Style {
	return lipgloss.NewStyle().
		Background(theme.Current().Palette.Accent).
		Foreground(theme.Current().Palette.Bg)
}
func bpDimStyle() lipgloss.Style { return theme.Current().DimStyle() }

// Render renders the branch picker at a constant height (one header line, a blank line,
// then visibleRows item rows) so the surrounding overlay never changes size as
// focus moves or results load. When unfocused it shows the chosen branch on the header
// line and leaves the rows blank; when focused it shows the filter and the list, with a
// "searching…" hint while results are in flight rather than blanking the list.
func (bp *BranchPicker) Render() string {
	var s strings.Builder

	if !bp.focused {
		s.WriteString(bpLabelStyle().Render("Base: "))
		if sel := bp.selectedLabel(); sel != "" {
			s.WriteString(sel)
		} else {
			s.WriteString(bpDimStyle().Render("(none)"))
		}
		s.WriteString("\n\n")
		s.WriteString(renderPickerRows(nil, 0, bp.visibleRows, false, "", bpSelectedStyle(), bpDimStyle()))
		return s.String()
	}

	s.WriteString(bpLabelStyle().Render("Base branch"))
	s.WriteString(bpFilterStyle().Render(" (filter: " + bp.filter + "█)"))
	if bp.loading {
		s.WriteString(bpDimStyle().Render("  searching…"))
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
