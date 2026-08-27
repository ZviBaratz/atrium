package session

// reposeed_test.go — #815's gate proof. The seam that makes these tests
// load-bearing is the FILESYSTEM: an untrusted repo's carry_files must leave no
// copy in the worktree, which no amount of correct
// reporting can fake. Every positive control grants through
// repotrust.AssessCreateDefault, the same derivation the create-time prompt and
// `atrium trust allow` use, so a systematic disagreement between the granting
// reader and the enforcing one fails here rather than at a user.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/internal/repotrust"
	"github.com/ZviBaratz/atrium/repocfg"
	"github.com/ZviBaratz/atrium/session/git"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedFixture is a repo whose committed .atrium.json declares seed lists, plus the
// gitignored content those lists name sitting in the origin checkout — and an
// Instance whose resolver is installed on the worktree BEFORE Setup runs, which is
// the ordering production uses: Start and FromInstanceData both push the resolver
// in before the worktree is shared or set up, for the reason SetIsolateDeps'
// docstring gives about its own two call sites.
//
// materialize is deliberately separate from the build: a test grants, or does not,
// between the two, which is the whole variable under test.
type seedFixture struct {
	inst     *Instance
	wt       *git.Worktree
	repoPath string
}

// newSeedFixture commits .gitignore covering every name in ignored, commits
// repoConfig as .atrium.json, and creates each ignored path in the origin checkout
// (a file for a name with an extension, a directory otherwise) so the seeding code
// has something real to copy or link.
func newSeedFixture(t *testing.T, repoConfig string, ignored ...string) *seedFixture {
	t.Helper()
	repoPath := filepath.Join(t.TempDir(), "repo")
	runGit(t, "", "init", repoPath)
	runGit(t, repoPath, "config", "user.email", "test@example.com")
	runGit(t, repoPath, "config", "user.name", "Test User")
	// core.excludesFile is pinned at an absent path for session/git's stated reason:
	// a developer's global gitignore of node_modules must not decide the result.
	runGit(t, repoPath, "config", "core.excludesFile", filepath.Join(t.TempDir(), "absent-global-gitignore"))
	require.NoError(t, os.WriteFile(filepath.Join(repoPath, "README.md"), []byte("hello\n"), 0o644))

	var ignoreRules string
	for _, rel := range ignored {
		ignoreRules += rel + "\n"
	}
	require.NoError(t, os.WriteFile(filepath.Join(repoPath, ".gitignore"), []byte(ignoreRules), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repoPath, ".atrium.json"), []byte(repoConfig), 0o644))
	runGit(t, repoPath, "add", ".")
	runGit(t, repoPath, "commit", "-m", "initial")

	for _, rel := range ignored {
		abs := filepath.Join(repoPath, rel)
		if filepath.Ext(rel) != "" {
			require.NoError(t, os.MkdirAll(filepath.Dir(abs), 0o755))
			require.NoError(t, os.WriteFile(abs, []byte("origin content of "+rel+"\n"), 0o600))
			continue
		}
		require.NoError(t, os.MkdirAll(abs, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(abs, "installed.txt"), []byte("dep\n"), 0o644))
	}
	return &seedFixture{repoPath: repoPath}
}

// materialize builds the worktree, installs the Instance's resolver on it, and runs
// Setup — the order Start uses. Returns the worktree path.
func (f *seedFixture) materialize(t *testing.T, name string) string {
	t.Helper()
	wt, _, err := git.NewWorktree(context.Background(), f.repoPath, name)
	require.NoError(t, err)
	inst := &Instance{ident: identity{title: name}, Path: f.repoPath, gitWorktree: wt}
	wt.SetRepoLocalSeeds(inst.repoLocalSeedResolver)
	require.NoError(t, wt.Setup())
	t.Cleanup(func() { _ = wt.Cleanup() })
	f.inst, f.wt = inst, wt
	return wt.GetWorktreePath()
}

const seedConfig = `{"carry_files":[".dev.vars",".env.local"]}`

// TestRepoSeeds_UntrustedNeverMaterialize is the issue's "done means" for the seed
// half: the negative (no grant → nothing on disk) and its positive control (grant →
// both appear) in one test, so a gate that refuses everything and a gate that
// refuses nothing both fail it.
func TestRepoSeeds_UntrustedNeverMaterialize(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// The global lists are EMPTY, so anything on disk came from the repo's file.
	require.NoError(t, config.SaveConfig(func() *config.Config {
		c := config.DefaultConfig()
		c.CarryFiles, c.LinkPaths = []string{}, nil
		return c
	}()))

	f := newSeedFixture(t, seedConfig, ".dev.vars", ".env.local")

	untrusted := f.materialize(t, "cold")
	_, err := os.Lstat(filepath.Join(untrusted, ".dev.vars"))
	assert.True(t, os.IsNotExist(err), "an untrusted repo's carry_files must copy nothing, lstat err = %v", err)
	assert.NoFileExists(t, filepath.Join(untrusted, ".env.local"),
		"an untrusted repo's carry_files must copy nothing")
	assert.Equal(t, RepoConfigUntrusted, f.inst.RepoConfigStatus())

	// Positive control: the same file, the same fixture, one grant apart.
	grantRepo(t, f.repoPath)
	trusted := f.materialize(t, "warm")
	body, err := os.ReadFile(filepath.Join(trusted, ".dev.vars"))
	require.NoError(t, err, "a trusted repo's carry_files must copy")
	assert.Contains(t, string(body), "origin content of .dev.vars")
	assert.FileExists(t, filepath.Join(trusted, ".env.local"),
		"a trusted repo's carry_files must copy every entry, not just the first")
	assert.Equal(t, RepoConfigActive, f.inst.RepoConfigStatus())
}

// TestRepoSeeds_EditedFileGoesInert closes the same TOCTOU the script half closes:
// a grant covers exact bytes, so an agent editing .atrium.json on its own branch
// makes the whole file inert at the next materialization.
func TestRepoSeeds_EditedFileGoesInert(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	f := newSeedFixture(t, seedConfig, ".dev.vars", ".env.local")
	grantRepo(t, f.repoPath)

	// Positive control first: the grant works before the edit.
	require.FileExists(t, filepath.Join(f.materialize(t, "before"), ".dev.vars"))

	require.NoError(t, os.WriteFile(filepath.Join(f.repoPath, ".atrium.json"),
		[]byte(`{"carry_files":[".dev.vars",".env.local"],"repo_scripts":[]}`), 0o644))
	runGit(t, f.repoPath, "commit", "-am", "tweak")

	after := f.materialize(t, "after")
	_, err := os.Lstat(filepath.Join(after, ".dev.vars"))
	assert.True(t, os.IsNotExist(err), "changed content must be inert, lstat err = %v", err)
	assert.Equal(t, RepoConfigChanged, f.inst.RepoConfigStatus())
}

// TestRepoSeeds_UnionWithGlobal is #815's layering decision on disk: the repo ADDS
// to the user's lists rather than replacing them, so the personal carry survives in
// a repo that declares its own.
func TestRepoSeeds_UnionWithGlobal(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	require.NoError(t, config.SaveConfig(func() *config.Config {
		c := config.DefaultConfig()
		c.CarryFiles = []string{".personal"}
		return c
	}()))

	f := newSeedFixture(t, `{"carry_files":[".dev.vars"]}`, ".dev.vars", ".personal")
	grantRepo(t, f.repoPath)
	dir := f.materialize(t, "union")

	assert.FileExists(t, filepath.Join(dir, ".dev.vars"), "the repo's entry must be seeded")
	assert.FileExists(t, filepath.Join(dir, ".personal"), "the user's own entry must NOT be replaced")
}

// TestRepoSeeds_TwoReposDoNotCrossContaminate is #477's headline, and the reason
// this feature exists: project A's layout must not reach project B. A non-empty
// global list is present in both so the test also proves the union is per-repo
// rather than a single accumulating list.
func TestRepoSeeds_TwoReposDoNotCrossContaminate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	require.NoError(t, config.SaveConfig(func() *config.Config {
		c := config.DefaultConfig()
		c.CarryFiles = []string{".personal"}
		return c
	}()))

	// Both repos carry BOTH gitignored files, so what each session gets is decided
	// by its own .atrium.json and not by which file happened to exist.
	a := newSeedFixture(t, `{"carry_files":[".a-only"]}`, ".a-only", ".b-only", ".personal")
	b := newSeedFixture(t, `{"carry_files":[".b-only"]}`, ".a-only", ".b-only", ".personal")
	grantRepo(t, a.repoPath)
	grantRepo(t, b.repoPath)

	dirA, dirB := a.materialize(t, "proj-a"), b.materialize(t, "proj-b")

	assert.FileExists(t, filepath.Join(dirA, ".a-only"))
	assert.FileExists(t, filepath.Join(dirB, ".b-only"))
	_, err := os.Lstat(filepath.Join(dirA, ".b-only"))
	assert.True(t, os.IsNotExist(err), "repo B's entry must not reach repo A, lstat err = %v", err)
	_, err = os.Lstat(filepath.Join(dirB, ".a-only"))
	assert.True(t, os.IsNotExist(err), "repo A's entry must not reach repo B, lstat err = %v", err)
	// The user's own entry reaches both, which is what "add, don't replace" buys.
	assert.FileExists(t, filepath.Join(dirA, ".personal"))
	assert.FileExists(t, filepath.Join(dirB, ".personal"))
}

// TestRepoSeeds_PublishedForThePanel: the settings panel reads the resolution
// rather than performing one, so what it reads has to track the verdict — empty
// after a refusal, populated after a grant, and never "resolved" before any
// resolution has run.
func TestRepoSeeds_PublishedForThePanel(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	f := newSeedFixture(t, seedConfig, ".dev.vars", ".env.local")

	fresh := &Instance{ident: identity{title: "unstarted"}, Path: f.repoPath}
	_, resolved := fresh.RepoLocalSeeds()
	assert.False(t, resolved, "an instance that never resolved must read as unknown, not as empty")

	f.materialize(t, "cold")
	seeds, resolved := f.inst.RepoLocalSeeds()
	assert.True(t, resolved)
	assert.Empty(t, seeds[repocfg.KeyCarryFiles], "an untrusted repo must not be advertised as contributing")
	// Every layer key is PRESENT even after a refusal, so the panel's producer can
	// forward this map whole instead of naming the keys itself.
	assert.ElementsMatch(t, repocfg.RepoLocalLayerKeys(), mapKeys(seeds),
		"the published map must carry every layerable key, or a third one reaches the panel as a silent nil")

	grantRepo(t, f.repoPath)
	f.materialize(t, "warm")
	seeds, resolved = f.inst.RepoLocalSeeds()
	assert.True(t, resolved)
	assert.Equal(t, []string{".dev.vars", ".env.local"}, seeds[repocfg.KeyCarryFiles])
	assert.ElementsMatch(t, repocfg.RepoLocalLayerKeys(), mapKeys(seeds))
}

// TestRepoSeeds_PausedSessionReadsAsUnknown: the panel reads the last resolution
// rather than performing one, so a session that can no longer GET a fresh one must
// stop being quoted. ComputeRunState is the only thing that re-resolves for a live
// session and it early-returns on a paused instance — so a session paused after a
// positive resolution kept advertising it forever, and the moment that matters is
// the moment a user runs `atrium trust revoke` and opens the panel to check the
// revoke took effect.
func TestRepoSeeds_PausedSessionReadsAsUnknown(t *testing.T) {
	inst := &Instance{ident: identity{title: "parked"}}
	inst.setRepoSeeds(map[string][]string{repocfg.KeyCarryFiles: {".dev.vars"}})

	seeds, resolved := inst.RepoLocalSeeds()
	require.True(t, resolved, "the fixture must start resolved, or this tests nothing")
	require.Equal(t, []string{".dev.vars"}, seeds[repocfg.KeyCarryFiles])

	inst.mu.Lock()
	inst.status = Paused
	inst.mu.Unlock()

	seeds, resolved = inst.RepoLocalSeeds()
	assert.False(t, resolved, "a paused session's last resolution is stale and nothing will refresh it")
	assert.Empty(t, seeds)
}

// mapKeys is the map's keys, for a set comparison against a vocabulary.
func mapKeys(m map[string][]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestRepoSeeds_PreSeedGrantSeedsNothing is the enforcement-seam guard for the
// grant-version gate, and it is the test whose absence let that gate ship as
// advisory-only.
//
// The gate was asserted in exactly one place — against AssessRepo, the function
// that computes it — so a prompt that correctly said "re-allow this" passed while
// routeRepoLocal kept asking the old hash-only question and the worktree seeded
// the lists anyway. Every existing session's next resume, every poll sweep and the
// autoyes daemon reach that funnel with NO UI, so the powers turned on across the
// fleet on upgrade with no prompt at all — and declining the prompt, which writes
// nothing to the ledger, changed nothing.
//
// The seam is therefore the FILESYSTEM, per this file's own header: a pre-#815
// record must leave no copied file in the worktree, whatever any surface says.
func TestRepoSeeds_PreSeedGrantSeedsNothing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	f := newSeedFixture(t, seedConfig, ".dev.vars", ".env.local")

	// Grant normally, then strip grant_version from the ledger ON DISK. That is
	// exactly what every ledger written before this release looks like, and going
	// through the file rather than through an in-memory Record also proves the
	// omitted field decodes as "does not cover seeds" rather than as some default.
	grantRepo(t, f.repoPath)
	ledgerPath, err := repotrust.Path()
	require.NoError(t, err)
	raw, err := os.ReadFile(ledgerPath)
	require.NoError(t, err)
	require.Contains(t, string(raw), "grant_version", "the fixture must start from a versioned grant, or it proves nothing")
	var ledger map[string]any
	require.NoError(t, json.Unmarshal(raw, &ledger))
	for _, rec := range ledger["repos"].(map[string]any) {
		delete(rec.(map[string]any), "grant_version")
	}
	stripped, err := json.Marshal(ledger)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(ledgerPath, stripped, 0o600))

	dir := f.materialize(t, "upgraded")

	// Nothing seeded. This is the assertion the gate's own package could not make.
	assert.NoFileExists(t, filepath.Join(dir, ".dev.vars"),
		"a grant made before atrium read carry_files must not copy them in — the funnel is authoritative, the prompt is advisory")
	assert.Equal(t, RepoConfigUntrusted, f.inst.RepoConfigStatus(),
		"and the session must say so rather than reading as Active")
	// The report must not claim the file changed: it did not, this atrium reads more
	// of it than the one that granted it did.
	assert.NotContains(t, f.inst.repoConfigReportSnapshot(), "CHANGED")
	assert.Contains(t, f.inst.repoConfigReportSnapshot(), "the file you trusted")

	// Positive control: re-granting at the current version seeds it. Without this a
	// gate that refused EVERYTHING would pass the assertions above.
	grantRepo(t, f.repoPath)
	dir2 := f.materialize(t, "reallowed")
	assert.FileExists(t, filepath.Join(dir2, ".dev.vars"),
		"a current grant must still apply the lists it described")
}
