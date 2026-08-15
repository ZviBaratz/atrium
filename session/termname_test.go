package session

import (
	"context"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/session/git"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// startedForTermName builds a started instance with a real worktree and a fake tmux
// session, the same shape session's other rename tests use (instance_rename_test.go).
func startedForTermName(t *testing.T, title string) *Instance {
	t.Helper()
	repoPath := renameTestRepo(t)
	wt, _, err := git.NewWorktree(context.Background(), repoPath, title)
	require.NoError(t, err)
	require.NoError(t, wt.Setup())
	return &Instance{
		Title:       title,
		status:      Running,
		started:     true,
		gitWorktree: wt,
		tmuxSession: liveTmux(t, title),
		tmuxName:    "atrium_" + title,
		Branch:      wt.GetBranchName(),
	}
}

// The mint is the convention; the claim is what freezes it. The mint stays reachable
// (terminalKey falls back to it before any shell exists) but only a claim STORES one, and
// only the first claim mints — which is what makes "the name it was created under is the
// name it is reaped under" true rather than usually true.
func TestClaimTerminalSessionNameMintsOnceAndKeepsIt(t *testing.T) {
	inst := startedForTermName(t, "claim-once")

	require.Empty(t, inst.TerminalSessionName(), "nothing claimed, nothing owned")
	require.Equal(t, "atrium_claim-once"+TermSessionSuffix, inst.MintTerminalSessionName())

	first := inst.ClaimTerminalSessionName()
	assert.Equal(t, inst.MintTerminalSessionName(), first, "a first claim takes the minted name")
	assert.Equal(t, first, inst.TerminalSessionName(), "a claim is what makes the name owned")
	assert.Equal(t, first, inst.ClaimTerminalSessionName(), "a second claim must not re-mint")
}

// The property the whole fix rests on: a deep rename moves the tmux name the shell was
// named after, and the shell's own name does not follow it. Without this the pane's cache
// key, its reap generation and every later lookup move off a live shell (#708).
func TestClaimedTerminalSessionNameSurvivesARename(t *testing.T) {
	inst := startedForTermName(t, "claim-rename")

	claimed := inst.ClaimTerminalSessionName()
	require.NotEmpty(t, claimed)

	renamed, err := inst.Rename("claim-rename-after")
	require.NoError(t, err)
	inst.AdoptRename(renamed)

	require.NotEqual(t, claimed, inst.MintTerminalSessionName(),
		"precondition: the rename must have moved the name a derivation would produce")
	assert.Equal(t, claimed, inst.TerminalSessionName(), "a rename must not move the owned name")
	assert.Equal(t, claimed, inst.ClaimTerminalSessionName(), "and a later claim must not re-mint it")
}

// Release is what keeps ownership from outliving the shell. A reaped shell frees the name,
// so the next one is named after the title the session has by then rather than the one it
// had when its first shell was opened — and so a renamed session stops squatting on a name
// a new session with its old title would mint (see OwnedSiblingCollides).
func TestReleaseTerminalSessionNameRemintsFromTheCurrentName(t *testing.T) {
	inst := startedForTermName(t, "claim-release")

	claimed := inst.ClaimTerminalSessionName()
	renamed, err := inst.Rename("claim-release-after")
	require.NoError(t, err)
	inst.AdoptRename(renamed)

	inst.ReleaseTerminalSessionName()
	assert.Empty(t, inst.TerminalSessionName(), "a release must give the name up")

	reclaimed := inst.ClaimTerminalSessionName()
	assert.NotEqual(t, claimed, reclaimed, "a claim after a release must follow the current name")
	assert.Equal(t, inst.MintTerminalSessionName(), reclaimed)
}

// An instance with no tmux name has nothing to mint from, and a claim must not invent one:
// the pane reads "" as "no shell can be cached for this" and returns before any tmux call.
func TestTerminalSessionNameIsEmptyWithoutATmuxName(t *testing.T) {
	inst := &Instance{Title: "never-started"}

	assert.Empty(t, inst.MintTerminalSessionName())
	assert.Empty(t, inst.ClaimTerminalSessionName())
	assert.Empty(t, inst.TerminalSessionName(), "a claim that minted nothing must own nothing")
}

// The suffix has one home. It used to be a bare "_term" in ui and another in collision.go,
// coupled by a prose cross-reference and nothing that could fail when they diverged: the
// collision fixtures pin their own spelling, so changing the one in ui alone would have
// minted shells under a suffix the guards did not reserve, silently.
func TestTermSessionSuffixIsTheReservedOne(t *testing.T) {
	assert.Contains(t, reservedTmuxSuffixes, TermSessionSuffix,
		"the suffix the shell is minted under must be the one the collision guards reserve")

	inst := startedForTermName(t, "suffix-drift")
	minted := inst.ClaimTerminalSessionName()
	assert.True(t, strings.HasSuffix(minted, TermSessionSuffix),
		"the minted shell name must carry the reserved suffix")
	assert.Equal(t, inst.TmuxSessionName()+TermSessionSuffix, minted,
		"and must be exactly <tmux name><suffix>, which is what DerivedTmuxNameCollides assumes")
}

// The half of the restart property that lives here: the owned name reaches the next process.
// Without it a restart after a rename mints a second shell beside the one still running and
// strands it for good, since nothing sweeps shells at exit.
func TestTermSessionSurvivesTheStateRoundTrip(t *testing.T) {
	inst := startedForTermName(t, "roundtrip")
	claimed := inst.ClaimTerminalSessionName()
	require.NotEmpty(t, claimed)

	data := inst.ToInstanceData()
	require.Equal(t, claimed, data.TermSession, "the owned name must be persisted")

	restored, err := FromInstanceData(context.Background(), data, "test-")
	require.NoError(t, err)
	assert.Equal(t, claimed, restored.TerminalSessionName(),
		"a restored session must own the shell name it left running")
}

// A state.json predating the field decodes to "", which is the right answer rather than a
// gap: a pre-upgrade shell sits on the name the mint produces anyway, so the first claim
// after the upgrade lands on it and adopts it.
func TestAbsentTermSessionClaimsTheDerivedName(t *testing.T) {
	inst := startedForTermName(t, "pre-upgrade")
	data := inst.ToInstanceData()
	data.TermSession = ""

	restored, err := FromInstanceData(context.Background(), data, "test-")
	require.NoError(t, err)
	assert.Empty(t, restored.TerminalSessionName())
	assert.Equal(t, inst.MintTerminalSessionName(), restored.ClaimTerminalSessionName(),
		"the first claim after an upgrade must land on the name the old code derived")
}
