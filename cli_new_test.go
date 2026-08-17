package main

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
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

// TestNewWaitTimesOut says the request is still in the outbox, because it is: the
// timeout is this process giving up, not Atrium refusing. What it must not say is
// which of the two states the record is in — untouched, or held open for the whole of
// a Start already under way — because from here those are the same file, nor that a
// relaunch is what picks it up, because an attached TUI drains it on detach.
func TestNewWaitTimesOut(t *testing.T) {
	sandboxDataDir(t)
	_, _, err := newSession(t, newRequest{
		title: "fix-auth", path: tempRepo(t), wait: 150 * time.Millisecond,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "still in the outbox")
	assert.Contains(t, err.Error(), "on detach", "an attached TUI is the case the old wording misdirected")
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

	assert.NoFileExists(t, cfg, "a pure producer seeds no config.json, however it reads one")
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
	restoreRootCmd(t)

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
	restoreRootCmd(t)

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

// restoreRootCmd undoes what driving rootCmd through Execute leaves on it. The flag
// variables above are only half the global state: SetArgs and SetOut/SetErr write to
// the shared command object itself and are not cleared by a later Execute, so without
// this the next test in this package to call rootCmd.Execute() — for any subcommand —
// silently runs the argv this one left behind, with its output discarded. Latent
// today, and exactly the kind of thing CI's -shuffle turns into a mystery.
func restoreRootCmd(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		resetNewFlags()
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})
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

	err = awaitSpool(path, outbox.ClaimPath(path), 5*time.Second,
		spoolWaitCopy{refused: "refused", timedOut: "timed out"})
	require.True(t, fired, "precondition: the window was reproduced")
	require.Error(t, err, "a refusal read as a success is the whole failure mode")
	assert.Contains(t, err.Error(), "already exists here")
}

// TestNewWaitKeepsWaitingWhileTheRequestIsClaimed is the completion protocol's #716
// half. The drain renames the record to a claim for the whole of the session build, so
// the record's absence alone no longer means "created" — it means "some atrium has
// taken this", which is precisely the state --wait exists to sit through. Reading it as
// success would print `created` and exit 0 while the worktree is still going up, and a
// script would move on to a session with no branch.
func TestNewWaitKeepsWaitingWhileTheRequestIsClaimed(t *testing.T) {
	sandboxDataDir(t)
	path, err := outbox.WriteCreate(outbox.Request{Title: "fix-auth", Path: tempRepo(t)})
	require.NoError(t, err)
	require.NoError(t, outbox.Claim(path, outbox.ClaimMeta{At: time.Now(), SessionBranch: "zvi/fix-auth"}))
	require.NoFileExists(t, path, "precondition: the record itself is gone")

	err = awaitSpool(path, outbox.ClaimPath(path), 20*time.Millisecond,
		spoolWaitCopy{refused: "refused", timedOut: "still queued"})
	require.Error(t, err, "a claimed request has not completed")
	assert.Contains(t, err.Error(), "still queued")
}

// TestWaitForCreateWatchesTheClaimToo is the wiring, and a mutation battery is what
// found it missing: every other test here drives awaitSpool directly and passes the
// claim path itself, so all of them stay green against a waitForCreate that passes "".
//
// What that mutant produces is not a hang but a WRONG ANSWER, which is why the assertion
// is on the wording. With the claim unwatched the record's absence ends the wait, the
// state.json read-back finds no row, and the caller is told its outcome "was lost" and
// sent to check the log — for a session whose worktree is at that moment still going up.
func TestWaitForCreateWatchesTheClaimToo(t *testing.T) {
	sandboxDataDir(t)
	repo := tempRepo(t)
	path, err := outbox.WriteCreate(outbox.Request{Title: "fix-auth", Path: repo})
	require.NoError(t, err)
	require.NoError(t, outbox.Claim(path, outbox.ClaimMeta{At: time.Now(), SessionBranch: "zvi/fix-auth"}))

	var out bytes.Buffer
	err = waitForCreate(&out, path, "fix-auth", repo, 20*time.Millisecond)

	require.Error(t, err, "a session that is still being built has not been created")
	assert.Contains(t, err.Error(), "being built right now",
		"a claimed request is in flight, not an outcome that was lost")
	assert.NotContains(t, err.Error(), "outcome was lost")
	assert.Empty(t, out.String(), "and nothing may be reported as created")
}

// TestSpoolSettledReadsTheRecordBeforeTheClaim pins the stat ORDER, which is the whole
// correctness of the two-file wait.
//
// Record first: once it is absent the request is either claimed or finished, and the
// claim stat separates those two whichever way it then races the drain. Claim first
// loses outright — the claim reads absent (not taken yet), the drain claims, the record
// then reads absent too, both look gone, and a build that has not begun is reported to
// `atrium new --wait` as a created session, exit 0.
//
// betweenSpoolStats reproduces exactly that window; arranging the end state cannot,
// because both orders agree about every state that holds still.
func TestSpoolSettledReadsTheRecordBeforeTheClaim(t *testing.T) {
	sandboxDataDir(t)
	path, err := outbox.WriteCreate(outbox.Request{Title: "fix-auth", Path: tempRepo(t)})
	require.NoError(t, err)

	// The drain claims in the window between the two stats. A record-first reader has
	// already seen the record present and reports "not settled"; a claim-first reader
	// would have seen no claim, then find the record gone, and call it done.
	fired := false
	betweenSpoolStats = func() {
		if fired {
			return
		}
		fired = true
		require.NoError(t, outbox.Claim(path, outbox.ClaimMeta{At: time.Now()}))
	}
	t.Cleanup(func() { betweenSpoolStats = func() {} })

	settled, statErr := spoolSettled(path, outbox.ClaimPath(path))
	require.NoError(t, statErr)
	assert.False(t, settled, "a request claimed mid-sample has not settled")
	assert.False(t, fired, "record-first never reaches the second stat while the record is present")
}

// TestSpoolSettledIsTerminalOnlyWhenBothAreGone is the positive half — without it the
// test above passes against a spoolSettled that never returns true at all.
func TestSpoolSettledIsTerminalOnlyWhenBothAreGone(t *testing.T) {
	sandboxDataDir(t)
	path, err := outbox.WriteCreate(outbox.Request{Title: "fix-auth", Path: tempRepo(t)})
	require.NoError(t, err)

	settled, statErr := spoolSettled(path, outbox.ClaimPath(path))
	require.NoError(t, statErr)
	require.False(t, settled, "queued")

	require.NoError(t, outbox.Claim(path, outbox.ClaimMeta{At: time.Now()}))
	settled, statErr = spoolSettled(path, outbox.ClaimPath(path))
	require.NoError(t, statErr)
	require.False(t, settled, "claimed")

	require.NoError(t, outbox.DiscardCreate(path))
	settled, statErr = spoolSettled(path, outbox.ClaimPath(path))
	require.NoError(t, statErr)
	assert.True(t, settled, "settled")
}

// TestSpoolSettledIgnoresAClaimForASpoolWithoutOne is the prompt spool's side. `send`
// has no claim step — queuing a prompt IS the whole job — so passing "" must leave its
// protocol exactly as it was, and must not make it stat a path that will never exist.
func TestSpoolSettledIgnoresAClaimForASpoolWithoutOne(t *testing.T) {
	sandboxDataDir(t)
	path, err := outbox.WriteCreate(outbox.Request{Title: "fix-auth", Path: tempRepo(t)})
	require.NoError(t, err)
	// A claim beside it, which a spool with no claim step must not consult.
	require.NoError(t, outbox.Claim(path, outbox.ClaimMeta{At: time.Now()}))

	settled, statErr := spoolSettled(path, "")
	require.NoError(t, statErr)
	assert.True(t, settled, "with no companion, the record's absence is the whole answer")
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

// TestNewWaitRefusesToClaimASessionThatWasNeverRecorded closes the hole that made the
// record's absence sufficient evidence. outbox.Reject writes the receipt first but
// unlinks the record even when that write fails — and the conditions that break the
// write (a full or read-only data dir) are exactly the ones that make it necessary. So
// "record gone, no receipt" is reachable with no session behind it, and reading it as
// success printed `created "fix-auth"` and exited 0 for a CI job to run against a
// worktree that does not exist. Simulated here by removing the record without writing
// either a receipt or a row, which is precisely the state that failure leaves on disk.
func TestNewWaitRefusesToClaimASessionThatWasNeverRecorded(t *testing.T) {
	sandboxDataDir(t)
	dir := tempRepo(t)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 200 {
			if entries, err := outbox.ListCreates(); err == nil && len(entries) == 1 {
				_ = outbox.Remove(entries[0].Path) // no receipt, and no session recorded
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()

	stdout, _, err := newSession(t, newRequest{title: "fix-auth", path: dir, wait: 5 * time.Second})
	<-done
	require.Error(t, err, "a vanished record with nothing behind it is not a creation")
	assert.Contains(t, err.Error(), "recorded no session")
	assert.NotContains(t, stdout, "created", "and nothing may be printed as if it were")
}

// TestNewRefusesAControlCharacterInTheTitle: `atrium new` is the first producer that can
// put one into a Title, and a Title is the one field that reaches the renderer
// unsanitized — session.NewInstance stores it verbatim, and only the derived branch and
// tmux names are slugged. The create form cannot: its field is a bubbles textinput,
// which collapses newlines and tabs to spaces on the way in.
//
// The trigger is ordinary rather than adversarial. `atrium new "$(gh issue view 42 --json
// title -q .title)"` and a title taken from `git log --format=%B` both carry a trailing
// newline, and the "agent working through a queue of issues" the help text pitches is
// exactly who writes that. One embedded newline splits the session row across two lines:
// the second gets no selection indicator and no status glyph, and every mouse zone below
// it shifts by a line.
//
// Refused rather than stripped, for the same reason titles are not slugged — the branch
// name derives from the title, so rewriting one picks a branch the caller did not ask
// for. A trailing newline is the exception and is trimmed, because TrimSpace runs first
// and that is unambiguous.
func TestNewRefusesAControlCharacterInTheTitle(t *testing.T) {
	for name, title := range map[string]string{
		"interior newline": "fix auth\nand tests",
		"tab":              "fix\tauth",
		"escape":           "fix\x1bauth",
		"carriage return":  "fix\rauth",
	} {
		t.Run(name, func(t *testing.T) {
			sandboxDataDir(t)
			_, _, err := newSession(t, newRequest{title: title, path: tempRepo(t)})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "control characters")
			assert.Empty(t, spooledCreates(t), "and nothing is spooled")
		})
	}

	// The controls. A trailing newline is trimmed rather than refused — TrimSpace has
	// already run by then, so the caller's intent is unambiguous — and an ordinary title
	// with punctuation and non-ASCII is untouched. Without these, refusing every title
	// would score the same.
	t.Run("a trailing newline is trimmed", func(t *testing.T) {
		sandboxDataDir(t)
		_, _, err := newSession(t, newRequest{title: "fix-auth\n", path: tempRepo(t)})
		require.NoError(t, err)
		entries := spooledCreates(t)
		require.Len(t, entries, 1)
		assert.Equal(t, "fix-auth", entries[0].Request.Title)
	})
	t.Run("an ordinary title is untouched", func(t *testing.T) {
		sandboxDataDir(t)
		const title = "fix auth (#42) — café"
		_, _, err := newSession(t, newRequest{title: title, path: tempRepo(t)})
		require.NoError(t, err)
		entries := spooledCreates(t)
		require.Len(t, entries, 1)
		assert.Equal(t, title, entries[0].Request.Title)
	})
}

// TestAwaitSpoolRetriesAStatItCouldNotAnswer: a Stat error other than "not found" is
// carried to the deadline rather than returned from the sample that saw it.
//
// The two cases it could be cannot be told apart at one sample. A data dir on NFS,
// sshfs or a container bind mount answers a single Stat with ESTALE or EIO and the next
// one fine; a permissions problem does not clear. Persistence is the only discriminator,
// and the deadline is already measuring it — so returning on the first error would fail
// a CI job for a record the TUI drains half a second later.
//
// Staged with a path whose parent is a regular file (ENOTDIR), swapped for a real
// directory between samples. betweenSpoolSamples is the only place that window is
// reachable.
func TestAwaitSpoolRetriesAStatItCouldNotAnswer(t *testing.T) {
	sandboxDataDir(t)
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	require.NoError(t, os.WriteFile(blocker, []byte("not a directory"), 0o600))
	record := filepath.Join(blocker, "record.json")

	_, err := os.Stat(record)
	require.Error(t, err, "precondition: the path errors")
	require.False(t, errors.Is(err, fs.ErrNotExist), "and not with ENOENT: %v", err)

	samples := 0
	betweenSpoolSamples = func() {
		samples++
		if samples == 2 {
			// The mount comes back: the record is genuinely gone, which is the drain's
			// success signal.
			require.NoError(t, os.Remove(blocker))
			require.NoError(t, os.Mkdir(blocker, 0o700))
		}
	}
	t.Cleanup(func() { betweenSpoolSamples = func() {} })

	err = awaitSpool(record, "", 10*time.Second, spoolWaitCopy{refused: "refused", timedOut: "timed out"})
	require.GreaterOrEqual(t, samples, 2, "precondition: the window was reproduced")
	assert.NoError(t, err, "a transient Stat error must not end the wait")
}

// The other half: an error that never clears is reported at the deadline, and reported
// as ITSELF rather than as the timeout wording — which would blame a TUI that was never
// the problem and send the caller looking in the wrong place.
func TestAwaitSpoolReportsAStatErrorThatNeverClears(t *testing.T) {
	sandboxDataDir(t)
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	require.NoError(t, os.WriteFile(blocker, []byte("not a directory"), 0o600))

	err := awaitSpool(filepath.Join(blocker, "record.json"), "", 10*time.Millisecond,
		spoolWaitCopy{refused: "refused", timedOut: "no atrium picked it up"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to read the outbox")
	assert.NotContains(t, err.Error(), "no atrium picked it up",
		"a bad data dir must not be reported as a TUI that did not answer")
}

// TestSpoolSettledSurvivesARequeueMidSample is the mirror of the test above, and the
// case record-first ordering alone loses.
//
// outbox.Claim renames record→claim, which is what the record-first argument is built
// against. outbox.Requeue renames the other way, claim→record — the startup reconcile
// handing an interrupted build back to the drain — and against THAT rename record-first
// is exactly as wrong as claim-first is against Claim: the record reads absent (it is
// still a claim), Requeue renames, the claim reads absent too, and a request that is
// queued and about to be built normally is reported to `atrium new --wait` as a created
// session. waitForCreate then finds no row and aborts with a message about a session
// that is seconds from existing.
//
// The re-read of the record is what closes it, so this test stages the rename in the
// window the re-read looks across.
func TestSpoolSettledSurvivesARequeueMidSample(t *testing.T) {
	sandboxDataDir(t)
	path, err := outbox.WriteCreate(outbox.Request{Title: "fix-auth", Path: tempRepo(t)})
	require.NoError(t, err)
	require.NoError(t, outbox.Claim(path, outbox.ClaimMeta{At: time.Now()}))

	// In the FIRST window — between the record stat and the claim stat — which is where
	// this rename has to land to hide the file from both. Fired later it would be
	// harmless: the claim stat would still see the claim and end the sample.
	calls := 0
	betweenSpoolStats = func() {
		calls++
		if calls == 1 {
			require.NoError(t, outbox.Requeue(path, true))
		}
	}
	t.Cleanup(func() { betweenSpoolStats = func() {} })

	settled, statErr := spoolSettled(path, outbox.ClaimPath(path))
	require.NoError(t, statErr)
	assert.False(t, settled, "a request re-queued mid-sample is queued, not created")
	assert.Equal(t, 2, calls, "reached only by re-reading the record, which is the fix")
	assert.FileExists(t, path, "and the record really is back, so 'not settled' is the true answer")
}

// TestSpoolSettledStillReportsATrulySettledRecord is the negative control for the
// re-read: an implementation that answered "not settled" unconditionally, or that
// re-read some other path, would pass every ordering test above and never terminate a
// real wait.
func TestSpoolSettledStillReportsATrulySettledRecord(t *testing.T) {
	sandboxDataDir(t)
	path, err := outbox.WriteCreate(outbox.Request{Title: "fix-auth", Path: tempRepo(t)})
	require.NoError(t, err)
	require.NoError(t, outbox.Claim(path, outbox.ClaimMeta{At: time.Now()}))
	require.NoError(t, outbox.DiscardCreate(path))

	settled, statErr := spoolSettled(path, outbox.ClaimPath(path))
	require.NoError(t, statErr)
	assert.True(t, settled, "both files gone with nothing racing is the settled state")
}
