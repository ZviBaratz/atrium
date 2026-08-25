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
		_, err := AssessRepo(context.Background(), t.TempDir(), "")
		assert.Error(t, err)
	})

	t.Run("no repo-local config: nothing to prompt about", func(t *testing.T) {
		sandbox(t)
		repo := gitRepo(t)
		a, err := AssessRepo(context.Background(), repo, "")
		require.NoError(t, err)
		assert.False(t, a.Present)
		assert.False(t, a.WantsPrompt())
	})

	t.Run("ungranted config wants a prompt; a grant satisfies it; an edit re-arms it", func(t *testing.T) {
		sandbox(t)
		repo := gitRepo(t)
		commitRepoConfig(t, repo, `{"repo_scripts":[{"name":"web","setup_script":"make deps"}]}`)

		a, err := AssessRepo(context.Background(), repo, "")
		require.NoError(t, err)
		assert.True(t, a.Present)
		require.Len(t, a.Entries, 1)
		assert.True(t, a.WantsPrompt())
		assert.False(t, a.HasGrant, "never granted: the prompt copy should read as a first ask")

		require.NoError(t, Grant(a.Key, a.Hash, a.Remote, time.Now()))
		a, err = AssessRepo(context.Background(), repo, "")
		require.NoError(t, err)
		assert.True(t, a.Granted)
		assert.False(t, a.WantsPrompt())

		commitRepoConfig(t, repo, `{"repo_scripts":[{"name":"web","setup_script":"make evil"}]}`)
		a, err = AssessRepo(context.Background(), repo, "")
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

		fromRoot, err := AssessRepo(context.Background(), repo, "")
		require.NoError(t, err)
		fromSub, err := AssessRepo(context.Background(), sub, "")
		require.NoError(t, err)
		assert.Equal(t, fromRoot.Key, fromSub.Key, "one repo, one key, wherever assessed from")
		assert.Equal(t, fromRoot.Hash, fromSub.Hash)
	})

	t.Run("an undecodable file is present with FileErr and never prompts", func(t *testing.T) {
		sandbox(t)
		repo := gitRepo(t)
		commitRepoConfig(t, repo, `{not json`)

		a, err := AssessRepo(context.Background(), repo, "")
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

		a, err := AssessRepo(context.Background(), repo, "")
		require.NoError(t, err)
		assert.True(t, a.Present)
		assert.False(t, a.WantsPrompt())
	})

	t.Run("the ref parameter reads that ref's bytes, not HEAD's", func(t *testing.T) {
		// The create form can pick any base branch; the assessment must hash the
		// file as THAT ref carries it, because that is what the worktree checks
		// out — a HEAD read here would grant one version and enforce another.
		sandbox(t)
		repo := gitRepo(t)
		commitRepoConfig(t, repo, `{"repo_scripts":[{"name":"main","setup_script":"make main"}]}`)
		run := func(args ...string) {
			t.Helper()
			cmd := exec.CommandContext(context.Background(), "git", args...)
			cmd.Dir = repo
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, "git %v: %s", args, out)
		}
		run("checkout", "-b", "feature")
		commitRepoConfig(t, repo, `{"repo_scripts":[{"name":"feature","setup_script":"make feature"}]}`)
		run("checkout", "-") // back on the first branch; HEAD now differs from feature

		atHead, err := AssessRepo(context.Background(), repo, "")
		require.NoError(t, err)
		atFeature, err := AssessRepo(context.Background(), repo, "feature")
		require.NoError(t, err)
		require.Len(t, atFeature.Entries, 1)
		assert.Equal(t, "feature", atFeature.Entries[0].Name)
		assert.Equal(t, "feature", atFeature.Ref)
		assert.NotEqual(t, atHead.Hash, atFeature.Hash,
			"two refs, two contents — one hash would grant bytes the worktree never holds")

		// A grant for the feature ref's bytes satisfies exactly that ref.
		require.NoError(t, Grant(atFeature.Key, atFeature.Hash, atFeature.Remote, time.Now()))
		re, err := AssessRepo(context.Background(), repo, "feature")
		require.NoError(t, err)
		assert.False(t, re.WantsPrompt())
		reHead, err := AssessRepo(context.Background(), repo, "")
		require.NoError(t, err)
		assert.True(t, reHead.WantsPrompt(), "HEAD's differing bytes stay ungranted")
	})

	t.Run("AssessCreateDefault follows the freshened base past a stale local HEAD", func(t *testing.T) {
		// update_base_on_create (default on) makes a new worktree check out
		// origin/<branch> whenever local is behind — so when origin changed
		// .atrium.json and local main has not caught up, the bytes a session
		// materializes are origin's. Review finding #1's Case A: assessing literal
		// HEAD here granted the OLD hash and every session then reported
		// "changed" seconds after the user pressed trust.
		sandbox(t)
		origin := gitRepo(t)
		commitRepoConfig(t, origin, `{"repo_scripts":[{"name":"v1","setup_script":"make v1"}]}`)

		clone := filepath.Join(t.TempDir(), "clone")
		runIn := func(dir string, args ...string) {
			t.Helper()
			cmd := exec.CommandContext(context.Background(), "git", args...)
			cmd.Dir = dir
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, "git %v: %s", args, out)
		}
		cmd := exec.CommandContext(context.Background(), "git", "clone", origin, clone)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "clone: %s", out)
		runIn(clone, "config", "user.name", "Test User")
		runIn(clone, "config", "user.email", "test@example.com")

		// Origin moves the file; the clone fetches but does NOT advance local.
		commitRepoConfig(t, origin, `{"repo_scripts":[{"name":"v2","setup_script":"make v2"}]}`)
		runIn(clone, "fetch", "origin")

		fresh, err := AssessCreateDefault(context.Background(), clone, true)
		require.NoError(t, err)
		require.Len(t, fresh.Entries, 1)
		assert.Equal(t, "v2", fresh.Entries[0].Name, "the session will materialize origin's tip; the prompt must describe it")
		assert.Contains(t, fresh.Ref, "origin/", "behind-or-equal local means the start point is the remote ref")

		stale, err := AssessCreateDefault(context.Background(), clone, false)
		require.NoError(t, err)
		require.Len(t, stale.Entries, 1)
		assert.Equal(t, "v1", stale.Entries[0].Name, "with freshening off the session starts from local HEAD")
		assert.NotEqual(t, fresh.Hash, stale.Hash)
	})

	t.Run("a corrupt ledger fails closed and says so", func(t *testing.T) {
		path := sandbox(t)
		repo := gitRepo(t)
		commitRepoConfig(t, repo, `{"repo_scripts":[{"setup_script":"make"}]}`)
		writeRaw(t, path, []byte("{not json"))

		a, err := AssessRepo(context.Background(), repo, "")
		require.NoError(t, err)
		assert.Error(t, a.LedgerErr)
		assert.False(t, a.Granted)
		assert.True(t, a.WantsPrompt(), "zero readable grants: the repo reads as untrusted")
	})
}
