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
		require.Len(t, a.Local.Entries, 1)
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
		assert.Empty(t, a.Local.Entries)
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
		require.Len(t, atFeature.Local.Entries, 1)
		assert.Equal(t, "feature", atFeature.Local.Entries[0].Name)
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
		require.Len(t, fresh.Local.Entries, 1)
		assert.Equal(t, "v2", fresh.Local.Entries[0].Name, "the session will materialize origin's tip; the prompt must describe it")
		assert.Contains(t, fresh.Ref, "origin/", "behind-or-equal local means the start point is the remote ref")

		stale, err := AssessCreateDefault(context.Background(), clone, false)
		require.NoError(t, err)
		require.Len(t, stale.Local.Entries, 1)
		assert.Equal(t, "v1", stale.Local.Entries[0].Name, "with freshening off the session starts from local HEAD")
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

// TestAssessRepo_SeedLists covers #815's half of the prompt decision: a file whose
// only content is carry_files/link_paths declares something, so it is grantable and
// asks — it executes nothing, but it decides which of the user's own gitignored
// files are copied in front of an agent and which of their trees it may write
// through, which is the recorded reason both halves ride one grant.
func TestAssessRepo_SeedLists(t *testing.T) {
	t.Run("a seed-only file wants a prompt", func(t *testing.T) {
		repo := gitRepo(t)
		commitRepoConfig(t, repo, `{"carry_files":[".dev.vars"],"link_paths":["node_modules"]}`)

		a, err := AssessRepo(context.Background(), repo, "HEAD")
		require.NoError(t, err)
		require.True(t, a.Present)
		assert.Equal(t, []string{".dev.vars"}, a.Local.CarryFiles)
		assert.Equal(t, []string{"node_modules"}, a.Local.LinkPaths)
		assert.True(t, a.WantsPrompt(), "a file that seeds paths must be grantable")

		// And the grant satisfies it, so the prompt is asked once rather than forever.
		require.NoError(t, Grant(a.Key, a.Hash, a.Remote, time.Now()))
		again, err := AssessRepo(context.Background(), repo, "HEAD")
		require.NoError(t, err)
		assert.False(t, again.WantsPrompt())
	})

	t.Run("empty lists declare nothing and never prompt", func(t *testing.T) {
		// The distinction the enforcement gate leans on: present-but-empty must read
		// like no file, or every repo with a stub .atrium.json carries a standing nag
		// whose named remedies both refuse.
		repo := gitRepo(t)
		commitRepoConfig(t, repo, `{"carry_files":[],"link_paths":[]}`)

		a, err := AssessRepo(context.Background(), repo, "HEAD")
		require.NoError(t, err)
		assert.True(t, a.Present)
		assert.False(t, a.WantsPrompt())
	})

	t.Run("an unusable seed list is a FileErr, not a prompt", func(t *testing.T) {
		repo := gitRepo(t)
		commitRepoConfig(t, repo, `{"carry_files":["../../.ssh/id_rsa"]}`)

		a, err := AssessRepo(context.Background(), repo, "HEAD")
		require.NoError(t, err)
		require.Error(t, a.FileErr, "a refused file must be reported, not silently dropped")
		assert.False(t, a.WantsPrompt(), "nothing can be granted from a file that refuses whole")
	})
}

// TestLiveStateNamesWhatAGrantCovers: the COVERS column `atrium trust status` and
// doctor both print rides LiveState's single assessment. It is populated only for a
// current grant — for any other state the old file's surfaces would describe a
// session nobody can create.
func TestLiveStateNamesWhatAGrantCovers(t *testing.T) {
	repo := gitRepo(t)
	commitRepoConfig(t, repo, `{"carry_files":[".dev.vars"],"repo_scripts":[{"setup_script":"npm ci"}]}`)
	a, err := AssessRepo(context.Background(), repo, "HEAD")
	require.NoError(t, err)
	require.NoError(t, Grant(a.Key, a.Hash, a.Remote, time.Now()))
	rec, ok := func() (Record, bool) { l, _ := Load(); return l.Lookup(a.Key) }()
	require.True(t, ok)

	state, covers := LiveState(context.Background(), a.Key, rec, false)
	assert.Equal(t, "current", state)
	assert.Contains(t, covers, "setup script")
	assert.Contains(t, covers, "1 carried file", "the seed half of the grant must be named too")

	// Edit the file: the state changes and COVERS empties, because what the grant
	// covered is no longer what a session would get.
	commitRepoConfig(t, repo, `{"carry_files":[".dev.vars",".other"]}`)
	state, covers = LiveState(context.Background(), a.Key, rec, false)
	assert.Equal(t, "changed (re-allow to use)", state)
	assert.Empty(t, covers)
}

// TestGrantScopeUpgradeReprompts: a grant covers what its prompt described, and the
// content hash alone cannot say what that was once the set of powers has grown.
//
// repoLocalWire tolerated carry_files/link_paths as unknown keys before #815 read
// them, and its own comment invited repos to ship them ahead of the reader — so a
// repo's file could already carry both lists while the dialog that granted it
// described only the setup script. The bytes are identical afterwards, so a hash
// comparison silently extends that grant to two powers nobody was asked about: on
// the next materialization, including an automatic resume or the autoyes daemon's
// relaunch (neither of which has a UI), the repo starts choosing which of the user's
// gitignored files are copied in front of an agent.
func TestGrantScopeUpgradeReprompts(t *testing.T) {
	repo := gitRepo(t)
	commitRepoConfig(t, repo, `{"repo_scripts":[{"name":"web","setup_script":"npm ci"}],"carry_files":[".dev.vars"]}`)

	a, err := AssessRepo(context.Background(), repo, "HEAD")
	require.NoError(t, err)

	// A pre-#815 record: the hash the user allowed, with no grant version — exactly
	// what every ledger written before this release holds.
	l, err := loadForWrite()
	require.NoError(t, err)
	l.Repos[a.Key] = Record{Hash: a.Hash, GrantedAt: time.Now()}
	require.NoError(t, save(l))

	again, err := AssessRepo(context.Background(), repo, "HEAD")
	require.NoError(t, err)
	require.Equal(t, a.Hash, again.Hash, "the file must be unchanged, or this tests the wrong thing")
	assert.True(t, again.HasGrant, "the record is still there")
	assert.False(t, again.Granted, "a grant cannot cover powers its prompt never described")
	assert.True(t, again.ScopeUpgrade, "and the reason must be distinguishable from a changed file")
	assert.True(t, again.WantsPrompt(), "so the user is asked, once")

	// Re-granting stamps the current version, and the prompt does not come back.
	require.NoError(t, Grant(again.Key, again.Hash, again.Remote, time.Now()))
	settled, err := AssessRepo(context.Background(), repo, "HEAD")
	require.NoError(t, err)
	assert.True(t, settled.Granted)
	assert.False(t, settled.ScopeUpgrade)
	assert.False(t, settled.WantsPrompt())
}

// TestGrantScopeUpgradeOnlyForSeedLists is the control: a pre-#815 record for a file
// that declares nothing NEW is still a valid grant. Without it, invalidating every
// old record — which would re-prompt every trusted repo on upgrade for no reason —
// would read as a pass.
func TestGrantScopeUpgradeOnlyForSeedLists(t *testing.T) {
	repo := gitRepo(t)
	commitRepoConfig(t, repo, `{"repo_scripts":[{"name":"web","setup_script":"npm ci"}]}`)

	a, err := AssessRepo(context.Background(), repo, "HEAD")
	require.NoError(t, err)
	l, err := loadForWrite()
	require.NoError(t, err)
	l.Repos[a.Key] = Record{Hash: a.Hash, GrantedAt: time.Now()}
	require.NoError(t, save(l))

	again, err := AssessRepo(context.Background(), repo, "HEAD")
	require.NoError(t, err)
	assert.True(t, again.Granted, "a script-only file confers exactly what the old prompt described")
	assert.False(t, again.ScopeUpgrade)
	assert.False(t, again.WantsPrompt())
}

// TestRefusedEntryIsNotGrantable: a file whose repo_scripts entry the parse refused
// is unusable, not partly usable — enforcement refuses it WHOLE, its Problems check
// running before its surfaces check. Before this, such a file's seed lists made it
// describable, so the dialog offered a grant ("copies in: .dev.vars"), never
// mentioned the refused entry, and confirming wrote a record for a file that then
// applied nothing — permanently, since re-creating re-prompts and `trust allow`
// re-grants. main gated on Entries and so never had the hole; the seed lists are
// what gave a Problems-only file something to advertise.
func TestRefusedEntryIsNotGrantable(t *testing.T) {
	repo := gitRepo(t)
	commitRepoConfig(t, repo, `{"repo_scripts":[{"name":"web","path_matches":["/elsewhere"],"setup_script":"echo hi"}],"carry_files":[".dev.vars"]}`)

	a, err := AssessRepo(context.Background(), repo, "HEAD")
	require.NoError(t, err)
	require.True(t, a.Present)
	require.Error(t, a.FileErr, "a refused entry makes the file unusable, and that must be reported")
	assert.False(t, a.WantsPrompt(), "nothing may offer a grant the gate would apply nothing from")
	assert.Empty(t, a.Local.CarryFiles, "the lists must not survive the refusal that drops the file")
}
