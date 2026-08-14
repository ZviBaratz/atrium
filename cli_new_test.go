package main

import (
	"bytes"
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
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 200 {
			if entries, err := outbox.ListCreates(); err == nil && len(entries) == 1 {
				seedInstances(t, session.InstanceData{
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

	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 200 {
			if entries, err := outbox.ListCreates(); err == nil && len(entries) == 1 {
				seedInstances(t, inst("fix-auth", dir)) // no Branch, no worktree
				_ = outbox.Remove(entries[0].Path)
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()

	stdout, _, err := newSession(t, newRequest{title: "fix-auth", path: dir, wait: 5 * time.Second})
	<-done
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
