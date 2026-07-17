package overlay

import (
	"fmt"
	"strings"
	"time"

	"github.com/ZviBaratz/atrium/cmdlog"
	"github.com/ZviBaratz/atrium/ui/theme"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/reflow/truncate"
)

// CmdLogMode selects which slice of the command log the overlay shows.
type CmdLogMode int

const (
	// CmdLogAll shows every recorded subprocess, newest first.
	CmdLogAll CmdLogMode = iota
	// CmdLogFailures shows only non-zero exits.
	CmdLogFailures
	// CmdLogSession shows only the currently-selected session's commands.
	CmdLogSession
)

// CmdLogOverlay renders the command log (#372): the tmux/git/gh subprocesses
// Atrium has run, filterable by mode, with a failure row expandable to its full
// stderr. It holds no records of its own — every Render reads live from the
// package ring — so it can never show a stale log. `session` is the selected
// session's name, supplied by the app for the CmdLogSession filter.
type CmdLogOverlay struct {
	mode     CmdLogMode
	session  string
	cursor   int
	expanded bool
	scroll   int
	width    int
	height   int
}

// NewCmdLogOverlay builds the overlay in the all-commands view. session is the
// selected session's name used by the per-session filter ("" disables it).
func NewCmdLogOverlay(session string) *CmdLogOverlay {
	return &CmdLogOverlay{session: session, width: 90, height: 24}
}

// SetSize sets the box dimensions; the list windows to the available height.
func (c *CmdLogOverlay) SetSize(width, height int) {
	if width < 40 {
		width = 40
	}
	if height < 8 {
		height = 8
	}
	c.width = width
	c.height = height
}

// SetWidth mirrors the other overlays' responsive-width setter.
func (c *CmdLogOverlay) SetWidth(width int) { c.SetSize(width, c.height) }

// records returns the currently-filtered command records, newest first. Read live
// so the view is always current.
func (c *CmdLogOverlay) records() []cmdlog.Record {
	switch c.mode {
	case CmdLogFailures:
		return cmdlog.Failures()
	case CmdLogSession:
		if c.session == "" {
			return nil
		}
		return cmdlog.ForSession(c.session)
	default:
		return cmdlog.Snapshot()
	}
}

// HandleKeyPress moves the cursor, cycles the filter, toggles a failure's
// expansion, or closes. Returns true only on esc/ctrl+c.
func (c *CmdLogOverlay) HandleKeyPress(msg tea.KeyMsg) (shouldClose bool) {
	recs := c.records()
	switch msg.String() {
	case "esc", "ctrl+c":
		return true
	case "tab", "f":
		c.mode = (c.mode + 1) % 3
		// CmdLogSession with no session in scope is empty and useless — skip it.
		if c.mode == CmdLogSession && c.session == "" {
			c.mode = CmdLogAll
		}
		c.cursor, c.scroll, c.expanded = 0, 0, false
		return false
	case "up", "k":
		if c.cursor > 0 {
			c.cursor--
			c.expanded = false
		}
		return false
	case "down", "j":
		if c.cursor < len(recs)-1 {
			c.cursor++
			c.expanded = false
		}
		return false
	case "enter":
		// Expansion only means something for a failure row (it has stderr).
		if c.cursor < len(recs) && recs[c.cursor].Err {
			c.expanded = !c.expanded
		}
		return false
	default:
		return false
	}
}

// relTime renders a compact "12s"/"3m"/"2h" age.
func relTime(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
}

// Render draws the bordered log.
func (c *CmdLogOverlay) Render() string {
	th := theme.Current()
	box := lipgloss.NewStyle().
		Border(th.Borders.Style).
		BorderForeground(th.Palette.Accent).
		Padding(1, 2).
		Width(c.width)

	inner := c.width - 6 // borders (2) + horizontal padding (2*2)
	if inner < 20 {
		inner = 20
	}
	recs := c.records()

	var mode string
	switch c.mode {
	case CmdLogFailures:
		mode = "failures"
	case CmdLogSession:
		mode = c.session
	default:
		mode = "all"
	}

	var b strings.Builder
	b.WriteString(th.OverlayTitleStyle().Render("Command Log — "+mode) + "\n")
	b.WriteString(overlayDimStyle().Render(fmt.Sprintf("%d commands", len(recs))) + "\n\n")

	if len(recs) == 0 {
		b.WriteString(overlayDimStyle().Render("no commands recorded yet") + "\n\n")
	} else {
		// Window the list to the box height, keeping the cursor visible. Reserve
		// rows for the title/counter (3), footer (2), and an expanded stderr block.
		visible := c.height - 7
		if c.expanded {
			visible -= 6
		}
		if visible < 3 {
			visible = 3
		}
		c.clampScroll(len(recs), visible)
		end := c.scroll + visible
		if end > len(recs) {
			end = len(recs)
		}
		for i := c.scroll; i < end; i++ {
			b.WriteString(c.renderRow(recs[i], i == c.cursor, inner) + "\n")
		}
		if end < len(recs) {
			b.WriteString(overlayDimStyle().Render(fmt.Sprintf("  … %d more", len(recs)-end)) + "\n")
		}
		b.WriteString("\n")
		if c.expanded && c.cursor < len(recs) && recs[c.cursor].Err {
			b.WriteString(c.renderStderr(recs[c.cursor], inner) + "\n")
		}
	}

	b.WriteString(th.OverlayHintStyle().Render("tab filter · ↵ expand failure · j/k move · esc close"))
	return box.Render(b.String())
}

func (c *CmdLogOverlay) clampScroll(n, visible int) {
	if c.cursor < c.scroll {
		c.scroll = c.cursor
	}
	if c.cursor >= c.scroll+visible {
		c.scroll = c.cursor - visible + 1
	}
	if c.scroll < 0 {
		c.scroll = 0
	}
	if c.scroll > n-1 {
		c.scroll = 0
	}
}

// renderRow lays out one record: status glyph, age, duration, session, argv.
func (c *CmdLogOverlay) renderRow(r cmdlog.Record, selected bool, width int) string {
	th := theme.Current()
	glyph := th.SuccessStyle().Render("✓")
	if r.Err {
		glyph = th.DangerStyle().Render("✗")
	}
	age := fmt.Sprintf("%4s", relTime(time.Since(r.Start)))
	dur := fmt.Sprintf("%6s", r.Dur.Round(time.Millisecond).String())
	sess := r.Session
	if sess == "" {
		sess = "—"
	}
	sess = truncate.StringWithTail(sess, 14, "…")
	// Fixed columns: cursor(2) glyph(1) age(4) dur(6) sess(≤14) + spaces.
	prefix := fmt.Sprintf("%s %s %s %-14s ", glyph, age, dur, sess)
	argvWidth := width - lipgloss.Width(prefix) - 2
	if argvWidth < 8 {
		argvWidth = 8
	}
	argv := truncate.StringWithTail(r.Argv, uint(argvWidth), "…")
	row := prefix + argv
	if selected {
		return overlaySelectedStyle().Render("▸ " + row)
	}
	return "  " + row
}

func (c *CmdLogOverlay) renderStderr(r cmdlog.Record, width int) string {
	th := theme.Current()
	text := strings.TrimRight(r.Stderr, "\n")
	if text == "" {
		text = "(no stderr captured)"
	}
	var lines []string
	for _, ln := range strings.Split(text, "\n") {
		lines = append(lines, "  "+truncate.StringWithTail(ln, uint(width-2), "…"))
	}
	// Cap the expanded block so a huge stderr can't overflow the box.
	if len(lines) > 6 {
		lines = append(lines[:6], overlayDimStyle().Render("  …"))
	}
	header := th.DangerStyle().Render(fmt.Sprintf("stderr (exit %d):", r.Exit))
	return header + "\n" + th.DimStyle().Render(strings.Join(lines, "\n"))
}
