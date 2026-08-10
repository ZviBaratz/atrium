package session

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/internal/testutil"
	"github.com/ZviBaratz/atrium/session/transcript"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Live fork verification (#644). Skipped unless scripts/verify-fork.sh set the
// environment up, because every arm here spends real API turns against a real
// account — `just test` must stay free and hermetic.
//
// # WHY THESE CANNOT BE ORDINARY TESTS
//
// The defect this feature exists to prevent is invisible to everything except a
// real transcript. Outside print mode claude accepts --resume-session-at and
// ignores it: the run exits 0, the JSON envelope reports no error, the session
// starts and answers. fork_test.go's control arm stubs ContainsForkEntries, which
// proves verifyFork's LOGIC separates the two cases — it cannot prove claude still
// behaves the way the logic assumes. Only a driven fork can, and only against a
// version, which is why the harness prints the one it ran on.
const (
	liveForkEnv       = "ATRIUM_LIVE_FORK"
	liveForkConfigEnv = "ATRIUM_LIVE_FORK_CONFIG"
	liveForkSrcEnv    = "ATRIUM_LIVE_FORK_SRC"
)

// liveFork returns the sandboxed config dir and the source conversation's working
// directory, or skips.
//
// The harness supplies both because the source cannot be built here, or anywhere
// else in Go: a checkpoint is a file-history snapshot and `claude -p` does not write
// them, so the source has to be an existing interactive conversation copied into a
// sandbox. That is the harness's job, and doing it here would be the harness written
// twice.
func liveFork(t *testing.T) (configDir, srcDir string) {
	t.Helper()
	if os.Getenv(liveForkEnv) != "1" {
		t.Skipf("live fork verification is off; run scripts/verify-fork.sh to set %s=1", liveForkEnv)
	}
	configDir, srcDir = os.Getenv(liveForkConfigEnv), os.Getenv(liveForkSrcEnv)
	if configDir == "" || srcDir == "" {
		t.Fatalf("%s=1 but %s/%s are not both set — run through scripts/verify-fork.sh",
			liveForkEnv, liveForkConfigEnv, liveForkSrcEnv)
	}
	// The credentials symlink is what makes the sandbox usable; without it every
	// call returns "Not logged in" and each arm below fails for a reason that has
	// nothing to do with forking.
	if _, err := os.Stat(filepath.Join(configDir, ".credentials.json")); err != nil {
		t.Fatalf("no credentials in the sandbox config dir %s: %v", configDir, err)
	}
	return configDir, srcDir
}

// liveCheckpoint reads the source conversation and returns the newest checkpoint —
// the one whose prompt a fork drops — plus the enumeration it came from.
func liveCheckpoint(t *testing.T, configDir, srcDir string) (transcript.Checkpoints, transcript.Checkpoint) {
	t.Helper()
	cps, err := transcript.LoadCheckpoints(context.Background(), "claude", srcDir, transcript.Options{Root: configDir})
	require.NoError(t, err, "could not enumerate the source conversation's checkpoints")
	require.GreaterOrEqual(t, len(cps.List), 2,
		"the source conversation needs at least two checkpoints, or there is no turn to drop")

	cp := cps.List[len(cps.List)-1]
	require.NotEmpty(t, cp.ForkAtID,
		"the newest checkpoint has no fork point — the walk-back found no chain entry before its prompt")
	require.NotEqual(t, cp.ForkAtID, cp.MessageID, "the cut must be a different entry from the turn it drops")
	return cps, cp
}

// runClaude runs claude with the sandbox's config dir and returns its stderr on
// failure. Used only for the control arm, which has to build an argv Atrium's own
// code will not produce.
func runClaude(t *testing.T, configDir, workDir string, args ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	path, err := exec.LookPath("claude")
	require.NoError(t, err, "claude must be on PATH")
	c := exec.CommandContext(ctx, path, args...)
	c.Dir = workDir
	c.Env = forkEnv(configDir)
	out, err := c.CombinedOutput()
	require.NoErrorf(t, err, "claude %v failed: %s", args, out)
}

// TestLiveFork_TruncatesAndVerifies drives Atrium's own fork path end to end
// against live claude: the checkpoint comes from the real reader, the argv from
// forkArgv, the environment from forkEnv, the path from transcript.ForkPath, and
// the verdict from verifyFork.
//
// The assertions after ForkConversation are deliberately not redundant with it.
// ForkConversation returning nil means verifyFork was satisfied; re-reading the
// transcript here says the same thing in the test's own words, so a verifyFork
// that silently stopped checking anything would still be caught.
func TestLiveFork_TruncatesAndVerifies(t *testing.T) {
	configDir, srcDir := liveFork(t)
	cps, cp := liveCheckpoint(t, configDir, srcDir)

	// A directory of its own, because a fork is filed under sanitizeCWD of the cwd
	// it RAN in — that is what puts it in the new session's project rather than the
	// forking session's, and running it in srcDir would hide a regression in that.
	dstDir := t.TempDir()
	id, err := NewSessionID()
	require.NoError(t, err)
	seed := ForkSeed{
		SourceTranscript: cps.Path,
		CutEntryID:       cp.ForkAtID,
		DroppedMessageID: cp.MessageID,
		NewSessionID:     id,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	require.NoError(t, ForkConversation(ctx, dstDir, configDir, seed, "Reply with exactly one word: DELTA"),
		"the fork was refused; if this says the dropped turn survived, claude stopped honouring "+
			"--resume-session-at in print mode and the feature is broken, not the test")

	path := transcript.ForkPath("claude", dstDir, id, transcript.Options{Root: configDir})
	require.FileExists(t, path, "the fork verified but is not where ForkPath says it is")

	present, err := transcript.ContainsEntries(ctx, path, cp.ForkAtID, cp.MessageID)
	require.NoError(t, err)
	assert.True(t, present[cp.ForkAtID], "the fork lost the entry it was cut at")
	assert.False(t, present[cp.MessageID], "the fork kept the turn it was meant to drop")

	// Not vacuous: a transcript of pure failures would satisfy the absence check
	// above, since a turn that never ran leaves no trace either. The fork has to be
	// a live conversation that answered.
	body, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(body), `"type":"assistant"`,
		"the forked transcript has no assistant turn — the run failed rather than forked")
	t.Logf("forked %s -> %s (cut at %s, dropped %s)", filepath.Base(cps.Path), filepath.Base(path), cp.ForkAtID, cp.MessageID)
}

// TestLiveFork_VerifierRefusesAnUntruncatedFork is the arm that matters, and the
// one no hermetic test can stand in for.
//
// It builds a genuinely untruncated fork — the same command Atrium issues, minus
// --resume-session-at — and hands it to the production verifier. Everything about
// that run says success: exit 0, a clean JSON envelope, a real transcript with a
// real answer in it. The only thing wrong is the seed, and verifyFork refusing it
// is the whole of what stands between this feature and shipping a checkpoint fork
// that quietly forks from HEAD.
//
// Its sibling above is not a substitute. A verifyFork mutated to always return nil
// passes that one and fails this one; without this arm, "the fork worked" and "the
// verifier does nothing" are the same observation.
func TestLiveFork_VerifierRefusesAnUntruncatedFork(t *testing.T) {
	configDir, srcDir := liveFork(t)
	cps, cp := liveCheckpoint(t, configDir, srcDir)

	dstDir := t.TempDir()
	id, err := NewSessionID()
	require.NoError(t, err)

	// Atrium's argv with the truncation flag removed — which is exactly what claude
	// behaves like when the same argv is handed to an interactive session.
	runClaude(t, configDir, dstDir,
		"-p", "Reply with exactly one word: ECHO",
		"--output-format", "json",
		"--session-id", id,
		"--fork-session",
		"--resume", cps.Path,
	)

	seed := ForkSeed{
		SourceTranscript: cps.Path,
		CutEntryID:       cp.ForkAtID,
		DroppedMessageID: cp.MessageID,
		NewSessionID:     id,
	}
	err = verifyFork(context.Background(), dstDir, configDir, seed)

	require.Error(t, err,
		"verifyFork accepted an untruncated fork. This is the silent failure #644 exists to prevent: "+
			"the run exited 0 with a clean envelope and forked the whole conversation.")
	assert.Contains(t, err.Error(), "--resume-session-at",
		"the refusal must name the mechanism; the user sees it as a failed start with no pane to inspect")
	t.Logf("verifier correctly refused the control fork: %v", err)
}

// TestLiveFork_InstanceStartSeedsTheSession covers the ordering inside
// Instance.Start, which the unit tests cannot reach: the worktree has to exist
// before the fork runs (the fork is filed under its cwd) and the fork has to
// succeed before tmux launches (a pane seeded from the wrong conversation is
// worse than no pane).
//
// The program is a sleep rather than a real agent. What is being checked is the
// sequencing and the --resume pin, and launching claude in the pane would add a
// trust dialog and a second billable session to a test that learns nothing from
// either.
func TestLiveFork_InstanceStartSeedsTheSession(t *testing.T) {
	configDir, srcDir := liveFork(t)
	testutil.RequireTmux(t)
	cps, cp := liveCheckpoint(t, configDir, srcDir)

	id, err := NewSessionID()
	require.NoError(t, err)
	inst := liveForkInstance(t, configDir, ForkSeed{
		SourceTranscript: cps.Path,
		CutEntryID:       cp.ForkAtID,
		DroppedMessageID: cp.MessageID,
		NewSessionID:     id,
	}, "Reply with exactly one word: FOXTROT")

	require.NoError(t, inst.Start(true), "the session did not start")

	assert.Contains(t, inst.Program, "--resume "+id,
		"the launch command does not pin the forked conversation, so a relaunch would start blank")
	present, err := transcript.ContainsEntries(context.Background(),
		transcript.ForkPath("claude", inst.WorkingDir(), id, transcript.Options{Root: configDir}),
		cp.ForkAtID, cp.MessageID)
	require.NoError(t, err, "the fork is not in the session's own project directory")
	assert.True(t, present[cp.ForkAtID])
	assert.False(t, present[cp.MessageID])
}

// A fork that cannot be materialized must take the whole session down with it. The
// worktree is created before the fork runs, so a refusal that left it behind would
// leave an orphan directory and branch for a session that never existed.
func TestLiveFork_AFailedForkLeavesNothingBehind(t *testing.T) {
	configDir, srcDir := liveFork(t)
	testutil.RequireTmux(t)
	cps, _ := liveCheckpoint(t, configDir, srcDir)

	id, err := NewSessionID()
	require.NoError(t, err)
	inst := liveForkInstance(t, configDir, ForkSeed{
		SourceTranscript: cps.Path,
		// A well-formed uuid that is not in the transcript: claude answers "No message
		// found with message.uuid of" and exits 1, which is the realistic failure — a
		// stale checkpoint, or a transcript claude has since rewritten.
		CutEntryID:       "00000000-0000-4000-8000-000000000000",
		DroppedMessageID: "11111111-1111-4111-8111-111111111111",
		NewSessionID:     id,
	}, "Reply with exactly one word: GOLF")

	err = inst.Start(true)

	require.Error(t, err, "a fork claude refused must fail the start rather than launch a blank session")
	assert.Contains(t, err.Error(), "fork", "the failure must say what went wrong")
	// Read the worktree path AFTER the failed Start, not before: until Start runs
	// there is no worktree, and WorkingDir() falls back to the repo itself — so
	// asserting on the earlier value would be asserting that the user's repo is gone.
	if wt := inst.GetWorktreePath(); wt != "" {
		assert.NoDirExists(t, wt, "the worktree survived a failed fork")
	}
}

// liveForkInstance builds a git-backed instance armed with seed. The repo is a
// throwaway under the test's own temp dir — never an Atrium worktree.
func liveForkInstance(t *testing.T, configDir string, seed ForkSeed, prompt string) *Instance {
	t.Helper()
	repo := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"-c", "user.email=fork@test", "-c", "user.name=fork", "commit", "-q", "--allow-empty", "-m", "root"},
	} {
		c := exec.CommandContext(context.Background(), "git", args...)
		c.Dir = repo
		out, err := c.CombinedOutput()
		require.NoErrorf(t, err, "git %v: %s", args, out)
	}

	inst, err := NewInstance(InstanceOptions{Title: "live-fork-" + seed.NewSessionID[:8], Path: repo, Program: "sleep 300"})
	require.NoError(t, err)
	inst.SetBaseContext(context.Background())
	inst.SetClaudeAccount("live-fork", configDir, false)
	inst.SetForkSeed(&seed, prompt)
	t.Cleanup(func() {
		inst.RebindBaseContext(context.Background())
		_ = inst.Kill()
	})
	return inst
}

// The harness is expected to have left the source conversation in a shape these
// tests can read. Stated as its own check so a setup failure reads as one rather
// than as four unrelated assertion failures — which is exactly how the first run of
// this harness reported that its print-mode source had no checkpoints at all.
func TestLiveFork_HarnessSuppliedAUsableSource(t *testing.T) {
	configDir, srcDir := liveFork(t)
	cps, cp := liveCheckpoint(t, configDir, srcDir)

	require.FileExists(t, cps.Path)
	assert.True(t, strings.HasSuffix(cps.Path, cps.SessionID+".jsonl"),
		"a transcript's filename is its session id; %q is not", cps.Path)
	t.Logf("source: %s (%d checkpoints); newest cuts at %s, drops %s",
		cps.Path, len(cps.List), cp.ForkAtID, cp.MessageID)
}
