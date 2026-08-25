package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ClaudeAccountNamed is the lookup behind `atrium new --account` (#854): a name, resolved
// against nothing but the account list — no rules, no pool, no rotation cursor. What it
// has to get right is the ambiguity, because the caller of a pin is asking which login
// they get and a guess is the divergence the pin exists to close.
func TestClaudeAccountNamed(t *testing.T) {
	t.Setenv("HOME", "/home/tester")

	cfg := &Config{ClaudeAccounts: []ClaudeAccount{
		{Name: "work-1", ConfigDir: "~/.claude-work", Pool: "quantivly", PathMatches: []string{"/quantivly/"}},
		{Name: "work-2", ConfigDir: "~/.claude-work2", Pool: "quantivly", PathMatches: []string{"/quantivly/"}},
		{Name: "ambient"}, // no config_dir: inherits the ambient env
	}}

	t.Run("resolves the non-routed member of a shared pool", func(t *testing.T) {
		// The one routing would never pick for these rules: both accounts match, and the
		// first in config order wins. Naming it is the whole feature.
		routed, _, _ := cfg.ResolveClaudeAccount("", "/home/tester/quantivly/qspace")
		require.Equal(t, "work-1", routed, "precondition: routing picks the sibling")

		got, ok := cfg.ClaudeAccountNamed("work-2")
		require.True(t, ok)
		assert.Equal(t, "work-2", got.Name)
		assert.Equal(t, "/home/tester/.claude-work2", got.ResolvedConfigDir())
		assert.Equal(t, "quantivly", got.Pool, "the pool the pinned session clusters under")
	})

	t.Run("an account with no config_dir still resolves", func(t *testing.T) {
		// It is a legal entry — "inherit the ambient env" — so pinning it is legal too,
		// and the empty dir it carries is the answer rather than a miss.
		//
		// Note what pinning it can and cannot promise. Nothing is injected, so the session
		// runs on whatever CLAUDE_CONFIG_DIR the tmux server's environment already holds:
		// the pin fixes the recorded name, not the login. That is the same deal routing
		// gives such an entry (ResolveClaudeAccount returns its empty dir too), and
		// FindClaudeAccount will not re-anchor it — an empty dir never matches — so the
		// dir-anchored guarantee the flag buys for a config_dir account is not available
		// for this one. Refusing the pin here would only take away the picker's own
		// behaviour without putting the guarantee in its place.
		got, ok := cfg.ClaudeAccountNamed("ambient")
		require.True(t, ok)
		assert.Equal(t, "ambient", got.Name)
		assert.Empty(t, got.ResolvedConfigDir())
	})

	t.Run("an unknown name resolves to nothing", func(t *testing.T) {
		_, ok := cfg.ClaudeAccountNamed("work-3")
		assert.False(t, ok)
	})

	t.Run("the empty name resolves to nothing", func(t *testing.T) {
		// "Not asked for" must not land on the entry that also declares no name, which is
		// what a bare equality test would do to a config carrying one.
		nameless := &Config{ClaudeAccounts: []ClaudeAccount{{ConfigDir: "~/.claude"}}}
		_, ok := nameless.ClaudeAccountNamed("")
		assert.False(t, ok)
	})

	t.Run("a name two entries share resolves to nothing", func(t *testing.T) {
		dup := &Config{ClaudeAccounts: []ClaudeAccount{
			{Name: "work", ConfigDir: "~/.claude-a"},
			{Name: "work", ConfigDir: "~/.claude-b"},
		}}
		_, ok := dup.ClaudeAccountNamed("work")
		assert.False(t, ok, "guessing would inject one login under a name meant for the other")
	})

	t.Run("matching is exact, not case-folded", func(t *testing.T) {
		// Routing folds case because it matches rules against paths and remotes; a name is
		// a key the user typed into config.json and typed again on the command line, and
		// folding it would make two entries differing only in case ambiguous.
		_, ok := cfg.ClaudeAccountNamed("WORK-2")
		assert.False(t, ok)
	})
}

// ClaudeAccountPinProblem is the diagnosis behind every --account refusal, and it is one
// function because two processes refuse the same name: `atrium new` as a courtesy at the
// terminal, the create drain authoritatively against the config it will honour. What is
// asserted here is that the four cases stay APART — each has a different remedy, and
// collapsing them is how a config error gets reported as a typo.
func TestClaudeAccountPinProblem(t *testing.T) {
	cfg := &Config{ClaudeAccounts: []ClaudeAccount{
		{Name: "work-1", Pool: "quantivly"},
		{Name: "work-2", Pool: "quantivly"},
		{Name: "solo"},
	}}

	t.Run("a resolvable name is no problem", func(t *testing.T) {
		assert.Empty(t, cfg.ClaudeAccountPinProblem("work-2"))
		assert.Empty(t, cfg.ClaudeAccountPinProblem("solo"))
	})

	t.Run("a misspelling names what it could have said", func(t *testing.T) {
		problem := cfg.ClaudeAccountPinProblem("work-3")
		assert.Contains(t, problem, "names no configured claude account")
		assert.Contains(t, problem, "work-1, work-2, solo")
	})

	t.Run("a pool name is not a misspelling", func(t *testing.T) {
		// The near miss the flag invites: the config does declare quantivly, so telling
		// the caller it has no such account is a sentence they can see is false. --account
		// pins one login; the pool's own behaviour is what an unpinned request gets.
		problem := cfg.ClaudeAccountPinProblem("quantivly")
		assert.Contains(t, problem, "names a pool, not an entry")
		assert.Contains(t, problem, "work-1, work-2", "and the entries to choose between")
		assert.NotContains(t, problem, "solo", "which is in no pool")
	})

	t.Run("an ungrouped account is not reported as its own pool", func(t *testing.T) {
		// PoolMembers answers for a singleton — an ungrouped account matched by its own
		// name — and that is the wrong table here: "solox" is a misspelling of an entry,
		// and there is no pool row for the caller to be pointed at.
		problem := cfg.ClaudeAccountPinProblem("solox")
		assert.Contains(t, problem, "names no configured claude account")
	})

	t.Run("a shared name is a config to repair", func(t *testing.T) {
		dup := &Config{ClaudeAccounts: []ClaudeAccount{
			{Name: "work", ConfigDir: "~/.claude-a"},
			{Name: "work", ConfigDir: "~/.claude-b"},
		}}
		problem := dup.ClaudeAccountPinProblem("work")
		assert.Contains(t, problem, "names 2 entries")
		assert.Contains(t, problem, "distinct names", "the remedy, which is not retyping the name")
		assert.NotContains(t, problem, "names no configured claude account",
			"a message that listed the name twice while denying it exists argues with itself")
	})

	t.Run("a dormant config is named as itself", func(t *testing.T) {
		// No name would have worked, which is a different mistake from a misspelling and
		// must not be rendered as an empty list of alternatives.
		problem := (&Config{}).ClaudeAccountPinProblem("work")
		assert.Contains(t, problem, "no claude_accounts")
	})
}
