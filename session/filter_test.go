package session

import (
	"testing"

	"github.com/ZviBaratz/atrium/session/git"
	"github.com/stretchr/testify/require"
)

// newFilterInstance builds a bare instance (no worktree/tmux) carrying the given
// title and branch, suitable for exercising the matcher via the exported setters.
func newFilterInstance(t *testing.T, title, branch string) *Instance {
	t.Helper()
	inst, err := NewInstance(InstanceOptions{Title: title, Path: "/tmp/repoA", Program: "echo"})
	require.NoError(t, err)
	inst.Branch = branch
	return inst
}

func TestParseFilter_EmptyMatchesAll(t *testing.T) {
	inst := newFilterInstance(t, "alpha", "feat/alpha")
	for _, q := range []string{"", "   ", "\t"} {
		require.True(t, ParseFilter(q).Matches(inst), "query %q should match all", q)
	}
}

func TestFilter_Substring(t *testing.T) {
	inst := newFilterInstance(t, "Refactor Parser", "zvi/refactor-parser")

	require.True(t, ParseFilter("refactor").Matches(inst), "DisplayName substring")
	require.True(t, ParseFilter("REFACTOR").Matches(inst), "case-insensitive")
	require.True(t, ParseFilter("parser").Matches(inst), "branch substring")
	require.False(t, ParseFilter("deploy").Matches(inst), "non-matching substring")
}

func TestFilter_SubstringTermsAreANDed(t *testing.T) {
	inst := newFilterInstance(t, "fix the bug", "feat/x")

	require.True(t, ParseFilter("fix bug").Matches(inst), "both words present (order-independent)")
	require.False(t, ParseFilter("fix gone").Matches(inst), "one word missing fails the AND")
}

// Free-text terms match as a case-insensitive fuzzy SUBSEQUENCE (issue #373), not
// just a substring, so an abbreviation finds a longer name. Negatives that lack the
// runes in order still fail — the swap only widens matching, it does not match all.
// (This is the mutation guard for the substring→subsequence swap: reverting to
// strings.Contains flips the two fuzzy-positive assertions to false.)
func TestFilter_FreeTextIsFuzzy(t *testing.T) {
	inst := newFilterInstance(t, "Refactor Parser", "zvi/refactor-parser")

	require.True(t, ParseFilter("rfp").Matches(inst), "abbreviation subsequence r-f-p matches 'Refactor Parser'")
	require.True(t, ParseFilter("refpar").Matches(inst), "gapped subsequence matches")
	require.False(t, ParseFilter("deploy").Matches(inst), "no d-e-p-l-o-y-in-order still fails")
	require.False(t, ParseFilter("zzz").Matches(inst), "no subsequence fails")
}

func TestFilter_Status(t *testing.T) {
	ready := newFilterInstance(t, "r", "b")
	ready.SetStatus(Ready)
	running := newFilterInstance(t, "g", "b")
	running.SetStatus(Running)
	paused := newFilterInstance(t, "p", "b")
	paused.SetStatus(Paused)
	needs := newFilterInstance(t, "n", "b")
	needs.SetStatus(NeedsInput)

	require.True(t, ParseFilter("status:ready").Matches(ready))
	require.False(t, ParseFilter("status:ready").Matches(running))
	require.True(t, ParseFilter("status:paused").Matches(paused))
	require.True(t, ParseFilter("status:needsinput").Matches(needs))
	require.True(t, ParseFilter("STATUS:READY").Matches(ready), "case-insensitive")
}

func TestFilter_StatusPrefixIncremental(t *testing.T) {
	ready := newFilterInstance(t, "r", "b")
	ready.SetStatus(Ready)
	running := newFilterInstance(t, "g", "b")
	running.SetStatus(Running)
	needs := newFilterInstance(t, "n", "b")
	needs.SetStatus(NeedsInput)

	// Empty value is a no-op: the list never blinks empty while typing "status:".
	require.True(t, ParseFilter("status:").Matches(ready))
	require.True(t, ParseFilter("status:").Matches(running))

	// "r" is a prefix of both ready and running.
	require.True(t, ParseFilter("status:r").Matches(ready))
	require.True(t, ParseFilter("status:r").Matches(running))

	// "re" narrows to ready only.
	require.True(t, ParseFilter("status:re").Matches(ready))
	require.False(t, ParseFilter("status:re").Matches(running))

	// "n" already selects needsinput without typing the full word.
	require.True(t, ParseFilter("status:n").Matches(needs))

	// A value prefixing no status matches nothing (typo feedback).
	require.False(t, ParseFilter("status:xyz").Matches(ready))
}

func TestFilter_Dirty(t *testing.T) {
	dirty := newFilterInstance(t, "d", "b")
	dirty.SetDiffStats(&git.DiffStats{Dirty: true})
	clean := newFilterInstance(t, "c", "b")
	clean.SetDiffStats(&git.DiffStats{Dirty: false})
	unknown := newFilterInstance(t, "u", "b") // nil diffStats

	require.True(t, ParseFilter("dirty").Matches(dirty))
	require.False(t, ParseFilter("dirty").Matches(clean))
	require.False(t, ParseFilter("dirty").Matches(unknown), "nil diffStats is not dirty")
}

func TestFilter_Behind(t *testing.T) {
	behind3 := newFilterInstance(t, "a", "b")
	behind3.SetDiffStats(&git.DiffStats{Behind: 3})
	even := newFilterInstance(t, "c", "b")
	even.SetDiffStats(&git.DiffStats{Behind: 0})
	unknown := newFilterInstance(t, "u", "b")

	require.True(t, ParseFilter("behind").Matches(behind3))
	require.False(t, ParseFilter("behind").Matches(even))
	require.False(t, ParseFilter("behind").Matches(unknown), "nil diffStats is not behind")

	require.True(t, ParseFilter("behind:>2").Matches(behind3))
	require.False(t, ParseFilter("behind:>3").Matches(behind3))
	require.True(t, ParseFilter("behind:>=3").Matches(behind3))
	require.True(t, ParseFilter("behind:<1").Matches(even))
	require.True(t, ParseFilter("behind:3").Matches(behind3), "bare number is equality")
	require.True(t, ParseFilter("behind:0").Matches(even))
	require.False(t, ParseFilter("behind:0").Matches(behind3))
}

func TestFilter_BehindIncompleteFallsBackToPositive(t *testing.T) {
	behind3 := newFilterInstance(t, "a", "b")
	behind3.SetDiffStats(&git.DiffStats{Behind: 3})
	even := newFilterInstance(t, "c", "b")
	even.SetDiffStats(&git.DiffStats{Behind: 0})

	// Mid-type states must behave like the bareword "behind" (> 0), not blink empty.
	for _, q := range []string{"behind:", "behind:>", "behind:>=", "behind:abc"} {
		require.True(t, ParseFilter(q).Matches(behind3), "%q should match behind>0", q)
		require.False(t, ParseFilter(q).Matches(even), "%q should not match behind==0", q)
	}
}

func TestFilter_PR(t *testing.T) {
	open := newFilterInstance(t, "o", "b")
	open.SetPRStatus(&git.PRStatus{HasPR: true, State: "OPEN"})
	merged := newFilterInstance(t, "m", "b")
	merged.SetPRStatus(&git.PRStatus{HasPR: true, State: "MERGED"})
	closed := newFilterInstance(t, "c", "b")
	closed.SetPRStatus(&git.PRStatus{HasPR: true, State: "CLOSED"})
	none := newFilterInstance(t, "n", "b")
	none.SetPRStatus(&git.PRStatus{HasPR: false})
	unknown := newFilterInstance(t, "u", "b") // nil prStatus

	require.True(t, ParseFilter("pr:open").Matches(open))
	require.False(t, ParseFilter("pr:open").Matches(none))
	require.False(t, ParseFilter("pr:open").Matches(merged), "merged is not open")
	require.False(t, ParseFilter("pr:open").Matches(closed), "closed is not open")

	require.True(t, ParseFilter("pr:merged").Matches(merged))
	require.False(t, ParseFilter("pr:merged").Matches(open), "open is not merged")
	require.False(t, ParseFilter("pr:merged").Matches(closed), "closed is not merged")
	require.False(t, ParseFilter("pr:merged").Matches(none), "none is not merged")

	require.True(t, ParseFilter("pr:closed").Matches(closed))
	require.False(t, ParseFilter("pr:closed").Matches(open), "open is not closed")
	require.False(t, ParseFilter("pr:closed").Matches(merged), "merged is not closed")
	require.False(t, ParseFilter("pr:closed").Matches(none), "none is not closed")

	require.True(t, ParseFilter("pr:none").Matches(none))
	require.True(t, ParseFilter("pr:none").Matches(unknown), "nil prStatus is none")
	require.False(t, ParseFilter("pr:none").Matches(open))

	// Prefix / incremental.
	require.True(t, ParseFilter("pr:o").Matches(open))
	require.True(t, ParseFilter("pr:m").Matches(merged))
	require.True(t, ParseFilter("pr:c").Matches(closed))
	require.True(t, ParseFilter("pr:n").Matches(none))

	// Empty value is a no-op (match all) so "pr:" never blinks the list empty.
	require.True(t, ParseFilter("pr:").Matches(open))
	require.True(t, ParseFilter("pr:").Matches(merged))
	require.True(t, ParseFilter("pr:").Matches(closed))
	require.True(t, ParseFilter("pr:").Matches(none))

	// A value prefixing no known state matches nothing.
	require.False(t, ParseFilter("pr:xyz").Matches(open))
}

func TestFilter_Account(t *testing.T) {
	work := newFilterInstance(t, "deploy", "b")
	work.SetClaudeAccount("work", "", false)
	personal := newFilterInstance(t, "sideproj", "b")
	personal.SetClaudeAccount("personal", "", false)
	none := newFilterInstance(t, "legacy", "b") // no account resolved

	require.True(t, ParseFilter("account:work").Matches(work))
	require.False(t, ParseFilter("account:work").Matches(personal))
	require.True(t, ParseFilter("account:wo").Matches(work), "prefix match")
	require.True(t, ParseFilter("ACCOUNT:WORK").Matches(work), "case-insensitive")
	require.True(t, ParseFilter("account:none").Matches(none), "none matches the empty account")
	require.False(t, ParseFilter("account:none").Matches(work))
	require.True(t, ParseFilter("account:").Matches(personal), "empty value is a no-op")
}

func TestFilter_StatusPending(t *testing.T) {
	pending := newFilterInstance(t, "sub", "b")
	pending.SetStatus(Pending)
	paused := newFilterInstance(t, "wip", "b")
	paused.SetStatus(Paused)
	ready := newFilterInstance(t, "done", "b")
	ready.SetStatus(Ready)

	require.True(t, ParseFilter("status:pending").Matches(pending))
	require.False(t, ParseFilter("status:pending").Matches(paused))
	require.False(t, ParseFilter("status:pending").Matches(ready))

	// "p" is a prefix of both "pending" and "paused".
	require.True(t, ParseFilter("status:p").Matches(pending))
	require.True(t, ParseFilter("status:p").Matches(paused))

	// "pe" narrows to pending; "pa" narrows to paused.
	require.True(t, ParseFilter("status:pe").Matches(pending))
	require.False(t, ParseFilter("status:pe").Matches(paused))
	require.True(t, ParseFilter("status:pa").Matches(paused))
	require.False(t, ParseFilter("status:pa").Matches(pending))
}

func TestFilter_SubstringMatchesNote(t *testing.T) {
	inst := newFilterInstance(t, "session-alpha", "feat/alpha")
	inst.SetNote("blocked on review")

	require.True(t, ParseFilter("blocked").Matches(inst), "note substring match")
	require.True(t, ParseFilter("BLOCKED").Matches(inst), "note match is case-insensitive")
	require.True(t, ParseFilter("review").Matches(inst), "partial note match")
	require.False(t, ParseFilter("deploy").Matches(inst), "non-matching substring")

	// Bare substring still matches title/branch too.
	require.True(t, ParseFilter("alpha").Matches(inst), "title still matched")
}

func TestFilter_Note(t *testing.T) {
	tagged := newFilterInstance(t, "s", "b")
	tagged.SetNote("fix auth")
	other := newFilterInstance(t, "s2", "b")
	other.SetNote("blocked")
	empty := newFilterInstance(t, "s3", "b") // no note

	require.True(t, ParseFilter("note:fix").Matches(tagged), "prefix match")
	require.False(t, ParseFilter("note:fix").Matches(other), "different note does not match")
	require.True(t, ParseFilter("NOTE:FIX").Matches(tagged), "case-insensitive")

	// note: is a *prefix* predicate, not a substring one: "auth" is a substring of
	// "fix auth" but not a prefix, so note:auth must not match. (This isolates
	// noteTerm from substringTerm, which does match "auth" against the note.)
	require.False(t, ParseFilter("note:auth").Matches(tagged), "note: matches by prefix, not substring")

	// A multi-word query splits on whitespace into ANDed terms: note:fix (the
	// prefix predicate) AND auth (a plain substring, which now also scans the note).
	require.True(t, ParseFilter("note:fix auth").Matches(tagged), "note:fix AND substring auth")

	// Empty value is a no-op (match all) so "note:" never blinks the list empty.
	require.True(t, ParseFilter("note:").Matches(tagged))
	require.True(t, ParseFilter("note:").Matches(other))
	require.True(t, ParseFilter("note:").Matches(empty))

	// A session with no note does not match a specific note predicate.
	require.False(t, ParseFilter("note:fix").Matches(empty))
}

func TestFilter_Effort(t *testing.T) {
	low := newFilterInstance(t, "routine", "b")
	low.SetEffortMeta("low")
	high := newFilterInstance(t, "refactor", "b")
	high.SetEffortMeta("high")
	maxed := newFilterInstance(t, "hardest", "b")
	maxed.SetEffortMeta("max")
	medium := newFilterInstance(t, "mid", "b")
	medium.SetEffortMeta("medium")
	xhigh := newFilterInstance(t, "deepest", "b")
	xhigh.SetEffortMeta("xhigh")
	none := newFilterInstance(t, "unknown", "b") // no effort set

	require.True(t, ParseFilter("effort:low").Matches(low))
	require.False(t, ParseFilter("effort:low").Matches(high))
	require.False(t, ParseFilter("effort:low").Matches(none))

	require.True(t, ParseFilter("effort:high").Matches(high))
	require.False(t, ParseFilter("effort:high").Matches(low))

	// The match is a *prefix*, not a substring: "high" is contained in "xhigh" but
	// does not prefix it, so an effort:high filter must leave xhigh sessions out.
	// This is the only pair in the level set where the two differ, so it is the
	// only assertion that pins prefix semantics — without it, swapping
	// effortTerm's strings.HasPrefix for strings.Contains passes the whole test.
	require.False(t, ParseFilter("effort:high").Matches(xhigh))
	require.True(t, ParseFilter("effort:x").Matches(xhigh))
	require.True(t, ParseFilter("effort:xhigh").Matches(xhigh))
	require.False(t, ParseFilter("effort:x").Matches(high))

	require.True(t, ParseFilter("effort:max").Matches(maxed))
	require.False(t, ParseFilter("effort:max").Matches(low))

	// "m" is a prefix of both "medium" and "max".
	require.True(t, ParseFilter("effort:m").Matches(medium))
	require.True(t, ParseFilter("effort:m").Matches(maxed))
	require.False(t, ParseFilter("effort:m").Matches(low))

	// "me" narrows to medium only; "ma" narrows to max only.
	require.True(t, ParseFilter("effort:me").Matches(medium))
	require.False(t, ParseFilter("effort:me").Matches(maxed))
	require.True(t, ParseFilter("effort:ma").Matches(maxed))
	require.False(t, ParseFilter("effort:ma").Matches(medium))

	// Case-insensitive on the query side.
	require.True(t, ParseFilter("EFFORT:LOW").Matches(low))

	// ...and on the value side: EffortInfo is raw hook truth, deliberately
	// unvalidated (session/effort.go), so a level can arrive in any case. This
	// pins effortTerm's own strings.ToLower — without it that call is dead.
	shouty := newFilterInstance(t, "shouty", "b")
	shouty.SetEffortMeta("HIGH")
	require.True(t, ParseFilter("effort:high").Matches(shouty))

	// Empty value is a no-op (match all) so "effort:" never blinks the list empty.
	require.True(t, ParseFilter("effort:").Matches(low))
	require.True(t, ParseFilter("effort:").Matches(none))

	// A value prefixing no known level matches nothing.
	require.False(t, ParseFilter("effort:xyz").Matches(low))

	// "none" is the sentinel for sessions with no resolved effort, mirroring
	// account:none / pr:none.
	require.True(t, ParseFilter("effort:none").Matches(none))
	require.False(t, ParseFilter("effort:none").Matches(low))

	// The sentinel is an EXACT match, not a prefix one, mirroring account:none:
	// EffortInfo is unvalidated, so a level a newer CLI resolves could begin with
	// "n" and must stay reachable rather than being swallowed to mean no-effort.
	novel := newFilterInstance(t, "novel", "b")
	novel.SetEffortMeta("nova")
	require.True(t, ParseFilter("effort:no").Matches(novel))
	require.False(t, ParseFilter("effort:no").Matches(none))

	// Sessions with no effort match only the empty predicate and the sentinel,
	// never a specific level.
	require.False(t, ParseFilter("effort:low").Matches(none))
}

func TestFilter_MixedPredicateAndSubstringANDed(t *testing.T) {
	inst := newFilterInstance(t, "feat login", "feat/login")
	inst.SetStatus(Ready)
	inst.SetDiffStats(&git.DiffStats{Dirty: true, Behind: 2})

	require.True(t, ParseFilter("feat dirty").Matches(inst))
	require.True(t, ParseFilter("status:ready behind").Matches(inst))
	require.False(t, ParseFilter("status:paused dirty").Matches(inst), "status fails the AND")
	require.False(t, ParseFilter("login pr:open").Matches(inst), "no PR fails the AND")
}
