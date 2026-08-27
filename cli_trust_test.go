package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/internal/repotrust"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// trustRepo builds a repo whose HEAD commits an .atrium.json, under the
// caller's sandboxed HOME.
func trustRepo(t *testing.T, repoConfig string) string {
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
	run(repo, "config", "user.email", "test@example.com")
	run(repo, "config", "user.name", "Test User")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "README.md"), []byte("x\n"), 0o644))
	if repoConfig != "" {
		require.NoError(t, os.WriteFile(filepath.Join(repo, ".atrium.json"), []byte(repoConfig), 0o644))
	}
	run(repo, "add", ".")
	run(repo, "commit", "-m", "initial")
	return repo
}

func TestTrustAllowRevokeStatusRoundTrip(t *testing.T) {
	sandboxDataDir(t)
	repo := trustRepo(t, `{"repo_scripts":[{"name":"web","setup_script":"make deps"}]}`)
	ctx := context.Background()

	var out strings.Builder
	require.NoError(t, runTrustAllow(ctx, &out, repo, true))
	assert.Contains(t, out.String(), "trusted")
	assert.Contains(t, out.String(), "setup script",
		"the receipt names the declared surfaces, off the same list the TUI dialog renders")

	a, err := repotrust.AssessRepo(ctx, repo, "")
	require.NoError(t, err)
	assert.True(t, a.Granted, "allow must record the grant the gate will honor")

	// Idempotent: allowing again reports, changes nothing, fails nothing.
	out.Reset()
	require.NoError(t, runTrustAllow(ctx, &out, repo, true))
	assert.Contains(t, out.String(), "already trusted")

	out.Reset()
	require.NoError(t, runTrustStatus(ctx, &out, repo, true))
	assert.Contains(t, out.String(), "current")

	// A subdirectory names its repo, matching the grant made at the root.
	sub := filepath.Join(repo, "pkg")
	require.NoError(t, os.MkdirAll(sub, 0o755))
	out.Reset()
	require.NoError(t, runTrustRevoke(ctx, &out, sub))
	assert.Contains(t, out.String(), "revoked")

	a, err = repotrust.AssessRepo(ctx, repo, "")
	require.NoError(t, err)
	assert.False(t, a.Granted, "revoke must withdraw the grant")
}

func TestTrustAllowRefusesWhatCannotRun(t *testing.T) {
	sandboxDataDir(t)
	ctx := context.Background()

	t.Run("no tracked file at HEAD", func(t *testing.T) {
		repo := trustRepo(t, "")
		// An untracked file is exactly the case the HEAD rule exists for: it never
		// reaches a worktree, so a grant for it would approve nothing.
		require.NoError(t, os.WriteFile(filepath.Join(repo, ".atrium.json"),
			[]byte(`{"repo_scripts":[{"setup_script":"make"}]}`), 0o644))
		err := runTrustAllow(ctx, &strings.Builder{}, repo, true)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "commit the file first")
	})

	t.Run("declares nothing usable", func(t *testing.T) {
		repo := trustRepo(t, `{"repo_scripts":[]}`)
		err := runTrustAllow(ctx, &strings.Builder{}, repo, true)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "nothing to trust")
	})

	t.Run("not a repo", func(t *testing.T) {
		assert.Error(t, runTrustAllow(ctx, &strings.Builder{}, t.TempDir(), true))
	})
}

func TestTrustAllowSurvivesAnEdit(t *testing.T) {
	sandboxDataDir(t)
	repo := trustRepo(t, `{"repo_scripts":[{"setup_script":"make v1"}]}`)
	ctx := context.Background()
	require.NoError(t, runTrustAllow(ctx, &strings.Builder{}, repo, true))

	// A new committed version goes stale...
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".atrium.json"),
		[]byte(`{"repo_scripts":[{"setup_script":"make v2"}]}`), 0o644))
	for _, args := range [][]string{{"add", "."}, {"commit", "-m", "v2"}} {
		cmd := exec.CommandContext(context.Background(), "git", args...)
		cmd.Dir = repo
		require.NoError(t, cmd.Run())
	}
	var out strings.Builder
	require.NoError(t, runTrustStatus(ctx, &out, repo, true))
	assert.Contains(t, out.String(), "changed", "status must flag a grant the repo has moved past")

	// ...and re-allowing replaces the grant and says so.
	out.Reset()
	require.NoError(t, runTrustAllow(ctx, &out, repo, true))
	assert.Contains(t, out.String(), "replaces the grant")
	a, err := repotrust.AssessRepo(ctx, repo, "")
	require.NoError(t, err)
	assert.True(t, a.Granted)
}

func TestTrustRevokeAGoneRepoByItsRecordedKey(t *testing.T) {
	sandboxDataDir(t)
	// A key granted for a repo that no longer exists — the case revoke must still
	// reach, because no git is left to resolve anything. The user copies the key
	// out of `trust status`, and canonicalizing an already-canonical path is a
	// no-op, so it matches.
	key := filepath.Join(t.TempDir(), "deleted-repo")
	require.NoError(t, repotrust.Grant(key, "deadbeef", "", time.Now()))

	var out strings.Builder
	require.NoError(t, runTrustStatus(context.Background(), &out, key, true))
	assert.Contains(t, out.String(), "missing", "status must flag a grant whose repo is gone")

	out.Reset()
	require.NoError(t, runTrustRevoke(context.Background(), &out, key))
	assert.Contains(t, out.String(), "revoked")
	l, err := repotrust.Load()
	require.NoError(t, err)
	assert.Empty(t, l.Repos)
}

func TestTrustRevokeAll(t *testing.T) {
	sandboxDataDir(t)
	require.NoError(t, repotrust.Grant("/a", "h1", "", time.Now()))
	require.NoError(t, repotrust.Grant("/b", "h2", "", time.Now()))

	var out strings.Builder
	require.NoError(t, runTrustRevokeAll(&out))
	assert.Contains(t, out.String(), "revoked all 2")
	l, err := repotrust.Load()
	require.NoError(t, err)
	assert.Empty(t, l.Repos)
}

func TestTrustStatusOnAnEmptyLedger(t *testing.T) {
	sandboxDataDir(t)
	var out strings.Builder
	require.NoError(t, runTrustStatus(context.Background(), &out, t.TempDir(), true))
	assert.Contains(t, out.String(), "no repos are trusted")
}

func TestTrustStatusOnACorruptLedgerWarnsWithoutTheFreshInstallLine(t *testing.T) {
	sandboxDataDir(t)
	path, err := repotrust.Path()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("{not json"), 0o600))

	var out strings.Builder
	require.NoError(t, runTrustStatus(context.Background(), &out, t.TempDir(), true))
	assert.Contains(t, out.String(), "warning")
	// Load returns an EMPTY ledger on corruption, so without the early return the
	// empty-ledger branch fires too — and "no repos are trusted; grant one…" reads
	// as a fresh install, inviting a re-grant while the user's real grants sit
	// unreadable in the file the warning just named.
	assert.NotContains(t, out.String(), "no repos are trusted")
}

// TestTrustReceiptsNameTheSeedLists: a grant covers the whole .atrium.json, so every
// receipt that says what it covers has to say all of it. A seed-only repo is
// grantable — it runs nothing and still decides which of the user's gitignored files
// reach an agent — and `trust allow`'s old "declares nothing usable" refusal was
// written when repo_scripts was the only readable key.
//
// link_paths is deliberately absent from the fixture: it is not a key this release
// reads (see repocfg.RepoLocal.CarryFiles), so a receipt naming it would be a
// receipt for something no grant confers.
func TestTrustReceiptsNameTheSeedLists(t *testing.T) {
	sandboxDataDir(t)
	repo := trustRepo(t, `{"carry_files":[".dev.vars",".env.local"]}`)
	ctx := context.Background()

	var out strings.Builder
	require.NoError(t, runTrustAllow(ctx, &out, repo, true))
	assert.Contains(t, out.String(), "trusted")
	assert.Contains(t, out.String(), "2 carried files")

	out.Reset()
	require.NoError(t, runTrustStatus(ctx, &out, repo, true))
	assert.Contains(t, out.String(), "COVERS", "the column has to exist to carry the answer")
	assert.Contains(t, out.String(), "2 carried files")
}

// TestTrustAllowStillRefusesAFileThatDeclaresNothing: the counter-case to the test
// above. Present-but-empty must stay unbuyable, or `trust allow` writes a grant the
// enforcement gate treats as being about a file that declares nothing.
func TestTrustAllowStillRefusesAFileThatDeclaresNothing(t *testing.T) {
	sandboxDataDir(t)
	repo := trustRepo(t, `{"carry_files":[],"link_paths":[]}`)

	var out strings.Builder
	err := runTrustAllow(context.Background(), &out, repo, true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "declares nothing usable")
}
