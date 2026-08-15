package main

import (
	"bytes"
	"io"
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

func spooledCreates(t *testing.T) []outbox.CreateEntry {
	t.Helper()
	entries, err := outbox.ListCreates()
	require.NoError(t, err)
	return entries
}

// newSession runs the command with everything but the title and path defaulted,
// which is what most of these tests vary.
func newSession(t *testing.T, r newRequest) (stdout, stderr string, err error) {
	t.Helper()
	var out, errOut bytes.Buffer
	err = runNew(&out, &errOut, r)
	return out.String(), errOut.String(), err
}

// tempRepo returns a directory that exists, standing in for a repo. The CLI
// deliberately runs no git, so a plain directory is the whole fixture.
func tempRepo(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	return dir
}

// TestNewSpoolsRequest is the basic contract: everything the caller chose reaches
// the spool, and the confirmation names what was queued.
func TestNewSpoolsRequest(t *testing.T) {
	sandboxDataDir(t)
	dir := tempRepo(t)

	stdout, _, err := newSession(t, newRequest{
		title: "fix-auth", path: dir, program: "codex",
		branch: "release/2.0", prompt: "start on the parser", force: true,
	})
	require.NoError(t, err)

	entries := spooledCreates(t)
	require.Len(t, entries, 1)
	got := entries[0].Request
	assert.Equal(t, "fix-auth", got.Title)
	assert.Equal(t, dir, got.Path)
	assert.Equal(t, "codex", got.Program)
	assert.Equal(t, "release/2.0", got.Branch)
	assert.Equal(t, "start on the parser", got.Prompt)
	assert.True(t, got.Force)
	assert.Contains(t, stdout, "fix-auth")
	assert.Contains(t, stdout, dir)
}

// TestNewNeverWritesState is the invariant the whole spool design exists for: a
// second writer would be clobbered by the TUI's next whole-file save, so this
// process must not touch state.json at all. Asserted on the bytes, so any write
// fails this.
func TestNewNeverWritesState(t *testing.T) {
	dataDir := sandboxDataDir(t)
	seedInstances(t, inst("other", "/repo/web"))
	statePath := filepath.Join(dataDir, config.StateFileName)
	before, err := os.ReadFile(statePath)
	require.NoError(t, err)

	_, _, err = newSession(t, newRequest{title: "fix-auth", path: tempRepo(t)})
	require.NoError(t, err)

	after, err := os.ReadFile(statePath)
	require.NoError(t, err)
	assert.Equal(t, before, after, "atrium new must not write state.json")
}

// TestNewRefusesUnusableTitle: each of these spools nothing, because a request
// that cannot become a session is better refused where the caller can see it than
// rejected minutes later by a receipt.
func TestNewRefusesUnusableTitle(t *testing.T) {
	cases := map[string]string{
		"empty":      "",
		"whitespace": "   \t ",
		// One over the cap the new-session field enforces. A session past it could
		// not be renamed to its own name.
		"too long": strings.Repeat("x", session.MaxTitleLen+1),
	}
	for name, title := range cases {
		t.Run(name, func(t *testing.T) {
			sandboxDataDir(t)
			_, _, err := newSession(t, newRequest{title: title, path: tempRepo(t)})
			require.Error(t, err)
			assert.Empty(t, spooledCreates(t))
		})
	}
}

// TestNewAcceptsTitleAtTheCap is the boundary's other side: exactly
// MaxTitleLen is what the field accepts, so it is what this accepts.
func TestNewAcceptsTitleAtTheCap(t *testing.T) {
	sandboxDataDir(t)
	_, _, err := newSession(t, newRequest{
		title: strings.Repeat("x", session.MaxTitleLen), path: tempRepo(t),
	})
	require.NoError(t, err)
	assert.Len(t, spooledCreates(t), 1)
}

// TestNewRefusesMissingDirectory: reported here rather than as a receipt read
// back minutes later.
func TestNewRefusesMissingDirectory(t *testing.T) {
	sandboxDataDir(t)
	_, _, err := newSession(t, newRequest{
		title: "fix-auth", path: filepath.Join(t.TempDir(), "no-such-dir"),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a directory")
	assert.Empty(t, spooledCreates(t))
}

// TestNewRefusesTitleAlreadyTakenInThatRepo pins the pre-check: the same title in
// the same repo is the common mistake, and it is caught before anything is
// spooled — the resolve-then-spool order `send` uses.
func TestNewRefusesTitleAlreadyTakenInThatRepo(t *testing.T) {
	sandboxDataDir(t)
	dir := tempRepo(t)
	seedInstances(t, inst("fix-auth", dir))

	_, _, err := newSession(t, newRequest{title: "fix-auth", path: dir})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already uses that name")
	assert.Empty(t, spooledCreates(t))
}

// TestNewAllowsTheSameTitleInAnotherRepo is the negative control: titles are
// unique only within a repo, so scoping the pre-check by path is what keeps it
// from refusing a legitimate create.
func TestNewAllowsTheSameTitleInAnotherRepo(t *testing.T) {
	sandboxDataDir(t)
	seedInstances(t, inst("fix-auth", "/repo/elsewhere"))

	_, _, err := newSession(t, newRequest{title: "fix-auth", path: tempRepo(t)})
	require.NoError(t, err)
	assert.Len(t, spooledCreates(t), 1)
}

// TestNewRefusesTitleWhoseBranchWouldCollide: the pre-check compares *derived*
// names, not raw titles, so two titles that slug to one branch are caught. A raw
// string compare would let this through to a drain that refuses it.
func TestNewRefusesTitleWhoseBranchWouldCollide(t *testing.T) {
	sandboxDataDir(t)
	dir := tempRepo(t)
	seedInstances(t, inst("Fix Auth", dir))

	_, _, err := newSession(t, newRequest{title: "fix-auth", path: dir})
	require.Error(t, err)
	assert.Empty(t, spooledCreates(t))
}

// TestNewDefaultsPathToTheCurrentDirectory: the flagless form is the one an agent
// or a Justfile runs.
func TestNewDefaultsPathToTheCurrentDirectory(t *testing.T) {
	sandboxDataDir(t)
	dir := tempRepo(t)
	t.Chdir(dir)

	_, _, err := newSession(t, newRequest{title: "fix-auth"})
	require.NoError(t, err)

	entries := spooledCreates(t)
	require.Len(t, entries, 1)
	assert.Equal(t, dir, entries[0].Request.Path)
}

// TestNewRedirectsAWorktreeToItsRepo is the case #703 was filed for. An agent
// inside an Atrium session is standing in a worktree, whose git toplevel is the
// worktree itself — so the default would build a worktree of a worktree. The repo
// is a lookup of what Atrium recorded, and it is announced rather than silent.
func TestNewRedirectsAWorktreeToItsRepo(t *testing.T) {
	sandboxDataDir(t)
	repo := tempRepo(t)
	worktree := tempRepo(t)
	seedInstances(t, session.InstanceData{
		Title: "Issue #703", Path: repo, Program: "claude",
		Worktree: session.GitWorktreeData{RepoPath: repo, WorktreePath: worktree},
	})

	// Deep inside the worktree, which is where an agent actually stands.
	deep := filepath.Join(worktree, "app", "nested")
	require.NoError(t, os.MkdirAll(deep, 0o755))
	t.Chdir(deep)

	_, stderr, err := newSession(t, newRequest{title: "follow-up"})
	require.NoError(t, err)

	entries := spooledCreates(t)
	require.Len(t, entries, 1)
	assert.Equal(t, repo, entries[0].Request.Path, "the request must target the repo, not the worktree")
	assert.Contains(t, stderr, "Issue #703", "the redirection names the session it came from")
	assert.Contains(t, stderr, repo)
}

// TestNewDoesNotRedirectASiblingOfAWorktree guards the containment check's
// boundary: a path that merely shares a prefix with a worktree is not inside it.
func TestNewDoesNotRedirectASiblingOfAWorktree(t *testing.T) {
	sandboxDataDir(t)
	parent := tempRepo(t)
	worktree := filepath.Join(parent, "web")
	sibling := filepath.Join(parent, "web-old")
	require.NoError(t, os.MkdirAll(worktree, 0o755))
	require.NoError(t, os.MkdirAll(sibling, 0o755))
	seedInstances(t, session.InstanceData{
		Title: "web", Path: "/repo/web", Program: "claude",
		Worktree: session.GitWorktreeData{RepoPath: "/repo/web", WorktreePath: worktree},
	})

	_, stderr, err := newSession(t, newRequest{title: "fix-auth", path: sibling})
	require.NoError(t, err)

	entries := spooledCreates(t)
	require.Len(t, entries, 1)
	assert.Equal(t, sibling, entries[0].Request.Path)
	assert.NotContains(t, stderr, "worktree")
}

// TestNewResolvesProfileToItsProgram: the request carries a concrete program, so
// the drain does no profile lookup and a typo cannot survive until then.
func TestNewResolvesProfileToItsProgram(t *testing.T) {
	sandboxDataDir(t)
	cfg := config.LoadConfig()
	cfg.Profiles = []config.Profile{{Name: "codex", Program: "codex --full-auto"}}
	require.NoError(t, config.SaveConfig(cfg))

	_, _, err := newSession(t, newRequest{title: "fix-auth", path: tempRepo(t), profile: "codex"})
	require.NoError(t, err)

	entries := spooledCreates(t)
	require.Len(t, entries, 1)
	assert.Equal(t, "codex --full-auto", entries[0].Request.Program)
}

// TestNewRefusesUnknownProfile, spooling nothing — and naming what is configured,
// because "no profile x" alone leaves the caller guessing.
func TestNewRefusesUnknownProfile(t *testing.T) {
	sandboxDataDir(t)
	cfg := config.LoadConfig()
	cfg.Profiles = []config.Profile{{Name: "codex", Program: "codex"}}
	require.NoError(t, config.SaveConfig(cfg))

	_, _, err := newSession(t, newRequest{title: "fix-auth", path: tempRepo(t), profile: "nope"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "codex", "the error lists the profiles that do exist")
	assert.Empty(t, spooledCreates(t))
}

// TestNewRefusesProgramAndProfileTogether: they name the same thing, and silently
// preferring one would make the other's presence a lie.
func TestNewRefusesProgramAndProfileTogether(t *testing.T) {
	sandboxDataDir(t)
	_, _, err := newSession(t, newRequest{
		title: "fix-auth", path: tempRepo(t), program: "codex", profile: "claude",
	})
	require.Error(t, err)
	assert.Empty(t, spooledCreates(t))
}

// TestNewLeavesProgramUnsetByDefault is what makes an unflagged `atrium new`
// equivalent to pressing the new-session key: the drain fills it with the TUI's
// own program, which a value baked in here would override.
func TestNewLeavesProgramUnsetByDefault(t *testing.T) {
	sandboxDataDir(t)
	_, _, err := newSession(t, newRequest{title: "fix-auth", path: tempRepo(t)})
	require.NoError(t, err)

	entries := spooledCreates(t)
	require.Len(t, entries, 1)
	assert.Empty(t, entries[0].Request.Program)
}

// TestNewTrimsTrailingNewlineOnly: a heredoc or a pipe adds the tail; leading
// whitespace could be meaningful to the agent, so only the tail goes.
func TestNewTrimsTrailingNewlineOnly(t *testing.T) {
	sandboxDataDir(t)
	_, _, err := newSession(t, newRequest{
		title: "fix-auth", path: tempRepo(t), prompt: "  indented\nsecond line\n\n",
	})
	require.NoError(t, err)

	entries := spooledCreates(t)
	require.Len(t, entries, 1)
	assert.Equal(t, "  indented\nsecond line", entries[0].Request.Prompt)
}

// TestFirstPrompt: unlike send's, an absent argument means no prompt rather than
// stdin — the create form's field is optional, and a bare `atrium new fix-auth`
// in a terminal would otherwise block on a tty forever.
func TestFirstPrompt(t *testing.T) {
	got, err := firstPrompt([]string{"title"}, strings.NewReader("from stdin"))
	require.NoError(t, err)
	assert.Empty(t, got, "no prompt argument means no prompt, not stdin")

	got, err = firstPrompt([]string{"title", "inline"}, strings.NewReader("from stdin"))
	require.NoError(t, err)
	assert.Equal(t, "inline", got)

	got, err = firstPrompt([]string{"title", "-"}, strings.NewReader("from stdin"))
	require.NoError(t, err)
	assert.Equal(t, "from stdin", got)
}

// TestNewWaitReportsTheBranchTheTUIRecorded: the branch is read back out of
// state.json rather than derived from the title, because the slug rules have a
// hash fallback and a direct session has no branch at all. Printing a name we did
// not see would be a guess dressed as a fact.
func TestNewWaitReportsTheBranchTheTUIRecorded(t *testing.T) {
	sandboxDataDir(t)
	dir := tempRepo(t)

	// Stand in for the TUI's drain: once the request appears, record the session
	// it produced and clear the file — the order the real drain uses.
	// writeInstances, not seedInstances: a require.NoError here would be a t.FailNow
	// off the test goroutine, which testing forbids — it would kill this goroutine
	// silently and surface as a wait timeout blaming the protocol.
	var seedErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 200 {
			if entries, err := outbox.ListCreates(); err == nil && len(entries) == 1 {
				seedErr = writeInstances(session.InstanceData{
					Title: "fix-auth", Path: dir, Program: "claude", Branch: "zvi/fix-auth",
					Worktree: session.GitWorktreeData{
						RepoPath: dir, WorktreePath: "/worktrees/fix-auth", BranchName: "zvi/fix-auth",
					},
				})
				_ = outbox.Remove(entries[0].Path)
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()

	stdout, _, err := newSession(t, newRequest{title: "fix-auth", path: dir, wait: 5 * time.Second})
	<-done
	require.NoError(t, seedErr)
	require.NoError(t, err)
	assert.Contains(t, stdout, "created \"fix-auth\"")
	assert.Contains(t, stdout, "zvi/fix-auth")
	assert.Contains(t, stdout, "/worktrees/fix-auth")
}

// TestNewWaitSaysNoBranchForADirectSession: a non-git target creates a session
// with no worktree and no branch, and the line must not invent one.
func TestNewWaitSaysNoBranchForADirectSession(t *testing.T) {
	sandboxDataDir(t)
	dir := tempRepo(t)

	var seedErr error // see the note in TestNewWaitReportsTheBranchTheTUIRecorded
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 200 {
			if entries, err := outbox.ListCreates(); err == nil && len(entries) == 1 {
				seedErr = writeInstances(inst("fix-auth", dir)) // no Branch, no worktree
				_ = outbox.Remove(entries[0].Path)
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()

	stdout, _, err := newSession(t, newRequest{title: "fix-auth", path: dir, wait: 5 * time.Second})
	<-done
	require.NoError(t, seedErr)
	require.NoError(t, err)
	assert.Contains(t, stdout, "created \"fix-auth\"")
	assert.NotContains(t, stdout, " on ", "a direct session has no branch to name")
}

// TestNewWaitFailsOnRejection: the receipt is checked before the file's absence,
// because Reject writes it first — so a refusal can never be read as a success.
func TestNewWaitFailsOnRejection(t *testing.T) {
	sandboxDataDir(t)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 200 {
			if entries, err := outbox.ListCreates(); err == nil && len(entries) == 1 {
				_ = outbox.Reject(entries[0].Path, "branch already exists")
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()

	_, _, err := newSession(t, newRequest{title: "fix-auth", path: tempRepo(t), wait: 5 * time.Second})
	<-done
	require.Error(t, err)
	assert.Contains(t, err.Error(), "branch already exists")
}

// TestNewWaitClearsConsumedReceipt: the sweep only clears receipts past the TTL
// horizon, so a reported one has to be cleared by its reader or it lingers for a
// day.
func TestNewWaitClearsConsumedReceipt(t *testing.T) {
	sandboxDataDir(t)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 200 {
			if entries, err := outbox.ListCreates(); err == nil && len(entries) == 1 {
				_ = outbox.Reject(entries[0].Path, "nope")
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()

	_, _, err := newSession(t, newRequest{title: "fix-auth", path: tempRepo(t), wait: 5 * time.Second})
	<-done
	require.Error(t, err)

	dir, err := outbox.CreateDir()
	require.NoError(t, err)
	left, err := filepath.Glob(filepath.Join(dir, "*.rejected"))
	require.NoError(t, err)
	assert.Empty(t, left)
}

// TestNewWaitTimesOut says the request is still queued, because it is: the
// timeout is this process giving up, not Atrium refusing.
func TestNewWaitTimesOut(t *testing.T) {
	sandboxDataDir(t)
	_, _, err := newSession(t, newRequest{
		title: "fix-auth", path: tempRepo(t), wait: 150 * time.Millisecond,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "still queued")
	assert.Len(t, spooledCreates(t), 1, "and it really is still there")
}

// TestNewRedirectsThroughASymlinkedWorktree: state.json records the path Atrium
// resolved at creation, and the cwd an agent reports may reach the same directory by
// another name — on macOS a data dir under /tmp is recorded as /private/tmp, which is
// the default layout, not a corner case. A missed redirect is silent: it produces a
// worktree of a worktree rather than an error.
//
// tempRepo resolves symlinks precisely so the rest of these tests are not testing the
// platform's /tmp layout; this one reintroduces one deliberately.
func TestNewRedirectsThroughASymlinkedWorktree(t *testing.T) {
	sandboxDataDir(t)
	repo := tempRepo(t)
	worktree := tempRepo(t)
	seedInstances(t, session.InstanceData{
		Title: "Issue #703", Path: repo, Program: "claude",
		Worktree: session.GitWorktreeData{RepoPath: repo, WorktreePath: worktree},
	})

	// A second name for the same worktree, which is what the caller stands in.
	link := filepath.Join(t.TempDir(), "via-link")
	require.NoError(t, os.Symlink(worktree, link))

	_, stderr, err := newSession(t, newRequest{title: "follow-up", path: link})
	require.NoError(t, err)

	entries := spooledCreates(t)
	require.Len(t, entries, 1)
	assert.Equal(t, repo, entries[0].Request.Path, "a symlinked worktree is still a worktree")
	assert.Contains(t, stderr, "Issue #703")
}

// TestNewRefusesWhenTheRedirectTargetIsGone: the repo path comes off disk rather than
// off the command line, so unlike --path it has not been validated by anything. A repo
// that has since been moved or deleted would otherwise be spooled as a path that no
// longer exists — reported to the caller as "queued", and refused by the drain minutes
// later where a fire-and-forget caller never sees it.
func TestNewRefusesWhenTheRedirectTargetIsGone(t *testing.T) {
	sandboxDataDir(t)
	worktree := tempRepo(t)
	gone := filepath.Join(t.TempDir(), "moved-away")
	seedInstances(t, session.InstanceData{
		Title: "Issue #703", Path: gone, Program: "claude",
		Worktree: session.GitWorktreeData{RepoPath: gone, WorktreePath: worktree},
	})

	_, _, err := newSession(t, newRequest{title: "follow-up", path: worktree})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is gone")
	assert.Empty(t, spooledCreates(t), "a resolve failure spools nothing")
}

// TestNewRefusesANegativeWait: cobra parses "--wait -5s" happily, and a plain
// `wait > 0` test would silently read it as "do not wait" — so a caller that
// fat-fingered a sign would be told the request was queued and never learn what
// became of it.
func TestNewRefusesANegativeWait(t *testing.T) {
	sandboxDataDir(t)
	_, _, err := newSession(t, newRequest{title: "fix-auth", path: tempRepo(t), wait: -5 * time.Second})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "negative")
	assert.Empty(t, spooledCreates(t), "a bad flag spools nothing")
}

// TestNewDoesNotSweepAnInFlightConfigWrite is the reason this command reads
// config.json directly instead of calling config.LoadConfig.
//
// The loader's first act is sweepStaleTempFiles, whose glob is exactly the name
// writeFileAtomic gives an in-flight write — so a TUI saving config.json from the
// settings panel while a CI loop runs `atrium new` would have its rename fail and lose
// the save silently. cli_session.go's never-call-the-loader rule opens with that
// hazard; `new` is the command most advertised for running beside a live TUI.
func TestNewDoesNotSweepAnInFlightConfigWrite(t *testing.T) {
	dir := sandboxDataDir(t)
	inFlight := filepath.Join(dir, ".config.json.tmp-123456")
	require.NoError(t, os.WriteFile(inFlight, []byte(`{"default_program":"claude"}`), 0o644))

	_, _, err := newSession(t, newRequest{title: "fix-auth", path: tempRepo(t)})
	require.NoError(t, err)

	assert.FileExists(t, inFlight, "another process's in-flight write must survive a headless read")
}

// TestNewCreatesNoConfigFile: the loader also seeds config.json from defaults when it
// is absent, which is a write from a command whose whole contract is that it performs
// none. TestNewNeverWritesState covers state.json; this covers the other one.
func TestNewCreatesNoConfigFile(t *testing.T) {
	dir := sandboxDataDir(t)
	cfg := filepath.Join(dir, "config.json")
	require.NoFileExists(t, cfg, "precondition: a fresh data dir has no config.json")

	_, _, err := newSession(t, newRequest{title: "fix-auth", path: tempRepo(t)})
	require.NoError(t, err)

	assert.NoFileExists(t, cfg, "a pure producer creates nothing in the data dir but its request")
}

// TestNewCommandFlagsAreAllWired executes the cobra command itself, which nothing else
// here does: every other test calls runNew directly, so a flag could be registered,
// documented and dead — the CLI analogue of the drift-sites gap where nothing asserts
// a registered key has a case in handleKeyPress.
//
// It sets every flag to a non-default value and reads them back off the spooled
// request, so a flag whose registration is missing or bound to the wrong variable
// fails here rather than shipping.
func TestNewCommandFlagsAreAllWired(t *testing.T) {
	sandboxDataDir(t)
	dir := tempRepo(t)
	t.Cleanup(resetNewFlags)

	cmd := rootCmd
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{
		"new", "fix-auth", "start on the parser",
		"--path", dir, "--program", "codex", "--branch", "release/2.0", "--force",
	})
	require.NoError(t, cmd.Execute())

	entries := spooledCreates(t)
	require.Len(t, entries, 1)
	got := entries[0].Request
	assert.Equal(t, "fix-auth", got.Title, "the title argument")
	assert.Equal(t, "start on the parser", got.Prompt, "the prompt argument")
	assert.Equal(t, dir, got.Path, "--path")
	assert.Equal(t, "codex", got.Program, "--program")
	assert.Equal(t, "release/2.0", got.Branch, "--branch")
	assert.True(t, got.Force, "--force")
}

// TestNewCommandProfileFlagIsWired covers the two flags the test above cannot set
// together: --profile is mutually exclusive with --program, and --wait would block.
func TestNewCommandProfileFlagIsWired(t *testing.T) {
	sandboxDataDir(t)
	t.Cleanup(resetNewFlags)

	cmd := rootCmd
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"new", "fix-auth", "--path", tempRepo(t), "--profile", "nope"})
	err := cmd.Execute()

	require.Error(t, err, "--profile must reach resolveNewProgram")
	assert.Contains(t, err.Error(), `no profile "nope"`)

	// The flag variables are package-level and outlive an Execute, so --profile has to
	// be cleared before the next run or it decides that one too.
	resetNewFlags()
	cmd.SetArgs([]string{"new", "fix-auth", "--path", tempRepo(t), "--wait", "1ms"})
	err = cmd.Execute()
	require.Error(t, err, "--wait must reach the wait loop")
	assert.Contains(t, err.Error(), "waited 1ms")
}

// resetNewFlags clears the package-level flag variables cobra writes into. They
// outlive a single Execute, so without this one test's --force leaks into the next.
func resetNewFlags() {
	newPathFlag, newProgramFlag, newProfileFlag, newBranchFlag = "", "", "", ""
	newForceFlag = false
	newWaitFlag = 0
}

// TestNewWaitDoesNotReadARefusalAsSuccess pins awaitSpool's sampling ORDER, which is
// the reverse of the writer's. Reject writes the receipt and then unlinks the record,
// which guarantees only "if the record is gone, the receipt is there" — so the record
// has to be sampled last. Sampling it first loses the race outright: receipt absent,
// then the drain completes both halves, then Stat returns ENOENT and a refusal for a
// taken title or a full cap is printed as `created "fix-auth"` with exit 0, sending a
// CI job on against a session that does not exist.
//
// betweenSpoolSamples reproduces exactly that window; nothing outside awaitSpool can.
func TestNewWaitDoesNotReadARefusalAsSuccess(t *testing.T) {
	sandboxDataDir(t)
	path, err := outbox.WriteCreate(outbox.Request{Title: "fix-auth", Path: tempRepo(t)})
	require.NoError(t, err)

	fired := false
	betweenSpoolSamples = func() {
		if fired {
			return
		}
		fired = true
		require.NoError(t, outbox.Reject(path, "a session named \"fix-auth\" already exists here"))
	}
	t.Cleanup(func() { betweenSpoolSamples = func() {} })

	err = awaitSpool(path, 5*time.Second, spoolWaitCopy{refused: "refused", timedOut: "timed out"})
	require.True(t, fired, "precondition: the window was reproduced")
	require.Error(t, err, "a refusal read as a success is the whole failure mode")
	assert.Contains(t, err.Error(), "already exists here")
}

// TestNewProfileResolvesWithNoConfigFile is the headless-bootstrap case --profile
// exists for: a machine where an agent is installed and no TUI has ever run, so there
// is no config.json to read. The fallback has to be LoadConfig's own — a plain
// DefaultConfig carries no Profiles at all, so GetProfiles synthesizes a lone "claude"
// and the flag is refused for a profile the draining TUI's table will list.
func TestNewProfileResolvesWithNoConfigFile(t *testing.T) {
	dir := sandboxDataDir(t)
	require.NoFileExists(t, filepath.Join(dir, config.ConfigFileName),
		"precondition: nothing has ever written a config")

	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "codex"), []byte("#!/bin/sh\nexit 0\n"), 0o755))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	_, _, err := newSession(t, newRequest{title: "fix-auth", path: tempRepo(t), profile: "codex"})
	require.NoError(t, err)

	entries := spooledCreates(t)
	require.Len(t, entries, 1)
	assert.Equal(t, "codex", entries[0].Request.Program)
}
