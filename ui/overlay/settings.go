package overlay

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/ui/theme"
	"github.com/charmbracelet/x/ansi"
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

// settingsVChrome is the vertical chrome around the panes and the help pane:
// border (2) + Padding(1,2) verticals (2) + title (1) + blank-after-title (1)
// + hint (1).
//
// The pane/help separator is deliberately NOT counted here — it is counted with the help
// pane (helpBlockHeight), because it is only drawn when there is a help pane to separate.
const settingsVChrome = 7

// settingsMinBody is the minimum number of pane rows kept visible, which keeps the cursor
// row on screen. On a terminal too short for the full layout the help pane sheds lines
// down to zero before this floor is touched — the row list is what the panel is for.
const settingsMinBody = 3

// helpPaneLines is the help pane's height whenever the terminal can afford it (spec §10).
// Fixed height is the entire point: the old footer grew with the help text and stole rows
// from the list, so selecting Account clustering at 80x24 left 8 visible rows while its
// help took 8 lines (D5).
const helpPaneLines = 3

// SettingsOverlay is the in-TUI configuration panel: a rail of categories beside the
// highlighted category's rows, edited in place. It mutates the *live* Config it was
// constructed with; the home model persists and live-applies after each change
// (see HandleKeyPress's changedKey return).
type SettingsOverlay struct {
	rows   []settingRow
	cfg    *config.Config
	cursor int // index into rows; global, not per category

	// focus selects which pane consumes navigation keys. railCursor indexes
	// railEntries(); the rows pane shows whatever that entry owns.
	focus      settingsFocus
	railCursor int

	// helpOpen is the `?` expanded-help view, with its own scroll offset. It takes over the box
	// rather than opening a second overlay, so the panel's focus and rail cursor survive it
	// untouched.
	helpOpen   bool
	helpScroll int

	width, height int

	editing bool
	input   textinput.Model
	lastErr string

	// clusteringVisible is home's answer to "does ui.List currently render account clusters?"
	// — nil until home injects it. It is a *bool rather than a bool because nil must mean
	// "unknown, show no chip": group_mode's honest gate is session-derived and a panel that
	// cannot see the session list must not guess. See SetAccountClusteringVisible and
	// TestGroupModeHasNoConfigOnlyInertPredicate.
	clusteringVisible *bool

	// handoff is the sibling surface a rail entry asked home to open in this panel's place.
	// Read once, as the panel closes — see Handoff.
	handoff SettingsHandoff

	// search is the `/` filter (spec §8). It is a NAMED field rather than an embedded Picker:
	// embedding would promote Focus/Blur/SetWidth/SetVisibleRows/SetPreviewHook onto this
	// type's public API, where five of the six are meaningless for a panel that sizes itself.
	// The picker owns the filter text, the result cursor and the shared key grammar; its
	// IsFocused() is the "a filter is active" flag, so there is no second bool to keep in step
	// with it.
	search Picker

	// The Profiles editor's state (spec §9). profileCursor indexes cfg.Profiles — a SECOND
	// index space beside s.cursor, which indexes s.rows; see paneCursor for the one place that
	// distinction is load-bearing. The rest are transient and cleared by resetProfileTransients.
	profileCursor  int
	profileForm    *profileForm
	profileConfirm bool
	// profileConfirmIdx is the record an armed delete is about, captured when it is armed —
	// the same discipline profileForm.editIndex uses. The cursor can move between the key
	// that arms the confirmation and the key that answers it (a detection landing follows
	// the record it added), and a prompt that named one record must not delete another.
	profileConfirmIdx int
	// profileNote is a one-keypress informational line for the editor's help pane — a detection
	// result. It is not lastErr: "no new agents detected" is an outcome, not a failure, and the
	// help pane paints lastErr in DangerStyle.
	profileNote string
	// profileDetectPending is D's request for home to run detection off the update loop;
	// profileDetecting stays set until the result lands, so a second D cannot queue a second
	// shell probe and the pane can say a probe is running. See requestProfileDetect.
	profileDetectPending bool
	profileDetecting     bool
}

// NewSettingsOverlay builds the settings panel over the given live config, focused on
// the rail at its default category.
func NewSettingsOverlay(cfg *config.Config) *SettingsOverlay {
	s := &SettingsOverlay{
		rows:       newSettingRows(cfg),
		cfg:        cfg,
		focus:      focusRail,
		railCursor: railDefaultIndex(),
		// Sync source: a filter edit re-ranks and resets the cursor to the top.
		search: newPicker(false),
		// Sensible floor so Render works before the first SetSize.
		width:  80,
		height: 24,
	}
	s.syncCursorToRail()
	return s
}

// OpenAt moves the panel to the row with the given key, reporting whether it exists. It
// syncs the rail to that row's category, focuses the rows pane, and drops any transient
// state (an open editor, the ? view) so the cursor lands somewhere the user can actually
// see.
//
// That composite behavior is the deep-link contract of spec §12 — it is what makes a jump
// from a dialog or a notice land somewhere usable. It is also the path most of this
// package's tests take to reach a row, through the settingsAt helper: they select a row,
// then send keys expecting them to reach it.
//
// It takes the key alone, where spec §12 wrote OpenAt(category, key). settingCategory is
// unexported, so app cannot name one; and guard 1 pins that every key belongs to exactly one
// category, so a passed category would be a second source of truth that can only ever
// disagree with the row's own. The spec is amended to match.
func (s *SettingsOverlay) OpenAt(key string) bool {
	for i, r := range s.rows {
		if r.key != key {
			continue
		}
		s.cursor = i
		s.railCursor = railIndexForCategory(r.category)
		s.focus = focusRows
		s.editing = false
		s.helpOpen = false
		s.helpScroll = 0
		s.clearSearch()
		s.resetProfileTransients()
		s.lastErr = ""
		return true
	}
	return false
}

// SetAccountClusteringVisible records whether ui.List currently renders account clusters, so the
// Account clustering row can be dimmed when the setting is on but doing nothing.
//
// It exists because that gate is session-derived and settingRow.activeWhen can only see
// *config.Config. Until home calls this, the panel shows no chip for the row at all: spec §5's
// config-only predicate was wrong in both directions (accounts sharing a rotation pool collapse
// to one cluster; unattributed sessions form one anyway), so a panel that cannot see the session
// list must not guess. See TestGroupModeHasNoConfigOnlyInertPredicate.
func (s *SettingsOverlay) SetAccountClusteringVisible(visible bool) {
	s.clusteringVisible = &visible
}

// RailIndex reports which rail entry is current, so home can restore it the next time the panel
// opens (spec §7). Persisting it to state.json is a deliberate non-goal.
func (s *SettingsOverlay) RailIndex() int { return s.railCursor }

// Handoff reports which sibling surface the panel asked to open in its place, or
// HandoffNone. home reads it when HandleKeyPress reports the panel closed.
func (s *SettingsOverlay) Handoff() SettingsHandoff { return s.handoff }

// SelectedRowKey is the key of the row the rows pane has highlighted, so home's tests can
// assert where a deep link landed without reaching into the panel's cursor.
func (s *SettingsOverlay) SelectedRowKey() string { return s.selectedRow().key }

// RailEntryCount is how many entries the rail has, so home (and its tests) can address the
// last one without importing the rail's vocabulary.
func (s *SettingsOverlay) RailEntryCount() int { return len(railEntries()) }

// SetRailIndex moves the rail to the given entry, clamping out-of-range values — a remembered
// index can outlive a rail that shrank — and pulling the row cursor with it.
func (s *SettingsOverlay) SetRailIndex(i int) {
	s.railCursor = clamp(i, 0, len(railEntries())-1)
	s.syncCursorToRail()
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

// SetSize is given the full terminal dimensions; the panel sizes itself within them, falls
// back to a single pane on a narrow terminal, and windows its rows on a short one.
func (s *SettingsOverlay) SetSize(width, height int) {
	s.width = width
	s.height = height
	s.input.SetWidth(s.editorWidth())
}

// HandleKeyPress processes one key press. It reports whether the panel should
// close, and — when a value changed — the changed row's key so the home model
// can persist the config and run that field's live-apply hook.
//
// The order of these guards is the grammar: an open editor swallows everything (so j/k type
// rather than navigate) — the inline line editor or the Profiles record form — then the
// expanded-help view, then an active filter (which swallows runes for the same reason), then
// the focused pane, where the Profiles editor takes its own keys.
func (s *SettingsOverlay) HandleKeyPress(msg tea.KeyPressMsg) (closed bool, changedKey string) {
	switch {
	case s.editing:
		return false, s.handleEditKey(msg)
	case s.profileForm != nil:
		return false, s.handleProfileFormKey(msg)
	case s.helpOpen:
		s.handleHelpKey(msg)
		return false, ""
	case s.searching():
		return s.handleSearchKey(msg)
	case s.focus == focusRail:
		return s.handleRailKey(msg), ""
	case s.profilesPaneActive():
		return s.handleProfilesKey(msg)
	default:
		return s.handleRowsKey(msg)
	}
}

// handleEditKey routes keys while the inline editor is open: enter commits
// (staying in edit mode on a validation error so the value can be fixed), esc
// abandons the edit, and everything else goes to the text input.
func (s *SettingsOverlay) handleEditKey(msg tea.KeyPressMsg) (changedKey string) {
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
	in.SetWidth(s.editorWidth())
	in.Focus()
	in.CursorEnd()
	s.input = in
	s.editing = true
	s.lastErr = ""
}

// boxWidth is the lipgloss .Width of the panel (content + padding, excluding the border);
// innerWidth is the usable text width inside the padding.
//
// The 96 cap replaces the old fixed 64, which wasted a third of a 100-column terminal (D12).
// At the 80-column floor the box is 78 and the inner width 74 — which is exactly the
// summaryBudget PR A wrote the copy against.
func (s *SettingsOverlay) boxWidth() int {
	w := 96
	if limit := s.width - 2; w > limit { // leave room for the border
		w = limit
	}
	if w < 20 {
		w = 20
	}
	return w
}

func (s *SettingsOverlay) innerWidth() int { return s.boxWidth() - 4 }

// titleLine is the panel's first row: its name, plus the active filter.
//
// The filter rides this row rather than claiming one of its own because the box's height
// depends only on the terminal size — a taller box on `/` would re-centre the whole panel
// under the user mid-keystroke (the jump ui/overlay/textInput_size.go:3-8 warns about). The
// query is truncated to the inner width for the same reason: an over-wide line soft-wraps,
// grows the box a row, and clips the pinned hint off the bottom.
func (s *SettingsOverlay) titleLine() string {
	t := theme.Current()
	if !s.searching() {
		return t.OverlayTitleStyle().Render("Settings")
	}
	const gap = "   "
	filter := "/" + s.search.filter + t.Glyphs.TextCursor
	if budget := s.innerWidth() - ansi.StringWidth("Settings"+gap); ansi.StringWidth(filter) > budget {
		filter = ansi.Truncate(filter, max(0, budget), "…")
	}
	return t.OverlayTitleStyle().Render("Settings") + gap + overlayFilterStyle().Render(filter)
}

// Render draws the panel as a centered bordered box: a title, the rail beside the highlighted
// category's rows, a separator, the fixed-height help pane, and the key hints.
//
// The box's height is a function of the terminal size alone, so it never changes as the rail
// or row cursor moves — a centered overlay that resizes gets re-centered under the user
// mid-navigation.
func (s *SettingsOverlay) Render() string {
	t := theme.Current()

	var lines []string
	if s.helpOpen {
		// The expanded view fills the panes and the help block together, so the box's height
		// does not change when it opens.
		lines = s.expandedHelpLines()
	} else {
		lines = s.bodyLines()
		if sep := s.separatorLine(); sep != "" {
			lines = append(lines, sep)
		}
		lines = append(lines, s.helpLines()...)
	}
	lines = append(lines, s.hintLine())

	title := s.titleLine()
	return lipgloss.NewStyle().
		Border(t.Borders.Style).
		BorderForeground(t.Palette.Accent).
		Padding(1, 2).
		// +2 for the left/right border — v2 counts it inside Width. See theme.Panel.
		Width(s.boxWidth() + 2).
		Render(title + "\n\n" + strings.Join(lines, "\n"))
}
