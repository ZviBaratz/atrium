package app

// create_drain_account_test.go — `atrium new --account` at the drain (#854).
//
// The defect the flag closes is a DIVERGENCE, not a wrong pick: routing chose one pool
// member and stamped it, while the CLAUDE_CONFIG_DIR the session actually ran under came
// from an `env CLAUDE_CONFIG_DIR=…` in the program string, and Config.FindClaudeAccount
// anchors an existing session on the dir it was born with — so the wrong label was
// durable. What has to be proved is therefore not "the pin is honoured" but "the two
// halves agree", which is why every test here reads both.
//
// The fixture needs two accounts sharing a pool AND the same path_matches, and that is
// load-bearing rather than thorough: with one candidate, routing picks the pinned account
// by luck and every assertion below passes against a drain that ignores Account entirely.

import (
	"path/filepath"
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/internal/outbox"
	"github.com/ZviBaratz/atrium/session"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sharedPoolDrainHome is a drain home whose two accounts are indistinguishable to
// routing: one pool, and a path_matches ("/") that every absolute path contains. Routing
// resolves first-match in config order, so work-1 is what an unpinned request gets and
// work-2 is the member only a pin can reach.
func sharedPoolDrainHome(t *testing.T) *home {
	t.Helper()
	h := drainHome(t)
	// Under the test's own temp root, not a fixed /tmp path: creating a session runs the
	// identity check, which READS <config_dir>/.claude.json before it looks at whether
	// anything is expected of it (config.CheckIdentity), so a hardcoded dir would have
	// these tests opening files outside the sandbox.
	root := t.TempDir()
	h.appConfig.ClaudeAccounts = []config.ClaudeAccount{
		{Name: "work-1", ConfigDir: filepath.Join(root, "claude-work-1"), Pool: "quantivly", PathMatches: []string{"/"}},
		{Name: "work-2", ConfigDir: filepath.Join(root, "claude-work-2"), Pool: "quantivly", PathMatches: []string{"/"}},
	}
	return h
}

// accountDir is the directory the named fixture account declares — which is what the
// assertions below compare an instance's injected dir against, because "the pin's own
// entry decided it" is the invariant, not any particular path.
func accountDir(t *testing.T, h *home, name string) string {
	t.Helper()
	acct, ok := h.appConfig.ClaudeAccountNamed(name)
	require.True(t, ok, "fixture account %q", name)
	return acct.ResolvedConfigDir()
}

// drainOneCreate spools a request, drains it, and returns the instance it created.
func drainOneCreate(t *testing.T, h *home, r outbox.Request) *session.Instance {
	t.Helper()
	r.Title, r.Path = "fix-auth", t.TempDir()
	spoolCreate(t, r)
	require.NotNil(t, h.drainCreateRequests(), "the request must be created, not refused")
	inst := titled(h, "fix-auth")
	require.NotNil(t, inst, "the session must be in the list")
	return inst
}

// TestCreateDrainRoutesWhenNoAccountIsPinned is the other half of the fixture, and it is
// what makes every assertion below mean something: it fixes which member routing picks,
// so a pin naming the OTHER one cannot be satisfied by routing doing what it always did.
func TestCreateDrainRoutesWhenNoAccountIsPinned(t *testing.T) {
	h := sharedPoolDrainHome(t)

	inst := drainOneCreate(t, h, outbox.Request{})

	assert.Equal(t, "work-1", inst.ClaudeAccountName(),
		"unpinned, the first account whose rules match is the one routing stamps")
	assert.Equal(t, accountDir(t, h, "work-1"), inst.ClaudeConfigDir())
}

// TestCreateDrainPinnedAccountStampsTheAccountItRuns is the invariant #854 asks for: for
// a session created with --account naming the NON-routed member of a shared pool, the
// account Atrium records equals the account the process runs under.
//
// "The account the process runs under" is read here as the injected CLAUDE_CONFIG_DIR,
// which is what decides the login: Instance.Start hands the dir to
// tmux.Session.SetClaudeConfigDir, which puts it on the `tmux new-session` command line
// ahead of the program (session/tmux's TestStartSessionInjectsBothConfigDirs asserts that
// ordering, and TestStartSessionConfigDirReachesPane confirms against a real server that
// the session environment ends up holding it). The dir on the Instance is the value that
// reaches all of it, and it is what this test can see without launching tmux.
//
// The last assertion is the durability half. FindClaudeAccount is the anchor that made
// the wrong label survive every relaunch — it resolves an existing session by the dir it
// was born with — so asking it, and getting the name that was stamped, is the statement
// that there is nothing left to diverge.
func TestCreateDrainPinnedAccountStampsTheAccountItRuns(t *testing.T) {
	h := sharedPoolDrainHome(t)

	inst := drainOneCreate(t, h, outbox.Request{Account: "work-2"})

	assert.Equal(t, "work-2", inst.ClaudeAccountName(), "the pin, not the routed member")
	assert.Equal(t, accountDir(t, h, "work-2"), inst.ClaudeConfigDir(),
		"and the dir injected at launch is that same account's")
	assert.Equal(t, "quantivly", inst.ClaudeAccountPool(),
		"a pinned session still clusters under its own declared pool")

	anchored, ok := h.appConfig.FindClaudeAccount(inst.ClaudeAccountName(), inst.ClaudeConfigDir())
	require.True(t, ok, "the stamped pair must resolve to a configured account at all")
	assert.Equal(t, inst.ClaudeAccountName(), anchored.Name,
		"the account the config-dir anchor finds is the account that was stamped")
}

// TestCreateDrainPinnedAccountCreatesOnALimitedOne: a pin is a deliberate choice and
// overrides availability, exactly as the create form's member pin does — otherwise
// `--account` would be refused for the pool state it was passed to escape. allExhausted
// is the gate that has to see it, which is why the pin goes onto the plan and not only
// into the selection handed to startNewSession.
func TestCreateDrainPinnedAccountCreatesOnALimitedOne(t *testing.T) {
	h := sharedPoolDrainHome(t)
	require.NoError(t, h.appState.SetAccountLimited("work-1", ""))
	require.NoError(t, h.appState.SetAccountLimited("work-2", ""))

	// No Force: an unpinned request into this pool is refused without it, so reaching a
	// session at all is the assertion, and the account is which one.
	inst := drainOneCreate(t, h, outbox.Request{Account: "work-2"})

	assert.Equal(t, "work-2", inst.ClaudeAccountName())
	assert.Equal(t, accountDir(t, h, "work-2"), inst.ClaudeConfigDir())
}

// TestCreateDrainRefusesAnUnknownAccount: an account name this config does not have is
// refused, never quietly downgraded to routing — routing is the answer the caller passed
// a flag to avoid, and delivering it silently is the whole defect. The receipt names the
// accounts that exist, because a misspelling is the mistake being made.
func TestCreateDrainRefusesAnUnknownAccount(t *testing.T) {
	h := sharedPoolDrainHome(t)
	path := spoolCreate(t, outbox.Request{Title: "fix-auth", Path: t.TempDir(), Account: "work-3"})

	refuseDrain(t, h)

	assert.Nil(t, titled(h, "fix-auth"), "nothing may be created on a fallback account")
	reason, ok := outbox.Rejection(path)
	require.True(t, ok, "the caller is owed a receipt")
	assert.Contains(t, reason, "work-3")
	assert.Contains(t, reason, "work-1, work-2", "and the names it could have said")
	assert.Contains(t, reason, "restarted",
		"and why a name the CLI just accepted can still miss here: this process read its "+
			"config at startup and never re-reads it, so an entry added since is invisible")
}

// TestCreateDrainRefusesAnAmbiguousAccount: two entries under one name make the pin
// unanswerable, and answering it anyway is the defect re-introduced by the thing meant to
// close it — one dir injected under a name the caller may have meant for the other login.
func TestCreateDrainRefusesAnAmbiguousAccount(t *testing.T) {
	h := drainHome(t)
	root := t.TempDir()
	h.appConfig.ClaudeAccounts = []config.ClaudeAccount{
		{Name: "work", ConfigDir: filepath.Join(root, "claude-a")},
		{Name: "work", ConfigDir: filepath.Join(root, "claude-b")},
	}
	path := spoolCreate(t, outbox.Request{Title: "fix-auth", Path: t.TempDir(), Account: "work"})

	refuseDrain(t, h)

	assert.Nil(t, titled(h, "fix-auth"))
	reason, ok := outbox.Rejection(path)
	require.True(t, ok)
	// The diagnosis, not the name: the message quotes the flag's value either way, so
	// asserting "work" would pass for a receipt that called this a misspelling — and it
	// would pass for a title conflict or a cap refusal too.
	assert.Contains(t, reason, "names 2 entries")
	assert.Contains(t, reason, "distinct names")
}

// TestCreateDrainRefusesAPinAgainstAProgramThatSetsTheConfigDir: a program that assigns
// CLAUDE_CONFIG_DIR is a second place deciding the login, and it is the place that wins,
// so a pin arriving beside one is refused rather than stamped over it. The CLI refuses the
// same pair, but a refusal there cannot stand in for this one — see the next test.
func TestCreateDrainRefusesAPinAgainstAProgramThatSetsTheConfigDir(t *testing.T) {
	h := sharedPoolDrainHome(t)
	path := spoolCreate(t, outbox.Request{
		Title: "fix-auth", Path: t.TempDir(), Account: "work-2",
		Program: "env CLAUDE_CONFIG_DIR=" + accountDir(t, h, "work-1") + " claude",
	})

	refuseDrain(t, h)

	assert.Nil(t, titled(h, "fix-auth"), "nothing may be stamped work-2 while running work-1")
	reason, ok := outbox.Rejection(path)
	require.True(t, ok)
	assert.Contains(t, reason, "CLAUDE_CONFIG_DIR")
	assert.Contains(t, reason, "work-2")
}

// TestCreateDrainRefusesAPinAgainstItsOwnDefaultProgram is why the guard cannot live only
// in the CLI. A request carrying no program of its own runs the DRAINING atrium's default
// — a string chosen when this TUI was launched, which the process that spooled the
// request never saw and could not have checked. That is the exact shape of the workaround
// #854 is about: the account written into the program, once, for every session.
func TestCreateDrainRefusesAPinAgainstItsOwnDefaultProgram(t *testing.T) {
	h := sharedPoolDrainHome(t)
	h.program = "env CLAUDE_CONFIG_DIR=" + accountDir(t, h, "work-1") + " claude"
	path := spoolCreate(t, outbox.Request{Title: "fix-auth", Path: t.TempDir(), Account: "work-2"})

	refuseDrain(t, h)

	assert.Nil(t, titled(h, "fix-auth"))
	reason, ok := outbox.Rejection(path)
	require.True(t, ok)
	assert.Contains(t, reason, "CLAUDE_CONFIG_DIR")
}

// TestCreateDrainPinsBesideAProgramThatOnlyNamesTheVariable: the guard tests for an
// assignment. A program that merely mentions the variable name sets nothing, contradicts
// nothing, and must not cost the caller their pin — the refusal offers no override, so
// over-reading the shape here would mean a legitimate program cannot use --account at all.
func TestCreateDrainPinsBesideAProgramThatOnlyNamesTheVariable(t *testing.T) {
	h := sharedPoolDrainHome(t)

	inst := drainOneCreate(t, h, outbox.Request{
		Account: "work-2",
		Program: `claude --append-system-prompt "never read CLAUDE_CONFIG_DIR"`,
	})

	assert.Equal(t, "work-2", inst.ClaudeAccountName())
	assert.Equal(t, accountDir(t, h, "work-2"), inst.ClaudeConfigDir())
}
