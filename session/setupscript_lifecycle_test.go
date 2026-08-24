package session

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ZviBaratz/atrium/cmd/cmd_test"
	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/session/tmux"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// installRepoScript writes a config.json with one catch-all repo_scripts entry into
// whatever HOME the caller has already sandboxed (newTestWorktree sets its own, so
// this must run after it).
func installRepoScript(t *testing.T, entry config.RepoScript) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(os.Getenv("HOME"), ".atrium"), 0o755))
	cfg := config.DefaultConfig()
	cfg.RepoScripts = []config.RepoScript{entry}
	require.NoError(t, config.SaveConfig(cfg))
}

// fakeTmux stands in for a tmux server that starts out empty. It is one object
// because its two halves must agree: new-session arrives through the PtyFactory (the
// agent runs in a pty), while the "is it up yet?" poll that start() then blocks on
// arrives through the command executor. A has-session that always succeeds makes
// Start refuse with "session already exists"; one that always fails makes it time out
// waiting for the session it just created. Neither ever reaches the code under test.
//
// It also records, at the moment the agent's pty is launched, whether the setup
// script's marker file already existed — which is the ordering claim this file is
// about: the agent must never come up in a tree whose dependencies are still
// installing.
type fakeTmux struct {
	marker string

	mu             sync.Mutex
	opened         []*os.File
	exists         bool
	markerAtLaunch bool
	launched       bool
	launches       [][]string
	// dieAtAttach makes the session vanish when the attach client connects — see
	// dieOnAttach.
	dieAtAttach bool
	// dieFlag, when non-empty, kills only the launches whose command line contains it —
	// see dieOnAttachWhenLaunchContains. dieNext carries that decision from the
	// new-session that made it to the attach that acts on it.
	dieFlag string
	dieNext bool
}

// newSessionArgv is the `tmux new-session` command line the agent was created with.
// Every pty the session opens comes through Start — the create and then the attach —
// so the one under test has to be picked out by name rather than by being the last.
func (f *fakeTmux) newSessionArgv(t *testing.T) []string {
	t.Helper()
	argvs := f.newSessionArgvs()
	if len(argvs) == 0 {
		f.mu.Lock()
		defer f.mu.Unlock()
		t.Fatalf("no tmux new-session was launched (saw %v)", f.launches)
	}
	return argvs[0]
}

// newSessionArgvs is every `tmux new-session` this server was asked for, in order. A
// test about a RELAUNCH needs the sequence rather than one of them: "it retried blank"
// is a claim about the second launch existing and about what the first one carried, and
// newSessionArgv alone can support neither half.
func (f *fakeTmux) newSessionArgvs() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out [][]string
	for _, argv := range f.launches {
		for _, arg := range argv {
			if arg == "new-session" {
				out = append(out, argv)
				break
			}
		}
	}
	return out
}

func (f *fakeTmux) Start(cmd *exec.Cmd) (*os.File, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.marker != "" {
		_, err := os.Stat(f.marker)
		f.markerAtLaunch = err == nil
	}
	f.launched = true
	f.exists = true
	argv := append([]string(nil), cmd.Args...)
	f.launches = append(f.launches, argv)
	for _, arg := range argv {
		switch arg {
		case "new-session":
			// The program is one trailing argv element ("claude --continue …"), so the
			// flag is looked for in the whole line rather than as a word of its own.
			f.dieNext = f.dieFlag != "" && strings.Contains(strings.Join(argv, " "), f.dieFlag)
		case "attach-session":
			if f.dieAtAttach || f.dieNext {
				f.exists = false
			}
		}
	}
	file, err := os.CreateTemp("", "pty-stub")
	if err != nil {
		return nil, err
	}
	f.opened = append(f.opened, file)
	return file, nil
}

// Close closes and removes every pty stub file Start handed out (PtyFactory).
func (f *fakeTmux) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, file := range f.opened {
		_ = file.Close()
		_ = os.Remove(file.Name())
	}
}

// sessionExists reports whether the fake server currently holds a session — true from
// the first new-session until a kill-session takes it away.
func (f *fakeTmux) sessionExists() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.exists
}

// dieOnAttach makes the session vanish the moment the attach client connects, which is
// the last thing tmux.Session.Start does. It stands in for a command that cannot run at
// all: tmux creates the session, the existence poll inside Start catches it, and the
// shell exits 127 immediately after.
//
// Deterministic on purpose. Expressing the same thing as "kill it from another goroutine
// after a millisecond" leaves it a coin flip whether the death lands before Start's own
// poll (a different failure) or after the settle check (no failure at all), and that
// flake reached the gate once already.
func (f *fakeTmux) dieOnAttach() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dieAtAttach = true
}

// dieOnAttachWhenLaunchContains is dieOnAttach narrowed to the launches whose command
// line contains flag — a session that dies because of WHAT it was asked to run.
//
// Keying on the flag rather than on "the first launch" is the whole point: a retry
// asserted against a fake that dies once and then stops would pass for a retry that
// relaunched the identical resuming command, which is the bug rather than the fix. Here
// the second launch survives by being blank, and a retry that kept the flag would die
// again and be observable.
func (f *fakeTmux) dieOnAttachWhenLaunchContains(flag string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dieFlag = flag
}

// killSession takes the session away without anyone here asking — an agent that
// crashed, or an external kill. It is what makes a "once per launch" claim testable:
// the SECOND death has to be a real one, or a refusal to relaunch proves only that the
// first repair left a live session behind.
func (f *fakeTmux) killSession() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.exists = false
}

// adopt marks the fake server as already holding a session nobody here created — a
// stranger sitting on a name, which is what a pre-existing session titled "foo.run" is to
// session "foo".
func (f *fakeTmux) adopt() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.exists = true
}

// reset forgets the recorded launches, leaving the server's state alone, so a test can
// assert about what a SECOND phase launched rather than about everything since the
// fixture was built.
func (f *fakeTmux) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.launches = nil
	f.launched = false
}

// exec answers tmux commands: has-session tracks whether the pty was launched,
// kill-session takes the session away again (so a teardown is observable, not just
// unrefused), everything else succeeds.
func (f *fakeTmux) exec() cmd_test.MockCmdExec {
	return cmd_test.MockCmdExec{
		RunFunc: func(c *exec.Cmd) error {
			for _, arg := range c.Args {
				switch arg {
				case "has-session":
					f.mu.Lock()
					defer f.mu.Unlock()
					if f.exists {
						return nil
					}
					return errors.New("no server running on /tmp/tmux-test")
				case "kill-session":
					f.mu.Lock()
					defer f.mu.Unlock()
					f.exists = false
					return nil
				}
			}
			return nil
		},
		OutputFunc: func(*exec.Cmd) ([]byte, error) { return nil, nil },
	}
}

// newFakeTmux builds the pair and registers the pty cleanup. marker is the path whose
// existence is sampled when the agent launches ("" to sample nothing).
func newFakeTmux(t *testing.T, marker string) *fakeTmux {
	t.Helper()
	f := &fakeTmux{marker: marker}
	t.Cleanup(f.Close)
	return f
}

// TestStart_RunsTheSetupScriptBeforeTheAgentLaunches is the acceptance test for the
// ordering the whole feature rests on. A script that lands after the agent is a
// script the agent's first command races.
func TestStart_RunsTheSetupScriptBeforeTheAgentLaunches(t *testing.T) {
	seed := newTestWorktree(t) // sandboxes HOME and builds a repo with one commit
	repoPath := seed.GetRepoPath()
	// The marker lands at a path known BEFORE the worktree exists, so the fake can
	// sample it at launch. Routed through session_env, which is the same mechanism a
	// real entry uses to hand the script a per-session value.
	marker := filepath.Join(t.TempDir(), "SETUP_RAN")
	installRepoScript(t, config.RepoScript{
		Name:        "any",
		SetupScript: `touch "$MARKER"`,
		SessionEnv:  map[string]string{"MARKER": marker},
	})

	fake := newFakeTmux(t, marker)
	ts := tmux.NewSessionWithDeps(context.Background(), "started", "claude", fake, fake.exec())
	inst := &Instance{ident: identity{title: "started"}, Path: repoPath, tmuxSession: ts}

	require.NoError(t, inst.Start(true))
	t.Cleanup(func() { _ = inst.Kill() })

	require.FileExists(t, marker, "the setup script must run")
	require.True(t, fake.launched, "the agent must still be launched")
	assert.True(t, fake.markerAtLaunch, "the agent launched before the setup script finished")
	assert.NoError(t, inst.SetupError())
}

// A failing setup script leaves a usable session. Start's deferred unwind kills the
// instance on any returned error, so this is the assertion that the failure never
// reaches it: the worktree, the branch and the agent all survive.
func TestStart_AFailingSetupScriptLeavesTheSessionUsable(t *testing.T) {
	seed := newTestWorktree(t)
	repoPath := seed.GetRepoPath()
	installRepoScript(t, config.RepoScript{Name: "any", SetupScript: "echo boom >&2; exit 1"})

	fake := newFakeTmux(t, "")
	ts := tmux.NewSessionWithDeps(context.Background(), "failing", "claude", fake, fake.exec())
	inst := &Instance{ident: identity{title: "failing"}, Path: repoPath, tmuxSession: ts}

	require.NoError(t, inst.Start(true), "a non-zero setup script must not fail Start")
	t.Cleanup(func() { _ = inst.Kill() })

	assert.True(t, inst.Started())
	assert.DirExists(t, inst.WorkingDir(), "the worktree must survive a failed script")
	require.Error(t, inst.SetupError())
	assert.Contains(t, inst.SetupOutput(), "boom")
}

// Resume re-materializes a worktree that pause deleted — empty of every gitignored
// path the last run installed — so the script has to run again. "Exactly once per new
// worktree" is about the worktree, not the session.
func TestResume_RerunsTheSetupScriptOnARecreatedWorktree(t *testing.T) {
	wt := newTestWorktree(t)
	installRepoScript(t, config.RepoScript{Name: "any", SetupScript: "touch SETUP_RAN"})
	wtPath := wt.GetWorktreePath()

	fake := newFakeTmux(t, "")
	ts := tmux.NewSessionWithDeps(context.Background(), "resumed", "claude", fake, fake.exec())
	inst := &Instance{ident: identity{title: "resumed"}, status: Running, started: true, gitWorktree: wt, tmuxSession: ts}

	require.NoError(t, inst.Pause())
	require.NoDirExists(t, wtPath, "pause must remove the worktree for this test to mean anything")

	require.NoError(t, inst.Resume())

	assert.FileExists(t, filepath.Join(wtPath, "SETUP_RAN"),
		"a resume that recreates the worktree must re-run the setup script")
}

// The mirror image: a resume whose worktree is still on disk skips Worktree.Setup, so
// it must skip the script too. Re-running `npm ci` on every unpark of a park that
// never removed anything is exactly the cost this feature is supposed to pay once.
func TestResume_KeepsAMaterializedWorktreeAndSkipsTheScript(t *testing.T) {
	wt := newTestWorktree(t)
	installRepoScript(t, config.RepoScript{Name: "any", SetupScript: "touch SETUP_RAN"})

	fake := newFakeTmux(t, "")
	ts := tmux.NewSessionWithDeps(context.Background(), "parked", "claude", fake, fake.exec())
	inst := &Instance{ident: identity{title: "parked"}, status: Paused, started: true, gitWorktree: wt, tmuxSession: ts}

	require.NoError(t, inst.Resume())

	assert.NoFileExists(t, filepath.Join(wt.GetWorktreePath(), "SETUP_RAN"))
}

// The agent's own pane gets session_env too, not just the setup script — a per-session
// cache directory is worth nothing if only the install step can see it. tmux -e is the
// only per-session env channel there is, and it is a create-time argument, so this can
// only be set before the session is born.
func TestStart_InjectsSessionEnvIntoTheAgentPane(t *testing.T) {
	seed := newTestWorktree(t)
	repoPath := seed.GetRepoPath()
	installRepoScript(t, config.RepoScript{
		Name:       "any",
		SessionEnv: map[string]string{"CACHE_DIR": "/tmp/cache-{{.Session.Title}}"},
	})

	fake := newFakeTmux(t, "")
	ts := tmux.NewSessionWithDeps(context.Background(), "envd", "claude", fake, fake.exec())
	inst := &Instance{ident: identity{title: "envd"}, Path: repoPath, tmuxSession: ts}

	require.NoError(t, inst.Start(true))
	t.Cleanup(func() { _ = inst.Kill() })

	assert.Contains(t, fake.newSessionArgv(t), "CACHE_DIR=/tmp/cache-envd")
}

// An entry with session_env and no setup_script is legitimate — the environment IS the
// configuration — so the absence of a script must not skip the injection.
func TestStart_InjectsSessionEnvWithNoSetupScript(t *testing.T) {
	seed := newTestWorktree(t)
	installRepoScript(t, config.RepoScript{Name: "any", SessionEnv: map[string]string{"ONLY": "env"}})

	fake := newFakeTmux(t, "")
	ts := tmux.NewSessionWithDeps(context.Background(), "envonly", "claude", fake, fake.exec())
	inst := &Instance{ident: identity{title: "envonly"}, Path: seed.GetRepoPath(), tmuxSession: ts}

	require.NoError(t, inst.Start(true))
	t.Cleanup(func() { _ = inst.Kill() })

	assert.Contains(t, fake.newSessionArgv(t), "ONLY=env")
}

// A repo with nothing configured launches exactly the argv it did before the feature.
func TestStart_UnconfiguredRepoAddsNoEnv(t *testing.T) {
	seed := newTestWorktree(t)
	require.NoError(t, os.MkdirAll(filepath.Join(os.Getenv("HOME"), ".atrium"), 0o755))
	require.NoError(t, config.SaveConfig(config.DefaultConfig()))

	fake := newFakeTmux(t, "")
	ts := tmux.NewSessionWithDeps(context.Background(), "plain", "claude", fake, fake.exec())
	inst := &Instance{ident: identity{title: "plain"}, Path: seed.GetRepoPath(), tmuxSession: ts}

	require.NoError(t, inst.Start(true))
	t.Cleanup(func() { _ = inst.Kill() })

	for _, arg := range fake.newSessionArgv(t) {
		assert.NotContains(t, arg, "CACHE_DIR")
	}
}

// path_matches is tested against the REPOSITORY ROOT, not the session's worktree.
// That distinction is the whole usability of the rule: a worktree lives under
// ~/.atrium/worktrees/<branch>_<nonce> and contains none of the project's own path, so
// a rule naming the project directory — the only spelling a user would think to write
// — would never match if the worktree path were the input.
func TestRunSetupScript_PathMatchesTestsTheRepositoryRoot(t *testing.T) {
	wt := newTestWorktree(t)
	repoPath := wt.GetRepoPath()
	installRepoScript(t, config.RepoScript{
		Name:        "by-path",
		PathMatches: []string{filepath.Base(repoPath)},
		SetupScript: "touch MATCHED",
	})
	inst := &Instance{ident: identity{title: "web"}, gitWorktree: wt}

	inst.RunSetupScript(wt.GetWorktreePath())

	assert.FileExists(t, filepath.Join(wt.GetWorktreePath(), "MATCHED"),
		"a rule naming the project directory must match")
}

// The mirror: a rule naming the worktree's own location does NOT match, which is what
// makes the rule above the only one worth documenting.
func TestRunSetupScript_PathMatchesDoesNotTestTheWorktreePath(t *testing.T) {
	wt := newTestWorktree(t)
	installRepoScript(t, config.RepoScript{
		Name:        "by-worktree",
		PathMatches: []string{"/worktrees/"},
		SetupScript: "touch MATCHED",
	})
	inst := &Instance{ident: identity{title: "web"}, gitWorktree: wt}

	inst.RunSetupScript(wt.GetWorktreePath())

	assert.NoFileExists(t, filepath.Join(wt.GetWorktreePath(), "MATCHED"))
}

// A direct (non-git) session gets session_env but never a setup script: it runs in the
// user's own checkout, which is already warm and is not Atrium's to install into. The
// asymmetry is documented in the README, and this is what keeps it true.
func TestStart_DirectSessionGetsTheEnvironmentButNotTheScript(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // no repo needed: a direct session has no worktree
	dir := t.TempDir()
	installRepoScript(t, config.RepoScript{
		Name:        "any",
		SetupScript: "touch SETUP_RAN",
		SessionEnv:  map[string]string{"ONLY": "env"},
	})

	fake := newFakeTmux(t, "")
	ts := tmux.NewSessionWithDeps(context.Background(), "plainrepo", "claude", fake, fake.exec())
	inst := &Instance{ident: identity{title: "plainrepo"}, Path: dir, direct: true, tmuxSession: ts}

	require.NoError(t, inst.Start(true))
	t.Cleanup(func() { _ = inst.Kill() })

	assert.NoFileExists(t, filepath.Join(dir, "SETUP_RAN"), "a direct session has no worktree to install into")
	assert.Contains(t, fake.newSessionArgv(t), "ONLY=env", "but it still gets the environment")
}
