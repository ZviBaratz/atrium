package overlay

import (
	"fmt"
	"strings"
	"time"

	"github.com/ZviBaratz/atrium/ui/theme"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/muesli/reflow/truncate"
)

// CheckpointRow is one displayable checkpoint. It is deliberately primitives
// only — the app maps session/transcript's Checkpoint onto this, so the overlay
// never learns a domain type (the QueueOverlay contract).
type CheckpointRow struct {
	// When the checkpoint was taken.
	When time.Time
	// Label is a one-line summary of the prompt it precedes ("" = unknown).
	Label string
	// Files is how many files the session had touched as of this checkpoint.
	Files int
	// Outside is how many of those sit outside the session's worktree.
	Outside int
}

// CheckpointOverlay lists a Claude session's native checkpoints, newest first.
// Two things can be done with a row. Attaching jumps into the session so Claude's
// own Esc-Esc can restore one — Atrium never rewinds a checkpoint itself, because
// Claude's rewind is the only surface that can take code and conversation back
// together (#385). Forking seeds a *new* Atrium session from the conversation as
// it stood before that checkpoint's prompt (#644), which leaves this session
// untouched and is therefore still not a restore.
//
// Like the QueueOverlay it is a dumb view: the app pushes a snapshot in via
// SetRows and reads intent back out (AttachRequested/ForkRequested/
// RefreshRequested). It holds no session type.
type CheckpointOverlay struct {
	title      string
	rows       []CheckpointRow
	cursor     int
	scroll     int
	loading    bool
	unavailure string // why there is nothing to list ("" = there is)
	note       string // standing caveat, e.g. the backups having been swept
	width      int
	height     int
	attachReq  bool
	forkReq    bool
	refreshReq bool
}

// CheckpointTimeFormat is deliberately absolute and now-independent: a
// "3h ago" column would re-render differently every run and could not be pinned
// by a frame golden.
//
// Exported because the fork form's heading stamps the chosen checkpoint with it
// (#644). That heading exists to be read against the row the cursor was on, so
// the two must be the same format, not two spellings that agree today.
const CheckpointTimeFormat = "Jan _2 15:04"

// checkpointTimeWidth is the rendered width of CheckpointTimeFormat ("Aug  5
// 10:00"), used to keep the label column aligned.
const checkpointTimeWidth = 12

// NewCheckpointOverlay builds the overlay for a session with the given display
// name, in its loading state — the enumeration is a file read the app performs
// off the UI thread, so there is never data at open time.
func NewCheckpointOverlay(name string) *CheckpointOverlay {
	return &CheckpointOverlay{title: name, width: 76, height: 24, loading: true}
}

// checkpointMinWidth is the narrowest box the overlay will accept. Named because
// the footer ladder's budget is derived from it (minus the 6 cells of border and
// padding), so the two cannot be reasoned about apart.
const checkpointMinWidth = 40

// SetSize sets the box dimensions; the list windows to the available height.
//
// The height floor is checkpointChrome + 2: the box's own furniture costs
// checkpointChrome lines, so anything shorter could not hold even a single row
// without overflowing the height it was handed.
func (c *CheckpointOverlay) SetSize(width, height int) {
	if width < checkpointMinWidth {
		width = checkpointMinWidth
	}
	if height < checkpointChrome+2 {
		height = checkpointChrome + 2
	}
	c.width = width
	c.height = height
}

// SetWidth mirrors the other overlays' responsive-width setter.
func (c *CheckpointOverlay) SetWidth(width int) { c.SetSize(width, c.height) }

// SetRows replaces the list (newest first), leaves the loading state, and clamps
// the cursor so a reload that returns fewer rows cannot strand it past the end.
func (c *CheckpointOverlay) SetRows(rows []CheckpointRow) {
	c.rows = rows
	c.loading = false
	c.unavailure = ""
	if c.cursor >= len(c.rows) {
		c.cursor = len(c.rows) - 1
	}
	if c.cursor < 0 {
		c.cursor = 0
	}
}

// SetLoading puts the overlay back into its loading state, for a reload.
func (c *CheckpointOverlay) SetLoading() {
	c.loading = true
}

// SetUnavailable explains why there is nothing to list — no checkpoints yet, no
// transcript, an older Claude that does not record them. It is a statement, not
// an error: the empty timeline is a legitimate reading.
//
// It drops the note as well as the rows, because the note describes the rows: a
// reload that fails after a successful read would otherwise leave "claude has
// already swept this session's file backups" standing under "no transcript for
// this session yet", a caveat about data the box has just said is not there.
func (c *CheckpointOverlay) SetUnavailable(reason string) {
	c.loading = false
	c.rows = nil
	c.note = ""
	c.unavailure = reason
}

// SetNote sets a standing caveat shown above the footer (cleared by passing "").
// It survives SetRows, because what it reports — Claude having swept this
// session's file backups — is a property of the data rather than of an action, and
// every successful read sets it afresh.
func (c *CheckpointOverlay) SetNote(text string) { c.note = text }

// HandleKeyPress moves the cursor, arms an attach or a reload, or closes. It
// returns true only when the overlay should close (esc/ctrl+c); an attach arms
// the flag and lets the app decide, since attaching suspends the event loop.
func (c *CheckpointOverlay) HandleKeyPress(msg tea.KeyPressMsg) (shouldClose bool) {
	switch msg.String() {
	case "esc", "ctrl+c":
		return true
	case "up", "k":
		if c.cursor > 0 {
			c.cursor--
		}
		return false
	case "down", "j":
		if c.cursor < len(c.rows)-1 {
			c.cursor++
		}
		return false
	case "enter":
		// Not while the read is still running. A whole-transcript scan is not
		// instantaneous, so enter pressed in the gap would hand the terminal to tmux
		// for a timeline the user opened to look at and never got to see. Once the
		// box has an answer — rows, or a statement of why there are none — the user
		// has read it and the press is deliberate.
		if !c.loading {
			c.attachReq = true
		}
		return false
	case "f":
		// Gated on the same condition as enter, and for a weaker version of the same
		// reason: the cursor is meaningless until the rows exist, so a press in the
		// gap would fork from whatever row happened to land under index 0. Whether
		// the selected checkpoint can be forked at all is the app's to answer — it
		// holds the chain entry the cut needs, and says so in a notice, exactly as it
		// does for an attach it has to refuse.
		if !c.loading {
			c.forkReq = true
		}
		return false
	case "r":
		c.refreshReq = true
		return false
	default:
		return false
	}
}

// AttachRequested reports whether an attach was armed since the last call and
// clears the flag (read-once), so the app acts on each press exactly once.
func (c *CheckpointOverlay) AttachRequested() bool {
	r := c.attachReq
	c.attachReq = false
	return r
}

// ForkRequested reports whether a fork was armed since the last call and clears
// the flag (read-once), so the app acts on each press exactly once. Read against
// SelectedIndex: the app holds the checkpoint table in the order it pushed rows.
func (c *CheckpointOverlay) ForkRequested() bool {
	r := c.forkReq
	c.forkReq = false
	return r
}

// RefreshRequested reports whether a reload was armed since the last call and
// clears the flag (read-once).
func (c *CheckpointOverlay) RefreshRequested() bool {
	r := c.refreshReq
	c.refreshReq = false
	return r
}

// SelectedIndex is the 0-based cursor position in the row order the app pushed in.
//
// No action consumes it yet — the timeline's one action attaches to the session,
// which the cursor does not affect — so today it exists to make the cursor's own
// behaviour assertable. A per-checkpoint action would read it against a row table
// held in the same order the app pushed.
func (c *CheckpointOverlay) SelectedIndex() int { return c.cursor }

// Render draws the bordered list.
func (c *CheckpointOverlay) Render() string {
	th := theme.Current()
	box := lipgloss.NewStyle().
		Border(th.Borders.Style).
		BorderForeground(th.Palette.Accent).
		Padding(1, 2).
		Width(c.width)

	inner := c.width - 6 // border (2) + horizontal padding (2*2)
	if inner < 20 {
		inner = 20
	}

	lay := c.layout()

	var b strings.Builder
	// Truncated like every other line: a session's display name is user-authored
	// and has no length ceiling, so a wrapped title would eat a list row.
	b.WriteString(th.OverlayTitleStyle().Render(truncate.StringWithTail(
		`Checkpoints for "`+c.title+`"`, uint(inner), "…")) + "\n\n")

	switch {
	case c.loading:
		b.WriteString(overlayDimStyle().Render("reading transcript…") + "\n")
		// Padded to exactly the height the rows it is replacing occupy, so a reload
		// holds the box still: PlaceOverlay re-centres on every height change, so any
		// difference makes `r` jump the whole overlay twice per press. Same reason the
		// hidden-rows line is drawn unconditionally below; this is that accounting
		// applied to the other branch.
		//
		// The window height is the wrong number to pad to, and wrong in the direction
		// that bites hardest: a session with three checkpoints in a forty-line terminal
		// draws three rows, so padding to the window's thirty grows the box on reload
		// instead of holding it — a bigger jump than the one this replaced, and the
		// common case, since most sessions have far fewer checkpoints than rows. On
		// first open there are no rows yet and nothing to match; the box is small and
		// grows once, which no padding here can avoid.
		for i := 1; i < min(len(c.rows), lay.visible); i++ {
			b.WriteString("\n")
		}
		if len(c.rows) > lay.visible {
			b.WriteString("\n")
		}
		b.WriteString("\n")
	case c.unavailure != "":
		b.WriteString(overlayDimStyle().Render(truncate.StringWithTail(c.unavailure, uint(inner), "…")) + "\n\n")
	default:
		c.clampScroll(len(c.rows), lay.visible)
		end := c.scroll + lay.visible
		if end > len(c.rows) {
			end = len(c.rows)
		}
		for idx := c.scroll; idx < end; idx++ {
			row := c.renderRow(c.rows[idx], inner-2) // -2 for the "▸ " cursor column
			if idx == c.cursor {
				b.WriteString(overlaySelectedStyle().Render("▸ " + row))
			} else {
				b.WriteString("  " + row)
			}
			b.WriteString("\n")
		}
		// Drawn whenever the list overflows at all, not only when rows sit below the
		// window. Two reasons, and the second is the one that bites: a list scrolled
		// to its end would otherwise claim to be complete, and — since layout() has
		// already charged this line — the box would render a row shorter down there,
		// which PlaceOverlay re-centres, so the whole overlay jumps as the cursor
		// crosses into the last window.
		if len(c.rows) > lay.visible {
			b.WriteString(overlayDimStyle().Render(
				"  "+hiddenRowSummary(c.scroll, len(c.rows)-end)) + "\n")
		}
		b.WriteString("\n")
	}

	if lay.showNote {
		b.WriteString(overlayDimStyle().Render(truncate.StringWithTail(c.note, uint(inner), "…")) + "\n\n")
	}
	// Truncated, never wrapped: a wrapped footer would silently claim a row the
	// height budget already spent.
	b.WriteString(th.OverlayHintStyle().Render(checkpointFooter(inner)))
	return box.Render(b.String())
}

// checkpointFooterHints is the footer's ladder, widest first (80, 73, 62, 59, 48,
// 33 cells). One fixed string truncated would not do: on an 80-column terminal —
// the 80×24 floor the frame goldens capture — the app sizes the box to 0.7×80 = 56,
// so inner is 50 and truncating the full line dropped everything from `(then Esc
// Esc` on, including `esc close`, the only key out of a state that hides the hint
// bar.
//
// It sheds in order of what a user can find without being told, and the order is
// the whole point rather than a detail: `j/k move` first, because arrows work too
// and the cursor is visible; then `r reload`, because esc and a fresh `H` reach the
// same place; then the Esc-Esc reminder, because it is the one thing on this
// surface nothing else teaches — the timeline is read-only, so an attach the user
// does not know to follow with Esc Esc leads nowhere. `enter attach`, `f fork` and
// `esc close` never go: the first two are the surface's only actions, and neither
// key is in keys.Registry, so this footer is the *only* place either is written
// down — no `?` cheatsheet row, no palette entry, no README table.
//
// Wording compresses before a clause is shed, but only while the reminder stays a
// parenthetical on `enter attach` — that is what says *once you are in there, press
// Esc Esc*. Freestanding, `· Esc Esc rewinds ·` reads as a key Atrium itself
// honours, which is the one idea this whole surface exists to correct.
//
// The 48-cell rung is the load-bearing one, because 50 is exactly what the default
// terminal gives. It sheds `r reload` there, which is not free — re-opening pays
// another whole-transcript scan — and is a change from before #644 added `f fork`:
// the tightest line keeping the reminder, the fork key and `r reload` together is
// 54 cells, four over budget, so one of the three had to go at the default size.
// `r reload` is the one with another route to the same place.
//
// Which rung a width lands on is therefore load-bearing, not cosmetic: a rung that
// restores a clause a wider rung had already shed reads as a ladder while
// behaving like a shuffle. TestCheckpointFooter_ShedsMonotonically is that
// invariant; the 80×24 golden is where it shows.
var checkpointFooterHints = []string{
	"j/k move · enter attach (then Esc Esc to rewind) · f fork · r reload · esc close",
	"j/k move · enter attach (Esc Esc rewinds) · f fork · r reload · esc close",
	"enter attach (Esc Esc rewinds) · f fork · r reload · esc close",
	"enter attach (then Esc Esc) · f fork · r reload · esc close",
	"enter attach (then Esc Esc) · f fork · esc close",
	"enter attach · f fork · esc close",
}

// checkpointFooter is the widest rung of checkpointFooterHints that fits in inner
// cells.
func checkpointFooter(inner int) string {
	hints := fitHint(inner, "", checkpointFooterHints...)
	// fitHint hands back the narrowest rung when none fits, which is not a promise
	// that one does. SetSize floors the width at checkpointMinWidth, so inner is at
	// least 34 against a 24-cell narrowest rung — but a floor is not a proof, the same
	// reason layout() refuses to trust it. The width test is not redundant either:
	// StringWithTail replaces a character at *exactly* its budget, so calling it
	// unconditionally would corrupt the rung fitHint chose precisely because it fits.
	if lipgloss.Width(hints) > inner {
		return truncate.StringWithTail(hints, uint(inner), "…")
	}
	return hints
}

// hiddenRowSummary names what the window is not showing, in one line, whichever
// side it is on.
func hiddenRowSummary(above, below int) string {
	switch {
	case above > 0 && below > 0:
		return fmt.Sprintf("… %d above, %d below", above, below)
	case above > 0:
		return fmt.Sprintf("… %d above", above)
	case below > 0:
		return fmt.Sprintf("… %d more", below)
	default:
		// Reached only if the list overflows the window yet nothing is outside it,
		// which the caller's own condition rules out. Kept honest rather than
		// silently drawing an empty line.
		return "…"
	}
}

// renderRow lays out one checkpoint in exactly width cells: a fixed-width time
// column, the prompt label, and the coverage summary flush right.
//
// The summary is right-aligned rather than trailing the label because the whole
// use of the list is comparing rows — where the work piled up, which checkpoints
// reach outside the worktree — and a ragged column defeats that. Widths are
// measured in cells, not bytes: a prompt label is arbitrary user text and may hold
// anything.
func (c *CheckpointOverlay) renderRow(row CheckpointRow, width int) string {
	stamp := "unknown"
	if !row.When.IsZero() {
		stamp = row.When.Format(CheckpointTimeFormat)
	}
	summary := checkpointFileSummary(row)
	label := row.Label
	if label == "" {
		label = "(no prompt text)"
	}

	// One space after the time column, at least two before the summary.
	const gap = 2
	labelBudget := width - checkpointTimeWidth - 1 - gap - lipgloss.Width(summary)
	if labelBudget < 1 {
		// Too narrow for both: the summary is the part a user cannot reconstruct
		// from anywhere else on screen, so the label yields.
		return fmt.Sprintf("%-*s %s", checkpointTimeWidth, stamp,
			truncate.StringWithTail(summary, uint(max(1, width-checkpointTimeWidth-1)), "…"))
	}
	if lipgloss.Width(label) > labelBudget {
		label = truncate.StringWithTail(label, uint(labelBudget), "…")
	}

	pad := width - checkpointTimeWidth - 1 - lipgloss.Width(label) - lipgloss.Width(summary)
	if pad < gap {
		pad = gap
	}
	return fmt.Sprintf("%-*s %s%s%s", checkpointTimeWidth, stamp,
		label, strings.Repeat(" ", pad), summary)
}

// checkpointFileSummary states what the checkpoint covers, including how much of
// it lies outside the worktree — the honest part, since Claude tracks every file
// a session touches wherever it lives, not just the ones under the session tree.
func checkpointFileSummary(row CheckpointRow) string {
	var s string
	switch row.Files {
	case 0:
		return "no files"
	case 1:
		s = "1 file"
	default:
		s = fmt.Sprintf("%d files", row.Files)
	}
	if row.Outside > 0 {
		s += fmt.Sprintf(", %d outside", row.Outside)
	}
	return s
}

// checkpointChrome is the number of lines the box costs before any list row:
// border (2) + vertical padding (2) + title (1) + blank after the title (1) +
// blank before the footer (1) + footer (1). SetSize's height floor is derived
// from it, and layout charges every optional line on top.
const checkpointChrome = 8

// checkpointLayout is one render's line budget, decided in a single place so the
// accounting and the drawing cannot disagree — the way a box silently overflows.
type checkpointLayout struct {
	visible  int
	showNote bool
}

// layout allocates the available height. Rows come first: in a box too short for
// the list plus the note, the note gives way, because it is a caveat about the rows
// and a box with no rows in it has nothing to caveat.
func (c *CheckpointOverlay) layout() checkpointLayout {
	lay := checkpointLayout{showNote: c.note != ""}
	for {
		extra := 0
		if lay.showNote {
			extra += 2 // the note and its trailing blank
		}
		budget := c.height - checkpointChrome - extra
		if len(c.rows) > budget {
			budget-- // the hidden-rows line
		}
		if budget >= 1 {
			lay.visible = budget
			return lay
		}
		if lay.showNote {
			lay.showNote = false
			continue
		}
		// Unreachable while SetSize floors the height at checkpointChrome+2, but a
		// floor is not a proof — never return a budget below one row.
		lay.visible = 1
		return lay
	}
}

// clampScroll keeps the cursor inside the visible window, and the window over the
// rows.
//
// Both bounds matter, and the upper one is the easy one to leave out: it is what
// pulls the window back when the list shrinks under it (a reload returning fewer
// rows) or the window grows past it (a resize). Without it the box renders a
// handful of rows in a space with room for many, and — since the hidden-rows line
// is only drawn while the list overflows — says nothing about the ones it left
// stranded above.
func (c *CheckpointOverlay) clampScroll(n, visible int) {
	if c.cursor < c.scroll {
		c.scroll = c.cursor
	}
	if c.cursor >= c.scroll+visible {
		c.scroll = c.cursor - visible + 1
	}
	if c.scroll > n-visible {
		c.scroll = n - visible
	}
	if c.scroll < 0 {
		c.scroll = 0
	}
}
