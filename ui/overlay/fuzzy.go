package overlay

import (
	"sort"
	"strings"
)

// fuzzy.go is a small, dependency-free subsequence matcher used to rank repo
// candidates and on-disk directory names in the project picker. It is deliberately
// simple: a query matches a target when its runes appear in order (case-insensitively),
// and the score rewards matches that start at a word boundary and runs of consecutive
// matches — so "ar" ranks "archive" (contiguous) above "atrium" (gapped) and "bar"
// ranks "bar-baz" (boundary) above "foobar" (mid-word).

const (
	fuzzyBoundaryBonus    = 10 // a match at the start of the target or just after a separator
	fuzzyConsecutiveBonus = 5  // per step of an unbroken run of matches (grows with the run)
)

// isFuzzySep reports whether r separates "words" in a path or identifier, so a match
// immediately after one counts as a boundary start.
func isFuzzySep(r rune) bool {
	switch r {
	case '/', '\\', '-', '_', ' ', '.':
		return true
	}
	return false
}

// fuzzyMatch reports whether query is a case-insensitive subsequence of target and,
// if so, a score where higher is a better match. An empty query matches everything
// with score 0.
func fuzzyMatch(query, target string) (bool, int) {
	if query == "" {
		return true, 0
	}
	q := []rune(strings.ToLower(query))
	t := []rune(strings.ToLower(target))

	score := 0
	qi := 0
	prevMatch := -2 // so the first match is never counted as consecutive
	run := 0
	for ti := 0; ti < len(t) && qi < len(q); ti++ {
		if t[ti] != q[qi] {
			continue
		}
		if ti == 0 || isFuzzySep(t[ti-1]) {
			score += fuzzyBoundaryBonus
		}
		if ti == prevMatch+1 {
			run++
			score += fuzzyConsecutiveBonus * run
		} else {
			run = 0
		}
		prevMatch = ti
		qi++
	}
	if qi < len(q) {
		return false, 0
	}
	return true, score
}

// fuzzyRank filters items to those matching query and returns them sorted by score
// (descending), preserving input order on ties. An empty query returns every item in
// its original order.
func fuzzyRank(items []string, query string) []string {
	type scored struct {
		item  string
		score int
		idx   int
	}
	matches := make([]scored, 0, len(items))
	for i, it := range items {
		if ok, score := fuzzyMatch(query, it); ok {
			matches = append(matches, scored{item: it, score: score, idx: i})
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
