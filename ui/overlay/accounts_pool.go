package overlay

import (
	"fmt"

	"charm.land/lipgloss/v2"
	"github.com/ZviBaratz/atrium/config"
)

// poolGutter maps each Claude account to its gutter cell, bracketing every run of
// two or more ADJACENT accounts sharing a pool: "┌ " at the run head, "│ " inside,
// "└ " at the tail, "  " for anything not in a run. It returns nil when no such run
// exists, which is how a pool-free config keeps rendering with no gutter column at
// all. Pool == "" never forms a run — two adjacent unpooled accounts are not a pool.
// Grouping is deliberately a read-out of the real config order (which is routing
// precedence) and never a re-sort, so the row index stays the config index.
func poolGutter(accts []config.ClaudeAccount) []string {
	n := len(accts)
	if n == 0 {
		return nil
	}
	cells := make([]string, n)
	for i := range cells {
		cells[i] = "  "
	}
	found := false
	for i := 0; i < n; {
		pool := accts[i].Pool
		if pool == "" {
			i++
			continue
		}
		j := i
		for j+1 < n && accts[j+1].Pool == pool {
			j++
		}
		if j > i { // a run of 2+ adjacent members
			found = true
			cells[i] = "┌ "
			cells[j] = "└ "
			for k := i + 1; k < j; k++ {
				cells[k] = "│ "
			}
		}
		i = j + 1
	}
	if !found {
		return nil
	}
	return cells
}

// splitPools names the pools whose members are not all adjacent, in first-appearance
// order — the case poolGutter cannot bracket as ONE group. Each contiguous run of two
// or more still gets its own brackets, so the trigger here is non-contiguity, not the
// absence of brackets: a pool broken into two runs renders both bracketed AND named
// below. renderList turns this into the nudge to reorder them together.
func splitPools(accts []config.ClaudeAccount) []string {
	type span struct{ first, last, count int }
	spans := make(map[string]*span)
	var order []string
	for i, a := range accts {
		if a.Pool == "" {
			continue
		}
		s, ok := spans[a.Pool]
		if !ok {
			s = &span{first: i, last: i}
			spans[a.Pool] = s
			order = append(order, a.Pool)
		}
		s.last = i
		s.count++
	}
	var names []string
	for _, name := range order {
		s := spans[name]
		// A pool is split when its members don't fill a single contiguous block:
		// count distinct member positions spanning more than count slots means a
		// gap somewhere in between.
		if s.count >= 2 && s.last-s.first+1 != s.count {
			names = append(names, name)
		}
	}
	return names
}

// splitPoolNote phrases splitPools' result for the list footer, naming at most two
// pools and falling back to a bounded count form when the names would not fit
// width. The single-pool case additionally degrades to a terse form when even
// the count form does not fit a very narrow terminal: a bare count form is
// otherwise reachable and grammatically wrong for exactly one pool ("1 pools
// are split"), and reachable in practice — the naming form's fixed text is 43
// cells against a 60-column terminal's inner() of 54, so any pool name of 12+
// characters (e.g. "quantivly-work") already overflows it.
func splitPoolNote(names []string, width int) string {
	switch {
	case len(names) == 0:
		return ""
	case len(names) == 1:
		naming := fmt.Sprintf("pool '%s' is split — J/K to group its members", names[0])
		if lipgloss.Width(naming) <= width {
			return naming
		}
		count := "1 pool is split — J/K to group its members"
		if lipgloss.Width(count) <= width {
			return count
		}
		return "pool split — J/K to group"
	case len(names) == 2:
		first, second := names[0], names[1]
		naming := fmt.Sprintf("pools '%s', '%s' are split — J/K to group their members", first, second)
		if lipgloss.Width(naming) <= width {
			return naming
		}
		return fmt.Sprintf("%d pools are split — J/K to group their members", len(names))
	default:
		return fmt.Sprintf("%d pools are split — J/K to group their members", len(names))
	}
}
