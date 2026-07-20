package session

import (
	"strconv"
	"strings"

	"github.com/ZviBaratz/atrium/internal/fuzzy"
	"github.com/ZviBaratz/atrium/session/agent"
)

// Filter is a compiled list-filter query. It is produced by ParseFilter and
// matched against instances via Matches. The zero value (no terms) matches every
// instance, so an empty/blank query disables filtering.
//
// A query is split on whitespace into terms that are combined with AND. Each term
// is either a predicate over cached instance state (status:, dirty, behind[:expr],
// pr:, account:, note:, effort:, model:, mode:) or a plain substring matched
// against DisplayName, Branch, or the session note. Predicate values are matched
// by case-insensitive prefix so the list narrows progressively as the user types
// rather than blinking empty mid-word (see the package tests). model: is the sole
// exception and matches a substring instead, because model names are
// vendor-prefixed — see modelTerm for why.
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
	Pending:    "pending",
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
	case strings.HasPrefix(tok, "account:"):
		return accountTerm(strings.TrimPrefix(tok, "account:"))
	case strings.HasPrefix(tok, "note:"):
		return noteTerm(strings.TrimPrefix(tok, "note:"))
	case strings.HasPrefix(tok, "effort:"):
		return effortTerm(strings.TrimPrefix(tok, "effort:"))
	case strings.HasPrefix(tok, "model:"):
		return modelTerm(strings.TrimPrefix(tok, "model:"))
	// The trailing colon is what keeps these two apart: "model:opus" does not
	// carry the prefix "mode:", so swapping these two arms changes nothing. The
	// colon is still load-bearing — a bare "mode" prefix ahead of the model: arm
	// would swallow every model: query.
	case strings.HasPrefix(tok, "mode:"):
		return modeTerm(strings.TrimPrefix(tok, "mode:"))
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

// prTerm matches the cached PR state. value is prefix-matched against "open",
// "merged", "closed", and "none"; an empty value is a no-op (matches all) so a
// mid-typed "pr:" never blinks the list empty. A value prefixing none of the known
// states matches nothing.
func prTerm(value string) term {
	if value == "" {
		return func(*Instance) bool { return true }
	}
	wantOpen := strings.HasPrefix("open", value)
	wantMerged := strings.HasPrefix("merged", value)
	wantClosed := strings.HasPrefix("closed", value)
	wantNone := strings.HasPrefix("none", value)
	return func(i *Instance) bool {
		pr := i.GetPRStatus()
		if pr == nil || !pr.HasPR {
			return wantNone
		}
		switch pr.State {
		case "OPEN":
			return wantOpen
		case "MERGED":
			return wantMerged
		case "CLOSED":
			return wantClosed
		}
		return false
	}
}

// accountTerm matches the session's Claude account name by case-insensitive
// prefix, mirroring statusTerm. An empty value is a no-op (matches every session)
// so a mid-typed "account:" never blinks the list empty. The literal value "none"
// matches sessions with no resolved account (ClaudeAccountName == ""), mirroring
// pr:none. Unlike pr:none this is an exact match, not a prefix match, and that is
// intentional: account names are an open, user-defined namespace (unlike the
// fixed pr: states), so "account:no" must prefix-match a real account (e.g.
// "nova"), not silently be swallowed as meaning no-account.
func accountTerm(value string) term {
	return func(i *Instance) bool {
		name := strings.ToLower(i.ClaudeAccountName())
		if value == "none" {
			return name == ""
		}
		return strings.HasPrefix(name, value)
	}
}

// substringTerm matches a free-text term against DisplayName, Branch, or the
// session note as a case-insensitive fuzzy SUBSEQUENCE via the shared matcher
// (issue #373) — so "rfp" matches "Refactor Parser" — rather than a plain
// substring. It only widens which rows match: the list's grouped, status-sorted
// order is deliberately left untouched here (row position is muscle memory on that
// surface), so free-text does not re-rank the session list — a follow-up if wanted.
// Predicate terms (status:/dirty/behind/pr:/account:/note:/effort:/model:/mode:)
// keep exact semantics.
func substringTerm(q string) term {
	return func(i *Instance) bool {
		match := func(s string) bool { ok, _ := fuzzy.Match(q, s); return ok }
		return match(i.DisplayName()) || match(i.Branch) || match(i.Note())
	}
}

// noteTerm matches the session note by case-insensitive prefix, mirroring
// accountTerm. An empty value is a no-op (matches every session) so a
// mid-typed "note:" never blinks the list empty. Unlike accountTerm and
// prTerm there is deliberately no "none" sentinel for un-noted sessions: notes
// are freeform prose (a note could legitimately start with the word "none"), so
// "note:none" prefix-matches such a note rather than being swallowed to mean
// "no note".
func noteTerm(value string) term {
	return func(i *Instance) bool {
		return strings.HasPrefix(strings.ToLower(i.Note()), value)
	}
}

// effortTerm matches the session's resolved effort level (EffortInfo) by
// case-insensitive prefix, mirroring accountTerm. An empty value is a no-op
// (matches every session) so a mid-typed "effort:" never blinks the list empty.
// The claude CLI offers low, medium, high, xhigh and max, so prefix matching
// means "effort:m" matches both "medium" and "max", narrowing to one as the user
// keeps typing. That set is the offered one, not a closed one: EffortInfo prefers
// raw hook truth, which session/effort.go leaves deliberately unvalidated so a
// level a newer CLI resolves still reaches the list.
//
// The literal value "none" matches sessions with no resolved effort
// (EffortInfo == ""), mirroring account:none and pr:none. As with account:none —
// and unlike pr:none — this is an exact match rather than a prefix one, and for
// the same reason: the level set is open, so "effort:no" must stay able to
// prefix-match a level a newer CLI reports rather than being silently swallowed
// as meaning no-effort.
func effortTerm(value string) term {
	return func(i *Instance) bool {
		info := strings.ToLower(i.EffortInfo())
		if value == "none" {
			return info == ""
		}
		return strings.HasPrefix(info, value)
	}
}

// modelTerm matches the session's resolved model (ModelInfo) by case-insensitive
// SUBSTRING — the one predicate that does not prefix-match. Model names are
// vendor-prefixed ("claude-opus-4-8"), so a natural query like "opus" or "sonnet"
// has to reach the family segment without the user typing the leading "claude-".
// An empty value is a no-op (matches every session), since Contains reports true
// for an empty needle, so a mid-typed "model:" never blinks the list empty.
// Sessions with no resolved model (ModelInfo == "") match only that empty
// predicate — an empty haystack contains no non-empty needle.
//
// There is deliberately no "none" sentinel, following noteTerm rather than
// account:none / pr:none / effort:none: a sentinel is only safe where the value
// space cannot collide with the literal, and the vendor's name space is open.
// Substring matching also makes the collision wider here than it would be for
// those predicates — they check an exact "none" against a prefix match, so they
// only shadow values *starting* with "none", whereas this would shadow any model
// containing "none" anywhere.
func modelTerm(value string) term {
	return func(i *Instance) bool {
		return strings.Contains(strings.ToLower(i.ModelInfo()), value)
	}
}

// modeTerm matches the session's permission mode (PermissionModeInfo) by
// case-insensitive prefix, mirroring effortTerm. An empty value is a no-op
// (matches every session) so a mid-typed "mode:" never blinks the list empty.
//
// The match is against the display label rather than the raw enum value — so
// "mode:accept-edits" for acceptEdits and "mode:bypass" for bypassPermissions —
// because the label is what the row's chip shows, and a filter the user types
// should follow what they can see. agent.ClaudePermissionModeLabel is the one
// place that mapping lives, so this predicate cannot drift from the chip. It
// falls back to the raw value for an unlabelled mode (dontAsk, which only a
// profile-pinned flag can reach, or one a newer CLI introduces), which keeps
// such a mode filterable without a change here.
//
// "default" is the one mode folded into the no-mode sentinel rather than matched
// on its own, because the row renderer hides the chip for it exactly as it does
// for no-mode-yet (see ui/list_render.go). It is also the common case — an
// ordinary manual-mode session reports it from the footer marker table. Matching
// it literally would break this predicate's own rule twice over: "mode:d" would
// select rows displaying no mode at all, and two rows that look identical on
// screen would answer "mode:none" differently.
//
// The literal value "none" matches sessions showing no mode chip, mirroring
// effort:none and account:none — and, as with those two, it matches "none"
// exactly rather than by prefix so that "mode:no" stays able to prefix-match a
// real mode. That guard is live rather than theoretical here: PermissionModeInfo
// prefers runtimeMode, which tmux.setPermissionMode stores straight off the hook
// payload with no enum validation, so the value space reaching this predicate is
// open — the CLI's argv-time rejection of an unknown --permission-mode only
// constrains the pinned-flag fallback.
func modeTerm(value string) term {
	return func(i *Instance) bool {
		info := i.PermissionModeInfo()
		if info == "default" {
			info = "" // no chip on the row; no separate identity in the filter
		}
		if value == "none" {
			return info == ""
		}
		return strings.HasPrefix(strings.ToLower(agent.ClaudePermissionModeLabel(info)), value)
	}
}
