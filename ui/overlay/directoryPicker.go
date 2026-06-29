package overlay

import (
	"github.com/ZviBaratz/atrium/ui/theme"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// maxDirEntries bounds a single directory read so a pathological directory
// (e.g. /nix/store, /) can't make the synchronous listing block or balloon.
const maxDirEntries = 500

// DirectoryPicker is an embeddable component for choosing the target repository
// directory of a new session. Unlike the branch picker it is fully synchronous —
// the candidate list is local. The filter text doubles as free-text path entry:
// typing a fragment filters the candidates, while typing a path (one starting with
// "/", "~" or ".") offers that path as a selectable entry. Validation that the
// chosen path is a git repo happens at the call site, on selection/submit.
type DirectoryPicker struct {
	candidates  []string // absolute candidate repo paths, deduped; candidates[0] is the default
	filter      string
	cursor      int
	focused     bool
	width       int
	visibleRows int // number of candidate rows to render (kept constant across focus)

	// These let the call site surface an inline hint as the selection changes, instead of
	// only reacting at submit. selectionValid means the path exists and is a directory;
	// selectionDirect means it is a valid directory but not a git repo (→ direct session).
	validityChecked bool
	selectionValid  bool
	selectionDirect bool

	// cachedDir/cachedNames memoize the last directory listing so typing within one
	// directory (the base prefix grows, the directory does not) re-uses one read. The
	// cache lives for the picker's lifetime (one short-lived form) and is deliberately
	// never invalidated — a directory created mid-form won't appear until reopen, but
	// it stays reachable by typing the full path (literal fallback).
	cachedDir   string
	cachedNames []string
	cacheValid  bool
}

// NewDirectoryPicker creates a directory picker over the given candidate paths.
// The caller should pass the default/contextual target first; the list is deduped
// while preserving order and the cursor starts on the first entry.
func NewDirectoryPicker(candidates []string) *DirectoryPicker {
	return &DirectoryPicker{candidates: dedupePaths(candidates), visibleRows: defaultPickerRows}
}

// dedupePaths drops empty and duplicate entries, preserving first-seen order.
func dedupePaths(paths []string) []string {
	seen := make(map[string]bool, len(paths))
	deduped := make([]string, 0, len(paths))
	for _, p := range paths {
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		deduped = append(deduped, p)
	}
	return deduped
}

// UpdateCandidates replaces the candidate list in place — used when a background
// repo scan completes while the form is open — without disturbing the user's
// typed filter and, where possible, their current selection: the cursor is
// re-anchored on the previously selected path's new position, falling back to a
// clamp. A path-mode filter browses the filesystem and ignores candidates
// entirely, so only the list is swapped there.
func (dp *DirectoryPicker) UpdateCandidates(candidates []string) {
	prev := dp.GetSelectedPath()
	dp.candidates = dedupePaths(candidates)
	if looksLikePath(dp.filter) {
		return
	}
	items := dp.visibleItems()
	dp.cursor = 0
	for i, it := range items {
		if it == prev {
			dp.cursor = i
			break
		}
	}
}

// SetWidth sets the width of the directory picker.
func (dp *DirectoryPicker) SetWidth(w int) {
	dp.width = w
}

// SetVisibleRows sets how many candidate rows the picker renders (floored at 1). Driven by
// the overlay so the form can shrink to fit short terminals.
func (dp *DirectoryPicker) SetVisibleRows(n int) {
	if n < 1 {
		n = 1
	}
	dp.visibleRows = n
}

// Focus gives the directory picker focus.
func (dp *DirectoryPicker) Focus() {
	dp.focused = true
}

// Blur removes focus from the directory picker.
func (dp *DirectoryPicker) Blur() {
	dp.focused = false
}

// IsFocused returns whether the directory picker is focused.
func (dp *DirectoryPicker) IsFocused() bool {
	return dp.focused
}

// SetSelectionState records the currently selected path's state so Render can show an
// inline indicator while the user is choosing: valid means it is an existing directory;
// direct means it is a directory that is not a git repo (a direct session).
func (dp *DirectoryPicker) SetSelectionState(valid, direct bool) {
	dp.validityChecked = true
	dp.selectionValid = valid
	dp.selectionDirect = direct
}

// ClearSelectionState resets the indicator to "unknown" (validityChecked = false), so
// Render shows no target-state hint until a fresh check resolves. Called when the selected
// path changes, so the previous path's verdict isn't briefly asserted for the new one while
// the (debounced, async) re-check is in flight.
func (dp *DirectoryPicker) ClearSelectionState() {
	dp.validityChecked = false
	dp.selectionValid = false
	dp.selectionDirect = false
}

// HandleKeyPress processes a key event. Returns (consumed, selectionChanged).
func (dp *DirectoryPicker) HandleKeyPress(msg tea.KeyMsg) (consumed bool, selectionChanged bool) {
	switch msg.Type {
	case tea.KeyUp:
		if dp.cursor > 0 {
			dp.cursor--
			return true, true
		}
		return true, false
	case tea.KeyDown:
		if dp.cursor < len(dp.visibleItems())-1 {
			dp.cursor++
			return true, true
		}
		return true, false
	case tea.KeyBackspace:
		if len(dp.filter) > 0 {
			runes := []rune(dp.filter)
			dp.filter = string(runes[:len(runes)-1])
			dp.cursor = 0
			return true, true
		}
		return true, false
	case tea.KeyRunes:
		dp.filter += string(msg.Runes)
		dp.cursor = 0
		return true, true
	case tea.KeySpace:
		dp.filter += " "
		dp.cursor = 0
		return true, true
	}
	return false, false
}

// looksLikePath reports whether the filter should be treated as a path to enter
// rather than a fragment to match against the candidates.
func looksLikePath(s string) bool {
	return strings.HasPrefix(s, "/") || strings.HasPrefix(s, "~") || strings.HasPrefix(s, ".")
}

// expandPath expands a leading "~" and resolves the path to absolute.
func expandPath(s string) string {
	if s == "~" || strings.HasPrefix(s, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			s = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(s, "~"), "/"))
		}
	}
	if abs, err := filepath.Abs(s); err == nil {
		return abs
	}
	return s
}

// splitRawPath splits the raw (un-expanded) filter into the directory portion to list
// and the base prefix to match within it. It works on the raw string before expandPath
// because filepath.Abs strips trailing slashes, which would erase the dir/base boundary.
// A trailing slash (or a bare "~"/"."/"..") means "list this directory" (empty base).
func splitRawPath(raw string) (dirRaw, base string) {
	if strings.HasSuffix(raw, "/") {
		return raw, ""
	}
	if idx := strings.LastIndex(raw, "/"); idx >= 0 {
		return raw[:idx+1], raw[idx+1:]
	}
	// No separator: a bare "~", "." or ".." — list that directory, no base prefix.
	return raw, ""
}

// listSubdirs returns the names of the immediate sub-directories of dir, bounded by
// maxDirEntries. Any error (missing/permission-denied/unreadable) yields no names, so
// the caller falls back to the literal typed path. Uses DirEntry.IsDir (no per-child stat).
func listSubdirs(dir string) []string {
	f, err := os.Open(dir)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }() // read-only handle; close error is not actionable
	// ReadDir(n) returns up to n entries (io.EOF when fewer); the bound is what matters.
	// Truncation is in raw directory order (not sorted), so in a pathological dir the
	// target may fall outside the cap — the literal-fallback entry keeps it reachable
	// by typing the path out.
	entries, _ := f.ReadDir(maxDirEntries)
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names
}

// readSubdirs lists the sub-directories of dirRaw (after expansion), memoized so repeated
// keystrokes within one directory don't re-read it.
func (dp *DirectoryPicker) readSubdirs(dirRaw string) (dir string, names []string) {
	dir = expandPath(dirRaw)
	if dp.cacheValid && dp.cachedDir == dir {
		return dir, dp.cachedNames
	}
	names = listSubdirs(dir)
	dp.cachedDir, dp.cachedNames, dp.cacheValid = dir, names, true
	return dir, names
}

// visibleItems returns the entries to display for the current filter. With no filter it
// shows every candidate; a non-path fragment fuzzy-ranks the candidates; a path-like
// filter browses the filesystem — the matching sub-directories of the typed parent, plus
// the literal typed path as a fallback so a complete/new path stays selectable.
func (dp *DirectoryPicker) visibleItems() []string {
	if dp.filter == "" {
		return append([]string(nil), dp.candidates...)
	}
	if !looksLikePath(dp.filter) {
		return rankCandidates(dp.candidates, dp.filter, dp.displayPath)
	}

	dirRaw, base := splitRawPath(dp.filter)
	dir, names := dp.readSubdirs(dirRaw)

	var ranked []string
	if base == "" {
		ranked = append([]string(nil), names...)
		sort.Strings(ranked)
	} else {
		ranked = fuzzyRank(names, base)
	}

	items := make([]string, 0, len(ranked)+1)
	seen := make(map[string]bool, len(ranked)+1)
	for _, n := range ranked {
		p := filepath.Join(dir, n)
		if !seen[p] {
			seen[p] = true
			items = append(items, p)
		}
	}
	// Literal fallback: the fully-typed path, so entering a not-yet-existing or
	// fully-typed repo path is always selectable even when nothing on disk matches.
	if typed := expandPath(dp.filter); !seen[typed] {
		items = append(items, typed)
	}
	return items
}

// rankCandidates filters candidates to those whose *display* form matches the
// query and sorts them best-first, preserving input (priority) order on ties.
// Matching the display string rather than the raw absolute path keeps the
// "/home/<user>" prefix out of the match — otherwise its runes match queries
// they were never shown for. A candidate whose basename also matches gets that
// match's score added on top: users type project names, so a name hit should
// outrank an equal-score mid-path hit.
func rankCandidates(candidates []string, query string, display func(string) string) []string {
	type scored struct {
		path  string
		score int
	}
	matches := make([]scored, 0, len(candidates))
	for _, c := range candidates {
		ok, score := fuzzyMatch(query, display(c))
		if !ok {
			continue
		}
		if ok, base := fuzzyMatch(query, filepath.Base(c)); ok {
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

// CompletePrefix implements Tab-completion for the project field with shell-like
// "complete, then advance" semantics: it extends the filter to the longest common
// (literal, case-insensitive) prefix of the matching on-disk sub-directories, emitting
// the on-disk casing. It deliberately never appends a trailing "/": completing to an
// exact unique directory must leave that directory selectable rather than diving into it
// — descending is the user typing "/". Returns true when the filter grew (so the caller
// consumes Tab) and false otherwise (so Tab falls through to advance focus).
func (dp *DirectoryPicker) CompletePrefix() bool {
	if !looksLikePath(dp.filter) {
		return false
	}
	dirRaw, base := splitRawPath(dp.filter)
	_, names := dp.readSubdirs(dirRaw)

	lower := strings.ToLower(base)
	var matches []string
	for _, n := range names {
		if strings.HasPrefix(strings.ToLower(n), lower) {
			matches = append(matches, n)
		}
	}
	if len(matches) == 0 {
		return false
	}
	newFilter := dirRaw + longestCommonPrefix(matches)
	if newFilter == dp.filter {
		return false
	}
	dp.filter = newFilter
	dp.cursor = 0
	return true
}

// longestCommonPrefix returns the longest common prefix of the given names, comparing
// case-insensitively but emitting the casing of the first name (the on-disk casing).
func longestCommonPrefix(names []string) string {
	if len(names) == 0 {
		return ""
	}
	prefix := names[0]
	for _, n := range names[1:] {
		prefix = commonPrefix(prefix, n)
		if prefix == "" {
			break
		}
	}
	return prefix
}

func commonPrefix(a, b string) string {
	ra, rb := []rune(a), []rune(b)
	n := 0
	for n < len(ra) && n < len(rb) && unicode.ToLower(ra[n]) == unicode.ToLower(rb[n]) {
		n++
	}
	return string(ra[:n])
}

// GetSelectedPath returns the currently selected (absolute) path, or empty string
// if there is no selection.
func (dp *DirectoryPicker) GetSelectedPath() string {
	items := dp.visibleItems()
	if dp.cursor < 0 || dp.cursor >= len(items) {
		return ""
	}
	return items[dp.cursor]
}

// SelectPath points the cursor at the candidate equal to path and clears any typed
// filter so the selection takes effect immediately. It returns false (leaving the
// selection untouched) when path is not among the candidates — callers pass a path
// drawn from the same candidate list, so a miss means "nothing to pre-select".
func (dp *DirectoryPicker) SelectPath(path string) bool {
	for i, c := range dp.candidates {
		if c == path {
			dp.filter = ""
			dp.cursor = i
			return true
		}
	}
	return false
}

func dpLabelStyle() lipgloss.Style    { return overlayLabelStyle() }
func dpFilterStyle() lipgloss.Style   { return overlayFilterStyle() }
func dpSelectedStyle() lipgloss.Style { return overlaySelectedStyle() }
func dpDimStyle() lipgloss.Style      { return overlayDimStyle() }
func dpInvalidStyle() lipgloss.Style  { return theme.Current().DangerStyle() }
func dpDirectStyle() lipgloss.Style   { return theme.Current().DimStyle().Italic(true) }

// selectionHint returns the inline indicator for the current selection state: a red
// "(not a directory)" for an invalid target, a muted "(direct session — no git
// isolation)" for a valid non-git directory, or empty for a normal git repo.
func (dp *DirectoryPicker) selectionHint() string {
	if !dp.validityChecked {
		return ""
	}
	if !dp.selectionValid {
		return dpInvalidStyle().Render("  (not a directory)")
	}
	if dp.selectionDirect {
		return dpDirectStyle().Render("  (direct session — no git isolation)")
	}
	return ""
}

// Render renders the directory picker at a constant height (one header line, a blank
// line, then visibleRows item rows) so the surrounding overlay never changes size
// as focus moves. When unfocused it shows the chosen project on the header line and
// leaves the rows blank; when focused it shows the filter and the candidate list.
func (dp *DirectoryPicker) Render() string {
	var s strings.Builder

	if !dp.focused {
		s.WriteString(dpLabelStyle().Render("Project: "))
		if sel := dp.GetSelectedPath(); sel != "" {
			s.WriteString(dp.displayPath(sel))
		} else {
			s.WriteString(dpDimStyle().Render("(none)"))
		}
		s.WriteString(dp.selectionHint())
		s.WriteString("\n\n")
		s.WriteString(renderPickerRows(nil, 0, dp.visibleRows, false, "", dpSelectedStyle(), dpDimStyle()))
		return s.String()
	}

	s.WriteString(dpLabelStyle().Render("Project"))
	s.WriteString(dpFilterStyle().Render(" (filter/path: " + dp.filter + theme.Current().Glyphs.TextCursor + ")"))
	s.WriteString(dp.selectionHint())
	s.WriteString("\n\n")

	items := dp.visibleItems()
	labels := make([]string, len(items))
	for i, it := range items {
		labels[i] = dp.displayPath(it)
	}
	s.WriteString(renderPickerRows(labels, dp.cursor, dp.visibleRows, true, "no matches — type a path (/, ~, .)", dpSelectedStyle(), dpDimStyle()))
	return s.String()
}

// displayPath shortens a path for display by collapsing the home directory to "~".
func (dp *DirectoryPicker) displayPath(path string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if path == home {
			return "~"
		}
		if strings.HasPrefix(path, home+string(filepath.Separator)) {
			return "~" + path[len(home):]
		}
	}
	return path
}
