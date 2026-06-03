package tmux

import (
	"context"
	"fmt"
	cmd2 "github.com/ZviBaratz/atrium/cmd"
	"os/exec"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/cmd/cmd_test"

	"github.com/stretchr/testify/require"
)

// requireRan asserts that some recorded command contains all the given substrings.
func requireRan(t *testing.T, ran []string, substrs ...string) {
	t.Helper()
	for _, s := range ran {
		ok := true
		for _, sub := range substrs {
			if !strings.Contains(s, sub) {
				ok = false
				break
			}
		}
		if ok {
			return
		}
	}
	t.Fatalf("no command matched %v; ran: %v", substrs, ran)
}

// A live session is renamed in place: both the tmux session and its (human-readable) window
// are renamed, and the cached names are updated so later tmux commands target the new session.
func TestRename_LiveSessionRenamesSessionAndWindow(t *testing.T) {
	var ran []string
	cmdExec := cmd_test.MockCmdExec{
		RunFunc: func(c *exec.Cmd) error {
			s := cmd2.ToString(c)
			ran = append(ran, s)
			return nil // every command (incl. has-session) succeeds → session is live
		},
		OutputFunc: func(c *exec.Cmd) ([]byte, error) { return nil, nil },
	}
	sess := NewSessionWithDeps(context.Background(), "formalize-packaing", "claude", NewMockPtyFactory(t), cmdExec)
	oldSanitized := sess.sanitizedName

	if err := sess.Rename("formalize-packaging"); err != nil {
		t.Fatalf("Rename() error = %v", err)
	}

	wantSanitized := Prefix() + "formalize-packaging"
	require.Equal(t, wantSanitized, sess.sanitizedName)
	require.Equal(t, "formalize-packaging", sess.windowName)

	requireRan(t, ran, "rename-session", oldSanitized, wantSanitized)
	requireRan(t, ran, "rename-window", "formalize-packaging")
}

// When the session isn't live (e.g. paused after a reboot) Rename issues no tmux commands but
// still updates the cached names, so a later restore targets the corrected session name.
func TestRename_NotLiveUpdatesFieldsOnly(t *testing.T) {
	var ran []string
	cmdExec := cmd_test.MockCmdExec{
		RunFunc: func(c *exec.Cmd) error {
			s := cmd2.ToString(c)
			ran = append(ran, s)
			if strings.Contains(s, "has-session") {
				return fmt.Errorf("no such session") // not live
			}
			return nil
		},
		OutputFunc: func(c *exec.Cmd) ([]byte, error) { return nil, nil },
	}
	sess := NewSessionWithDeps(context.Background(), "alpha", "claude", NewMockPtyFactory(t), cmdExec)

	if err := sess.Rename("alpha-fixed"); err != nil {
		t.Fatalf("Rename() error = %v", err)
	}

	require.Equal(t, Prefix()+"alpha-fixed", sess.sanitizedName)
	require.Equal(t, "alpha-fixed", sess.windowName)
	for _, s := range ran {
		if strings.Contains(s, "rename-session") || strings.Contains(s, "rename-window") {
			t.Fatalf("expected no rename commands when the session is not live, got %q", s)
		}
	}
}
