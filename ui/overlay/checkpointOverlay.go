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

// CheckpointOverlay lists a Claude session's native checkpoints, newest first,
// and lets the user jump into the session to act on one. It is read-only by
// design: Atrium enumerates checkpoints, and Claude's own Esc-Esc restores them —
// the only surface that can rewind code and conversation together (#385). Hence
// the single action, attach.
//
// Like the QueueOverlay it is a dumb view: the app pushes a snapshot in via
// SetRows and reads intent back out (AttachRequested/RefreshRequested/
// SelectedIndex). It holds no session type.
type CheckpointOverlay struct {
	title      string
	rows       []CheckpointRow
	cursor     int
	scroll     int
	loading    bool
	unavailure string // why there is nothing to list ("" = there is)
	note       string // standing caveat, e.g. the backups having been swept
	message    string // transient note; cleared by SetRows
	width      int
	height     int
	attachReq  bool
	refreshReq bool
}

// checkpointTimeFormat is deliberately absolute and now-independent: a
// "3h ago" column would re-render differently every run and could not be pinned
// by a frame golden.
const checkpointTimeFormat = "Jan _2 15:04"

// checkpointTimeWidth is the rendered width of checkpointTimeFormat ("Aug  5
// 10:00"), used to keep the label column aligned.
const checkpointTimeWidth = 12

// NewCheckpointOverlay builds the overlay for a session with the given display
// name, in its loading state — the enumeration is a file read the app performs
// off the UI thread, so there is never data at open time.
func NewCheckpointOverlay(name string) *CheckpointOverlay {
	return &CheckpointOverlay{title: name, width: 76, height: 24, loading: true}
}

// SetSize sets the box dimensions; the list windows to the available height.
//
// The height floor is checkpointChrome + 2: the box's own furniture costs
// checkpointChrome lines, so anything shorter could not hold even a single row
// without overflowing the height it was handed.
func (c *CheckpointOverlay) SetSize(width, height int) {
	if width < 40 {
		width = 40
	}
	if height < checkpointChrome+2 {
		height = checkpointChrome + 2
	}
	c.width = width
	c.height = height
}

// SetWidth mirrors the other overlays' responsive-width setter.
func (c *CheckpointOverlay) SetWidth(width int) { c.SetSize(width, c.height) }

// SetRows replaces the list (newest first), leaves the loading state, clamps the
// cursor, and clears the transient message so a reload starts clean.
func (c *CheckpointOverlay) SetRows(rows []CheckpointRow) {
	c.rows = rows
	c.loading = false
	c.unavailure = ""
	c.message = ""
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
	c.message = ""
}

// SetUnavailable explains why there is nothing to list — no checkpoints yet, no
// transcript, an older Claude that does not record them. It is a statement, not
// an error: the empty timeline is a legitimate reading.
func (c *CheckpointOverlay) SetUnavailable(reason string) {
	c.loading = false
	c.rows = nil
	c.unavailure = reason
}

// SetNote sets a standing caveat shown above the footer (cleared by passing "").
// Unlike SetMessage it survives SetRows, because what it reports — Claude having
// swept this session's file backups — is a property of the data, not of an action.
func (c *CheckpointOverlay) SetNote(text string) { c.note = text }

// SetMessage sets a transient note rendered above the footer, cleared by the next
// SetRows. It is written inside the box rather than to the app's error line
// because this state hides the hint bar: surfacing a notice out there would
// recompute the height budget under a live overlay.
func (c *CheckpointOverlay) SetMessage(text string) { c.message = text }

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
		c.attachReq = true
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

// RefreshRequested reports whether a reload was armed since the last call and
// clears the flag (read-once).
func (c *CheckpointOverlay) RefreshRequested() bool {
	r := c.refreshReq
	c.refreshReq = false
	return r
}

// SelectedIndex is the 0-based cursor position in the row order the app pushed
// in, so the app's parallel row table indexes identically.
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
		b.WriteString(overlayDimStyle().Render("reading transcript…") + "\n\n")
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
		if hidden := len(c.rows) - end; hidden > 0 {
			b.WriteString(overlayDimStyle().Render(fmt.Sprintf("  … %d more", hidden)) + "\n")
		}
		b.WriteString("\n")
	}

	if lay.showNote {
		b.WriteString(overlayDimStyle().Render(truncate.StringWithTail(c.note, uint(inner), "…")) + "\n\n")
	}
	if lay.showMessage {
		b.WriteString(th.AttentionStyle().Render(truncate.StringWithTail(c.message, uint(inner), "…")) + "\n\n")
	}
	// Truncated, never wrapped: a wrapped footer would silently claim a row the
	// height budget already spent.
	b.WriteString(th.OverlayHintStyle().Render(truncate.StringWithTail(
		"j/k move · enter attach (then Esc Esc to rewind) · r reload · esc close", uint(inner), "…")))
	return box.Render(b.String())
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
		stamp = row.When.Format(checkpointTimeFormat)
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
	visible     int
	showNote    bool
	showMessage bool
}

// layout allocates the available height. Rows come first: in a box too short for
// the row list plus both optional lines, the standing note is dropped before the
// transient message, because the message is the response to what the user just
// did and the note is a caveat that returns as soon as the message clears.
func (c *CheckpointOverlay) layout() checkpointLayout {
	lay := checkpointLayout{showNote: c.note != "", showMessage: c.message != ""}
	for {
		extra := 0
		if lay.showNote {
			extra += 2 // the note and its trailing blank
		}
		if lay.showMessage {
			extra += 2
		}
		budget := c.height - checkpointChrome - extra
		if len(c.rows) > budget {
			budget-- // the "… N more" row
		}
		if budget >= 1 {
			lay.visible = budget
			return lay
		}
		switch {
		case lay.showNote:
			lay.showNote = false
		case lay.showMessage:
			lay.showMessage = false
		default:
			// Unreachable while SetSize floors the height at checkpointChrome+2,
			// but a floor is not a proof — never return a budget below one row.
			lay.visible = 1
			return lay
		}
	}
}

// clampScroll keeps the cursor inside the visible window.
func (c *CheckpointOverlay) clampScroll(n, visible int) {
	if c.cursor < c.scroll {
		c.scroll = c.cursor
	}
	if c.cursor >= c.scroll+visible {
		c.scroll = c.cursor - visible + 1
	}
	if c.scroll < 0 || c.scroll > n-1 {
		c.scroll = 0
	}
}
