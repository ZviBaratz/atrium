package tmux

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	cmd2 "github.com/ZviBaratz/atrium/cmd"

	"github.com/stretchr/testify/require"
)

// sentinelBrief uses values that appear nowhere in the copy, so a Contains assertion on
// any one of them cannot pass by coincidence with a word the template already carries.
var sentinelBrief = SessionBrief{
	Name:          "NAME-SENTINEL",
	Origin:        "/ORIGIN-SENTINEL",
	Branch:        "BRANCH-SENTINEL",
	WorktreesRoot: "/ROOT-SENTINEL",
}

// longBrief is the length worst case the cap is measured against: a title at the 32-char
// limit the new-session form allows, a deep origin path, the branch that title mints under
// a full `<username>/` prefix, and a worktrees root under a long home. Deliberately longer
// than a typical session so the cap has headroom against real paths and still leaves only
// about a clause of slack for copy growth.
var longBrief = SessionBrief{
	Name:          "reap zombie tmux clients on exit",
	Origin:        "/home/some-developer/Projects/organization/atrium",
	Branch:        "some-developer/reap-zombie-tmux-clients-on-exit",
	WorktreesRoot: "/home/some-developer/.atrium/worktrees",
}

// TestSessionBriefAssertsWorktreeOwnership is the reason this whole change exists, and it is
// deliberately written as an exact-literal assertion rather than a loose
// Contains("git worktree remove").
//
// Superpowers' finishing-a-development-branch decides worktree ownership by path and treats
// anything under a `worktrees/` component as its own to clean up — which is a false positive
// on every Atrium session, so the skill runs `git worktree remove` on a live worktree and
// orphans an instance Atrium still has in state.json. Renaming the data dir is forbidden (it
// is live state), so this sentence IS the fix. A substring check on the command names would
// still pass if the sentence were reworded into something that PERMITS them, which is exactly
// the regression that must not slip through.
//
// The literal is duplicated here on purpose rather than shared with the template: sharing it
// would make this test vacuous (it would pass against whatever the constant happened to say).
// Editing the prohibition is therefore meant to require deliberately editing this test.
func TestSessionBriefAssertsWorktreeOwnership(t *testing.T) {
	const prohibition = "Atrium owns this worktree: never run `git worktree remove` or " +
		"`git worktree prune` against it."

	require.Contains(t, RenderSessionBrief(sentinelBrief), prohibition,
		"the brief must claim the worktree and forbid removing or pruning it — this sentence is the payload")

	// The sibling clause is the same prohibition aimed at the OTHER live sessions' worktrees,
	// which `git worktree list` happily reports from inside this one.
	require.Contains(t, RenderSessionBrief(sentinelBrief),
		"belong to other Atrium sessions — never touch them.",
		"the brief must also fence off the sibling worktrees")
}

// TestSessionBriefNamesLiveFacts: each of the four baked facts is actually rendered. Asserted
// with sentinels (see sentinelBrief) so none of these can pass on a word from the copy.
func TestSessionBriefNamesLiveFacts(t *testing.T) {
	out := RenderSessionBrief(sentinelBrief)
	require.Contains(t, out, "NAME-SENTINEL", "the brief names the Atrium session")
	require.Contains(t, out, "/ORIGIN-SENTINEL", "the brief names the origin repo the worktree is of")
	require.Contains(t, out, "BRANCH-SENTINEL", "the brief names the session branch")
	require.Contains(t, out, "/ROOT-SENTINEL", "the brief names the worktrees root the siblings live under")

	// The branch fact is only useful with its instruction attached: the skill's failure mode is
	// creating a second branch on top of the session branch it is already on.
	require.Contains(t, out, "already the session branch, so do not create another")
}

// TestSessionBriefEmptyForIncompleteFacts: a brief missing any fact renders nothing rather
// than a sentence with a hole in it. A direct (non-git) session has no worktree and no branch,
// so every load-bearing claim above would be false for it — it gets no brief at all.
func TestSessionBriefEmptyForIncompleteFacts(t *testing.T) {
	require.False(t, SessionBrief{}.ok(), "a zero brief is not renderable")
	require.Empty(t, RenderSessionBrief(SessionBrief{}))

	for _, missing := range []SessionBrief{
		{Origin: "/o", Branch: "b", WorktreesRoot: "/r"},
		{Name: "n", Branch: "b", WorktreesRoot: "/r"},
		{Name: "n", Origin: "/o", WorktreesRoot: "/r"},
		{Name: "n", Origin: "/o", Branch: "b"},
	} {
		require.False(t, missing.ok(), "every fact is required: %+v", missing)
		require.Empty(t, RenderSessionBrief(missing))
	}

	require.True(t, sentinelBrief.ok())
}

// TestSessionBriefLengthCap pins the per-session token cost. additionalContext is paid on every
// session start, every /clear and every compaction, and it competes with CLAUDE.md and
// auto-memory for early-context attention — so a later copy edit must not be able to grow it
// quietly. Measured against longBrief (the realistic worst case for the interpolated values),
// which leaves only about one clause of slack.
func TestSessionBriefLengthCap(t *testing.T) {
	got := len(RenderSessionBrief(longBrief))
	require.LessOrEqual(t, got, sessionBriefMaxLen,
		"the brief grew past its budget (%d > %d); shorten the copy rather than raising the cap",
		got, sessionBriefMaxLen)
}

// TestSessionStartHookOutput pins the wire shape byte for byte, field names included. This is
// the one thing no other check can catch: Claude discards a malformed hook payload silently —
// no error in the pane, no error in atrium's log, the brief simply never appears — so a typo in
// "hookSpecificOutput", "hookEventName" or "additionalContext" would ship undetected.
func TestSessionStartHookOutput(t *testing.T) {
	out, err := SessionStartHookOutput(sentinelBrief)
	require.NoError(t, err)

	const prefix = `{"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":"`
	require.True(t, strings.HasPrefix(string(out), prefix),
		"envelope must be exactly %s…; got %s", prefix, out)
	require.True(t, strings.HasSuffix(string(out), `"}}`), "envelope closes both objects: %s", out)

	// The payload inside the envelope is the rendered brief, not some other string.
	require.Contains(t, string(out), "NAME-SENTINEL")

	// hookEventName must be Claude's event name ("SessionStart"), never atrium's own --event
	// verb ("session-start") — they are different vocabularies and the JSON takes Claude's.
	require.NotContains(t, string(out), `"hookEventName":"`+HookEventSessionStart+`"`)

	// An incomplete brief emits nothing at all, so the hook prints nothing and Claude has
	// nothing to parse — the fail-open degradation, not a half-rendered payload.
	empty, err := SessionStartHookOutput(SessionBrief{})
	require.NoError(t, err)
	require.Nil(t, empty)
}

// TestStartInjectsSessionBrief pins the pass-through that every other test in this file is
// blind to: the brief bound on the Session has to reach the settings.json claude is actually
// launched with. Dropping the provider's result in start() would leave every unit test above
// green and every real session silent, because a session with no SessionStart hook looks
// exactly like a session whose hook never fired.
func TestStartInjectsSessionBrief(t *testing.T) {
	forceSettingsFlag(t, true)
	ptyFactory := NewMockPtyFactory(t)
	session := NewSessionWithDeps(context.Background(), t.Name(), "claude", ptyFactory, startMockExec())
	session.SetSessionBriefFunc(func() SessionBrief { return sentinelBrief })

	require.NoError(t, session.Start(t.TempDir()))

	dir, err := hookSessionDir(session.snapshotName())
	require.NoError(t, err)
	// The sandbox HOME is shared across a `go test -count=N` run; don't leak this session's
	// artifacts into the next iteration.
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	settingsPath := filepath.Join(dir, "settings.json")
	require.Contains(t, cmd2.ToString(ptyFactory.cmds[0]), "--settings "+shellSingleQuote(settingsPath),
		"the launch command hands claude the settings file we wrote")

	data, err := os.ReadFile(settingsPath)
	require.NoError(t, err)
	require.Contains(t, string(data), "SessionStart", "the brief is wired into the launched session")
	require.Contains(t, string(data), sentinelBrief.Branch, "with this session's own facts, not a zero brief")
}

// TestStartRederivesSessionBriefAtLaunch pins WHEN the facts are read, which is the whole
// difference between a correct brief and a confidently wrong one.
//
// A Session object outlives its tmux session: pause→resume and recover-in-place both relaunch
// through start() on the SAME Session, and a deep rename changes the instance's title and
// branch in between. Holding an evaluated SessionBrief would freeze those facts at whichever
// launch happened to set them, so the next relaunch writes a fresh settings.json that names
// the PREVIOUS title and branch — the agent is then told, with authority, that it is a session
// it is not, on a branch it is not on. Taking a provider instead makes every launch re-read
// live state, and there is no second place to remember to refresh.
//
// The mutation this catches: evaluating the provider in SetSessionBriefFunc instead of in
// start(). Every other test in this file still passes under it, because they never change a
// fact between wiring and launch.
func TestStartRederivesSessionBriefAtLaunch(t *testing.T) {
	forceSettingsFlag(t, true)
	ptyFactory := NewMockPtyFactory(t)
	session := NewSessionWithDeps(context.Background(), t.Name(), "claude", ptyFactory, startMockExec())

	live := sentinelBrief
	session.SetSessionBriefFunc(func() SessionBrief { return live })

	// The rename lands after the Session is wired and before this launch. The post-rename
	// values deliberately share no substring with the pre-rename ones, so the NotContains
	// assertions below cannot be satisfied by the new value spelling the old one.
	live.Name = "POST-RENAME-TITLE"
	live.Branch = "POST-RENAME-BRANCH"

	require.NoError(t, session.Start(t.TempDir()))

	dir, err := hookSessionDir(session.snapshotName())
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	data, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	require.NoError(t, err)
	require.Contains(t, string(data), "POST-RENAME-TITLE", "the launch reads the title live")
	require.Contains(t, string(data), "POST-RENAME-BRANCH", "the launch reads the branch live")
	require.NotContains(t, string(data), sentinelBrief.Name,
		"the pre-rename title must not survive into the settings.json this launch writes")
	require.NotContains(t, string(data), sentinelBrief.Branch,
		"the pre-rename branch must not survive into the settings.json this launch writes")
}

// TestNilSessionBriefFuncInjectsNoBrief: a Session nobody wired a provider onto launches
// normally and silently. Terminal panes (ui/terminal.go) and any non-claude agent take this
// path, and a nil provider must read as "say nothing", never panic the launch.
func TestNilSessionBriefFuncInjectsNoBrief(t *testing.T) {
	forceSettingsFlag(t, true)
	ptyFactory := NewMockPtyFactory(t)
	session := NewSessionWithDeps(context.Background(), t.Name(), "claude", ptyFactory, startMockExec())

	require.NoError(t, session.Start(t.TempDir()))

	dir, err := hookSessionDir(session.snapshotName())
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	// The status hooks still ship — only the brief is absent.
	data, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	require.NoError(t, err)
	require.NotContains(t, string(data), "SessionStart", "no provider means no SessionStart hook")
	// Named events, not the "hooks" key — that key is in every settings.json ever written here,
	// so asserting on it would pass even if every event had vanished.
	require.Contains(t, string(data), `"Stop"`, "the status hooks are unaffected")
	require.Contains(t, string(data), `"PreToolUse"`, "the status hooks are unaffected")
}

// TestHookSessionStartCommand: the facts are baked into the command line the same way the
// state-file path already is — single-quoted, because a session title can carry shell
// metacharacters (a title like "Surya's comment" once killed the launch shell) and tmux hands
// the whole launch command to `sh -c`.
func TestHookSessionStartCommand(t *testing.T) {
	cmd := hookSessionStartCommand("/abs/bin/atrium", SessionBrief{
		Name:          "Surya's comment",
		Origin:        "/repos/my repo",
		Branch:        "zvi/surya-s-comment",
		WorktreesRoot: "/home/z/.atrium/worktrees",
	})

	require.Contains(t, cmd, "'/abs/bin/atrium'", "the atrium binary path is single-quoted")
	require.Contains(t, cmd, HookSubcommand, "invokes the hook subcommand")
	require.Contains(t, cmd, "--event "+HookEventSessionStart, "carries the session-start verb")
	require.Contains(t, cmd, `--session 'Surya'\''s comment'`, "an apostrophe in the title survives the shell")
	require.Contains(t, cmd, `--origin '/repos/my repo'`, "a space in the repo path survives the shell")
	require.Contains(t, cmd, `--branch 'zvi/surya-s-comment'`)
	require.Contains(t, cmd, `--worktrees-root '/home/z/.atrium/worktrees'`)
	require.NotContains(t, cmd, "--state-file", "session-start mutates no state record")
}
