package overlay

import (
	"path/filepath"
	"sort"

	"github.com/ZviBaratz/atrium/internal/fuzzy"

	tea "github.com/charmbracelet/bubbletea"
)

// Picker is the shared type-to-filter + cursor-nav primitive behind the overlay
// pickers (project, branch). It owns the filter text, the cursor, focus, width and
// row sizing, and the unified key grammar — so a picker embeds one Picker and
// delegates those mechanics rather than re-implementing them (issue #373).
//
// Sources are pluggable. A SYNC picker holds a local item list and re-ranks it in
// place, so a filter edit resets the cursor to the top. An ASYNC picker's items
// arrive out of band (e.g. the debounced branch search); a filter edit instead
// bumps a monotonic filterVersion so the owner can reject stale results, and the
// cursor is left for the owner to clamp when fresh results land.
//
// The optional preview hook is invoked with the highlighted item when the
// selection changes — the plumbing for a future highlight-tracking preview pane
// (AC1). No picker wires it yet, so it is nil by default and changes nothing.
type Picker struct {
	filter        string
	cursor        int
	focused       bool
	width         int
	visibleRows   int
	async         bool
	filterVersion uint64
	previewHook   func(item string)
}

// newPicker returns a Picker seeded with the default row count. async selects the
// filter-edit behavior (version bump vs cursor reset); see the type comment.
func newPicker(async bool) Picker {
	return Picker{visibleRows: defaultPickerRows, async: async}
}

// Focus gives the picker focus.
func (p *Picker) Focus() { p.focused = true }

// Blur removes focus from the picker.
func (p *Picker) Blur() { p.focused = false }

// IsFocused reports whether the picker is focused.
func (p *Picker) IsFocused() bool { return p.focused }

// SetWidth sets the picker's render width.
func (p *Picker) SetWidth(w int) { p.width = w }

// SetVisibleRows sets how many rows the picker renders (floored at 1) so a form
// can shrink to fit short terminals.
func (p *Picker) SetVisibleRows(n int) {
	if n < 1 {
		n = 1
	}
	p.visibleRows = n
}

// SetPreviewHook installs an optional callback invoked with the highlighted item
// whenever the selection changes — the plumbing for a future highlight-tracking
// preview pane (AC1). Passing nil (the default) disables it.
func (p *Picker) SetPreviewHook(hook func(item string)) { p.previewHook = hook }

// notifyHighlight fires the preview hook (if installed) with the highlighted item.
func (p *Picker) notifyHighlight(item string) {
	if p.previewHook != nil {
		p.previewHook(item)
	}
}

// handleKey applies the shared key grammar. itemCount is the number of currently
// visible items — the owner supplies it, since an async picker's items come from
// out of band. It reports whether the key was consumed, whether the filter text
// changed, and whether the cursor moved. On a filter edit a sync picker resets the
// cursor to the top; an async picker bumps its version so the owner can drop stale
// results. Nav clamps to [0, itemCount).
func (p *Picker) handleKey(msg tea.KeyMsg, itemCount int) (consumed, filterChanged, cursorMoved bool) {
	switch msg.Type {
	case tea.KeyUp:
		if p.cursor > 0 {
			p.cursor--
			return true, false, true
		}
		return true, false, false
	case tea.KeyDown:
		if p.cursor < itemCount-1 {
			p.cursor++
			return true, false, true
		}
		return true, false, false
	case tea.KeyBackspace:
		if len(p.filter) > 0 {
			r := []rune(p.filter)
			p.filter = string(r[:len(r)-1])
			p.onEdit()
			return true, true, false
		}
		return true, false, false
	case tea.KeyRunes:
		p.filter += string(msg.Runes)
		p.onEdit()
		return true, true, false
	case tea.KeySpace:
		p.filter += " "
		p.onEdit()
		return true, true, false
	}
	return false, false, false
}

// onEdit applies the source-specific reaction to a filter edit: an async picker
// bumps its version (so in-flight results are rejected on arrival); a sync picker
// resets the cursor to the top of its freshly re-ranked list.
func (p *Picker) onEdit() {
	if p.async {
		p.filterVersion++
	} else {
		p.cursor = 0
	}
}

// clampCursor keeps the cursor within [0, itemCount) after the item list changes
// out of band (an async source delivering fewer results than before).
func (p *Picker) clampCursor(itemCount int) {
	if p.cursor >= itemCount {
		if itemCount > 0 {
			p.cursor = itemCount - 1
		} else {
			p.cursor = 0
		}
	}
}

// rankCandidates filters candidates to those whose *display* form matches the
// query and sorts them best-first, preserving input (priority) order on ties.
// Matching the display string rather than the raw absolute path keeps the
// "/home/<user>" prefix out of the match — otherwise its runes match queries they
// were never shown for. A candidate whose basename also matches gets that match's
// score added on top: users type project names, so a name hit should outrank an
// equal-score mid-path hit. This is the shared picker's only ranking helper; the
// subsequence matcher itself lives in internal/fuzzy (the one matcher in the tree).
func rankCandidates(candidates []string, query string, display func(string) string) []string {
	type scored struct {
		path  string
		score int
	}
	matches := make([]scored, 0, len(candidates))
	for _, c := range candidates {
		ok, score := fuzzy.Match(query, display(c))
		if !ok {
			continue
		}
		if ok, base := fuzzy.Match(query, filepath.Base(c)); ok {
			score += base
		}
		matches = append(matches, scored{path: c, score: score})
	}
	sort.SliceStable(matches, func(a, b int) bool {
		return matches[a].score > matches[b].score
	})
	out := make([]string, len(matches))
	for i, m := range matches {
		out[i] = m.path
	}
	return out
}
