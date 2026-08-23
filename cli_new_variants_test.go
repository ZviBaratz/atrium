package main

import (
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/internal/outbox"
	"github.com/ZviBaratz/atrium/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fanOutConfig persists a known profile table and branch prefix. Both default to
// something derived from the machine — DetectAgentProfiles probes for installed agent
// binaries, BranchPrefix from the OS username — so an assertion about which program a
// variant runs, or which branch name is taken, would otherwise pass or fail by accident
// of where it ran.
func fanOutConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := config.LoadConfig()
	cfg.Profiles = []config.Profile{
		{Name: "claude", Program: "claude"},
		{Name: "codex", Program: "codex --full-auto"},
	}
	cfg.DefaultProgram = "claude"
	cfg.BranchPrefix = "t/"
	require.NoError(t, config.SaveConfig(cfg))
	return cfg
}

// spooledTitles is the spool in drain order — which is the order the variants were
// written, since a record's name is its creation timestamp.
func spooledTitles(t *testing.T) []string {
	t.Helper()
	var titles []string
	for _, e := range spooledCreates(t) {
		require.NoError(t, e.Err)
		titles = append(titles, e.Request.Title)
	}
	return titles
}

// TestParseVariantSpec covers the grammar and every refusal it has, each asserted to
// name the entry it is about — a fan-out spec is the one place this command takes
// structured input, so "invalid --variants" alone would leave the caller diffing their
// own comma.
func TestParseVariantSpec(t *testing.T) {
	t.Run("accepts", func(t *testing.T) {
		for _, tc := range []struct {
			raw  string
			want []variantSpec
		}{
			{"claude:2,codex:1", []variantSpec{{"claude", 2}, {"codex", 1}}},
			{"claude", []variantSpec{{"claude", 1}}},
			{"  claude : 2 , codex ", []variantSpec{{"claude", 2}, {"codex", 1}}},
			// The count splits on the LAST colon, so a profile whose name carries one —
			// which config.GetProfiles synthesizes on an install with no profiles block —
			// is still nameable.
			{"claude --model x:y:3", []variantSpec{{"claude --model x:y", 3}}},
		} {
			got, err := parseVariantSpec(tc.raw)
			require.NoError(t, err, tc.raw)
			assert.Equal(t, tc.want, got, tc.raw)
		}
	})

	t.Run("refuses", func(t *testing.T) {
		for _, tc := range []struct{ raw, names string }{
			{"", "--variants was given no profiles"},
			{"   ", "--variants was given no profiles"},
			{"claude,,codex", "empty entry"},
			{"claude:2,", "empty entry"},
			{"claude:two", `"claude:two"`},
			{"claude:0", `asks for 0 sessions`},
			{"claude:-1", `asks for -1 sessions`},
			{":2", "names no profile"},
			{"claude:1,claude:2", `names profile "claude" twice`},
			{fmt.Sprintf("claude:%d", session.MaxVariantBatch+1), "fans out to at most"},
		} {
			_, err := parseVariantSpec(tc.raw)
			require.Error(t, err, tc.raw)
			assert.Contains(t, err.Error(), tc.names, tc.raw)
		}
	})
}

// TestNewVariantsSpoolsOneRecordPerVariant is the shape of the whole feature: N
// ordinary records, each naming the session it will become and the program it will run,
// sharing everything a bake-off shares.
func TestNewVariantsSpoolsOneRecordPerVariant(t *testing.T) {
	sandboxDataDir(t)
	fanOutConfig(t)
	repo := gitRepoWithBranches(t)

	out, _, err := newSession(t, newRequest{
		title: "bake", path: repo, variants: "claude:2,codex:1",
		prompt: "start on the parser", branch: "main", force: true,
	})
	require.NoError(t, err)

	entries := spooledCreates(t)
	require.Len(t, entries, 3)
	assert.Equal(t, []string{"bake-1", "bake-2", "bake-3"}, spooledTitles(t))

	var programs []string
	for _, e := range entries {
		programs = append(programs, e.Request.Program)
		assert.Equal(t, repo, e.Request.Path)
		assert.Equal(t, "main", e.Request.Branch)
		assert.Equal(t, "start on the parser", e.Request.Prompt)
		assert.True(t, e.Request.Force)
	}
	assert.Equal(t, []string{"claude", "claude", "codex --full-auto"}, programs,
		"programs follow spec order, so title i runs program i")

	for _, title := range []string{"bake-1", "bake-2", "bake-3"} {
		assert.Contains(t, out, fmt.Sprintf("queued: create %q in %s", title, repo),
			"every derived name is printed, so the caller knows its branches without --wait")
	}
}

// TestNewVariantsShareOneBatchID: the id is the only thing that tells the drain these
// three are one request rather than three, which is what the whole-batch cap turns on.
func TestNewVariantsShareOneBatchID(t *testing.T) {
	sandboxDataDir(t)
	fanOutConfig(t)
	repo := gitRepoWithBranches(t)

	_, _, err := newSession(t, newRequest{title: "bake", path: repo, variants: "claude:2"})
	require.NoError(t, err)
	first := spooledCreates(t)
	require.Len(t, first, 2)
	require.NotEmpty(t, first[0].Request.Batch)
	assert.Equal(t, first[0].Request.Batch, first[1].Request.Batch)

	_, _, err = newSession(t, newRequest{title: "other", path: repo, variants: "claude:2"})
	require.NoError(t, err)
	all := spooledCreates(t)
	require.Len(t, all, 4)
	assert.NotEqual(t, all[0].Request.Batch, all[3].Request.Batch,
		"a second invocation is a second batch, or one caller's cap refusal would answer another's")
}

// TestNewVariantsWithATotalOfOneIsASingleton: a fan-out of one is not a fan-out. The
// bare title is the pre-#761 contract, and the empty batch id is what keeps the record
// byte-identical to one written before the field existed.
func TestNewVariantsWithATotalOfOneIsASingleton(t *testing.T) {
	sandboxDataDir(t)
	fanOutConfig(t)

	// tempRepo, not gitRepoWithBranches: a total of one derives no names, so it must not
	// reach git at all — the same target an ordinary `atrium new` accepts.
	_, _, err := newSession(t, newRequest{title: "bake", path: tempRepo(t), variants: "codex:1"})
	require.NoError(t, err)

	entries := spooledCreates(t)
	require.Len(t, entries, 1)
	assert.Equal(t, "bake", entries[0].Request.Title)
	assert.Empty(t, entries[0].Request.Batch)
	assert.Equal(t, "codex --full-auto", entries[0].Request.Program)
}

// TestNewVariantsRefusesProgramOrProfile: three flags naming what to run, and --variants
// is the one that names several. Refused before anything is spooled, in both directions.
func TestNewVariantsRefusesProgramOrProfile(t *testing.T) {
	sandboxDataDir(t)
	fanOutConfig(t)
	repo := gitRepoWithBranches(t)

	_, _, err := newSession(t, newRequest{
		title: "bake", path: repo, variants: "claude:2", program: "codex"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "drop --program")

	_, _, err = newSession(t, newRequest{
		title: "bake", path: repo, variants: "claude:2", profile: "codex"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "drop --profile")

	assert.Empty(t, spooledCreates(t), "a refused fan-out spools nothing")
}

// TestNewVariantsRefusesAnUnknownProfile in resolveNewProgram's own words, so the two
// flags that name a profile cannot drift into two different errors for one mistake.
func TestNewVariantsRefusesAnUnknownProfile(t *testing.T) {
	sandboxDataDir(t)
	fanOutConfig(t)

	_, _, err := newSession(t, newRequest{
		title: "bake", path: gitRepoWithBranches(t), variants: "claude:1,nope:2"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `no profile "nope"`)
	assert.Contains(t, err.Error(), "configured:", "the message names what IS configured")
	assert.Empty(t, spooledCreates(t))
}

// TestNewVariantsRefusesABatchOverTheCeiling, naming the ceiling rather than only the
// ask, and spooling nothing.
func TestNewVariantsRefusesABatchOverTheCeiling(t *testing.T) {
	sandboxDataDir(t)
	fanOutConfig(t)

	_, _, err := newSession(t, newRequest{
		title: "bake", path: gitRepoWithBranches(t),
		variants: fmt.Sprintf("claude:%d", session.MaxVariantBatch+1)})
	require.Error(t, err)
	assert.Contains(t, err.Error(), fmt.Sprintf("at most %d", session.MaxVariantBatch))
	assert.Empty(t, spooledCreates(t))
}

// TestNewVariantsSkipsATitleAStoredSessionOwns: the in-memory half of the freeness
// check, over the derived candidates rather than over the stem.
func TestNewVariantsSkipsATitleAStoredSessionOwns(t *testing.T) {
	sandboxDataDir(t)
	fanOutConfig(t)
	repo := gitRepoWithBranches(t)
	seedInstances(t, inst("bake-1", repo))

	_, _, err := newSession(t, newRequest{title: "bake", path: repo, variants: "claude:3"})
	require.NoError(t, err)
	assert.Equal(t, []string{"bake-2", "bake-3", "bake-4"}, spooledTitles(t))
}

// TestNewVariantsSkipsATitleAnOrphanBranchOwns is the only test of the git probe.
//
// A branch with no session row behind it — what a crashed build leaves — is invisible to
// the stored-instance check and would be handed to the drain as a free name, which
// Worktree.Setup reads as a resume. Delete the probe and every other test here still
// passes.
func TestNewVariantsSkipsATitleAnOrphanBranchOwns(t *testing.T) {
	sandboxDataDir(t)
	cfg := fanOutConfig(t)
	repo := gitRepoWithBranches(t, cfg.BranchPrefix+"bake-1")

	_, _, err := newSession(t, newRequest{title: "bake", path: repo, variants: "claude:2"})
	require.NoError(t, err)
	assert.Equal(t, []string{"bake-2", "bake-3"}, spooledTitles(t))
}

// TestNewVariantsRefusesATargetItCannotReadBranchesOf: a fan-out wants one worktree per
// variant, so a target that is not a repository has nothing to derive names against.
// Refused rather than read as a repo with no branches, which is what a probe folding
// "could not ask" into "free" would have done.
//
// It is also half of the guard that the fan-out is the ONLY path that runs git. The
// other half is every test in this package that spools through tempRepo — a directory
// that is not a repository — and succeeds, TestNewVariantsWithATotalOfOneIsASingleton
// among them. Reaching git on the singleton path would turn all of those red here.
func TestNewVariantsRefusesATargetItCannotReadBranchesOf(t *testing.T) {
	sandboxDataDir(t)
	fanOutConfig(t)

	_, _, err := newSession(t, newRequest{title: "bake", path: tempRepo(t), variants: "claude:2"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is not a git repository, and a fan-out needs one")
	// Never the wording reserved for a git that could not be RUN. The two are told apart
	// by git.ProbeGitRepo, and conflating them sends a CI retry after a missing .git for
	// a fork failure or a cold checkout that clears by itself.
	assert.NotContains(t, err.Error(), "could not read the branches")
	assert.Empty(t, spooledCreates(t))
}

// TestNewVariantsRefusesADerivedTitleOverTheCap names the derived title and how much to
// drop, rather than reporting the collision that is not there. The stem itself is under
// the limit, which is the case a length check on the title alone would let through.
func TestNewVariantsRefusesADerivedTitleOverTheCap(t *testing.T) {
	sandboxDataDir(t)
	fanOutConfig(t)
	stem := strings.Repeat("a", session.MaxTitleLen)

	_, _, err := newSession(t, newRequest{
		title: stem, path: gitRepoWithBranches(t), variants: "claude:2"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), stem+"-1")
	assert.Contains(t, err.Error(), fmt.Sprintf("the limit is %d", session.MaxTitleLen))
	assert.Empty(t, spooledCreates(t))
}

// TestNewVariantsRollsBackAPartiallyWrittenBatch: a batch half in the spool is a batch
// created half, which is the outcome the whole-batch cap gate exists to prevent — so it
// must not be reachable through a failed write either. The stdout assertion is the other
// half: a "queued" line for a record that has just been withdrawn would be a lie the
// caller acts on.
func TestNewVariantsRollsBackAPartiallyWrittenBatch(t *testing.T) {
	sandboxDataDir(t)
	fanOutConfig(t)

	calls := 0
	write := writeCreateRecord
	writeCreateRecord = func(r outbox.Request) (string, error) {
		calls++
		if calls == 3 {
			return "", fmt.Errorf("no space left on device")
		}
		return write(r)
	}
	t.Cleanup(func() { writeCreateRecord = write })

	out, _, err := newSession(t, newRequest{
		title: "bake", path: gitRepoWithBranches(t), variants: "claude:3"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `failed to queue variant "bake-3"`)
	assert.Contains(t, err.Error(), "no space left on device")
	assert.Empty(t, spooledCreates(t), "the two that landed are withdrawn")
	assert.NotContains(t, out, "queued:", "nothing is announced until the whole batch is committed")
}

// TestNewVariantsWaitReportsEverySessionCreated (#761's fourth criterion): a partial
// outcome must report BOTH halves. A wait that returned on the first refusal would drop
// the sessions the caller actually got, and one that reported only the successes would
// exit 0 on a bake-off that is short a variant.
//
// The refused member is the FIRST one on purpose. Refuse the last and a wait that
// returns early still prints both created lines, so the assertion would hold over the
// bug it is here to catch.
func TestNewVariantsWaitReportsEverySessionCreated(t *testing.T) {
	sandboxDataDir(t)
	fanOutConfig(t)
	repo := gitRepoWithBranches(t)

	_, _, err := newSession(t, newRequest{title: "bake", path: repo, variants: "claude:3"})
	require.NoError(t, err)
	entries := spooledCreates(t)
	require.Len(t, entries, 3)

	// A stand-in drain: the first member is refused, the other two settle with rows.
	// writeInstances rather than seedInstances — require ends in FailNow, which from a
	// spawned goroutine kills it silently and would surface here as a wait timeout
	// blamed on the protocol.
	drainErr := make(chan error, 1)
	go func() {
		drainErr <- func() error {
			rows := []session.InstanceData{
				{Title: "bake-2", Path: repo, Branch: "t/bake-2"},
				{Title: "bake-3", Path: repo, Branch: "t/bake-3"},
			}
			if err := writeInstances(rows...); err != nil {
				return err
			}
			for _, e := range entries[1:] {
				if err := outbox.Remove(e.Path); err != nil {
					return err
				}
			}
			return outbox.Reject(entries[0].Path, "the title is already used by another session")
		}()
	}()
	require.NoError(t, <-drainErr)

	out, waitErr := runNewWait(t, entries, repo, 2*time.Second)
	require.Error(t, waitErr)
	assert.Contains(t, out, `created "bake-2" on t/bake-2`)
	assert.Contains(t, out, `created "bake-3" on t/bake-3`)
	assert.Contains(t, waitErr.Error(), "1 of 3 requested sessions were not created")
	assert.Contains(t, waitErr.Error(), `atrium did not create "bake-1"`)
	assert.Contains(t, waitErr.Error(), "already used by another session")
}

// TestNewVariantsWaitTimesOutNamingTheBatch: nothing drains, so every member is still
// queued. Each member's message names the batch it belongs to and says a batch is built
// one session at a time — a --wait sized for one create is the mistake this copy exists
// to answer — and the tally is left to the returned error, which is taken once every
// member has been accounted for. A per-member count could only ever report the members
// awaited BEFORE it, which for the first is always zero while later members go on
// printing their own "created" lines.
func TestNewVariantsWaitTimesOutNamingTheBatch(t *testing.T) {
	sandboxDataDir(t)
	fanOutConfig(t)
	repo := gitRepoWithBranches(t)

	_, _, err := newSession(t, newRequest{title: "bake", path: repo, variants: "claude:2"})
	require.NoError(t, err)
	entries := spooledCreates(t)
	require.Len(t, entries, 2)

	out, waitErr := runNewWait(t, entries, repo, time.Millisecond)
	require.Error(t, waitErr)
	assert.NotContains(t, out, "created ")
	assert.Contains(t, waitErr.Error(), "2 of 2 requested sessions were not created")
	assert.Contains(t, waitErr.Error(), `without session "bake-1" appearing`)
	assert.Contains(t, waitErr.Error(), "one of 2 this atrium new asked for")
	assert.Contains(t, waitErr.Error(), "one session at a time")
	// waitForCreate's clause, kept word for word: a member held in the outbox for the
	// length of its own build is the common reason a batch outruns its --wait, and
	// dropping it would have a member mid-build read as untouched.
	assert.Contains(t, waitErr.Error(), "being built right now")
}

// runNewWait drives waitForCreates over already-spooled records, which is what the
// batch half of --wait does after runNew has spooled. Separate from newSession because
// the wait has to run against a spool a fake drain has already acted on.
func runNewWait(
	t *testing.T, entries []outbox.CreateEntry, repo string, timeout time.Duration,
) (string, error) {
	t.Helper()
	members := make([]spooledVariant, 0, len(entries))
	for _, e := range entries {
		members = append(members, spooledVariant{title: e.Request.Title, record: e.Path})
	}
	var out strings.Builder
	err := waitForCreates(&out, members, repo, timeout)
	return out.String(), err
}

// TestNewFlagErrorsPrecedeTargetErrors: a command line that contradicts itself is
// answered as such, ahead of whatever else happens to be wrong about the world.
//
// The pairs below are both wrong twice over — a flag conflict or a profile typo AND a
// path that is not a directory — and the caller can only see one of them in their own
// argv. Pinned because deriving the variant titles needs the target resolved, which is
// what pulled program resolution below it in the first place.
func TestNewFlagErrorsPrecedeTargetErrors(t *testing.T) {
	sandboxDataDir(t)
	fanOutConfig(t)
	missing := filepath.Join(t.TempDir(), "no-such-dir")

	for _, tc := range []struct {
		name, wants string
		r           newRequest
	}{
		{"variants with program", "drop --program",
			newRequest{title: "bake", path: missing, variants: "claude:2", program: "codex"}},
		{"variants with profile", "drop --profile",
			newRequest{title: "bake", path: missing, variants: "claude:2", profile: "codex"}},
		{"program with profile", "pass one",
			newRequest{title: "bake", path: missing, program: "codex", profile: "codex"}},
		{"unknown profile", `no profile "nope"`,
			newRequest{title: "bake", path: missing, profile: "nope"}},
		// The same mistake through the flag this feature adds. It reached the profile
		// table only inside the plan before, which runs AFTER the target is resolved —
		// so a --variants typo was reported behind a bad --path while the identical
		// --profile typo was reported ahead of it, and the comment at resolveNewProgram
		// claimed otherwise for both.
		{"unknown variant profile", `no profile "nope"`,
			newRequest{title: "bake", path: missing, variants: "nope:2"}},
		// An explicitly empty spec is a mistake about the command line too, and the one
		// most likely to arrive from a script: `--variants "$VARIANTS"` with the variable
		// unset. Read as "no fan-out" it would hand back one session, silently.
		{"empty variants", "--variants was given no profiles",
			newRequest{title: "bake", path: missing, variantsSet: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := newSession(t, tc.r)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wants)
			assert.NotContains(t, err.Error(), "is not a directory",
				"the argv mistake outranks the one about the world")
		})
	}
}

// TestParseVariantSpecCannotOverflowItsTotal: the ceiling used to be tested after the
// loop, so two counts near the top of int summed to a NEGATIVE total, passed
// `total > MaxVariantBatch`, and handed resolveVariantPrograms a loop that allocates one
// string per requested session until the process dies. Both entries are individually
// refusable, which is the point — the bound has to bite before the addition, not after it.
func TestParseVariantSpecCannotOverflowItsTotal(t *testing.T) {
	// A first entry that exactly fills the ceiling without tripping it, then one large
	// enough to wrap the sum. This shape is what the per-entry bound is for, and picking
	// it took a mutation: two halves of MaxInt are BOTH caught by the running total,
	// because the first alone is already over the ceiling — so a spec built that way
	// stays refused with the per-entry bound deleted and proves nothing about it.
	specs, err := parseVariantSpec(
		fmt.Sprintf("claude:%d,codex:%d", session.MaxVariantBatch, math.MaxInt))
	require.Error(t, err, "a count that wraps the total must be refused, not summed into a negative")
	assert.Nil(t, specs)
	assert.Contains(t, err.Error(), "at most")

	// The control: a single such entry was always refused, by the running total rather
	// than by the bound above. Without it a guard covering only this case would look
	// identical from here.
	_, err = parseVariantSpec(fmt.Sprintf("claude:%d", math.MaxInt))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "at most")
}

// TestNewVariantsRefusesAnEmptySpecFromArgv drives the flag rather than the struct,
// because the defect it guards is in the wiring: "" is both the flag's default and a
// value a caller can pass, so only cobra's Changed can tell "no --variants" from
// "--variants with nothing in it". Reading them as the same thing hands a script one
// session where it asked for N.
func TestNewVariantsRefusesAnEmptySpecFromArgv(t *testing.T) {
	sandboxDataDir(t)
	fanOutConfig(t)
	restoreRootCmd(t)

	cmd := rootCmd
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"new", "bake", "--path", tempRepo(t), "--variants", ""})
	err := cmd.Execute()

	require.Error(t, err, `--variants "" is a mistake about the command line, not a singleton`)
	assert.Contains(t, err.Error(), "--variants was given no profiles")
	assert.Empty(t, spooledCreates(t))
}

// TestSpoolBatchNamesAMemberItCouldNotWithdraw is the rollback's own failure path, and
// the case it must not report as a clean withdrawal is the one it exists for: a running
// drain CLAIMS a record, which renames it out from under this command. os.Remove then
// sees ENOENT — which outbox.Remove would answer nil to, leaving the caller told nothing
// was queued while that session, its branch and its worktree come up minutes later.
func TestSpoolBatchNamesAMemberItCouldNotWithdraw(t *testing.T) {
	sandboxDataDir(t)

	write := writeCreateRecord
	t.Cleanup(func() { writeCreateRecord = write })
	calls := 0
	writeCreateRecord = func(r outbox.Request) (string, error) {
		calls++
		if calls == 2 {
			return "", errors.New("disk full")
		}
		record, err := write(r)
		if err != nil {
			return "", err
		}
		// Stand in for the drain claiming it between the two writes.
		require.NoError(t, os.Rename(record, outbox.ClaimPath(record)))
		return record, nil
	}

	_, err := spoolBatch([]outbox.Request{
		{Title: "bake-1", Path: t.TempDir()}, {Title: "bake-2", Path: t.TempDir()},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `failed to queue variant "bake-2"`)
	assert.Contains(t, err.Error(), `queued variant "bake-1" was claimed by a running atrium`,
		"a claimed member is being built, which is exactly what the caller has to be told")
}
