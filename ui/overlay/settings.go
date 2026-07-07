package overlay

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/ui/theme"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	xansi "github.com/charmbracelet/x/ansi"
)

// settingKind selects how a settings row is displayed and edited: bools toggle
// in place, enums cycle with ←/→, ints and texts open an inline line editor.
type settingKind int

const (
	kindBool settingKind = iota
	kindEnum
	kindInt
	kindText
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

// settingRow declares one editable config field. The panel is driven entirely
// by this schema, so exposing a new Config field is a matter of appending a
// row in newSettingRows — the navigation, editing, and rendering are generic.
//
// Rows are presentational + value plumbing only: set mutates the Config (with
// validation), but persisting to disk and live-applying side effects (theme
// repaint, tmux conf re-render) are the home model's job, keyed off the row's
// key (see app.applySettingChange).
type settingRow struct {
	key         string // stable identifier home switches on for live-apply
	section     string // "General" | "Appearance" | "Behavior"
	label       string
	kind        settingKind
	description string // one-line help shown while the row is selected
	applyNote   string // "" | "affects new sessions" | "applies on restart"

	get func(c *config.Config) string // display value
	// editGet returns the raw value to prefill the inline editor with; nil
	// means use get. Needed where display and raw differ (e.g. "unlimited").
	editGet func(c *config.Config) string
	set     func(c *config.Config, v string) error
	options func(c *config.Config) []string // enum rows only
}

// boolRow builds a kindBool row over a getter and a setter; get displays
// "on"/"off" and set accepts the same strings (the toggle handler flips them).
func boolRow(key, section, label, description, applyNote string, get func(c *config.Config) bool, set func(c *config.Config, v bool)) settingRow {
	return settingRow{
		key: key, section: section, label: label, kind: kindBool,
		description: description, applyNote: applyNote,
		get: func(c *config.Config) string {
			if get(c) {
				return "on"
			}
			return "off"
		},
		set: func(c *config.Config, v string) error {
			set(c, v == "on")
			return nil
		},
	}
}

// newSettingRows declares the panel contents in display order. Section headers
// are derived at render time from consecutive rows sharing a section.
func newSettingRows(cfg *config.Config) []settingRow {
	// Captured at panel open: a hand-edited config may hold a raw command in
	// default_program rather than a profile name (GetProgram passes it through
	// as-is). The enum's options must keep offering that original value even
	// after cycling overwrites it in the live config — otherwise the first
	// ←/→/enter press would persist a profile name over it and the raw command
	// would be irrecoverable.
	rawDefaultProgram := cfg.DefaultProgram

	return []settingRow{
		{
			key: "default_program", section: "General", label: "Default program", kind: kindEnum,
			description: "Agent launched in new sessions.",
			get:         func(c *config.Config) string { return c.DefaultProgram },
			set: func(c *config.Config, v string) error {
				c.DefaultProgram = v
				return nil
			},
			// Walk the declared profile order, not GetProfiles(): that helper
			// reorders the default first, which would make cycling ping-pong
			// between the first two profiles and never reach the rest.
			options: func(c *config.Config) []string {
				if len(c.Profiles) == 0 {
					return []string{c.DefaultProgram}
				}
				names := make([]string, len(c.Profiles))
				for i, p := range c.Profiles {
					names[i] = p.Name
				}
				// Keep the captured raw value (see newSettingRows) as a cycle
				// option so touching the row can never silently destroy it —
				// cycling must always be able to return.
				if !slices.Contains(names, rawDefaultProgram) {
					names = append([]string{rawDefaultProgram}, names...)
				}
				return names
			},
		},
		{
			key: "branch_prefix", section: "General", label: "Branch prefix", kind: kindText,
			description: "Prefix for new session branches.", applyNote: "affects new sessions",
			get: func(c *config.Config) string { return c.BranchPrefix },
			set: func(c *config.Config, v string) error {
				c.BranchPrefix = strings.TrimSpace(v)
				return nil
			},
		},
		{
			key: "max_sessions", section: "General", label: "Max sessions", kind: kindInt,
			description: "Cap on concurrent sessions; empty or 0 means unlimited.",
			get: func(c *config.Config) string {
				if n := c.GetMaxSessions(); n > 0 {
					return strconv.Itoa(n)
				}
				return "unlimited"
			},
			editGet: func(c *config.Config) string {
				if n := c.GetMaxSessions(); n > 0 {
					return strconv.Itoa(n)
				}
				return ""
			},
			set: func(c *config.Config, v string) error {
				v = strings.TrimSpace(v)
				if v == "" || v == "0" {
					c.MaxSessions = nil
					return nil
				}
				n, err := strconv.Atoi(v)
				if err != nil || n < 0 {
					return fmt.Errorf("max sessions must be a non-negative number")
				}
				c.MaxSessions = &n
				return nil
			},
		},
		{
			key: "theme", section: "Appearance", label: "Theme", kind: kindEnum,
			description: "UI color palette and border style.",
			get: func(c *config.Config) string {
				if c.Theme == "" {
					return theme.DefaultThemeName
				}
				return c.Theme
			},
			set: func(c *config.Config, v string) error {
				c.Theme = v
				return nil
			},
			options: func(c *config.Config) []string {
				names := theme.Names()
				sort.Strings(names)
				return names
			},
		},
		boolRow("nerd_font", "Appearance", "Nerd Font icons",
			"Vendor icons (branch, PR, dirty, auto) from a patched Nerd Font; off = plain Unicode.", "",
			(*config.Config).GetNerdFont,
			func(c *config.Config, v bool) { c.NerdFont = &v }),
		{
			key: "model_indicator", section: "Appearance", label: "Model chip", kind: kindEnum,
			description: "Per-session model chip in the list, shown whenever the model is known.",
			get: func(c *config.Config) string {
				return c.GetModelIndicator()
			},
			set: func(c *config.Config, v string) error {
				c.ModelIndicator = v
				return nil
			},
			options: func(c *config.Config) []string {
				return []string{config.ModelIndicatorOn, config.ModelIndicatorOff}
			},
		},
		{
			key: "permission_indicator", section: "Appearance", label: "Permission chip", kind: kindEnum,
			description: "Per-session permission-mode chip (plan/accept-edits/auto) in the list.",
			get: func(c *config.Config) string {
				return c.GetPermissionIndicator()
			},
			set: func(c *config.Config, v string) error {
				c.PermissionIndicator = v
				return nil
			},
			options: func(c *config.Config) []string {
				return []string{config.PermissionIndicatorOn, config.PermissionIndicatorOff}
			},
		},
		boolRow("hint_bar", "Appearance", "Hint bar",
			"Always-on key-hint bar at the bottom of the screen.", "",
			(*config.Config).GetHintBar,
			func(c *config.Config, v bool) { c.HintBar = &v }),
		boolRow("session_context_bar", "Appearance", "Session context bar",
			"In-session status line.", "affects new sessions",
			(*config.Config).GetSessionContextBar,
			func(c *config.Config, v bool) { c.SessionContextBar = &v }),
		boolRow("auto_attach", "Behavior", "Auto-attach",
			"Attach to a new session as soon as it starts.", "",
			(*config.Config).GetAutoAttach,
			func(c *config.Config, v bool) { c.AutoAttach = &v }),
		boolRow("auto_yes", "Behavior", "Auto-yes",
			"Auto-accept agent prompts (a daemon takes over after quit).", "",
			func(c *config.Config) bool { return c.AutoYes },
			func(c *config.Config, v bool) { c.AutoYes = v }),
		boolRow("show_release_notes_after_update", "Behavior", "Release notes after update",
			"Show a \"what's new\" overlay once after updating to a new version.", "",
			(*config.Config).GetShowReleaseNotesAfterUpdate,
			func(c *config.Config, v bool) { c.ShowReleaseNotesAfterUpdate = &v }),
		boolRow("trust_worktrees_root", "Behavior", "Trust worktrees root",
			"Pre-accept Claude's workspace-trust dialog for all session worktrees.", "applies on restart",
			(*config.Config).GetTrustWorktreesRoot,
			func(c *config.Config, v bool) { c.TrustWorktreesRoot = &v }),
		{
			key: "carry_files", section: "Behavior", label: "Carry files", kind: kindText,
			description: "Gitignored files copied into each new worktree; comma-separated repo-relative paths.",
			applyNote:   "affects new sessions",
			get: func(c *config.Config) string {
				files := c.GetCarryFiles()
				if len(files) == 0 {
					return "(none)"
				}
				return strings.Join(files, ", ")
			},
			editGet: func(c *config.Config) string {
				return strings.Join(c.GetCarryFiles(), ", ")
			},
			set: func(c *config.Config, v string) error {
				// Split on commas, trim each entry, drop blanks. Empty or
				// all-blank input collapses to a non-nil empty slice — the
				// explicit opt-out per GetCarryFiles's nil-vs-empty contract.
				parts := strings.Split(v, ",")
				files := make([]string, 0, len(parts))
				for _, p := range parts {
					if t := strings.TrimSpace(p); t != "" {
						files = append(files, t)
					}
				}
				c.CarryFiles = files
				return nil
			},
		},
		{
			key: "daemon_poll_interval", section: "Behavior", label: "Poll interval (ms)", kind: kindInt,
			description: "Auto-yes daemon polling rate.", applyNote: "applies on restart",
			get: func(c *config.Config) string { return strconv.Itoa(c.DaemonPollInterval) },
			set: func(c *config.Config, v string) error {
				n, err := strconv.Atoi(strings.TrimSpace(v))
				if err != nil {
					return fmt.Errorf("poll interval must be a number of milliseconds")
				}
				if n < minPollIntervalMs {
					return fmt.Errorf("poll interval must be at least %dms", minPollIntervalMs)
				}
				c.DaemonPollInterval = n
				return nil
			},
		},
		boolRow("kill_double_tap_confirm", "Behavior", "Kill double-tap",
			"A second Ctrl+X confirms the kill dialog in one motion.", "",
			(*config.Config).GetKillDoubleTapConfirm,
			func(c *config.Config, v bool) { c.KillDoubleTapConfirm = &v }),
		boolRow("pr_create_draft", "Behavior", "Create PRs as draft",
			"PRs opened with c start as drafts (turn off to merge them with m in-app).", "",
			(*config.Config).GetPRCreateDraft,
			func(c *config.Config, v bool) { c.PRCreateDraft = &v }),
		boolRow("update_base_on_create", "Behavior", "Update base on create",
			"Branch new sessions off the latest remote tip of their base, not a stale local copy.", "affects new sessions",
			(*config.Config).GetUpdateBaseOnCreate,
			func(c *config.Config, v bool) { c.UpdateBaseOnCreate = &v }),
		boolRow("fast_forward_local_base", "Behavior", "Fast-forward local base",
			"Also advance your local base branch to origin during create (clean fast-forward only).", "modifies your local branch",
			(*config.Config).GetFastForwardLocalBase,
			func(c *config.Config, v bool) { c.FastForwardLocalBase = &v }),
		{
			key: "session_sort", section: "Behavior", label: "Session sort", kind: kindEnum,
			description: "Order within each repo group: creation keeps the manual order (reorderable with J/K); status floats NeedsInput and unread sessions to the top. Group order stays manual ({ / }).",
			get:         func(c *config.Config) string { return c.GetSessionSort() },
			set: func(c *config.Config, v string) error {
				c.SessionSort = v
				return nil
			},
			options: func(c *config.Config) []string {
				return []string{config.SessionSortCreation, config.SessionSortStatus}
			},
		},
		{
			key: "group_mode", section: "Behavior", label: "Account clustering", kind: kindEnum,
			description: "Off (the default) keeps the list grouped by repo. On adds a top-level cluster by Claude account above the repo groups — a divider and tinted headers per account. The clustering is a visual no-op unless two or more accounts are present. Manual reordering stays available: J/K reorders within a repo group, and { / } reorders groups within an account cluster (a move across an account boundary is refused).",
			// Display value is off/on; the stored config value stays repo/account, so
			// config.json and a future third grouping axis keep their vocabulary.
			get: func(c *config.Config) string {
				if c.GetGroupMode() == config.GroupModeAccount {
					return "on"
				}
				return "off"
			},
			set: func(c *config.Config, v string) error {
				if v == "on" {
					c.GroupMode = config.GroupModeAccount
				} else {
					c.GroupMode = config.GroupModeRepo
				}
				return nil
			},
			options: func(c *config.Config) []string {
				return []string{"off", "on"}
			},
		},
		{
			key: "auto_update", section: "Behavior", label: "Auto-update", kind: kindEnum,
			description: "Update check at startup: notify shows a hint, auto installs in the background, off disables.",
			applyNote:   "applies on restart",
			get:         func(c *config.Config) string { return c.GetAutoUpdateMode() },
			set: func(c *config.Config, v string) error {
				c.AutoUpdate = v
				return nil
			},
			options: func(c *config.Config) []string {
				return []string{config.AutoUpdateNotify, config.AutoUpdateAuto, config.AutoUpdateOff}
			},
		},
		{
			key: "tmux_config_override", section: "Behavior", label: "Tmux config override", kind: kindText,
			description: "Custom tmux config path.", applyNote: "affects new sessions",
			get: func(c *config.Config) string {
				if c.TmuxConfigOverride == "" {
					return "(managed)"
				}
				return c.TmuxConfigOverride
			},
			editGet: func(c *config.Config) string { return c.TmuxConfigOverride },
			set: func(c *config.Config, v string) error {
				c.TmuxConfigOverride = strings.TrimSpace(v)
				return nil
			},
		},
	}
}

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
	lastSection := ""
	for i, r := range s.rows {
		if r.section != lastSection {
			if lastSection != "" {
				lines = append(lines, bodyLine{text: "", rowIdx: -1})
			}
			lines = append(lines, bodyLine{text: headerStyle.Render(r.section), rowIdx: -1})
			lastSection = r.section
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

	desc := row.description
	style := t.DimStyle()
	if s.lastErr != "" {
		desc = s.lastErr
		style = t.DangerStyle()
	} else if row.applyNote != "" {
		desc += " · " + row.applyNote
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
