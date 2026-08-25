package main

// cli_new_account_test.go — `atrium new --account` at the producer (#854).
//
// The flag's whole purpose is that ONE thing decides the account, so what is asserted
// here is deliberately thin: the CLI resolves the name against claude_accounts as a
// courtesy — a typo answered at the terminal that made it rather than in a receipt
// minutes later — and spools the canonical NAME. It spools no directory, because the
// draining atrium resolves the name against its own config and takes both the injected
// CLAUDE_CONFIG_DIR and the stamped account off that one entry
// (app.TestCreateDrainPinnedAccountStampsTheAccountItRuns is where the two are held
// together).
//
// The refusals are the other half, and one of them is not a courtesy: a --program that
// sets CLAUDE_CONFIG_DIR itself beats Atrium's injection, so a pin combined with one
// would stamp a name onto a session running somewhere else — the very divergence.

import (
	"testing"

	"github.com/ZviBaratz/atrium/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// accountsConfig persists fanOutConfig's profile table plus a two-member pool whose
// members share one rule, which is the config the divergence needs: routing cannot be
// steered, so the account is whatever the caller pins or whatever config order happens to
// put first.
func accountsConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := fanOutConfig(t)
	cfg.ClaudeAccounts = []config.ClaudeAccount{
		{Name: "zvi.baratz", ConfigDir: "~/.claude-work", Pool: "quantivly", PathMatches: []string{"/"}},
		{Name: "zvi.baratz2", ConfigDir: "~/.claude-work2", Pool: "quantivly", PathMatches: []string{"/"}},
	}
	require.NoError(t, config.SaveConfig(cfg))
	return cfg
}

// TestNewAccountSpoolsTheResolvedName: the name reaches the spool, and nothing else does
// — no directory, so there is no second value on the wire free to disagree with the
// config that has to honour it.
func TestNewAccountSpoolsTheResolvedName(t *testing.T) {
	sandboxDataDir(t)
	accountsConfig(t)

	_, _, err := newSession(t, newRequest{title: "fix-auth", path: tempRepo(t), account: "zvi.baratz"})
	require.NoError(t, err)

	entries := spooledCreates(t)
	require.Len(t, entries, 1)
	assert.Equal(t, "zvi.baratz", entries[0].Request.Account)
}

// TestNewUnpinnedAccountSpoolsEmpty holds the pre-flag contract: with no --account the
// record carries no account and the draining atrium routes, which is what every request
// this command has ever written did.
func TestNewUnpinnedAccountSpoolsEmpty(t *testing.T) {
	sandboxDataDir(t)
	accountsConfig(t)

	_, _, err := newSession(t, newRequest{title: "fix-auth", path: tempRepo(t)})
	require.NoError(t, err)

	entries := spooledCreates(t)
	require.Len(t, entries, 1)
	assert.Equal(t, "", entries[0].Request.Account)
}

// TestNewAccountRidesEveryVariant: one account for the whole fan-out. A bake-off compares
// programs, so splitting its members across two logins would put the thing measured and
// the thing paying for it on different accounts; a caller who wants the spread gets it by
// not passing the flag, which leaves the pool rotating.
func TestNewAccountRidesEveryVariant(t *testing.T) {
	sandboxDataDir(t)
	accountsConfig(t)

	_, _, err := newSession(t, newRequest{
		title: "bake", path: gitRepoWithBranches(t),
		variants: "claude:1,codex:1", variantsSet: true,
		account: "zvi.baratz2",
	})
	require.NoError(t, err)

	entries := spooledCreates(t)
	require.Len(t, entries, 2)
	for _, e := range entries {
		assert.Equal(t, "zvi.baratz2", e.Request.Account, "%s", e.Request.Title)
	}
}

// TestNewAccountRejectsAnUnknownName: refused before anything is spooled, and the message
// names the accounts that exist — a misspelling is the mistake being made, so the
// vocabulary is the actionable half.
func TestNewAccountRejectsAnUnknownName(t *testing.T) {
	sandboxDataDir(t)
	accountsConfig(t)

	_, _, err := newSession(t, newRequest{title: "fix-auth", path: tempRepo(t), account: "zvi.baratz3"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "zvi.baratz3")
	assert.Contains(t, err.Error(), "zvi.baratz, zvi.baratz2")
	assert.Empty(t, spooledCreates(t))
}

// TestNewAccountRejectsAnAmbiguousName: two entries under one name make the pin
// unanswerable, and guessing would inject one login's directory under a name the caller
// may have meant for the other — the divergence, re-introduced by its own fix.
func TestNewAccountRejectsAnAmbiguousName(t *testing.T) {
	sandboxDataDir(t)
	cfg := fanOutConfig(t)
	cfg.ClaudeAccounts = []config.ClaudeAccount{
		{Name: "work", ConfigDir: "~/.claude-a"},
		{Name: "work", ConfigDir: "~/.claude-b"},
	}
	require.NoError(t, config.SaveConfig(cfg))

	_, _, err := newSession(t, newRequest{title: "fix-auth", path: tempRepo(t), account: "work"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "work")
	assert.Empty(t, spooledCreates(t))
}

// TestNewAccountRejectsANoAccountsConfig names the dormant case as itself: with no
// claude_accounts nothing would have matched, which is a different mistake from a typo and
// gets a different sentence.
func TestNewAccountRejectsANoAccountsConfig(t *testing.T) {
	sandboxDataDir(t)
	fanOutConfig(t)

	_, _, err := newSession(t, newRequest{title: "fix-auth", path: tempRepo(t), account: "zvi.baratz"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no claude_accounts")
	assert.Empty(t, spooledCreates(t))
}

// TestNewAccountRefusesAProgramThatSetsTheConfigDir is the contradiction, and it is the
// one refusal here that is not a courtesy: the env a program sets is closer to the process
// than the one Atrium injects, so this command line would create a session stamped
// zvi.baratz and running on ~/.claude-work2. Refused rather than resolved either way —
// honouring the pin means rewriting the caller's own program string, honouring the program
// means accepting a flag and ignoring it.
func TestNewAccountRefusesAProgramThatSetsTheConfigDir(t *testing.T) {
	sandboxDataDir(t)
	accountsConfig(t)

	_, _, err := newSession(t, newRequest{
		title: "fix-auth", path: tempRepo(t), account: "zvi.baratz",
		program: "env CLAUDE_CONFIG_DIR=/home/zvi/.claude-work2 claude",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "CLAUDE_CONFIG_DIR")
	assert.Contains(t, err.Error(), "zvi.baratz")
	assert.Empty(t, spooledCreates(t))
}

// TestNewProgramSettingTheConfigDirWarnsAndProceeds: the pre-flag workaround still works —
// it is in active use, and breaking it would strand the callers this flag is for — but it
// stops being silent, and the warning names the flag that makes the two agree.
func TestNewProgramSettingTheConfigDirWarnsAndProceeds(t *testing.T) {
	sandboxDataDir(t)
	accountsConfig(t)

	_, stderr, err := newSession(t, newRequest{
		title: "fix-auth", path: tempRepo(t),
		program: "env CLAUDE_CONFIG_DIR=/home/zvi/.claude-work claude",
	})
	require.NoError(t, err, "the workaround must keep working")

	entries := spooledCreates(t)
	require.Len(t, entries, 1)
	assert.Equal(t, "env CLAUDE_CONFIG_DIR=/home/zvi/.claude-work claude", entries[0].Request.Program,
		"spooled verbatim: nothing rewrites a caller's program string")
	assert.Empty(t, entries[0].Request.Account, "and no account is invented for it")
	assert.Contains(t, stderr, "CLAUDE_CONFIG_DIR")
	assert.Contains(t, stderr, "--account", "the warning names the supported way to pin one")
}
