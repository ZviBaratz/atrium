package repotrust

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gitRepo initializes a repo with one commit and returns its path. Local to this
// package: the session/git test helpers are unexported and test-only.
func gitRepo(t *testing.T) string {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "repo")
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.CommandContext(context.Background(), "git", args...)
		if dir != "" {
			cmd.Dir = dir
		}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("", "init", repo)
	run(repo, "config", "user.name", "Test User")
	run(repo, "config", "user.email", "test@example.com")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644))
	run(repo, "add", "README.md")
	run(repo, "commit", "-m", "initial")
	return repo
}

func commitRepoConfig(t *testing.T, repo, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".atrium.json"), []byte(content), 0o644))
	cmd := exec.CommandContext(context.Background(), "git", "add", ".atrium.json")
	cmd.Dir = repo
	require.NoError(t, cmd.Run())
	cmd = exec.CommandContext(context.Background(), "git", "commit", "-m", "config")
	cmd.Dir = repo
	require.NoError(t, cmd.Run())
}

func TestAssessRepo(t *testing.T) {
	t.Run("a non-repo path is an error", func(t *testing.T) {
		sandbox(t)
		_, err := AssessRepo(context.Background(), t.TempDir())
		assert.Error(t, err)
	})

	t.Run("no repo-local config: nothing to prompt about", func(t *testing.T) {
		sandbox(t)
		repo := gitRepo(t)
		a, err := AssessRepo(context.Background(), repo)
		require.NoError(t, err)
		assert.False(t, a.Present)
		assert.False(t, a.WantsPrompt())
	})

	t.Run("ungranted config wants a prompt; a grant satisfies it; an edit re-arms it", func(t *testing.T) {
		sandbox(t)
		repo := gitRepo(t)
		commitRepoConfig(t, repo, `{"repo_scripts":[{"name":"web","setup_script":"make deps"}]}`)

		a, err := AssessRepo(context.Background(), repo)
		require.NoError(t, err)
		assert.True(t, a.Present)
		require.Len(t, a.Entries, 1)
		assert.True(t, a.WantsPrompt())
		assert.False(t, a.HasGrant, "never granted: the prompt copy should read as a first ask")

		require.NoError(t, Grant(a.Key, a.Hash, a.Remote, time.Now()))
		a, err = AssessRepo(context.Background(), repo)
		require.NoError(t, err)
		assert.True(t, a.Granted)
		assert.False(t, a.WantsPrompt())

		commitRepoConfig(t, repo, `{"repo_scripts":[{"name":"web","setup_script":"make evil"}]}`)
		a, err = AssessRepo(context.Background(), repo)
		require.NoError(t, err)
		assert.False(t, a.Granted, "an edited file must not ride an older grant")
		assert.True(t, a.HasGrant, "changed-not-new: the prompt copy should say the config changed")
		assert.True(t, a.WantsPrompt())
	})

	t.Run("a subdirectory assesses as its repo", func(t *testing.T) {
		sandbox(t)
		repo := gitRepo(t)
		commitRepoConfig(t, repo, `{"repo_scripts":[{"setup_script":"make"}]}`)
		sub := filepath.Join(repo, "pkg")
		require.NoError(t, os.MkdirAll(sub, 0o755))

		fromRoot, err := AssessRepo(context.Background(), repo)
		require.NoError(t, err)
		fromSub, err := AssessRepo(context.Background(), sub)
		require.NoError(t, err)
		assert.Equal(t, fromRoot.Key, fromSub.Key, "one repo, one key, wherever assessed from")
		assert.Equal(t, fromRoot.Hash, fromSub.Hash)
	})

	t.Run("an undecodable file is present with FileErr and never prompts", func(t *testing.T) {
		sandbox(t)
		repo := gitRepo(t)
		commitRepoConfig(t, repo, `{not json`)

		a, err := AssessRepo(context.Background(), repo)
		require.NoError(t, err)
		assert.True(t, a.Present)
		assert.Error(t, a.FileErr)
		assert.Empty(t, a.Entries)
		assert.False(t, a.WantsPrompt(), "nothing usable declared: nothing to ask about")
	})

	t.Run("a declared-nothing file never prompts", func(t *testing.T) {
		sandbox(t)
		repo := gitRepo(t)
		commitRepoConfig(t, repo, `{"repo_scripts":[]}`)

		a, err := AssessRepo(context.Background(), repo)
		require.NoError(t, err)
		assert.True(t, a.Present)
		assert.False(t, a.WantsPrompt())
	})

	t.Run("a corrupt ledger fails closed and says so", func(t *testing.T) {
		path := sandbox(t)
		repo := gitRepo(t)
		commitRepoConfig(t, repo, `{"repo_scripts":[{"setup_script":"make"}]}`)
		writeRaw(t, path, []byte("{not json"))

		a, err := AssessRepo(context.Background(), repo)
		require.NoError(t, err)
		assert.Error(t, a.LedgerErr)
		assert.False(t, a.Granted)
		assert.True(t, a.WantsPrompt(), "zero readable grants: the repo reads as untrusted")
	})
}
