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

// The refusal vocabulary is one function because both the CLI's courtesy refusal and the
// drain's authoritative one quote it, and it has to name the dormant case rather than
// render an empty list: with no claude_accounts no name would have worked, which is a
// different mistake from a misspelling.
func TestClaudeAccountVocabulary(t *testing.T) {
	cfg := &Config{ClaudeAccounts: []ClaudeAccount{{Name: "work-1"}, {Name: "work-2"}}}
	assert.Equal(t, "work-1, work-2", cfg.ClaudeAccountVocabulary())

	assert.Contains(t, (&Config{}).ClaudeAccountVocabulary(), "no claude_accounts")
}
