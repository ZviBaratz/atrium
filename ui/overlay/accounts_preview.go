package overlay

import (
	"fmt"
	"strings"
	"time"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/ui/theme"

	"charm.land/lipgloss/v2"
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
// config.SelectPoolMember normalizes its cursor.
//
// When allLimited, chosen must be the caller's already-computed
// config.SoonestResetMember pick, not SelectPoolMember's own chosen —
// SelectPoolMember's chosen on an exhausted pool is only its defensive
// fallback (the cursor's own member), not a member picked for any reason a
// user would recognize. Requiring the caller to pass the same pinned index it
// uses for the "← on confirm" marker (renderPoolDecision's marked) makes the
// row and the sentence structurally agree, rather than agreeing only because
// both call sites happen to compute the same thing independently. Rate-limit
// flags are indefinite-only in the current UI, so SoonestResetMember's
// "soonest" is frequently a same-as-first fallback rather than an actual
// soonest reset: the "(resets soonest)" wording is only honest when that
// pinned member has a Until that actually parses, and falls back to "(first
// member)" otherwise.
//
// The confirm this sentence describes fires on every path that creates a
// session: the create form and smart auto-dispatch both run gateAllExhausted,
// so the sentence asserts it bare. It was once scoped to "the form" because
// auto-dispatch bypassed the gate and could silently spawn on a different
// member — fixing that (#483) is what made the unqualified wording true.
//
// width bounds every string this returns: it's the space renderPoolDecision
// actually has left for a block line (o.inner() minus previewIndentWidth),
// the same budget the member rows are held to. Unlike those rows this
// sentence has no fixed-width column to truncate into, so a phrasing chosen
// without regard for width wraps — costing the block a row previewMemberBudget
// never counted (the defect this parameter exists to fix). The cascades below
// mirror splitPoolNote's: try the clearest wording, degrade to shorter ones,
// and only ever drop information forward (never substitute the wrong reason
// or assert a confirm the dropped wording no longer supports).
func previewDecisionLine(members []config.ClaudeAccount, avail map[string]config.AccountAvailability,
	start, chosen int, allLimited bool, now time.Time, width int) string {
	n := len(members)
	if n == 0 {
		return ""
	}

	if allLimited {
		pinned := ((chosen % n) + n) % n
		reason := "(first member)"
		if until := avail[members[pinned].Name].Until; until != "" {
			if _, err := time.Parse(time.RFC3339, until); err == nil {
				reason = "(resets soonest)"
			}
		}
		return allLimitedDecision(members[pinned].Name, reason, width)
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
		return skipOneDecision(members[s].Name, members[c].Name, width)
	default:
		return skipManyDecision(skipped, members[c].Name, width)
	}
}

// allLimitedDecision phrases the exhausted-pool confirm sentence, degrading
// through shorter wordings until one fits width. No rung ever collapses the
// (first member)/(resets soonest) distinction onto the other's case — that
// would misreport whichever one didn't happen.
//
// "creating", not "the form": every path that creates a session now runs
// gateAllExhausted, so the confirm is what a user gets from the create form
// and from smart auto-dispatch alike (#483). While that was false the wording
// was deliberately narrowed to name the form, and a third rung existed purely
// to drop "confirm" at widths where "form" no longer fit — a rung that said
// "all limited → <name> <reason>". It is gone: with the qualifier unnecessary,
// the terse rung is 4 cells *shorter* than that one, so it fit wherever the
// third rung would have and the third rung could never be reached.
//
// The full wording runs 57-59 cells for a 6-letter name against the
// 65-column width the default 80-column terminal actually leaves this line
// (o.inner() 74 minus previewIndentWidth 9) — but a 23-letter member name
// already pushes it to 76, over budget even at that "full" width, so the
// second rung is reachable there too, not only on a narrower terminal. That
// second rung holds the reason down to 31 cells, below which only the name
// survives, and then only "all limited".
func allLimitedDecision(name, reason string, width int) string {
	full := fmt.Sprintf("creating asks to confirm, then uses %s %s", name, reason)
	if lipgloss.Width(full) <= width {
		return full
	}
	terse := fmt.Sprintf("confirm → %s %s", name, reason)
	if lipgloss.Width(terse) <= width {
		return terse
	}
	named := fmt.Sprintf("all limited → %s", name)
	if lipgloss.Width(named) <= width {
		return named
	}
	const floor = "all limited"
	if lipgloss.Width(floor) <= width {
		return floor
	}
	// The floor itself is 11 cells, and boxWidth's 20-column minimum leaves
	// this line only 7 (inner() 16 minus previewIndentWidth 9) — so this is
	// reachable at the smallest terminal Atrium still renders a box for, not
	// just a defensive case. Say nothing rather than overflow the box.
	return ""
}

// skipOneDecision and skipManyDecision phrase the shorter rotation-skip
// sentences with the same width discipline as allLimitedDecision: these run
// far shorter today (35 cells for two 6-letter names, well inside the
// 65-column default), but a long enough member name overflows them too, and
// this preview has no special case for "not today's fixture" — the cascade
// applies regardless of how unlikely a given width is in practice.
func skipOneDecision(from, to string, width int) string {
	full := fmt.Sprintf("%s limited → rotating to %s", from, to)
	if lipgloss.Width(full) <= width {
		return full
	}
	terse := fmt.Sprintf("%s limited → %s", from, to)
	if lipgloss.Width(terse) <= width {
		return terse
	}
	arrow := "→ " + to
	if lipgloss.Width(arrow) <= width {
		return arrow
	}
	const floor = "limited → rotating"
	if lipgloss.Width(floor) <= width {
		return floor
	}
	// See allLimitedDecision's matching comment: reachable at the smallest
	// box Atrium still renders, so say nothing rather than overflow.
	return ""
}

func skipManyDecision(skipped int, to string, width int) string {
	full := fmt.Sprintf("%d members limited → rotating to %s", skipped, to)
	if lipgloss.Width(full) <= width {
		return full
	}
	terse := fmt.Sprintf("%d limited → %s", skipped, to)
	if lipgloss.Width(terse) <= width {
		return terse
	}
	arrow := "→ " + to
	if lipgloss.Width(arrow) <= width {
		return arrow
	}
	const floor = "limited → rotating"
	if lipgloss.Width(floor) <= width {
		return floor
	}
	return ""
}

// previewIndent aligns every pool-block line under the "Claude → " label —
// its printed width, so the block reads as a sub-list of that line rather
// than a new left-aligned column.
const previewIndent = "         " // 9 spaces

// previewIndentWidth is previewIndent's printed width, derived once so a
// caller computing how much room is left for a block line after the indent
// (see previewDecisionLine's width parameter) doesn't hardcode 9.
var previewIndentWidth = lipgloss.Width(previewIndent)

// previewChipText is one member row's availability chip: the same mark the account
// list paints, plus the word the list no longer has room for. The divergence is
// deliberate — the list is the width-constrained surface (#478 was a row wrapping at
// 96 cells), while this block is indented detail with room to spare, so the word
// survives where it costs nothing. The MARK must match either way, which is why both
// surfaces read it out of the glyph table rather than spelling it.
func previewChipText(available bool) string {
	g := theme.Current().Glyphs
	if available {
		return g.AcctAvailable + " available"
	}
	return g.AcctLimited + " limited"
}

// previewChipWidth right-pads the shorter chip to the same printed width as the
// longer one, so a row's "← next" / "← on confirm" marker lands in the same column
// regardless of which chip the row renders. Measured from the chips rather than
// frozen as a literal: the two marks come from the active glyph set, which the user
// can change at runtime, and a stale constant would misalign the markers by exactly
// the difference.
func previewChipWidth() int {
	return max(lipgloss.Width(previewChipText(true)), lipgloss.Width(previewChipText(false)))
}

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
func (o *AccountsOverlay) renderPoolDecision(pool string, members []config.ClaudeAccount, now time.Time) (headline, block, chosenDir string) {
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

	// The dir behind the "signed in as" line the caller renders. It follows marked
	// for the same reason previewDecisionLine does: on an exhausted pool, creation
	// pins SoonestResetMember on confirm, so that — not SelectPoolMember's
	// defensive cursor fallback — is the account a session here would bill.
	chosenDir = members[marked].ResolvedConfigDir()

	// marked, not chosen, goes to previewDecisionLine: on an exhausted pool
	// chosen is only SelectPoolMember's defensive fallback, while marked is
	// already the SoonestResetMember pick the decision sentence must name —
	// passing the same value the "← on confirm" marker uses keeps the two
	// structurally in agreement (see previewDecisionLine's doc comment). The
	// width passed is what's left for a block line after previewIndent, the
	// same budget every member row below is held to, so the sentence can
	// never wrap the box regardless of member name or reason length.
	decision := previewDecisionLine(members, avail, cursor, marked, allLimited, now, o.inner()-previewIndentWidth)
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
		chip := t.DimStyle().Render(previewChipText(true))
		if !config.AccountAvailable(avail[members[i].Name], now) {
			chip = t.DangerStyle().Render(previewChipText(false))
		}
		line := previewIndent + gutter + padRight(members[i].Name, nameWidth) + " " + padRight(chip, previewChipWidth())
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
	return headline, b.String(), chosenDir
}
