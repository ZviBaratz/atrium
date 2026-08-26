package retire

import (
	"errors"
	"testing"

	"github.com/ZviBaratz/atrium/session/git"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// clean is a DiffStats a successful RepoStats would return for a session whose
// tree holds nothing at risk: no uncommitted changes, nothing unpushed, no error.
// Tests that care about one field override it, so a test asserting on Dirty is the
// only one that mentions Dirty.
func clean() *git.DiffStats {
	return &git.DiffStats{Commits: 4, Behind: 2}
}

func TestGateClearsACleanIdleSession(t *testing.T) {
	v := Gate(clean(), false)

	assert.True(t, v.Allowed(), "a clean, pushed, idle session is the one shape that retires")
	assert.Equal(t, Clear, v.Condition)
	assert.Empty(t, v.Reason(), "a cleared verdict has nothing to explain")
}

// TestGateRefusesStatsItCouldNotEstablish is the whole point of gating on a
// recomputed DiffStats rather than a decoded one, and it is #835's second trap
// stated as a property.
//
// A DiffStats carrying an Error is byte-identical to a clean one in every field
// the gate reads: Dirty is Go's zero value (false) and Unpushed is 0. So a Gate
// that checked those two and not Error would clear a session whose tree it never
// managed to look at, which is precisely the "never computed is indistinguishable
// from genuinely clean" failure. Deleting the Error branch makes this fail; no
// other test here does.
func TestGateRefusesStatsItCouldNotEstablish(t *testing.T) {
	stats := &git.DiffStats{Error: errors.New("fatal: not a git repository")}
	require.False(t, stats.Dirty, "the trap: an errored stats reads as clean")
	require.Zero(t, stats.Unpushed, "and as fully pushed")

	v := Gate(stats, false)

	assert.False(t, v.Allowed())
	assert.Equal(t, Unestablished, v.Condition)
	assert.Contains(t, v.Reason(), "could not be established")
}

// TestGateRefusesNilStats: nil is not "nothing at risk", it is "nothing was
// computed", and it reaches the same refusal as an errored stats rather than
// panicking or clearing.
func TestGateRefusesNilStats(t *testing.T) {
	v := Gate(nil, false)

	assert.False(t, v.Allowed())
	assert.Equal(t, Unestablished, v.Condition)
}

func TestGateRefusesUncommittedWork(t *testing.T) {
	stats := clean()
	stats.Dirty = true

	v := Gate(stats, false)

	assert.False(t, v.Allowed())
	assert.Equal(t, Uncommitted, v.Condition)
	assert.Contains(t, v.Reason(), "uncommitted")
}

// TestGateRefusesUnpushedCommitsAndCountsThem: the count rides the verdict so the
// refusal can name it. A caller cannot recover it — the gate is handed the stats
// and the caller is handed only the verdict — so dropping it here makes the
// message vaguer than the TUI's own kill warning for the same session.
func TestGateRefusesUnpushedCommitsAndCountsThem(t *testing.T) {
	stats := clean()
	stats.Unpushed = 3

	v := Gate(stats, false)

	assert.False(t, v.Allowed())
	assert.Equal(t, Unpushed, v.Condition)
	assert.Contains(t, v.Reason(), "3")
}

func TestGateRefusesABusyAgent(t *testing.T) {
	v := Gate(clean(), true)

	assert.False(t, v.Allowed())
	assert.Equal(t, Busy, v.Condition)
	assert.Contains(t, v.Reason(), "still working")
}

// TestGateReportsUnestablishedStatsAheadOfEveryOtherCondition pins the precedence
// the Error branch needs to be worth anything. With an Error set, every other field
// the gate reads is untrustworthy rather than merely inconvenient — so a stats that
// errored AND happens to carry a stale Dirty must be reported as unestablished, not
// as dirty. Reporting the wrong one would send the caller to fix a condition that
// was never measured.
func TestGateReportsUnestablishedStatsAheadOfEveryOtherCondition(t *testing.T) {
	stats := &git.DiffStats{Error: errors.New("boom"), Dirty: true, Unpushed: 9}

	v := Gate(stats, true)

	assert.Equal(t, Unestablished, v.Condition)
}

// TestGateReportsWorkAtRiskAheadOfBusyness: Dirty and Unpushed say work would be
// destroyed, Busy only says the timing is wasteful. A caller acts on the first
// reason it is given, so the destructive one has to be the one it hears.
func TestGateReportsWorkAtRiskAheadOfBusyness(t *testing.T) {
	dirty := clean()
	dirty.Dirty = true
	assert.Equal(t, Uncommitted, Gate(dirty, true).Condition)

	unpushed := clean()
	unpushed.Unpushed = 1
	assert.Equal(t, Unpushed, Gate(unpushed, true).Condition)
}

// TestEveryRefusalNamesItsCondition keeps the refusal wording from going missing
// for a condition added later: a Verdict a caller cannot explain is one the agent
// has no way to act on. Clear is the only condition allowed to say nothing.
func TestEveryRefusalNamesItsCondition(t *testing.T) {
	for _, c := range []Condition{Unestablished, Uncommitted, Unpushed, Busy} {
		v := Verdict{Condition: c}
		assert.False(t, v.Allowed(), "%v is a refusal", c)
		assert.NotEmpty(t, v.Reason(), "%v must explain itself", c)
	}
}
