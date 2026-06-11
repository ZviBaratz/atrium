package tmux

import (
	"context"
	"fmt"
	"math/rand"
	"os/exec"
	"strings"
	"testing"

	cmd2 "github.com/ZviBaratz/atrium/cmd"
	"github.com/ZviBaratz/atrium/cmd/cmd_test"
	"github.com/stretchr/testify/require"
)

// SanitizeNameSegment must apply exactly the per-segment rules toSanitizedName has
// always applied (whitespace runs stripped, dots replaced), minus the brand prefix.
func TestSanitizeNameSegment(t *testing.T) {
	cases := []struct{ in, want string }{
		{"simple", "simple"},
		{"Fix Bug", "FixBug"},
		{"a  b\tc", "abc"},
		{"v1.2.3", "v1_2_3"},
		{"foo.term", "foo_term"},
		{"repo name.git", "reponame_git"},
		{"", ""},
	}
	for _, c := range cases {
		require.Equal(t, c.want, SanitizeNameSegment(c.in), "input %q", c.in)
	}
}

// The qualified form prefixes the brand and joins the sanitized group and title
// segments, so same-titled sessions in different repos get distinct names.
func TestQualifiedSessionName(t *testing.T) {
	require.Equal(t, Prefix()+"atrium_FixBug", QualifiedSessionName("atrium", "Fix Bug"))
	require.Equal(t, Prefix()+"my_repo_x", QualifiedSessionName("my.repo", "x"))

	a := QualifiedSessionName("repoA", "Same")
	b := QualifiedSessionName("repoB", "Same")
	require.NotEqual(t, a, b)
}

// toSanitizedName (the legacy derivation) must stay byte-for-byte stable: restored
// pre-upgrade sessions are found on the socket by exactly this name.
func TestToSanitizedNameDelegatesToSegment(t *testing.T) {
	require.Equal(t, Prefix()+SanitizeNameSegment("a b.c"), toSanitizedName("a b.c"))
}

// NewSessionWithName must adopt the given session name verbatim (no re-derivation)
// while keeping the human-readable window name independent of it.
func TestNewSessionWithNameKeepsExplicitName(t *testing.T) {
	s := NewSessionWithName(context.Background(), Prefix()+"repo_title", "My Title", "claude")
	require.Equal(t, Prefix()+"repo_title", s.sanitizedName)
	require.Equal(t, "My Title", s.windowName)
	require.Equal(t, Prefix()+"repo_title", s.Name())
}

// Name exposes the live session name for persistence (legacy sessions record the
// derived name on first load so it survives a later derivation change).
func TestNameReturnsSanitizedName(t *testing.T) {
	s := NewSession(context.Background(), "some title", "claude")
	require.Equal(t, toSanitizedName("some title"), s.Name())
}

// Two-arg Rename adopts the caller-supplied session name (the caller now owns
// derivation, e.g. repo-qualified names) and renames the live session to it.
func TestRenameAdoptsCallerSuppliedSessionName(t *testing.T) {
	var ran []string
	cmdExec := cmd_test.MockCmdExec{
		RunFunc: func(c *exec.Cmd) error {
			ran = append(ran, cmd2.ToString(c))
			return nil // live session
		},
		OutputFunc: func(c *exec.Cmd) ([]byte, error) { return nil, nil },
	}
	sess := NewSessionWithDeps(context.Background(), "old title", "claude", NewMockPtyFactory(t), cmdExec)
	oldSanitized := sess.sanitizedName

	newName := QualifiedSessionName("myrepo", "new title")
	require.NoError(t, sess.Rename("new title", newName))

	require.Equal(t, newName, sess.sanitizedName)
	require.Equal(t, "new title", sess.windowName)
	requireRan(t, ran, "rename-session", oldSanitized, newName)
	requireRan(t, ran, "rename-window", "new title")
}

// Two same-titled sessions in different repo groups must coexist on the real
// shared socket — the reason qualified names exist. Drives a REAL tmux server
// (self-skips without tmux), including a rename moving one session to a new
// qualified name while the other stays reachable.
func TestQualifiedSessionsCoexistOnSocket(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}

	title := fmt.Sprintf("Same-%d", rand.Int31())
	a := NewSessionWithName(context.Background(), QualifiedSessionName("repoA", title), title, "sleep 300")
	b := NewSessionWithName(context.Background(), QualifiedSessionName("repoB", title), title, "sleep 300")

	require.NoError(t, a.Start(t.TempDir()))
	t.Cleanup(func() { _ = a.Close() })
	require.NoError(t, b.Start(t.TempDir()), "the same title in another group must not collide")
	t.Cleanup(func() { _ = b.Close() })

	require.True(t, a.DoesSessionExist())
	require.True(t, b.DoesSessionExist())

	renamed := QualifiedSessionName("repoB", title+"-renamed")
	require.NoError(t, b.Rename(title+"-renamed", renamed))
	require.Equal(t, renamed, b.Name())
	require.True(t, b.DoesSessionExist(), "renamed session must be reachable under its new name")
	require.True(t, a.DoesSessionExist(), "sibling must be untouched by the rename")
}

// A dead (e.g. paused-after-reboot) session still swaps the cached names so a later
// restore targets the new name, without issuing tmux commands.
func TestRenameDeadSessionSwapsCacheToSuppliedName(t *testing.T) {
	cmdExec := cmd_test.MockCmdExec{
		RunFunc: func(c *exec.Cmd) error {
			if strings.Contains(cmd2.ToString(c), "has-session") {
				return fmt.Errorf("no such session")
			}
			return nil
		},
		OutputFunc: func(c *exec.Cmd) ([]byte, error) { return nil, nil },
	}
	sess := NewSessionWithDeps(context.Background(), "alpha", "claude", NewMockPtyFactory(t), cmdExec)

	newName := QualifiedSessionName("grp", "beta")
	require.NoError(t, sess.Rename("beta", newName))
	require.Equal(t, newName, sess.Name())
	require.Equal(t, "beta", sess.windowName)
}
