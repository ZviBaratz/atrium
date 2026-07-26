package overlay

import (
	"fmt"
	"strings"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/ui/theme"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	xansi "github.com/charmbracelet/x/ansi"
)

// settingKind selects how a settings row is displayed and edited: bools toggle
// in place, enums cycle with ←/→, ints and texts open an inline line editor, and
// read-only rows display a resolved fact with no editor at all.
type settingKind int

const (
	kindBool settingKind = iota
	kindEnum
	kindInt
	kindText
	// kindReadOnly is a display-only row: it has no set, no reset, and no options,
	// and every edit key is a no-op on it. Used for the resolved config.json path
	// (spec §4, Advanced).
	kindReadOnly
)

// minPollIntervalMs is the floor for the daemon poll interval; anything lower
// would have the daemon hammering tmux capture-pane in a hot loop.
const minPollIntervalMs = 100

// Vertical chrome around the settings body that is neither body nor footer:
// border (2) + Padding(1,2) verticals (2) + title (1) + blank-after-title (1)
// + blank-before-footer (1). Used to size the body window against the terminal
// height so the box (with its now variable-height footer) never overflows.
const settingsVChrome = 7

// settingsMinBody is the minimum number of body rows kept visible, which keeps
// the cursor row on screen; it is also the floor the wrapped-description cap
// reserves for the body on short terminals.
const settingsMinBody = 3

// SettingsOverlay is the in-TUI configuration panel: a navigable list of every
// scalar config field, edited in place. It mutates the *live* Config it was
// constructed with; the home model persists and live-applies after each change
// (see HandleKeyPress's changedKey return).
type SettingsOverlay struct {
	rows   []settingRow
	cfg    *config.Config
	cursor int

	width, height int

	editing bool
	input   textinput.Model
	lastErr string
}

// NewSettingsOverlay builds the settings panel over the given live config.
func NewSettingsOverlay(cfg *config.Config) *SettingsOverlay {
	return &SettingsOverlay{
		rows: newSettingRows(cfg),
		cfg:  cfg,
		// Sensible floor so Render works before the first SetSize.
		width:  80,
		height: 24,
	}
}

// SelectRow moves the cursor onto the row with the given key, reporting
// whether it exists.
func (s *SettingsOverlay) SelectRow(key string) bool {
	for i, r := range s.rows {
		if r.key == key {
			s.cursor = i
			return true
		}
	}
	return false
}

// isModified reports whether row i's value differs from its built-in default, for
// the "changed from default" marker. A row with no fixed default (see
// settingRow.defaultDisplay) is never modified.
func (s *SettingsOverlay) isModified(i int) bool {
	row := s.rows[i]
	if row.defaultDisplay == nil {
		return false
	}
	return row.get(s.cfg) != row.defaultDisplay()
}

// SetSize is given the full terminal dimensions; the panel sizes itself within
// them and windows its rows when the terminal is too short to show all.
func (s *SettingsOverlay) SetSize(width, height int) {
	s.width = width
	s.height = height
	s.input.Width = max(10, s.innerWidth()-s.labelColWidth()-4)
}

// HandleKeyPress processes one key press. It reports whether the panel should
// close, and — when a value changed — the changed row's key so the home model
// can persist the config and run that field's live-apply hook.
func (s *SettingsOverlay) HandleKeyPress(msg tea.KeyMsg) (closed bool, changedKey string) {
	if s.editing {
		return false, s.handleEditKey(msg)
	}

	row := &s.rows[s.cursor]
	switch msg.String() {
	case "esc", "ctrl+c":
		return true, ""
	case "up", "k":
		if s.cursor > 0 {
			s.cursor--
			s.lastErr = ""
		}
	case "down", "j":
		if s.cursor < len(s.rows)-1 {
			s.cursor++
			s.lastErr = ""
		}
	case "left":
		return false, s.cycleEnum(row, -1)
	case "right":
		return false, s.cycleEnum(row, +1)
	case " ":
		if row.kind == kindBool {
			return false, s.toggleBool(row)
		}
	case "enter":
		switch row.kind {
		case kindBool:
			return false, s.toggleBool(row)
		case kindEnum:
			return false, s.cycleEnum(row, +1)
		case kindInt, kindText:
			s.startEdit(row)
		}
	}
	return false, ""
}

// handleEditKey routes keys while the inline editor is open: enter commits
// (staying in edit mode on a validation error so the value can be fixed), esc
// abandons the edit, and everything else goes to the text input.
func (s *SettingsOverlay) handleEditKey(msg tea.KeyMsg) (changedKey string) {
	row := &s.rows[s.cursor]
	switch msg.String() {
	case "enter":
		if err := row.set(s.cfg, s.input.Value()); err != nil {
			s.lastErr = err.Error()
			return ""
		}
		s.editing = false
		s.lastErr = ""
		return row.key
	case "esc", "ctrl+c":
		s.editing = false
		s.lastErr = ""
		return ""
	default:
		s.input, _ = s.input.Update(msg)
		return ""
	}
}

// toggleBool flips a bool row and reports its key.
func (s *SettingsOverlay) toggleBool(row *settingRow) string {
	next := "on"
	if row.get(s.cfg) == "on" {
		next = "off"
	}
	_ = row.set(s.cfg, next) // bool setters never fail
	s.lastErr = ""
	return row.key
}

// cycleEnum advances an enum row by delta (wrapping). A single-option enum is
// a no-op and reports no change.
func (s *SettingsOverlay) cycleEnum(row *settingRow, delta int) string {
	if row.kind != kindEnum {
		return ""
	}
	opts := row.options(s.cfg)
	if len(opts) < 2 {
		return ""
	}
	cur := 0
	for i, o := range opts {
		if o == row.get(s.cfg) {
			cur = i
			break
		}
	}
	next := wrapIndex(cur, delta, len(opts))
	_ = row.set(s.cfg, opts[next]) // enum setters never fail
	s.lastErr = ""
	return row.key
}

// startEdit opens the inline line editor pre-filled with the row's raw value.
func (s *SettingsOverlay) startEdit(row *settingRow) {
	raw := row.get
	if row.editGet != nil {
		raw = row.editGet
	}
	in := textinput.New()
	in.Prompt = ""
	in.SetValue(raw(s.cfg))
	in.Width = max(10, s.innerWidth()-s.labelColWidth()-4)
	in.Focus()
	in.CursorEnd()
	s.input = in
	s.editing = true
	s.lastErr = ""
}

// boxWidth is the lipgloss .Width of the panel (content + padding, excluding
// the border); innerWidth is the usable text width inside the padding.
func (s *SettingsOverlay) boxWidth() int {
	w := 64
	if limit := s.width - 2; w > limit { // leave room for the border
		w = limit
	}
	if w < 20 {
		w = 20
	}
	return w
}

func (s *SettingsOverlay) innerWidth() int { return s.boxWidth() - 4 }

// labelColWidth returns the fixed label column width: the longest label plus
// the cursor marker and a separating gap.
func (s *SettingsOverlay) labelColWidth() int {
	w := 0
	for _, r := range s.rows {
		if len(r.label) > w {
			w = len(r.label)
		}
	}
	return w + 4 // "▸ " marker + 2-space gap
}

// Render draws the panel as a centered bordered box: a title, section-grouped
// rows windowed around the cursor on short terminals, then the selected row's
// description (or validation error) and the key hints.
func (s *SettingsOverlay) Render() string {
	t := theme.Current()
	inner := s.innerWidth()

	// Footer first: its (now variable) line count feeds the body's height budget.
	footer := s.renderFooter(inner)
	body := s.renderBody(inner, len(footer))

	title := t.OverlayTitleStyle().Render("Settings")
	content := title + "\n\n" + strings.Join(body, "\n") + "\n\n" + strings.Join(footer, "\n")

	return lipgloss.NewStyle().
		Border(t.Borders.Style).
		BorderForeground(t.Palette.Accent).
		Padding(1, 2).
		Width(s.boxWidth()).
		Render(content)
}

// renderBody renders the section headers + rows, windowed so the cursor's row
// is always visible within the height budget.
func (s *SettingsOverlay) renderBody(inner, footerHeight int) []string {
	t := theme.Current()
	headerStyle := t.DimStyle().Bold(true)
	dim := t.DimStyle()
	sel := t.AccentStyle()

	labelW := s.labelColWidth() - 2 // marker is rendered separately

	type bodyLine struct {
		text   string
		rowIdx int // -1 for headers/spacers
	}
	var lines []bodyLine
	// The old loop used `lastSection != ""` as its "first iteration" test, which a
	// zero-valued settingCategory cannot express — catSessions is 0, so an
	// uninitialized lastCategory would equal the first row's category and swallow
	// its header. Hence the explicit `first` flag.
	first := true
	lastCategory := allCategories()[0]
	for i, r := range s.rows {
		if first || r.category != lastCategory {
			if !first {
				lines = append(lines, bodyLine{text: "", rowIdx: -1})
			}
			lines = append(lines, bodyLine{text: headerStyle.Render(r.category.label()), rowIdx: -1})
			lastCategory = r.category
			first = false
		}

		marker := "  "
		if i == s.cursor {
			marker = t.Glyphs.SelectionMark + " "
		}
		value := s.renderValue(i)
		label := fmt.Sprintf("%-*s", labelW, r.label)
		line := marker + label + value
		switch {
		case i == s.cursor && s.editing:
			// The live text input carries its own cursor styling.
			line = sel.Render(marker+label) + value
		case i == s.cursor:
			line = sel.Render(line)
		default:
			line = dim.Render(marker+label) + t.FgStyle().Render(value)
		}
		lines = append(lines, bodyLine{text: xansi.Truncate(line, inner, "…"), rowIdx: i})
	}

	// Window the lines so the cursor's line stays visible on short terminals.
	// Budget = terminal height minus the fixed chrome and the now variable-height
	// footer (wrapped description + hint line); reduces to the old height-9 when
	// the description is a single line (footerHeight == 2).
	budget := s.height - settingsVChrome - footerHeight
	if budget < settingsMinBody {
		budget = settingsMinBody
	}
	if len(lines) <= budget {
		out := make([]string, len(lines))
		for i, l := range lines {
			out[i] = l.text
		}
		return out
	}
	cursorLine := 0
	for i, l := range lines {
		if l.rowIdx == s.cursor {
			cursorLine = i
			break
		}
	}
	start := 0
	if cursorLine >= budget {
		start = cursorLine - budget + 1
	}
	end := start + budget
	if end > len(lines) {
		end = len(lines)
	}
	out := make([]string, 0, budget)
	for _, l := range lines[start:end] {
		out = append(out, l.text)
	}
	return out
}

// renderValue formats a row's value cell by kind (or the live editor).
func (s *SettingsOverlay) renderValue(i int) string {
	if s.editing && i == s.cursor {
		return s.input.View()
	}
	row := s.rows[i]
	v := row.get(s.cfg)
	switch row.kind {
	case kindBool:
		if v == "on" {
			return "[x] on"
		}
		return "[ ] off"
	case kindEnum:
		return "‹ " + v + " ›"
	case kindReadOnly:
		// No editor affordance: a read-only row shows its resolved value bare.
		return v
	default:
		return v
	}
}

// renderFooter renders the selected row's description (or pending validation
// error) with its apply note, wrapped across as many lines as it needs, followed
// by the key-hint line. It returns one string per rendered line so Render can
// size the body window against the footer's actual height.
func (s *SettingsOverlay) renderFooter(inner int) []string {
	t := theme.Current()
	row := s.rows[s.cursor]

	desc := row.footerText()
	style := t.DimStyle()
	if s.lastErr != "" {
		desc = s.lastErr
		style = t.DangerStyle()
	}

	// Wrap the raw description to the inner width so long help is shown in full
	// rather than clipped to one line. xansi.Wrap hard-breaks over-long tokens, so
	// every line stays within inner (keeping the box within its width). Cap the
	// line count on short terminals — reserving chrome, the hint, and a minimum
	// body — so that on any terminal tall enough for the minimum layout the box
	// stays within the terminal and PlaceOverlay can't bottom-clip the pinned hint
	// line. On terminals shorter than that (below settingsVChrome + settingsMinBody
	// + a two-line footer) the box still degrades exactly like the pre-existing
	// body windowing. The cap only bites on short terminals; normally the full
	// description fits.
	lines := strings.Split(xansi.Wrap(desc, inner, ""), "\n")
	maxDescLines := max(1, s.height-settingsVChrome-1-settingsMinBody)
	if len(lines) > maxDescLines {
		lines = lines[:maxDescLines]
		last := lines[maxDescLines-1]
		if xansi.StringWidth(last) > inner-1 {
			last = xansi.Truncate(last, inner-1, "")
		}
		lines[maxDescLines-1] = last + "…"
	}
	// Style each wrapped line for color only; the outer box .Width pads them.
	for i, l := range lines {
		lines[i] = style.Render(l)
	}

	hint := "↑/↓ move · ←/→ change · ↵ edit · esc close"
	if s.editing {
		hint = "↵ save · esc cancel"
	}
	return append(lines, xansi.Truncate(t.OverlayHintStyle().Render(hint), inner, "…"))
}
