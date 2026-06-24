package session

import (
	"strconv"
	"strings"
)

// Filter is a compiled list-filter query. It is produced by ParseFilter and
// matched against instances via Matches. The zero value (no terms) matches every
// instance, so an empty/blank query disables filtering.
//
// A query is split on whitespace into terms that are combined with AND. Each term
// is either a predicate over cached instance state (status:, dirty, behind[:expr],
// pr:) or a plain substring matched against DisplayName/Branch. Predicate values
// are matched by case-insensitive prefix so the list narrows progressively as the
// user types rather than blinking empty mid-word (see the package tests).
type Filter struct {
	terms []term
}

// term reports whether a single parsed term is satisfied by an instance.
type term func(*Instance) bool

// statusNames maps each Status to the canonical lowercase token used by the
// status: predicate. A status: value matches by prefix against these.
var statusNames = map[Status]string{
	Running:    "running",
	Ready:      "ready",
	Loading:    "loading",
	Paused:     "paused",
	NeedsInput: "needsinput",
}

// ParseFilter compiles query into a Filter. It never fails: an unparseable
// predicate value degrades gracefully (an incomplete behind: expression falls back
// to "behind > 0"; a status:/pr: value that prefixes nothing simply matches no
// instance, which surfaces a typo as an empty list).
func ParseFilter(query string) Filter {
	fields := strings.Fields(query)
	terms := make([]term, 0, len(fields))
	for _, f := range fields {
		terms = append(terms, parseTerm(strings.ToLower(f)))
	}
	return Filter{terms: terms}
}

// Matches reports whether i satisfies every term (AND). The zero Filter matches all.
func (f Filter) Matches(i *Instance) bool {
	for _, t := range f.terms {
		if !t(i) {
			return false
		}
	}
	return true
}

// parseTerm classifies a single (already lowercased) token into a predicate or a
// substring term.
func parseTerm(tok string) term {
	switch {
	case strings.HasPrefix(tok, "status:"):
		return statusTerm(strings.TrimPrefix(tok, "status:"))
	case tok == "dirty":
		return dirtyTerm()
	case tok == "behind":
		return behindTerm(func(b int) bool { return b > 0 })
	case strings.HasPrefix(tok, "behind:"):
		return behindTerm(behindPredicate(strings.TrimPrefix(tok, "behind:")))
	case strings.HasPrefix(tok, "pr:"):
		return prTerm(strings.TrimPrefix(tok, "pr:"))
	default:
		return substringTerm(tok)
	}
}

// statusTerm matches when value is a prefix of the instance's status name. An empty
// value is a no-op (matches every status).
func statusTerm(value string) term {
	return func(i *Instance) bool {
		return strings.HasPrefix(statusNames[i.GetStatus()], value)
	}
}

func dirtyTerm() term {
	return func(i *Instance) bool {
		s := i.GetDiffStats()
		return s != nil && s.Dirty
	}
}

// behindTerm applies pred to the cached Behind count; a nil diff (count unknown) is
// treated as not-behind.
func behindTerm(pred func(int) bool) term {
	return func(i *Instance) bool {
		s := i.GetDiffStats()
		return s != nil && pred(s.Behind)
	}
}

// behindPredicate parses a behind: expression (N, >N, >=N, <N, <=N) into a
// comparison. An empty or unparseable expression falls back to "> 0" so a
// mid-typed "behind:" / "behind:>" keeps behaving like the bareword "behind".
func behindPredicate(expr string) func(int) bool {
	positive := func(b int) bool { return b > 0 }
	op := ""
	for _, p := range []string{">=", "<=", ">", "<"} {
		if strings.HasPrefix(expr, p) {
			op = p
			expr = strings.TrimPrefix(expr, p)
			break
		}
	}
	n, err := strconv.Atoi(strings.TrimSpace(expr))
	if err != nil {
		return positive
	}
	switch op {
	case ">":
		return func(b int) bool { return b > n }
	case ">=":
		return func(b int) bool { return b >= n }
	case "<":
		return func(b int) bool { return b < n }
	case "<=":
		return func(b int) bool { return b <= n }
	default: // bare number is equality
		return func(b int) bool { return b == n }
	}
}

// prTerm matches the cached PR state. value is prefix-matched against "open" and
// "none"; an empty value is a no-op (matches all) so a mid-typed "pr:" never blinks
// the list empty. A value prefixing neither matches nothing.
func prTerm(value string) term {
	if value == "" {
		return func(*Instance) bool { return true }
	}
	wantOpen := strings.HasPrefix("open", value)
	wantNone := strings.HasPrefix("none", value)
	return func(i *Instance) bool {
		pr := i.GetPRStatus()
		if wantOpen && pr != nil && pr.HasPR && pr.State == "OPEN" {
			return true
		}
		if wantNone && (pr == nil || !pr.HasPR) {
			return true
		}
		return false
	}
}

// substringTerm matches a plain (lowercased) substring against DisplayName or
// Branch, preserving the original filter's fields and case-insensitivity.
func substringTerm(q string) term {
	return func(i *Instance) bool {
		return strings.Contains(strings.ToLower(i.DisplayName()), q) ||
			strings.Contains(strings.ToLower(i.Branch), q)
	}
}
