package app

import (
	"strings"

	"github.com/ZviBaratz/atrium/customcmd"
	"github.com/ZviBaratz/atrium/keys"
	"github.com/ZviBaratz/atrium/ui"
	"github.com/ZviBaratz/atrium/ui/overlay"
	"github.com/ZviBaratz/atrium/ui/theme"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
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
type helpTypeGeneral struct {
	// commands are the validated custom_commands entries, listed in their own
	// section so the keys behind `!` are documented where every other key is
	// (#375). Empty omits the section entirely.
	//
	// It is populated only at the open site in dispatchAction. Every other
	// construction of this type is the bare literal helpTypeGeneral{}, which leaves
	// this nil — so any test built that way renders no custom rows and can say
	// nothing about them. TestHelpCustomSectionTruncatesLongDescriptions is the
	// guard that actually populates it.
	commands []customcmd.Command
}

// helpTypeWelcome is the one-time welcome shown on first launch ever.
type helpTypeWelcome struct{}

// Help styles read the active theme at render time.
func helpTitleStyle() lipgloss.Style  { return theme.Current().PurpleStyle().Bold(true).Underline(true) }
func helpHeaderStyle() lipgloss.Style { return theme.Current().CyanStyle().Bold(true) }
func helpKeyStyle() lipgloss.Style    { return theme.Current().AttentionStyle().Bold(true) }
func helpDescStyle() lipgloss.Style   { return theme.Current().FgStyle() }
func helpDimStyle() lipgloss.Style    { return theme.Current().DimStyle() }

// helpCustomRowWidth is what one custom row may occupy in the cheatsheet, markers included.
//
// Derived from the narrowest terminal the app supports: helpRow's key column is 12 cells and
// the help overlay's box costs 4 more for its border and padding plus a column of margin
// each side, which leaves 12 + 62 = 74 against 80.
const helpCustomRowWidth = 62

// helpCustomDescWidth is the NARROWEST description budget a row can get today: the
// both-markers case, helpCustomRowWidth less " (terminal) (repo)".
//
// It is a consequence of customDescWidth rather than an input to it. Markers are spent out
// of the row's budget instead of being added on top of it, so a row carrying one marker
// gets more than this and an unmarked row gets the whole helpCustomRowWidth.
// TestHelpCustomSectionTruncatesLongDescriptions pins it rather than trusting it.
const helpCustomDescWidth = helpCustomRowWidth - len(" (terminal) (repo)")

// helpCustomHeading titles the custom-commands section. It names the leader as well
// as the section, because the keys it lists do nothing on their own — and it reads
// the leader from the registry, so a rebind renames the heading with it.
func helpCustomHeading() string {
	return "Custom (" + keys.LabelOf(keys.KeyCustomCommands) + " opens the menu)"
}

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
// likewise a projection of the theme's two glyph tables — the Glyphs struct and
// the agent identity table (see legendGroups).
func (h helpTypeGeneral) toContent() string {
	lines := []string{helpTitleStyle().Render("Atrium — Keys")}
	for _, group := range keys.HelpGroups() {
		lines = append(lines, "", helpHeaderStyle().Render(group.Title))
		for _, row := range group.Rows {
			lines = append(lines, helpRow(rowKeyLabel(row), rowDesc(row)))
		}
	}
	lines = append(lines, h.customLines()...)
	lines = append(lines, "", helpHeaderStyle().Render("Mouse"))
	for _, row := range mouseHelpRows {
		lines = append(lines, helpRow(row[0], row[1]))
	}
	lines = append(lines, helpDimStyle().Render(
		"Shift+drag selects text for your terminal's own copy, bypassing capture; "+
			"turn the mouse off entirely in settings ("+keys.LabelOf(keys.KeySettings)+")."))
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

// legendGroups projects the theme's glyph tables into the '?' legend, grouped
// status / git / badges / agents. Every entry reads its glyph from the live theme, so
// the legend can never drift from what a row actually paints, and it re-renders
// under whichever fidelity rung is active. Completeness — every row-vocabulary
// glyph is present — is pinned by TestLegendCoversRowVocabulary, over both tables.
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
			{glyph: g.Muted, style: t.DimStyle(), label: "muted"},
			{glyph: g.Warn, style: t.AttentionStyle(), label: "stale"},
			// The context meter, shown by its full rung. One sample rather than the
			// whole eight-rung ladder: the legend answers "what is this mark on my
			// row", and the ramp is self-explaining once you know it is a meter —
			// eight glyphs here would cost 16 cells and push this group onto a third
			// line. It renders only in the opt-in `bar` mode, but the legend is a
			// projection of the glyph table, not of the active config.
			{glyph: contextRampSample(g), style: t.DimStyle(), label: "context"},
			{glyph: g.AutoBadge, rendered: t.BadgeStyle().Render(" " + g.AutoBadge + "AUTO "), label: "auto-accepting"},
		}},
		{"agents", agentLegendEntries(t)},
	}
}

// agentLegendEntries decodes the row's far-right column: which CLI a session runs.
// That column is pinned there (ui/row.go's agentSeg) precisely so the question is
// answerable at a glance, and until #673 it was the one glyph on the row the legend
// said nothing about.
//
// Keys and glyphs both come from the theme's agent table, which is what makes this a
// projection rather than a second copy: the label IS the key the table is keyed by
// (session/agent's canonical Key, the same string the row resolves a program to), so
// the legend cannot name an agent the table does not have or spell one differently.
// Each entry carries the agent's own accent, so brand colour and glyph agree here the
// way they do on the row.
func agentLegendEntries(t *theme.Theme) []legendEntry {
	keys := t.AgentKeys()
	entries := make([]legendEntry, 0, len(keys))
	for _, key := range keys {
		glyph, c := t.AgentGlyph(key)
		entries = append(entries, legendEntry{
			glyph: glyph,
			style: lipgloss.NewStyle().Foreground(c),
			label: key,
		})
	}
	return entries
}

// contextRampSample returns the ramp rung the legend stands the meter in for —
// the full one, which is both the most recognizable and the one whose meaning
// ("this session is nearly out of context") most needs a legend entry. Guarded
// against an empty table so the legend degrades to a blank cell rather than
// panicking; the table's length is pinned in ui/theme's own tests.
func contextRampSample(g theme.Glyphs) string {
	if len(g.ContextRamp) == 0 {
		return " "
	}
	return g.ContextRamp[len(g.ContextRamp)-1]
}

// legendTitleCol is the width of the group-title column: two leading spaces plus
// a title padded to eight. Continuation lines indent by the same amount, so a
// wrapped group's entries stay in one column.
const legendTitleCol = 10

// legendMaxWidth is the widest a legend line may be: the ? overlay's inner width
// at an 80-column terminal. TextOverlay.boxWidth() caps at width-4 (border and
// margin) and wrappedLines subtracts Padding(1,2)'s four more columns.
//
// The overlay does wrap a longer line, but it wraps to the box, not to the
// legend's grammar — it breaks mid-label, so "AUTO auto-accepting" lands as
// "AUTO auto-" over "accepting" with no indent. Packing here instead keeps a
// group's entries whole and hanging-indented, and means the next glyph added to
// a full group reflows rather than shipping a broken line. TestLegendLinesFit
// holds the bound.
//
// It is a fixed 80-column figure, not the live box width, and that is a real
// limit rather than an oversight to read past: below 80 columns the overlay's
// own mid-label wrap takes over again. Fixing that properly means threading the
// terminal width into helpText.toContent(), which is built before the overlay is
// sized — a change to that interface, not to this constant. Everything else in
// the help (the mouse rows, the Shift+drag paragraph) already reflows there, so
// the legend is no worse than its neighbours; 80 is where it is guaranteed.
const legendMaxWidth = 72

// legendLines renders the legend groups: a padded group title followed by
// "<glyph> <label>" entries, packed into lines no wider than legendMaxWidth. A
// group whose entries do not fit continues on further lines under a blank title.
func legendLines() []string {
	var lines []string
	for _, grp := range legendGroups() {
		title := grp.title
		for len(title) < legendTitleCol-2 {
			title += " "
		}
		var b strings.Builder
		b.WriteString(helpDimStyle().Render("  " + title))
		width := legendTitleCol
		for i, e := range grp.entries {
			glyph := e.rendered
			if glyph == "" {
				glyph = e.style.Render(e.glyph)
			}
			entry := glyph + " " + helpDimStyle().Render(e.label)
			entryW := ansi.StringWidth(entry)
			sepW := 2
			if i == 0 {
				sepW = 0
			}
			if i > 0 && width+sepW+entryW > legendMaxWidth {
				lines = append(lines, b.String())
				b.Reset()
				b.WriteString(strings.Repeat(" ", legendTitleCol))
				width, sepW = legendTitleCol, 0
			}
			b.WriteString(strings.Repeat(" ", sepW) + entry)
			width += sepW + entryW
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
	// Unbound actions contribute nothing rather than an empty label: joining an
	// empty string would leave a dangling separator ("↑/k / ") in the key column,
	// which reads as a key the row forgot to name.
	labels := make([]string, 0, len(row.Keys))
	for _, k := range row.Keys {
		if label := keys.GlobalKeyBindings[k].Help().Key; label != "" {
			labels = append(labels, label)
		}
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

// customLines renders the user's own verbs as a cheatsheet section, or nothing when
// none are configured.
//
// The description is truncated rather than left to wrap. helpRow does no bounding of
// its own, and the help overlay hard-wraps its content — so an over-long line does
// not overflow the frame, but it does spill one description across several rows and
// push the rest of the cheatsheet off the end of a short terminal. The bound is
// asserted, not assumed: see customDescWidth.
func (h helpTypeGeneral) customLines() []string {
	if len(h.commands) == 0 {
		return nil
	}
	lines := []string{"", helpHeaderStyle().Render(helpCustomHeading())}
	for _, c := range h.commands {
		// Same markers the `!` menu shows, in the same order, because this screen is where a
		// user goes to find out what their keys do — and which of them will take the
		// terminal is exactly the thing `output` is required in order not to surprise them
		// with.
		markers := ""
		if c.Output == customcmd.OutputTerminal {
			markers += " (terminal)"
		}
		if c.Context == customcmd.ContextRepo {
			markers += " (repo)"
		}
		// By display width, not rune count: a CJK description is two cells per rune, so a
		// rune bound would let it render at twice the width it was checked at. Charged
		// PER ROW rather than against the worst case, so the common unmarked row keeps the
		// columns a marked one needs instead of paying for markers it does not carry.
		desc := runewidth.Truncate(c.Description, customDescWidth(markers), "…")
		lines = append(lines, helpRow(keys.LabelOf(keys.KeyCustomCommands)+" "+c.Key, desc+markers))
	}
	return lines
}

// customDescWidth is the description budget for a row carrying markers: whatever the row's
// width leaves once they are subtracted.
//
// Plain subtraction, with NO floor, and that is a decision. A floor can only bind by
// returning more columns than the markers left — which overflows the row rather than
// protecting it, so a third marker added later shrinks the description here and the row
// still fits. At or below zero runewidth.Truncate collapses the description to "…", so the
// line stays bounded by the markers themselves; a marker set wide enough to reach that is a
// marker problem, and no budget arithmetic here can answer it.
func customDescWidth(markers string) int {
	return helpCustomRowWidth - runewidth.StringWidth(markers)
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
func (m *home) handleHelpState(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
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
		tea.RequestWindowSize,
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
