package overlay

import (
	"fmt"
	"strings"
	"time"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/ui/theme"

	"github.com/charmbracelet/lipgloss"
)

// previewChrome is the number of lines renderPreview costs outside the pool
// block: border 2 + Padding(1,2) 2 + title/blank 2 + 12 body lines ("Test
// routing", blank, two labelled inputs = 4, blank, three result lines, blank,
// hint).
const previewChrome = 18

// previewMemberBudget returns how many pool-member rows fit under the
// pool-free chrome (previewChrome) at the given overlay height. The pool
// block itself pays two costs previewChrome does not count, because they
// belong to the block, not to renderPreview's fixed body: its header line
// ("pool 'work' ⇄", always paid, always present above the member rows), and
// one more line for the decision sentence beneath the members, paid only
// when there is one to show. The result is floored at 2 so a very small
// overlay still shows something rather than collapsing the block away
// entirely.
func previewMemberBudget(height int, decision bool) int {
	budget := height - previewChrome - 1
	if decision {
		budget--
	}
	if budget < 2 {
		budget = 2
	}
	return budget
}

// previewMemberWindow picks which slice of members [start, end) to render out
// of n, given how many rows fit (budget), so that the chosen member is always
// inside the window — a cap that could scroll it out of view would defeat the
// preview's whole purpose. When everything fits, nothing is hidden and no
// overflow line is needed. Otherwise one row is reserved for the "N more
// members not shown" line (shown = budget - 1), and the window slides forward
// just far enough to keep chosen visible, never past the tail.
func previewMemberWindow(n, chosen, budget int) (start, end, hidden int) {
	if n <= budget {
		return 0, n, 0
	}
	shown := budget - 1
	start = 0
	if chosen >= shown {
		start = chosen - shown + 1
	}
	if start < 0 {
		start = 0
	}
	if maxStart := n - shown; start > maxStart {
		start = maxStart
	}
	end = start + shown
	hidden = n - shown
	return start, end, hidden
}

// previewDecisionLine phrases why creating a session here would land on
// members[chosen], or "" when there is nothing worth explaining (the pool's
// own cursor, start, was already available and chosen == start).
//
// start and chosen are both indices into members, normalized the same way
// config.SelectPoolMember normalizes its cursor — callers pass it the same
// cursor and the idx SelectPoolMember returned.
//
// When allLimited, SelectPoolMember's chosen is only its defensive fallback
// (the cursor's own member), not a member picked for any reason a user would
// recognize — so this asks config.SoonestResetMember directly for the member
// worth naming instead of trusting chosen. Rate-limit flags are indefinite-only
// in the current UI, so SoonestResetMember's "soonest" is frequently a
// same-as-first fallback rather than an actual soonest reset: the "(resets
// soonest)" wording is only honest when that pinned member has a Until that
// actually parses, and falls back to "(first member)" otherwise.
func previewDecisionLine(members []config.ClaudeAccount, avail map[string]config.AccountAvailability,
	start, chosen int, allLimited bool, now time.Time) string {
	n := len(members)
	if n == 0 {
		return ""
	}

	if allLimited {
		pinned := config.SoonestResetMember(members, avail)
		reason := "(first member)"
		if until := avail[members[pinned].Name].Until; until != "" {
			if _, err := time.Parse(time.RFC3339, until); err == nil {
				reason = "(resets soonest)"
			}
		}
		return fmt.Sprintf("creating asks to confirm, then uses %s %s", members[pinned].Name, reason)
	}

	s := ((start % n) + n) % n
	c := ((chosen % n) + n) % n
	skipped := 0
	for i := s; i != c; i = (i + 1) % n {
		if !config.AccountAvailable(avail[members[i].Name], now) {
			skipped++
		}
	}
	switch skipped {
	case 0:
		return ""
	case 1:
		return fmt.Sprintf("%s limited → rotating to %s", members[s].Name, members[c].Name)
	default:
		return fmt.Sprintf("%d members limited → rotating to %s", skipped, members[c].Name)
	}
}

// previewIndent aligns every pool-block line under the "Claude → " label —
// its printed width, so the block reads as a sub-list of that line rather
// than a new left-aligned column.
const previewIndent = "         " // 9 spaces

// previewChipWidth right-pads the shorter "⛔ limited" chip to the same
// printed width as the longer "● available" chip, so a row's "← next" /
// "← on confirm" marker lands in the same column regardless of which chip
// the row renders.
var previewChipWidth = lipgloss.Width("● available")

// renderPoolDecision computes what renderPreview's Claude line and pool block
// show when the routed account belongs to a rotation pool of two or more
// members: the headline (the member creating a session here would use, or the
// all-limited warning) and the indented block beneath it (the "pool '<name>'
// ⇄" header, one row per member in the visible window, an optional "N more
// members not shown" line, and the decision sentence).
//
// Selection is delegated entirely to config.SelectPoolMember and, on an
// exhausted pool, config.SoonestResetMember — the exact pair
// app_session.go's creation path calls — so what this preview shows and what
// creating a session here actually does can never drift apart. It reads
// state (GetAccountAvailability, GetAccountRotation) but never writes it:
// Bubble Tea re-renders on every keystroke, and a writing preview would
// rotate the pool once per typed character.
func (o *AccountsOverlay) renderPoolDecision(pool string, members []config.ClaudeAccount, now time.Time) (headline, block string) {
	t := theme.Current()
	avail := o.state.GetAccountAvailability()
	cursor := o.state.GetAccountRotation(pool)
	chosen, allLimited := config.SelectPoolMember(members, avail, cursor, now)

	// marked is the member row that carries the "← next" / "← on confirm"
	// marker, and the index previewMemberWindow must keep visible: on an
	// exhausted pool that's SoonestResetMember's pick (what creation actually
	// pins on confirm), not SelectPoolMember's defensive cursor fallback.
	marked := chosen
	marker := "← next"
	switch {
	case allLimited:
		marked = config.SoonestResetMember(members, avail)
		marker = "← on confirm"
		headline = "⚠ all '" + pool + "' accounts limited"
	case members[chosen].ResolvedConfigDir() != "":
		headline = members[chosen].Name + " (" + members[chosen].ResolvedConfigDir() + ")"
	default:
		headline = members[chosen].Name + " (inherit ambient env)"
	}

	decision := previewDecisionLine(members, avail, cursor, chosen, allLimited, now)
	budget := previewMemberBudget(o.height, decision != "")
	start, end, hidden := previewMemberWindow(len(members), marked, budget)
	// poolGutter can return nil even for this 2+-member slice: PoolMembers
	// also matches an ungrouped account whose name equals the pool name, and
	// that member's own Pool field is "", breaking the contiguous run
	// poolGutter looks for. Indexing a nil slice would panic, so every read
	// below falls back to two blank cells instead.
	gut := poolGutter(members)

	var b strings.Builder
	b.WriteString(previewIndent + "pool '" + pool + "' ⇄\n")
	for i := start; i < end; i++ {
		gutter := "  "
		if gut != nil {
			gutter = t.DimStyle().Render(gut[i])
		}
		chip := t.DimStyle().Render("● available")
		if !config.AccountAvailable(avail[members[i].Name], now) {
			chip = t.DangerStyle().Render("⛔ limited")
		}
		line := previewIndent + gutter + padRight(members[i].Name, 12) + " " + padRight(chip, previewChipWidth)
		if i == marked {
			line += "  " + marker
		}
		b.WriteString(line + "\n")
	}
	if hidden > 0 {
		fmt.Fprintf(&b, "%s… %d more members not shown\n", previewIndent, hidden)
	}
	if decision != "" {
		b.WriteString(previewIndent + decision + "\n")
	}
	return headline, b.String()
}
