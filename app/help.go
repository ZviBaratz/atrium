package app

import (
	"strings"

	"github.com/ZviBaratz/atrium/keys"
	"github.com/ZviBaratz/atrium/ui"
	"github.com/ZviBaratz/atrium/ui/overlay"
	"github.com/ZviBaratz/atrium/ui/theme"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

type helpText interface {
	// toContent returns the help UI content.
	toContent() string
	// hint returns the dismiss hint the overlay pins below the content (it
	// must stay visible while the content scrolls, so it is not part of
	// toContent).
	hint() string
	// mask returns the bit mask used to track which one-time screens have been
	// seen (persisted in app state). Screens with alwaysShow ignore it.
	mask() uint32
}

// helpTypeGeneral is the on-demand cheatsheet (opened with '?').
type helpTypeGeneral struct{}

// helpTypeWelcome is the one-time welcome shown on first launch ever.
type helpTypeWelcome struct{}

// Help styles read the active theme at render time.
func helpTitleStyle() lipgloss.Style  { return theme.Current().PurpleStyle().Bold(true).Underline(true) }
func helpHeaderStyle() lipgloss.Style { return theme.Current().CyanStyle().Bold(true) }
func helpKeyStyle() lipgloss.Style    { return theme.Current().AttentionStyle().Bold(true) }
func helpDescStyle() lipgloss.Style   { return theme.Current().FgStyle() }
func helpDimStyle() lipgloss.Style    { return theme.Current().DimStyle() }

// helpRow formats a "key   description" line with the key column padded to a
// fixed width so descriptions align.
func helpRow(key, desc string) string {
	const keyCol = 12
	pad := keyCol - runewidth.StringWidth(key)
	if pad < 1 {
		pad = 1
	}
	return helpKeyStyle().Render(key) + strings.Repeat(" ", pad) + helpDescStyle().Render(desc)
}

// The cheatsheet is generated from keys.HelpGroups — help is a projection of
// the keymap registry, never authored beside it (#371). Layout and prose live
// in that table; only the rendering rules live here. The glyph legend below is
// likewise a projection of the active Glyphs table (see legendGroups).
func (h helpTypeGeneral) toContent() string {
	lines := []string{helpTitleStyle().Render("Atrium — Keys")}
	for _, group := range keys.HelpGroups {
		lines = append(lines, "", helpHeaderStyle().Render(group.Title))
		for _, row := range group.Rows {
			lines = append(lines, helpRow(rowKeyLabel(row), rowDesc(row)))
		}
	}
	lines = append(lines, "", helpHeaderStyle().Render("Mouse"))
	for _, row := range mouseHelpRows {
		lines = append(lines, helpRow(row[0], row[1]))
	}
	lines = append(lines, helpDimStyle().Render(
		"Shift+drag selects text for your terminal's own copy, bypassing capture; "+
			"turn the mouse off entirely in settings (,)."))
	lines = append(lines, "", helpHeaderStyle().Render("Legend"))
	lines = append(lines, legendLines()...)
	return lipgloss.JoinVertical(lipgloss.Left, lines...)
}

// legendEntry is one glyph in the '?' legend: the glyph text, the semantic style
// it renders in on a row, an optional pre-rendered override for self-styled chips
// (the AUTO badge carries its own background), and a short gloss.
type legendEntry struct {
	glyph    string
	style    lipgloss.Style
	rendered string
	label    string
}

// legendGroup is a titled cluster of legend entries.
type legendGroup struct {
	title   string
	entries []legendEntry
}

// legendGroups projects the active Glyphs table into the '?' legend, grouped
// status / git / badges. Every entry reads its glyph from the live Glyphs set, so
// the legend can never drift from what a row actually paints, and it re-renders
// under whichever fidelity rung is active. Completeness — every row-vocabulary
// glyph field is present — is pinned by TestLegendCoversRowVocabulary.
func legendGroups() []legendGroup {
	t := theme.Current()
	g := t.Glyphs
	pending := lipgloss.NewStyle().Foreground(t.Palette.Pending)
	seen := lipgloss.NewStyle().Foreground(t.Palette.SuccessDim)
	spin := " "
	if len(g.SpinnerFrames) > 0 {
		spin = g.SpinnerFrames[0]
	}
	return []legendGroup{
		{"status", []legendEntry{
			{glyph: spin, style: t.WorkingStyle(), label: "working"},
			{glyph: g.Pending, style: pending, label: "pending"},
			{glyph: g.Ready, style: t.SuccessStyle(), label: "ready"},
			{glyph: g.ReadySeen, style: seen, label: "seen"},
			{glyph: g.Waiting, style: t.AttentionStyle(), label: "waiting"},
			{glyph: g.Paused, style: t.DimStyle(), label: "paused"},
		}},
		{"git", []legendEntry{
			{glyph: g.Branch, style: t.DimStyle(), label: "branch"},
			{glyph: g.Ahead, style: t.DimStyle(), label: "ahead"},
			{glyph: g.Behind, style: t.AttentionStyle(), label: "behind"},
			{glyph: g.Dirty, style: t.DimStyle(), label: "dirty"},
			{glyph: g.PR, style: t.AccentStyle(), label: "PR"},
			{glyph: g.DiffAdd, style: t.SuccessStyle(), label: "added"},
			{glyph: g.DiffDel, style: t.DangerStyle(), label: "removed"},
		}},
		{"badges", []legendEntry{
			{glyph: g.Queued, style: t.AccentStyle(), label: "queued"},
			{glyph: g.Note, style: t.PurpleStyle(), label: "note"},
			{glyph: g.Warn, style: t.AttentionStyle(), label: "stale"},
			{glyph: g.AutoBadge, rendered: t.BadgeStyle().Render(" " + g.AutoBadge + "AUTO "), label: "auto-accepting"},
		}},
	}
}

// legendLines renders the legend groups, one line per group: a padded group title
// followed by "<glyph> <label>" entries. The text overlay wraps a long line to the
// box width, so a narrow terminal reflows rather than overflows.
func legendLines() []string {
	var lines []string
	for _, grp := range legendGroups() {
		title := grp.title
		for len(title) < 8 {
			title += " "
		}
		var b strings.Builder
		b.WriteString(helpDimStyle().Render("  " + title))
		for i, e := range grp.entries {
			if i > 0 {
				b.WriteString("  ")
			}
			glyph := e.rendered
			if glyph == "" {
				glyph = e.style.Render(e.glyph)
			}
			b.WriteString(glyph + " " + helpDimStyle().Render(e.label))
		}
		lines = append(lines, b.String())
	}
	return lines
}

// mouseHelpRows document the mouse map in the ? overlay. Every mouse action
// mirrors a key (click zones in ui.Menu + app.handleMouse), so this is a map of
// what the mouse does, not a set of mouse-only powers. The Shift bypass and the
// off-switch are called out below the table.
var mouseHelpRows = [][2]string{
	{"click", "select a row · fold a repo header · switch tab · run a hint-bar key"},
	{"double-click", "attach to a session row (like ↵)"},
	{"wheel", "move the selection over the list · scroll the active pane"},
	{"drag", "the list/preview divider to resize the split"},
}

// rowKeyLabel derives a row's key column from its bindings' Help().Key labels
// — never free text, so the column cannot document a key the registry lacks.
func rowKeyLabel(row keys.HelpRow) string {
	sep := " / "
	if row.Compact {
		sep = " "
	}
	labels := make([]string, len(row.Keys))
	for i, k := range row.Keys {
		labels[i] = keys.GlobalKeyBindings[k].Help().Key
	}
	return strings.Join(labels, sep)
}

// rowDesc prefixes rows whose keys live in the attach layer, so generated help
// documents them truthfully by construction — the table's prose deliberately
// omits the prefix (see keys.HelpRow).
func rowDesc(row keys.HelpRow) string {
	for _, k := range row.Keys {
		if keys.LayerOf(k) != keys.LayerAttached {
			return row.Desc
		}
	}
	return "in a session: " + row.Desc
}

func (h helpTypeGeneral) hint() string { return "press any key to close" }

func (h helpTypeGeneral) mask() uint32 { return 1 }

// helpTypeWelcome is no longer a rendered help screen (the interactive
// overlay.WelcomeOverlay replaced it); only its seen-bit survives, so the type
// carries just mask(). Bit 4; bits 1-3 belonged to retired teaching modals.
func (h helpTypeWelcome) mask() uint32 { return 1 << 4 }

// showHelpScreen displays a help overlay. The cheatsheet (helpTypeGeneral) always
// shows on demand; one-time screens (welcome) show until their seen bit is set.
// Crucially, the bit is NOT set here on render — the welcome's bit is set only on
// the first successful session start (see the instanceStartedMsg handler), so a
// stray keypress that dismisses the welcome no longer burns it for good; it
// re-shows each launch until the user has actually created a session. onDismiss is
// retained for compatibility but is now always nil.
func (m *home) showHelpScreen(helpType helpText, onDismiss func()) (tea.Model, tea.Cmd) {
	var alwaysShow bool
	switch helpType.(type) {
	case helpTypeGeneral:
		alwaysShow = true
	}

	flag := helpType.mask()

	if alwaysShow || (m.appState.GetHelpScreensSeen()&flag) == 0 {
		m.textOverlay = overlay.NewTextOverlay(helpType.toContent())
		m.textOverlay.SetHint(helpType.hint())
		m.textOverlay.OnDismiss = onDismiss
		m.state = stateHelp
		// Size the overlay now rather than waiting for the next resize; no-op
		// before the first WindowSizeMsg (the overlay then renders unwindowed).
		m.recomputeLayout()
		return m, nil
	}

	if onDismiss != nil {
		onDismiss()
	}
	return m, nil
}

// maybeShowWelcome opens the interactive first-launch setup on first run (until
// the welcome seen-bit is set), returning the async agent-detection command. On
// later launches it instead runs the always-on missing-program check. Guarded by
// welcomeChecked so it acts once per process.
func (m *home) maybeShowWelcome() tea.Cmd {
	if m.welcomeChecked {
		return nil
	}
	m.welcomeChecked = true

	if m.appState.GetHelpScreensSeen()&(helpTypeWelcome{}.mask()) != 0 {
		// Welcome already retired — protect returning users whose default
		// program is no longer installed. The check runs off the main loop
		// (checkProgramInstalledCmd) so the claude shell-profile probe never
		// blocks the first frame.
		return m.checkProgramInstalledCmd()
	}

	m.welcomeOverlay = overlay.NewWelcomeOverlay()
	m.state = stateWelcome
	m.recomputeLayout()
	return m.detectAgentsCmd()
}

// handleHelpState handles key events when in help state
func (m *home) handleHelpState(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// The overlay scrolls on navigation keys while its content overflows;
	// any other key closes it.
	if m.textOverlay.HandleKeyPress(msg) {
		return m.closeTextOverlay()
	}
	return m, nil
}

// closeTextOverlay dismisses the modal text overlay (help or info) and
// restores the default state. Shared by every dismissal path: any-key from
// the help and info states, and a click outside the box.
func (m *home) closeTextOverlay() (tea.Model, tea.Cmd) {
	m.textOverlay.Dismiss()
	m.state = stateDefault
	return m, tea.Sequence(
		tea.WindowSize(),
		func() tea.Msg {
			m.menu.SetState(ui.StateDefault)
			return nil
		},
	)
}

// textOverlayContains reports whether the screen cell (x, y) falls inside the
// rendered modal box. PlaceOverlay centers the overlay on the composed frame,
// and the frame is exactly windowWidth×windowHeight (an invariant pinned by
// TestViewFitsTerminalBounds and TestHelpOverlayFitsShortTerminal), so the
// same centering math reproduces the box's on-screen rectangle.
func (m *home) textOverlayContains(x, y int) bool {
	box := m.textOverlay.Render()
	w, h := lipgloss.Width(box), lipgloss.Height(box)
	left := max(0, (m.windowWidth-w)/2)
	top := max(0, (m.windowHeight-h)/2)
	return x >= left && x < left+w && y >= top && y < top+h
}
