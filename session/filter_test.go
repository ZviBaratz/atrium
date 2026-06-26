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

func TestFilter_MixedPredicateAndSubstringANDed(t *testing.T) {
	inst := newFilterInstance(t, "feat login", "feat/login")
	inst.SetStatus(Ready)
	inst.SetDiffStats(&git.DiffStats{Dirty: true, Behind: 2})

	require.True(t, ParseFilter("feat dirty").Matches(inst))
	require.True(t, ParseFilter("status:ready behind").Matches(inst))
	require.False(t, ParseFilter("status:paused dirty").Matches(inst), "status fails the AND")
	require.False(t, ParseFilter("login pr:open").Matches(inst), "no PR fails the AND")
}
