package tmux

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/cmd/cmd_test"

	"github.com/stretchr/testify/require"
)

// bakedStateFile returns the --state-file path baked into the hook commands of a
// settings.json — i.e. the path the launched agent will actually write to for the whole
// life of that process. It also asserts every state-bearing hook agrees on that path, so
// a partial rewrite can't pass by matching on whichever command happens to be read first.
func bakedStateFile(t *testing.T, settingsPath string) string {
	t.Helper()
	data, err := os.ReadFile(settingsPath)
	require.NoError(t, err)

	var parsed struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	require.NoError(t, json.Unmarshal(data, &parsed))

	var paths []string
	for _, groups := range parsed.Hooks {
		for _, g := range groups {
			for _, h := range g.Hooks {
				_, after, found := strings.Cut(h.Command, "--state-file '")
				if !found {
					continue // SessionStart carries no state path (it mutates nothing).
				}
				p, _, _ := strings.Cut(after, "'")
				paths = append(paths, p)
			}
		}
	}
	require.NotEmpty(t, paths, "settings.json bakes a --state-file into its hook commands")
	for _, p := range paths {
		require.Equal(t, paths[0], p, "every hook writes to the same state file")
	}
	return paths[0]
}

// relaunchMockExec is startMockExec with an externally-flippable liveness flag, so one test
// can drive two launches on the same Session — the shape pause→resume and recover-in-place
// both take. While *alive is false the next has-session reports "not found" (satisfying
// start's entry guard) and flips it true, so start's poll loop then sees the session.
func relaunchMockExec(alive *bool) cmd_test.MockCmdExec {
	return cmd_test.MockCmdExec{
		RunFunc: func(c *exec.Cmd) error {
			if strings.Contains(c.String(), "has-session") && !*alive {
				*alive = true
				return fmt.Errorf("no such session")
			}
			return nil
		},
		OutputFunc: func(c *exec.Cmd) ([]byte, error) { return []byte("output"), nil },
	}
}

// hookArtifacts returns the hook directory and settings path for a session name.
func hookArtifacts(t *testing.T, name string) (dir, settings string) {
	t.Helper()
	dir, err := hookSessionDir(name)
	require.NoError(t, err)
	return dir, filepath.Join(dir, "settings.json")
}

// TestRenamePreservesHookChannel is the regression for #492: a deep rename used to sever the
// hook status channel outright.
//
// The path is derived from the sanitized session name at three sites that a rename
// desynchronizes. ensureHookSettings bakes an ABSOLUTE --state-file into every hook command at
// launch, so the running agent's write path is frozen the moment it is exec'd and no later
// rename can move it. HookStateFile used to re-derive the read path from the CURRENT name on
// every poll, so after a rename it read a directory that had never existed: readHookRecord
// returned ok=false, and every caller correctly treated that as "no hook signal" and fell back
// to the pane scrape. The working/ready latch, the in-flight sub-agent set (#290), the #311
// heartbeat and the effort chip (#325) all went dark, silently, for the life of that process.
//
// The reader has to follow the writer, which is the direction the truth flows: the name is
// frozen when the settings are ensured and both reads use the frozen value.
func TestRenamePreservesHookChannel(t *testing.T) {
	forceSettingsFlag(t, true)
	s := NewSessionWithDeps(context.Background(), t.Name(), "claude", NewMockPtyFactory(t), startMockExec())
	require.NoError(t, s.Start(t.TempDir()))

	launchName := s.snapshotName()
	launchDir, launchSettings := hookArtifacts(t, launchName)
	// The sandbox HOME is shared across a `go test -count=N` run; don't leak into the next.
	t.Cleanup(func() { _ = os.RemoveAll(launchDir) })

	// Presence first: assert the channel actually exists before asserting a rename preserves
	// it. Without this the assertions below would hold just as well over two paths that are
	// equal because neither is ever written to.
	require.FileExists(t, launchSettings, "the launch wrote a settings.json")
	written := bakedStateFile(t, launchSettings)
	require.Equal(t, filepath.Join(launchDir, "state"), written,
		"the baked write path is inside this launch's hook dir")
	readPath, err := s.HookStateFile()
	require.NoError(t, err)
	require.Equal(t, written, readPath, "before any rename, writer and reader already agree")

	// A record on that path is a live signal: the poller reads it.
	require.NoError(t, UpdateHookState(written, HookEventWorking, HookPayload{}, ""))
	rec, ok := s.readHookRecord()
	require.True(t, ok, "the hook record is readable before the rename")
	require.Equal(t, hookStateWorking, rec.State)

	// The deep rename. The tmux session and the cached names move; the already-launched
	// agent's baked --state-file cannot.
	require.NoError(t, s.Rename("after", Prefix()+"after"))
	require.Equal(t, Prefix()+"after", s.snapshotName(), "the rename really landed")

	readPath, err = s.HookStateFile()
	require.NoError(t, err)
	require.Equal(t, written, readPath,
		"after the rename the reader must still follow the writer's baked path")

	// The behavioural assertion, at the level the bug actually bit: the signal still arrives.
	// Comparing paths alone would pass on a fix that agreed on a path nothing writes to.
	require.NoError(t, UpdateHookState(written, HookEventReady, HookPayload{}, ""))
	rec, ok = s.readHookRecord()
	require.True(t, ok, "the renamed session still reads the live agent's hook record")
	require.Equal(t, hookStateReady, rec.State,
		"and sees the latch the agent wrote after the rename, not a stale value")
}

// TestRelaunchRebakesHookName: freezing the name at launch is only correct if every relaunch
// re-freezes it. start() is the single site that ensures the settings, and pause→resume,
// recover-in-place and a fresh create all route through it — so a resumed session must read
// the directory its NEW process writes to, not the one the dead process used.
//
// The mutation this catches: setting the frozen name once (at construction, or only when it is
// still empty). TestRenamePreservesHookChannel passes under that mutation; this one does not.
func TestRelaunchRebakesHookName(t *testing.T) {
	forceSettingsFlag(t, true)
	alive := false
	s := NewSessionWithDeps(context.Background(), t.Name(), "claude", NewMockPtyFactory(t), relaunchMockExec(&alive))
	require.NoError(t, s.Start(t.TempDir()))

	firstName := s.snapshotName()
	firstDir, firstSettings := hookArtifacts(t, firstName)
	t.Cleanup(func() { _ = os.RemoveAll(firstDir) })
	require.FileExists(t, firstSettings, "the first launch wrote its artifacts")
	firstWritten := bakedStateFile(t, firstSettings)

	// Rename, then relaunch on the same Session — the agent process is gone, so the new one
	// is launched with a settings.json keyed by the CURRENT name.
	secondName := Prefix() + "renamed-then-relaunched"
	require.NoError(t, s.Rename("renamed then relaunched", secondName))
	alive = false
	require.NoError(t, s.Start(t.TempDir()))

	secondDir, secondSettings := hookArtifacts(t, secondName)
	t.Cleanup(func() { _ = os.RemoveAll(secondDir) })
	require.FileExists(t, secondSettings, "the relaunch wrote artifacts under the new name")
	secondWritten := bakedStateFile(t, secondSettings)
	require.NotEqual(t, firstWritten, secondWritten, "the relaunch really re-keyed the path")

	readPath, err := s.HookStateFile()
	require.NoError(t, err)
	require.Equal(t, secondWritten, readPath,
		"a relaunched session reads what its LIVE agent writes, not the dead one's directory")

	// The first launch's directory is referenced by nothing now. start() runs only when no
	// live session exists, so sweeping it there bounds the leak to zero at all times.
	require.NoDirExists(t, firstDir,
		"the superseded launch's artifacts are swept when the name is re-frozen")
}

// TestCloseRemovesHookArtifactsAfterRename: teardown cleaned the CURRENT name, so a killed
// session that had been renamed left its real directory on disk forever — a leak nothing
// reports, recoverable only by `atrium reset`, which wipes every session's hook state.
func TestCloseRemovesHookArtifactsAfterRename(t *testing.T) {
	forceSettingsFlag(t, true)
	s := NewSessionWithDeps(context.Background(), t.Name(), "claude", NewMockPtyFactory(t), startMockExec())
	require.NoError(t, s.Start(t.TempDir()))

	launchName := s.snapshotName()
	launchDir, launchSettings := hookArtifacts(t, launchName)
	t.Cleanup(func() { _ = os.RemoveAll(launchDir) })

	// Presence first — an "is absent after Close" assertion proves nothing until the thing
	// was demonstrably there to remove.
	require.FileExists(t, launchSettings, "the launch wrote artifacts to remove")

	renamed := Prefix() + "closed-after-rename"
	require.NoError(t, s.Rename("closed after rename", renamed))
	renamedDir, _ := hookArtifacts(t, renamed)
	t.Cleanup(func() { _ = os.RemoveAll(renamedDir) })
	require.DirExists(t, launchDir, "the rename moves nothing: the live agent still writes here")

	require.NoError(t, s.Close())
	require.NoDirExists(t, launchDir, "Close removes the directory the session actually used")
	require.NoDirExists(t, renamedDir, "and leaves nothing under the post-rename name either")

	// The tree itself must survive — cleanupHookSession removes one session, never the root.
	root, err := hooksRoot()
	require.NoError(t, err)
	require.DirExists(t, root, "other sessions' hook state is untouched")
}

// TestHookSessionDirRejectsEmptyName guards the footgun the frozen name introduces: the field
// is empty for a Session that has never launched, and hookSessionDir("") would resolve to the
// hooks ROOT — which cleanupHookSession would then RemoveAll, destroying every live session's
// status channel. hookName never returns empty, so this is defence in depth; it is here
// because the blast radius of the one call that could is the whole feature.
func TestHookSessionDirRejectsEmptyName(t *testing.T) {
	_, err := hookSessionDir("")
	require.Error(t, err, "an empty session name must never resolve to the hooks root")

	root, err := hooksRoot()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "sentinel-session"), 0o755))
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Join(root, "sentinel-session")) })

	cleanupHookSession("")
	require.DirExists(t, filepath.Join(root, "sentinel-session"),
		"cleaning an empty name is a no-op, not a wipe of the whole tree")
}

// TestHookNameFallsBackToCurrentName: a Session that has never launched in this process and
// carries no persisted frozen name — a legacy state.json, or a paused instance rehydrated for
// a later Resume — must key off its current name, exactly as before #492.
func TestHookNameFallsBackToCurrentName(t *testing.T) {
	s := NewSessionWithDeps(context.Background(), t.Name(), "claude", NewMockPtyFactory(t), cmd_test.MockCmdExec{})
	require.Empty(t, s.HookSessionName(), "nothing launched, nothing frozen")
	require.Equal(t, s.snapshotName(), s.hookName())

	dir, err := hookSessionDir(s.snapshotName())
	require.NoError(t, err)
	path, err := s.HookStateFile()
	require.NoError(t, err)
	require.Equal(t, filepath.Join(dir, "state"), path)
}

// TestSetHookSessionNameSurvivesRestart: the frozen name is persisted, because a TUI restart
// rebuilds the Session from the POST-rename name while the surviving agent still writes to the
// pre-rename directory. Without the restored value the fix would hold only until the user
// quit atrium — reattach (Restore) never re-runs ensureHookSettings, so nothing would re-freeze
// the name and the channel would go dark again with no relaunch in sight.
func TestSetHookSessionNameSurvivesRestart(t *testing.T) {
	// Rehydration order mirrors FromInstanceData: the Session is built with the persisted
	// (post-rename) tmux name, then the persisted frozen hook name is injected.
	s := NewSessionWithDeps(context.Background(), t.Name(), "claude", NewMockPtyFactory(t), cmd_test.MockCmdExec{})
	launched := Prefix() + "pre-rename"
	s.SetHookSessionName(launched)

	require.Equal(t, launched, s.HookSessionName())
	require.NotEqual(t, launched, s.snapshotName(), "the session's own name is the post-rename one")

	dir, err := hookSessionDir(launched)
	require.NoError(t, err)
	path, err := s.HookStateFile()
	require.NoError(t, err)
	require.Equal(t, filepath.Join(dir, "state"), path,
		"a rehydrated session reads where the surviving agent writes")
}
