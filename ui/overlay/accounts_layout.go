package overlay

// Column geometry for the account list.
//
// A row is a fixed-width assembly — marker, gutter, name, dir, badge, and the Claude
// tab's pool/availability chips — and for most of its life every one of those widths
// was a constant. That is what #478 was: the constants add up to 96 cells for a
// pooled rule-less account, against an inner width of 80 at the box's 84-column cap
// and 74 at a plain 80-column terminal. A row wider than the box wraps, and the
// wrapped continuation costs a line rowWindow's height budget never counted.
//
// Shortening the copy bought the columns back, but could not GUARANTEE anything: a
// pool name is free text, so the tail has no upper bound to design against. So the
// row now has one flexible column. The dir is it — a path is the one field here that
// degrades gracefully, because truncTail keeps the leaf and nobody reads the middle
// of a config-dir path — and it absorbs whatever the badge and chips need. The
// gutter already worked this way (#475: its two columns come OUT of the dir rather
// than on top of the row); this generalises that to the whole tail.
//
// The last rung is Render's own line clip, not anything here: a ladder that can
// still overflow at its final step is not a guarantee (accounts_identity.go's
// fitOneOf makes the same argument about note wordings).

import (
	"time"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/ui/theme"

	"github.com/charmbracelet/lipgloss"
)

const (
	markerWidth = 2  // "› " / "  "
	nameWidth   = 12 // the name column padRight fills
	dirMinWidth = 8  // the dir column never shrinks past this; below it, Render clips

	// dirGapWidth is the blank the dir column keeps between the longest path it will
	// show and the badge — the difference between the base pad and trunc widths, kept
	// as a relation so a flexed column preserves the same breathing room.
	dirGapWidth = dirPadWidthBase - dirTruncWidthBase
)

// Badge texts, named so the layout pass can measure them unstyled. Measuring the
// rendered string would work today (every theme style here is a bare Foreground) but
// pins the row's arithmetic to that staying true.
const (
	badgeRouted      = "routed"
	badgeDefault     = "default"
	badgeUnreachable = "unreachable"
)

// badgeText names a row's routing state. It is order-dependent across the WHOLE
// list, not the visible window: the first rule-less account is the reachable
// catch-all and every later one is dead, so seen is threaded through rows that may
// have scrolled off the top.
func badgeText(catchAll bool, seen *bool) string {
	if !catchAll {
		return badgeRouted
	}
	if *seen {
		return badgeUnreachable
	}
	*seen = true
	return badgeDefault
}

// styleBadge paints badgeText's result. Split from the text so one walk can both
// measure and render a badge without computing it twice — and, more importantly,
// without a second walk restarting the seen flag, which would badge a visible
// unreachable account as the default one.
func styleBadge(text string) string {
	t := theme.Current()
	switch text {
	case badgeRouted:
		return t.AccentStyle().Render(text)
	case badgeUnreachable:
		return t.DangerStyle().Render(text)
	default:
		return t.DimStyle().Render(text)
	}
}

// rowTail is one row's badge plus the Claude tab's chips: the part of the row whose
// width the config dictates and the layout cannot shrink.
type rowTail struct {
	rendered string // styled, ready to write
	width    int    // display cells, measured from the unstyled text
}

// rowTails builds every row's tail once, in config order, and reports the widest.
// Whole-list rather than window-scoped for two reasons: the badge depends on rows
// above the window (see badgeText), and a dir column sized from the visible rows
// alone would change width as the list scrolls.
func (o *AccountsOverlay) rowTails(rows []acctRow, avail map[string]config.AccountAvailability, now time.Time) ([]rowTail, int) {
	t := theme.Current()

	// The badge is a column, not a word: `routed` is 6 cells and `unreachable` is 11,
	// so everything after it — the pool chip, the availability mark — only lines up if
	// every badge occupies the same width. Sized from the badges this list actually
	// renders rather than from the longest one that exists, so a list with no dead
	// catch-all pays nothing for a badge it never shows. This is also the reason the
	// badges are computed in their own pass: the width has to be known before the
	// first row is assembled, and badgeText's ordering must be threaded exactly once.
	badges := make([]string, len(rows))
	badgeWidth := 0
	seen := false
	for i, r := range rows {
		badges[i] = badgeText(r.catchAll, &seen)
		if w := lipgloss.Width(badges[i]); w > badgeWidth {
			badgeWidth = w
		}
	}

	tails := make([]rowTail, len(rows))
	widest := 0
	for i := range rows {
		text := badges[i]
		tail := rowTail{rendered: styleBadge(text), width: lipgloss.Width(text)}
		if o.tab == tabClaude {
			// Padded only where something follows it. On the GitHub and Antigravity
			// tabs the badge ends the row, so a pad there would be trailing whitespace
			// charged against the dir column for nothing.
			tail.rendered, tail.width = padRight(tail.rendered, badgeWidth), badgeWidth
			// rows is index-parallel to ClaudeAccounts on this tab only; the other two
			// tabs have their own lengths and must never reach this lookup.
			acct := o.cfg.ClaudeAccounts[i]
			if acct.Pool != "" {
				chip := "pool:" + acct.Pool
				tail.rendered += "  " + t.DimStyle().Render(chip)
				tail.width += 2 + lipgloss.Width(chip)
			}
			mark, style := t.Glyphs.AcctLimited, t.DangerStyle()
			if config.AccountAvailable(avail[acct.Name], now) {
				mark, style = t.Glyphs.AcctAvailable, t.DimStyle()
			}
			tail.rendered += "  " + style.Render(mark)
			tail.width += 2 + lipgloss.Width(mark)
		}
		tails[i] = tail
		if tail.width > widest {
			widest = tail.width
		}
	}
	return tails, widest
}

// dirWidths sizes the row's one flexible column. It returns the width a path is
// truncated to and the width the column is padded to, given the box's inner width,
// the gutter's width (0 when no pool run exists anywhere) and the widest tail in the
// list.
//
// The column keeps its full base width whenever there is room, so an ordinary config
// renders exactly as it always has; it gives up columns only when the tail would
// otherwise push the row past inner. Below dirMinWidth it stops giving, because a
// two-character path stub is no more useful than a clipped row — at that point the
// terminal is narrower than the legend line anyway, and Render's clip is what keeps
// the box intact.
func dirWidths(inner, gutter, tailMax int) (trunc, pad int) {
	pad = dirPadWidthBase - gutter
	// markerWidth + gutter + name + the two single-space separators.
	if room := inner - (markerWidth + gutter + nameWidth + 2) - tailMax; room < pad {
		pad = room
	}
	if pad < dirMinWidth {
		pad = dirMinWidth
	}
	return pad - dirGapWidth, pad
}
