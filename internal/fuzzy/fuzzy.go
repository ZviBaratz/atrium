// Package fuzzy is a small, dependency-free subsequence matcher shared across the
// UI pickers (project, branch) and the session filter's free-text terms. It lives
// in a neutral package so both ui/overlay and session can rank with one matcher —
// there is exactly one matching implementation in the tree (issue #373).
//
// A query matches a target when its runes appear in order (case-insensitively),
// and the score rewards matches that start at a word boundary and runs of
// consecutive matches — so "ar" ranks "archive" (contiguous) above "atrium"
// (gapped) and "bar" ranks "bar-baz" (boundary) above "foobar" (mid-word).
package fuzzy

import (
	"sort"
	"strings"
)

const (
	boundaryBonus    = 10 // a match at the start of the target or just after a separator
	consecutiveBonus = 5  // per step of an unbroken run of matches (grows with the run)
)

// isSep reports whether r separates "words" in a path or identifier, so a match
// immediately after one counts as a boundary start.
func isSep(r rune) bool {
	switch r {
	case '/', '\\', '-', '_', ' ', '.':
		return true
	}
	return false
}

// Match reports whether query is a case-insensitive subsequence of target and, if
// so, a score where higher is a better match. An empty query matches everything
// with score 0.
//
// Scoring uses fzf's v1 minimal-window approach (three O(n) passes, no DP): a
// purely greedy leftmost alignment would under-score targets whose query runes
// appear scattered early but contiguous late — "ses" against "projects/sessions"
// would spend the first 's' on "projects" and never see the boundary run in
// "sessions".
func Match(query, target string) (bool, int) {
	if query == "" {
		return true, 0
	}
	q := []rune(strings.ToLower(query))
	t := []rune(strings.ToLower(target))

	// Forward scan: subsequence test, and the earliest index a match can end at.
	qi, end := 0, -1
	for ti := 0; ti < len(t) && qi < len(q); ti++ {
		if t[ti] == q[qi] {
			end = ti
			qi++
		}
	}
	if qi < len(q) {
		return false, 0
	}

	// Backward scan from that end to the latest start — the smallest window
	// containing a match, where the densest (best-scoring) alignment lives.
	start := end
	for ti, bqi := end, len(q)-1; ti >= 0 && bqi >= 0; ti-- {
		if t[ti] == q[bqi] {
			start = ti
			bqi--
		}
	}

	// Score the greedy alignment within the window.
	score := 0
	qi = 0
	prevMatch := start - 2 // so the first match is never counted as consecutive
	run := 0
	for ti := start; ti <= end && qi < len(q); ti++ {
		if t[ti] != q[qi] {
			continue
		}
		if ti == 0 || isSep(t[ti-1]) {
			score += boundaryBonus
		}
		if ti == prevMatch+1 {
			run++
			score += consecutiveBonus * run
		} else {
			run = 0
		}
		prevMatch = ti
		qi++
	}
	return true, score
}

// Rank filters items to those matching query and returns them sorted by score
// (descending), preserving input order on ties. An empty query returns every item
// in its original order.
func Rank(items []string, query string) []string {
	type scored struct {
		item  string
		score int
	}
	matches := make([]scored, 0, len(items))
	for _, it := range items {
		if ok, score := Match(query, it); ok {
			matches = append(matches, scored{item: it, score: score})
		}
	}
	sort.SliceStable(matches, func(a, b int) bool {
		return matches[a].score > matches[b].score
	})
	out := make([]string, len(matches))
	for i, m := range matches {
		out[i] = m.item
	}
	return out
}
