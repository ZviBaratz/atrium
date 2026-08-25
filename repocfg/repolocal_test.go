package repocfg

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

	t.Run("matcher fields are refused per entry", func(t *testing.T) {
		got, err := ParseRepoLocal([]byte(`{
			"repo_scripts": [
				{"name": "routed", "remote_matches": ["github.com/acme"], "setup_script": "make"},
				{"name": "pathy", "path_matches": ["/home"], "setup_script": "make"},
				{"name": "clean", "setup_script": "make"}
			]
		}`))
		require.NoError(t, err)
		require.Len(t, got.Entries, 1, "a refused sibling must not hide the good entry")
		assert.Equal(t, "clean", got.Entries[0].Name)
		assert.Equal(t, 2, got.Entries[0].Index,
			"a survivor carries its FILE position — a message numbering it by the filtered slice would point at the wrong entry")
		require.Len(t, got.Problems, 2)
		for _, p := range got.Problems {
			assert.Contains(t, p.Msg, "repo-local")
		}
		// The problem is findable: it carries the entry's position in this file.
		assert.Equal(t, 0, got.Problems[0].Index)
		assert.Equal(t, 1, got.Problems[1].Index)
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
