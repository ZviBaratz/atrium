package session

// repoconfig_test.go — the #814 trust gate's proof. The load-bearing assertion
// style is the execSetup recorder (stubSetupExec): a refused entry must spawn
// NO PROCESS, not merely report that it didn't. Grants are made through
// repotrust.AssessRepo — the same derivation the create-time prompt and
// `atrium trust allow` use — so every granted-then-executes test also proves
// the pact between the two readers: the grant hashes HEAD's checked-out form
// (git.HeadFile), enforcement hashes the worktree's own file, and if the two
// ever disagree systematically (a trimmed newline, an unapplied filter) these
// tests fail before any user hits it.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/internal/repotrust"
	"github.com/ZviBaratz/atrium/session/git"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupRecorder captures every process the exec seam would have spawned.
type setupRecorder struct {
	mu   sync.Mutex
	runs []setupRun
}

func (r *setupRecorder) record(_ context.Context, run setupRun) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runs = append(r.runs, run)
	return "", nil
}

func (r *setupRecorder) scripts() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.runs))
	for _, run := range r.runs {
		out = append(out, run.script)
	}
	return out
}

// repoLocalFixture builds the real thing end to end: a sandboxed HOME, an
// origin repo whose HEAD commits repoConfig as .atrium.json, and a worktree
// materialized from it (so the file arrives the way production checkout
// delivers it), plus an Instance wired to both. The global config is the
// sandbox default — NO repo_scripts — so anything that executes here resolved
// from the repo-local file alone, which is also the regression pin for the
// empty-global early-out that would have made the feature dead for fresh
// installs.
func repoLocalFixture(t *testing.T, repoConfig string) (inst *Instance, worktreeDir, repoPath string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	repoPath = filepath.Join(t.TempDir(), "repo")
	runGit(t, "", "init", repoPath)
	runGit(t, repoPath, "config", "user.email", "test@example.com")
	runGit(t, repoPath, "config", "user.name", "Test User")
	require.NoError(t, os.WriteFile(filepath.Join(repoPath, "README.md"), []byte("hello\n"), 0o644))
	runGit(t, repoPath, "add", ".")
	runGit(t, repoPath, "commit", "-m", "initial")
	require.NoError(t, os.WriteFile(filepath.Join(repoPath, ".atrium.json"), []byte(repoConfig), 0o644))
	runGit(t, repoPath, "add", ".atrium.json")
	runGit(t, repoPath, "commit", "-m", "repo config")

	wt, _, err := git.NewWorktree(context.Background(), repoPath, "sess")
	require.NoError(t, err)
	require.NoError(t, wt.Setup())
	t.Cleanup(func() { _ = wt.Cleanup() })

	inst = &Instance{ident: identity{title: "web"}, Path: repoPath, gitWorktree: wt}
	return inst, wt.GetWorktreePath(), repoPath
}

// grantRepo records trust for the repo's CURRENT HEAD config, through the same
// assessment the prompt and the CLI use.
func grantRepo(t *testing.T, repoPath string) {
	t.Helper()
	a, err := repotrust.AssessRepo(context.Background(), repoPath)
	require.NoError(t, err)
	require.True(t, a.Present, "fixture should have committed a .atrium.json")
	require.NoError(t, repotrust.Grant(a.Key, a.Hash, a.Remote, time.Now()))
}

const oneEntry = `{"repo_scripts":[{"name":"web","setup_script":"echo repo-local-marker"}]}`

// TestRepoLocal_UntrustedNeverExecutes is the issue's "done means" line: the
// negative (no grant → no process) and its positive control (grant → process)
// in one test, so a gate that refuses everything and a gate that refuses
// nothing both fail it.
func TestRepoLocal_UntrustedNeverExecutes(t *testing.T) {
	inst, dir, repoPath := repoLocalFixture(t, oneEntry)
	rec := &setupRecorder{}
	defer stubSetupExec(rec.record)()

	inst.RunSetupScript(dir)

	assert.Empty(t, rec.scripts(), "an untrusted repo's script must spawn NO process")
	assert.Equal(t, RepoConfigUntrusted, inst.RepoConfigStatus())
	problem := inst.RepoConfigProblem()
	require.NotEmpty(t, problem, "the refusal must be armed for the flush-once modal")
	assert.Contains(t, problem, "atrium trust allow", "the report must name the remedy")
	inst.ClearRepoConfigProblem()

	grantRepo(t, repoPath)
	inst.RunSetupScript(dir)

	require.Len(t, rec.scripts(), 1, "the granted repo's script must run")
	assert.Contains(t, rec.scripts()[0], "repo-local-marker")
	assert.Equal(t, RepoConfigActive, inst.RepoConfigStatus())
	assert.Empty(t, inst.RepoConfigProblem(), "an active config has nothing to report")
}

// TestRepoLocal_EditedWorktreeFileGoesInert closes the prompt-to-execution
// TOCTOU and the agent-self-escalation path in one move: whatever edited the
// file after the grant — a race, or an agent committing to its own branch
// before a resume re-materializes it — the bytes at the moment of use are not
// the granted bytes, so nothing runs.
func TestRepoLocal_EditedWorktreeFileGoesInert(t *testing.T) {
	inst, dir, repoPath := repoLocalFixture(t, oneEntry)
	grantRepo(t, repoPath)
	rec := &setupRecorder{}
	defer stubSetupExec(rec.record)()

	require.NoError(t, os.WriteFile(filepath.Join(dir, ".atrium.json"),
		[]byte(`{"repo_scripts":[{"name":"web","setup_script":"echo edited-after-grant"}]}`), 0o644))
	inst.RunSetupScript(dir)

	assert.Empty(t, rec.scripts(), "edited bytes must not ride an older grant")
	assert.Equal(t, RepoConfigChanged, inst.RepoConfigStatus())
	assert.Contains(t, inst.RepoConfigProblem(), "CHANGED")
}

// TestRepoLocal_PrecedenceAndFallback pins #629's precedence answer from both
// sides: a TRUSTED repo-local entry beats a global entry that also matches
// this repo, and an UNTRUSTED one degrades to exactly what the repo would get
// with no file — the user's own global entry, which still runs (it is the
// user's own config; the trust boundary is the repo's content, not theirs).
func TestRepoLocal_PrecedenceAndFallback(t *testing.T) {
	inst, dir, repoPath := repoLocalFixture(t, oneEntry)
	cfg := config.LoadConfig()
	cfg.RepoScripts = []config.RepoScript{{Name: "global", SetupScript: "echo global-marker"}}
	require.NoError(t, config.SaveConfig(cfg))
	rec := &setupRecorder{}
	defer stubSetupExec(rec.record)()

	inst.RunSetupScript(dir)
	require.Len(t, rec.scripts(), 1, "untrusted repo-local must fall back to the user's own entry")
	assert.Contains(t, rec.scripts()[0], "global-marker")
	assert.Equal(t, RepoConfigUntrusted, inst.RepoConfigStatus(),
		"the fallback still says why the repo's own config was ignored")

	grantRepo(t, repoPath)
	inst.RunSetupScript(dir)
	require.Len(t, rec.scripts(), 2)
	assert.Contains(t, rec.scripts()[1], "repo-local-marker", "trusted repo-local wins over global")
}

// TestRepoLocal_SessionEnvIsGated: session_env is execution-adjacent (PATH,
// LD_PRELOAD) and rides the same entry, so the whole entry is inert together —
// no per-field trust.
func TestRepoLocal_SessionEnvIsGated(t *testing.T) {
	inst, dir, repoPath := repoLocalFixture(t,
		`{"repo_scripts":[{"name":"env","session_env":{"INJECTED":"yes"}}]}`)

	run, ok := inst.resolveSetupRun(dir)
	assert.False(t, ok, "untrusted: nothing resolves (global is empty)")
	assert.Empty(t, run.sessionEnv)

	grantRepo(t, repoPath)
	run, ok = inst.resolveSetupRun(dir)
	require.True(t, ok)
	assert.Contains(t, run.sessionEnv, "INJECTED=yes")
}

// TestRepoLocal_AbsentWithGrantSaysSo: a grant with no file in the worktree is
// divergence, not silence — the branch this worktree checked out does not
// carry the setup the user granted, and the session should say so rather than
// run cold unexplained.
func TestRepoLocal_AbsentWithGrantSaysSo(t *testing.T) {
	inst, dir, repoPath := repoLocalFixture(t, oneEntry)
	grantRepo(t, repoPath)
	rec := &setupRecorder{}
	defer stubSetupExec(rec.record)()

	require.NoError(t, os.Remove(filepath.Join(dir, ".atrium.json")))
	inst.RunSetupScript(dir)

	assert.Empty(t, rec.scripts())
	assert.Equal(t, RepoConfigAbsentGranted, inst.RepoConfigStatus())
	assert.NotEmpty(t, inst.RepoConfigProblem())
}

// TestRepoLocal_MovedRepoIsUntrusted: the ledger keys on the canonical repo
// root, so a repo moved after its grant is a different identity and re-prompts
// — deliberate, path identity IS the trust boundary.
func TestRepoLocal_MovedRepoIsUntrusted(t *testing.T) {
	_, dir, repoPath := repoLocalFixture(t, oneEntry)
	grantRepo(t, repoPath)
	rec := &setupRecorder{}
	defer stubSetupExec(rec.record)()

	moved := repoPath + "-moved"
	require.NoError(t, os.Rename(repoPath, moved))
	// A fresh instance, as a create at the new path would build: the memoized
	// ledger key belongs to the instance, not the process.
	movedInst := &Instance{ident: identity{title: "web"}, Path: moved}

	movedInst.RunSetupScript(dir)

	assert.Empty(t, rec.scripts(), "a moved repo must not inherit its old path's grant")
	assert.Equal(t, RepoConfigUntrusted, movedInst.RepoConfigStatus())
	// The unmoved identity still holds its grant (nothing was revoked).
	l, err := repotrust.Load()
	require.NoError(t, err)
	_, has := l.Lookup(mustCanonical(t, repoPath))
	assert.True(t, has)
}

func mustCanonical(t *testing.T, path string) string {
	t.Helper()
	key, err := repotrust.CanonicalRoot(path)
	require.NoError(t, err)
	return key
}

// TestRepoLocal_SymlinkedRepoPathSharesTheGrant: the same repo reached through
// a symlink is the same identity (EvalSymlinks at key derivation), so a grant
// made at the real path satisfies a session created through the link — and a
// link flipped to a DIFFERENT repo resolves to that repo's key instead, which
// its bytes must then match; nothing about the link itself carries trust.
func TestRepoLocal_SymlinkedRepoPathSharesTheGrant(t *testing.T) {
	_, dir, repoPath := repoLocalFixture(t, oneEntry)
	grantRepo(t, repoPath)
	rec := &setupRecorder{}
	defer stubSetupExec(rec.record)()

	link := repoPath + "-link"
	require.NoError(t, os.Symlink(repoPath, link))
	linked := &Instance{ident: identity{title: "web"}, Path: link}

	linked.RunSetupScript(dir)

	require.Len(t, rec.scripts(), 1, "the symlinked path is the same repo and shares its grant")
	assert.Equal(t, RepoConfigActive, linked.RepoConfigStatus())
}

func TestRepoLocal_InvalidShapesAreInertWithAReason(t *testing.T) {
	t.Run("a symlinked .atrium.json is refused", func(t *testing.T) {
		inst, dir, repoPath := repoLocalFixture(t, oneEntry)
		grantRepo(t, repoPath)
		rec := &setupRecorder{}
		defer stubSetupExec(rec.record)()

		cfgPath := filepath.Join(dir, ".atrium.json")
		require.NoError(t, os.Remove(cfgPath))
		target := filepath.Join(dir, "elsewhere.json")
		require.NoError(t, os.WriteFile(target, []byte(oneEntry), 0o644))
		require.NoError(t, os.Symlink(target, cfgPath))

		inst.RunSetupScript(dir)
		assert.Empty(t, rec.scripts())
		assert.Equal(t, RepoConfigInvalid, inst.RepoConfigStatus())
		assert.Contains(t, inst.RepoConfigReport(), "regular file")
	})

	t.Run("an oversized file is refused whole", func(t *testing.T) {
		inst, dir, repoPath := repoLocalFixture(t, oneEntry)
		grantRepo(t, repoPath)
		rec := &setupRecorder{}
		defer stubSetupExec(rec.record)()

		big := strings.Repeat(" ", (1<<20)+1)
		require.NoError(t, os.WriteFile(filepath.Join(dir, ".atrium.json"), []byte(big), 0o644))

		inst.RunSetupScript(dir)
		assert.Empty(t, rec.scripts())
		assert.Equal(t, RepoConfigInvalid, inst.RepoConfigStatus())
	})

	t.Run("granted-but-undecodable bytes are refused", func(t *testing.T) {
		// Unreachable through `atrium trust allow` (which refuses to grant a file
		// that declares nothing usable), but a hand-edited ledger can say anything;
		// the gate must still not act on bytes that do not parse.
		inst, dir, repoPath := repoLocalFixture(t, oneEntry)
		garbage := []byte("{not json")
		require.NoError(t, os.WriteFile(filepath.Join(dir, ".atrium.json"), garbage, 0o644))
		require.NoError(t, repotrust.Grant(mustCanonical(t, repoPath), repotrust.HashBytes(garbage), "", time.Now()))
		rec := &setupRecorder{}
		defer stubSetupExec(rec.record)()

		inst.RunSetupScript(dir)
		assert.Empty(t, rec.scripts())
		assert.Equal(t, RepoConfigInvalid, inst.RepoConfigStatus())
	})

	t.Run("matcher fields make an entry unusable even when granted", func(t *testing.T) {
		content := `{"repo_scripts":[{"name":"routed","remote_matches":["acme"],"setup_script":"echo x"}]}`
		inst, dir, repoPath := repoLocalFixture(t, content)
		grantRepo(t, repoPath)
		rec := &setupRecorder{}
		defer stubSetupExec(rec.record)()

		inst.RunSetupScript(dir)
		assert.Empty(t, rec.scripts())
		assert.Equal(t, RepoConfigInvalid, inst.RepoConfigStatus())
		assert.Contains(t, inst.RepoConfigReport(), ".atrium.json")
	})
}

// TestRepoLocal_DirectSessionsAreOutOfScope: a direct session runs in the
// user's own checkout — no worktree materializes anything — and #814 records
// them out of scope: no repo-local read, no state, and (with an empty global
// config) nothing to run.
func TestRepoLocal_DirectSessionsAreOutOfScope(t *testing.T) {
	_, _, repoPath := repoLocalFixture(t, oneEntry)
	// The committed .atrium.json is also present in the checkout itself.
	direct := &Instance{ident: identity{title: "web"}, Path: repoPath, direct: true}
	rec := &setupRecorder{}
	defer stubSetupExec(rec.record)()

	direct.RunSetupScript(repoPath)

	assert.Empty(t, rec.scripts())
	assert.Equal(t, RepoConfigUnset, direct.RepoConfigStatus(),
		"a direct session never evaluates repo-local config, so its state stays the zero value")
}

// TestRepoLocal_SweepRefreshesStateButNeverReArmsTheModal: the run-state sweep
// (ComputeRunState → routeRepoScript) keeps RepoConfigStatus live — a grant or
// an edit reaches a running session within a sweep — but must never re-arm the
// one-shot report, or the flush-once modal reopens forever after it is
// cleared.
func TestRepoLocal_SweepRefreshesStateButNeverReArmsTheModal(t *testing.T) {
	inst, _, repoPath := repoLocalFixture(t,
		`{"repo_scripts":[{"name":"web","setup_script":"echo s","run_command":"serve-local"}]}`)
	inst.status = Running
	inst.started = true
	rec := &setupRecorder{}
	defer stubSetupExec(rec.record)()

	// The applying path arms the report; the flush clears it.
	inst.RunSetupScript(inst.WorkingDir())
	require.NotEmpty(t, inst.RepoConfigProblem())
	inst.ClearRepoConfigProblem()

	// A sweep still refreshes the state — and does not resurrect the modal.
	state := inst.ComputeRunState()
	assert.True(t, state.ConfiguredKnown)
	assert.False(t, state.Configured, "untrusted run_command must not configure the d key")
	assert.Equal(t, RepoConfigUntrusted, inst.RepoConfigStatus())
	assert.Empty(t, inst.RepoConfigProblem(), "a read-only sweep must not re-arm the one-shot report")

	// The grant reaches the running session on the next sweep, no restart.
	grantRepo(t, repoPath)
	state = inst.ComputeRunState()
	assert.True(t, state.Configured, "the trusted repo-local run_command reaches the sweep")
	assert.Equal(t, RepoConfigActive, inst.RepoConfigStatus())
	assert.Empty(t, rec.scripts(), "a sweep only reads; nothing may execute from it")
}
