package session

import (
	"context"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/session/git"
	"github.com/ZviBaratz/atrium/session/tmux"
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

	first, minted := inst.ClaimTerminalSessionName()
	assert.Equal(t, inst.MintTerminalSessionName(), first, "a first claim takes the minted name")
	assert.True(t, minted, "a first claim must report that it minted")
	assert.Equal(t, first, inst.TerminalSessionName(), "a claim is what makes the name owned")

	again, mintedAgain := inst.ClaimTerminalSessionName()
	assert.Equal(t, first, again, "a second claim must not re-mint")
	assert.False(t, mintedAgain, "and must not report a mint it did not make")
}

// minted is what a failed create rolls back on, so it has to answer "did THIS call mint it"
// rather than "is the name unused". A claim against a name the instance already owned —
// restored from state, or claimed by an earlier create — reports false even though this
// process never minted it, because a shell may be sitting on it and the owned name is the
// only record of that shell.
func TestClaimReportsNoMintForANameOwnedBeforeIt(t *testing.T) {
	inst := startedForTermName(t, "claim-restored")
	inst.termName = "atrium_claim-restored" + TermSessionSuffix

	name, minted := inst.ClaimTerminalSessionName()
	assert.Equal(t, "atrium_claim-restored"+TermSessionSuffix, name)
	assert.False(t, minted, "a name owned before the claim was not minted by it")
}

// The property the whole fix rests on: a deep rename moves the tmux name the shell was
// named after, and the shell's own name does not follow it. Without this the pane's cache
// key, its reap generation and every later lookup move off a live shell (#708).
func TestClaimedTerminalSessionNameSurvivesARename(t *testing.T) {
	inst := startedForTermName(t, "claim-rename")

	claimed, _ := inst.ClaimTerminalSessionName()
	require.NotEmpty(t, claimed)

	renamed, err := inst.Rename("claim-rename-after")
	require.NoError(t, err)
	inst.AdoptRename(renamed)

	require.NotEqual(t, claimed, inst.MintTerminalSessionName(),
		"precondition: the rename must have moved the name a derivation would produce")
	assert.Equal(t, claimed, inst.TerminalSessionName(), "a rename must not move the owned name")
	again, minted := inst.ClaimTerminalSessionName()
	assert.Equal(t, claimed, again, "and a later claim must not re-mint it")
	assert.False(t, minted, "nor report a mint")
}

// Release is what keeps ownership from outliving the shell. A reaped shell frees the name,
// so the next one is named after the title the session has by then rather than the one it
// had when its first shell was opened — and so a renamed session stops squatting on a name
// a new session with its old title would mint (see OwnedSiblingCollides).
func TestReleaseTerminalSessionNameRemintsFromTheCurrentName(t *testing.T) {
	inst := startedForTermName(t, "claim-release")

	claimed, _ := inst.ClaimTerminalSessionName()
	renamed, err := inst.Rename("claim-release-after")
	require.NoError(t, err)
	inst.AdoptRename(renamed)

	inst.ReleaseTerminalSessionName()
	assert.Empty(t, inst.TerminalSessionName(), "a release must give the name up")

	reclaimed, minted := inst.ClaimTerminalSessionName()
	assert.True(t, minted, "a claim after a release mints again")
	assert.NotEqual(t, claimed, reclaimed, "a claim after a release must follow the current name")
	assert.Equal(t, inst.MintTerminalSessionName(), reclaimed)
}

// An instance with no tmux name has nothing to mint from, and a claim must not invent one:
// the pane reads "" as "no shell can be cached for this" and returns before any tmux call.
func TestTerminalSessionNameIsEmptyWithoutATmuxName(t *testing.T) {
	inst := &Instance{Title: "never-started"}

	assert.Empty(t, inst.MintTerminalSessionName())
	name, minted := inst.ClaimTerminalSessionName()
	assert.Empty(t, name)
	assert.False(t, minted, "a claim with nothing to mint from must not report a mint")
	assert.Empty(t, inst.TerminalSessionName(), "a claim that minted nothing must own nothing")
}

// What the minted name has to satisfy, asserted as the two CONSEQUENCES rather than as its
// spelling. The suffix has one home now, so any test comparing the mint against
// TermSessionSuffix restates ClaimTerminalSessionName's own expression and cannot fail —
// including against a mutation of the const, which moves both sides together.
//
// These two can. The guards reserve the name by iterating reservedTmuxSuffixes, and
// CleanupSessions sweeps it by matching tmux.Prefix() and nothing else (session/runcmd.go
// says so for both siblings), so a mint that stopped satisfying either would ship a shell
// that a new session can be named on top of, or one that `atrium reset` walks past.
func TestTheMintedShellNameIsReservedAndSwept(t *testing.T) {
	inst := startedForTermName(t, "suffix-drift")
	minted, _ := inst.ClaimTerminalSessionName()
	require.NotEmpty(t, minted)

	assert.True(t, DerivedTmuxNameCollides(minted, inst.TmuxSessionName()),
		"a new session must not be allowed the name the shell is minted under")
	assert.True(t, strings.HasPrefix(minted, tmux.Prefix()),
		"CleanupSessions matches the shared prefix and knows nothing of the suffix, "+
			"so a mint that loses the prefix is a shell `atrium reset` cannot sweep")
}

// The half of the restart property that lives here: the owned name reaches the next process.
// Without it a restart after a rename mints a second shell beside the one still running and
// strands it for good, since nothing sweeps shells at exit.
func TestTermSessionSurvivesTheStateRoundTrip(t *testing.T) {
	inst := startedForTermName(t, "roundtrip")
	claimed, _ := inst.ClaimTerminalSessionName()
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
	reclaimed, _ := restored.ClaimTerminalSessionName()
	assert.Equal(t, inst.MintTerminalSessionName(), reclaimed,
		"the first claim after an upgrade must land on the name the old code derived")
}
