package overlay

import (
	"fmt"
	"strings"

	"github.com/ZviBaratz/atrium/ui/theme"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/muesli/reflow/truncate"
)

// CustomCommandRow is one configured custom command as the menu shows it (#375).
// The overlay renders what it is handed and reports which row was chosen by index —
// the app owns the config, the gating and what running a row means (see
// app/custom_commands.go).
type CustomCommandRow struct {
	// Key is the single character that runs this row from inside the menu.
	Key string
	// Description is the user's own prose for the command. It is all the menu and
	// the ? screen have to identify it, which is why validation requires it.
	Description string
	// Repo marks a repo-context row: one that runs in the repository root rather
	// than the session's working directory. Shown, because it is the difference
	// between two directories and the user cannot otherwise tell which they get.
	Repo bool
	// Terminal marks a row that takes the whole terminal until it exits, rather than
	// running detached. Shown for the same reason `output` is a required config key
	// and has no default: the modes behave differently enough that finding out by
	// pressing is the surprise the requirement exists to prevent. Without it a
	// terminal lazygit and a background build are two identical rows, and picking
	// wrong either blacks out the app for minutes or silently detaches something the
	// user meant to sit and watch.
	Terminal bool
	// Inert is empty when the row can run against the current selection, and
	// otherwise says why it cannot. An inert row is dimmed and carries its reason —
	// never hidden, and never silently ignored.
	Inert string
}

// runnable reports whether the row can run right now.
func (r CustomCommandRow) runnable() bool { return r.Inert == "" }

// markers is the row's non-default properties, in the order they matter to someone about
// to press the key: what it will do to their screen first, then where it will run.
//
// Both are omitted when they are the default, so the common row stays uncluttered and the
// marked one is what draws the eye. TestCustomCommandRowMarkersFitTheNarrowBox pins the
// combined width, because two markers plus a reason is the widest tail a row can carry.
func (r CustomCommandRow) markers() string {
	var out []string
	if r.Terminal {
		out = append(out, customCmdTerminalMarker)
	}
	if r.Repo {
		out = append(out, customCmdRepoMarker)
	}
	return strings.Join(out, " ")
}

// CustomCommandsOverlay is the leader-key menu over the configured custom commands:
// `!` opens it, and each row's own key runs it.
//
// It is deliberately a KEYED menu and not the fuzzy picker the command palette uses.
// The two grammars are mutually exclusive: a type-to-filter list spends every
// printable rune on the query, and this menu spends them on the commands. Since the
// whole point is that the user chose the key, the key has to be what fires.
//
// The consequence is that j/k are NOT bound to movement here, unlike every other
// list in the app — they stay available as command keys. ↑/↓ and enter cover
// navigation for a user who does not remember the keys.
type CustomCommandsOverlay struct {
	rows   []CustomCommandRow
	cursor int
	scroll int
	width  int
	height int

	// notice holds a refusal for an inert row the user pressed anyway. It is shown
	// in the footer, INSIDE the box.
	//
	// That placement is load-bearing rather than cosmetic. From an overlay that
	// hides the hint bar, the app's notice path finds no bar to write to and falls
	// through to the error box, which recomputes the height budget — under a live,
	// already-sized overlay, and again when the toast expires. Answering here costs
	// no rows and no relayout.
	notice string
}

// NewCustomCommandsOverlay builds the menu over rows, in configured order.
func NewCustomCommandsOverlay(rows []CustomCommandRow) *CustomCommandsOverlay {
	return &CustomCommandsOverlay{rows: rows, width: 60, height: 20}
}

// SetSize sets the box's TOTAL dimensions, border and padding included — that is
// what lipgloss v2's Width and this box's height budget both mean, so a caller
// passing a fraction of the terminal is passing the room the menu may occupy. The
// floors keep the columns legible instead of collapsing into slivers.
func (o *CustomCommandsOverlay) SetSize(width, height int) {
	if width < customCmdMinWidth {
		width = customCmdMinWidth
	}
	if height < customCmdMinHeight {
		height = customCmdMinHeight
	}
	o.width = width
	o.height = height
}

// HandleKeyPress moves, runs or closes. It reports the index of the row to run
// (-1 for none) and whether the overlay is finished.
//
// An inert row answers in the box and does NOT close: the user pressed a key and is
// owed a reason, and closing first would leave that reason nowhere to go.
func (o *CustomCommandsOverlay) HandleKeyPress(msg tea.KeyPressMsg) (chosen int, shouldClose bool) {
	// Any key retires a refusal that was answering the previous one, so a stale
	// reason cannot sit under a row it no longer describes.
	o.notice = ""

	switch msg.String() {
	case "esc", "ctrl+c":
		return -1, true
	case "up":
		o.move(-1)
		return -1, false
	case "down":
		o.move(1)
		return -1, false
	case "enter":
		return o.choose(o.cursor)
	}

	// A single printable rune is a command key. Compared against msg.String()
	// rather than the rune itself so that whatever spelling bubbletea reports is
	// the spelling validation accepted — the space bar arrives as "space", which is
	// exactly why customcmd rejects a " " key.
	if s := msg.String(); len([]rune(s)) == 1 {
		for i, row := range o.rows {
			if row.Key == s {
				return o.choose(i)
			}
		}
	}
	return -1, false
}

// choose resolves a row the user asked for: runnable rows close the menu, inert
// ones keep it up and take its footer.
func (o *CustomCommandsOverlay) choose(i int) (chosen int, shouldClose bool) {
	if i < 0 || i >= len(o.rows) {
		return -1, false
	}
	row := o.rows[i]
	if !row.runnable() {
		o.cursor = i
		o.notice = row.Key + " — " + row.Inert
		return -1, false
	}
	return i, true
}

// move walks the cursor, clamped at both ends.
func (o *CustomCommandsOverlay) move(delta int) {
	o.cursor += delta
	if o.cursor < 0 {
		o.cursor = 0
	}
	if o.cursor >= len(o.rows) {
		o.cursor = len(o.rows) - 1
	}
	if o.cursor < 0 {
		o.cursor = 0
	}
}

// Render draws the box.
func (o *CustomCommandsOverlay) Render() string {
	th := theme.Current()
	box := lipgloss.NewStyle().
		Border(th.Borders.Style).
		BorderForeground(th.Palette.Accent).
		Padding(1, 2).
		// The border counts INSIDE Width in lipgloss v2, so this is the box's total
		// width on screen. See theme.Panel for the note on that silent change.
		Width(o.width)

	inner := o.width - 6 // border (2) + horizontal padding (2*2)
	if inner < customCmdMinInner {
		inner = customCmdMinInner
	}

	var b strings.Builder
	b.WriteString(fit(th.OverlayTitleStyle(), "Custom commands", inner) + "\n\n")

	if len(o.rows) == 0 {
		// Not an empty box: nothing else in the app says where custom commands come
		// from, so the one state that has room to explain must do it.
		b.WriteString(fit(overlayDimStyle(), customCmdEmptyHint, inner) + "\n\n")
		b.WriteString(fit(th.OverlayHintStyle(), customCmdEmptyFooterHint, inner))
		return box.Render(b.String())
	}

	// What is left for the list once the chrome is paid for. See customCmdChrome:
	// the height is the box's total, so lipgloss's own border and padding count.
	rows := o.height - customCmdChrome
	if rows < 1 {
		rows = 1
	}
	o.scroll = windowStart(len(o.rows), o.cursor, rows)
	end := o.scroll + rows
	if end > len(o.rows) {
		end = len(o.rows)
	}
	for i := o.scroll; i < end; i++ {
		b.WriteString(o.renderRow(o.rows[i], i == o.cursor, inner) + "\n")
	}
	if end < len(o.rows) {
		b.WriteString(fit(overlayDimStyle(), fmt.Sprintf("  … %d more", len(o.rows)-end), inner) + "\n")
	}
	b.WriteString("\n")

	// The notice takes the footer's row rather than adding one. A conditional row
	// would be a row the height budget cannot see: frameStates() only opens this
	// overlay, so no golden and no bounds sweep ever renders it with a refusal
	// showing, and the overflow would cost the bottom border invisibly.
	footer, footerStyle := customCmdFooterHint, th.OverlayHintStyle()
	if o.notice != "" {
		footer, footerStyle = o.notice, th.FaintStyle()
	}
	b.WriteString(fit(footerStyle, footer, inner))
	return box.Render(b.String())
}

// renderRow lays out one command: its key, its description, and a tail carrying its
// markers, the reason it cannot run, or both when they fit.
func (o *CustomCommandsOverlay) renderRow(row CustomCommandRow, selected bool, width int) string {
	th := theme.Current()

	// The reason outranks the markers, following the palette's rule that a row which
	// cannot run owes the user why rather than what it would have done. Both fit
	// often enough to be worth trying, because the markers are half of why the reason
	// differs from the row above it.
	markers := row.markers()
	tail := ""
	switch {
	case !row.runnable() && markers != "":
		tail = markers + " " + row.Inert
	case !row.runnable():
		tail = row.Inert
	default:
		tail = markers
	}

	prefix := "  " + padCells(row.Key, customCmdKeyCol) + "  "
	// The gap is charged only when there is something to separate, so a row with no
	// tail spends those columns on its description instead of on padding.
	gap := 0
	if tail != "" {
		gap = customCmdGap
	}
	descWidth := width - lipgloss.Width(prefix) - gap - lipgloss.Width(tail)
	if descWidth < customCmdMinDescWidth {
		// The description names the row; nothing else does. So the tail yields
		// first, and it yields whole rather than as an ellipsis and two letters.
		tail = ""
		gap = 0
		if !row.runnable() {
			tail, gap = clipLine(row.Inert, width/3), customCmdGap
		}
		descWidth = width - lipgloss.Width(prefix) - gap - lipgloss.Width(tail)
	}
	if descWidth < 1 {
		tail, gap, descWidth = "", 0, width-lipgloss.Width(prefix)
	}
	body := prefix + padCells(clipLine(row.Description, descWidth), descWidth)
	if tail != "" {
		body += strings.Repeat(" ", gap) + tail
	}
	// Padded to the full inner width, not merely clipped to it: the selected row is
	// rendered on a background, and a short body would leave the highlight ending
	// mid-row with plain cells beside it.
	body = padCells(clipLine(body, width), width)

	if selected {
		return overlaySelectedStyle().Render(body)
	}
	if !row.runnable() {
		return th.FaintStyle().Render(body)
	}
	return th.FgStyle().Render(body)
}

// fit renders s in style, clipped to width so no line can wrap. A wrap costs the
// box a row its height budget never counted, which PlaceOverlay then takes off the
// bottom border.
func fit(style lipgloss.Style, s string, width int) string {
	return style.Render(clipLine(s, width))
}

// padCells right-pads s to width DISPLAY CELLS.
//
// It replaces fmt's %-*s, which pads to a width counted in runes. Mixing the two is how
// a CJK description destroyed the `(repo)` marker: clipped to N cells it is ~N/2 runes,
// so %-*s added ~N/2 spaces too many, the row overshot the box, and the final clip ate
// the tail — leaving the marker as a lone ellipsis. Nothing caught it, because the
// pad-then-clip kept every line exactly the right width and the fixtures were ASCII.
func padCells(s string, width int) string {
	if pad := width - lipgloss.Width(s); pad > 0 {
		return s + strings.Repeat(" ", pad)
	}
	return s
}

// clipLine truncates s to width, and only when it is genuinely wider.
//
// The guard is the point: truncate.StringWithTail replaces a character at exactly
// the requested width, not only above it, so a line built to fit its budget loses
// its last cell to an ellipsis it did not need — which for a fixed marker like
// "(repo)" means every repo row reads "(repo…".
func clipLine(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= width {
		return s
	}
	return truncate.StringWithTail(s, uint(width), "…")
}

const (
	// customCmdFooterHint is the key grammar shown under the list. j/k are absent
	// on purpose — see the type comment.
	customCmdFooterHint = "↑↓ move · ↵ run · esc close"
	// customCmdEmptyFooterHint is the grammar of the empty state, where there is
	// nothing to move over and nothing to run.
	customCmdEmptyFooterHint = "esc close"
	// customCmdEmptyHint points at the one place custom commands come from. It names
	// the config key rather than a path because the data dir varies, and it is short
	// enough to survive the 80-column terminal's inner width whole.
	customCmdEmptyHint = `  none configured — see "custom_commands" in the README`

	// customCmdRepoMarker marks a row that runs in the repository root.
	customCmdRepoMarker = "(repo)"
	// customCmdTerminalMarker marks a row that takes over the terminal. Spelled as a
	// word rather than a glyph: the glyph tables are a guarded drift site of their own,
	// and this has to read unambiguously in a row the user is deciding about.
	customCmdTerminalMarker = "(terminal)"

	// customCmdChrome is every row of the box that is not a list row: the border
	// (2) and vertical padding (2) lipgloss draws around the content, the title and
	// its blank (2), the blank above the footer and the footer itself (2), and the
	// "… N more" line the list grows when it is windowed (1). Charged in full so the
	// box occupies AT MOST the height SetSize was given — at most, not exactly: the
	// "… N more" row is charged whether or not the list is windowed, so a short list
	// renders a shorter box. Under-counting is the failure that matters, since a box
	// taller than its budget loses its bottom border to the composer.
	//
	// A refusal is NOT charged here because it takes the footer's row rather than
	// adding one. That is the whole reason it is placed there.
	customCmdChrome = 4 + 2 + 2 + 1

	// customCmdMinHeight is the floor SetSize clamps to: the chrome plus one list
	// row. Below it the box would render taller than the height it was handed,
	// which is the invariant the composed frame depends on.
	customCmdMinHeight = customCmdChrome + 1
	// customCmdMinWidth floors the box where the columns stop being legible.
	customCmdMinWidth = 34
	// customCmdMinInner is minWidth less the frame, restated so the render path
	// cannot go negative if the floor is ever lowered.
	customCmdMinInner = customCmdMinWidth - 6

	// customCmdKeyCol is the key column's width in CELLS. Two, not one: validation
	// accepts any single printable rune, and a CJK or emoji key is one rune two cells
	// wide — so a one-cell column would let such a row push its description a cell
	// right of every other row's.
	customCmdKeyCol = 2
	// customCmdGap separates the description from its tail.
	customCmdGap = 2
	// customCmdMinDescWidth is the point below which the tail is dropped rather
	// than starving the description that names the row.
	customCmdMinDescWidth = 12
)
