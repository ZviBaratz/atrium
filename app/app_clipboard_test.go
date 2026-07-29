package app

import (
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/ZviBaratz/atrium/internal/actions"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"
)

// clipboardPayload digs the copied text out of a command batch by finding the
// message tea.SetClipboard produces and reading it back.
//
// That message type is unexported, so the only handle on it is its type name plus
// the fact that it is a defined string type carrying the payload verbatim (Bubble
// Tea turns it into ansi.SetSystemClipboard at the event loop). Asserting on the
// name is what keeps this honest: without it, any string-shaped message would
// satisfy the test and dropping the clipboard command would go unnoticed as long as
// something else in the batch stringified to the right thing.
func clipboardPayload(t *testing.T, cmd tea.Cmd) string {
	t.Helper()
	require.NotNil(t, cmd, "no command was returned, so nothing could reach the clipboard")
	found := ""
	seen := false
	var walk func(tea.Cmd)
	walk = func(c tea.Cmd) {
		if c == nil {
			return
		}
		msg := c()
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, sub := range batch {
				walk(sub)
			}
			return
		}
		if reflect.TypeOf(msg).String() == "tea.setClipboardMsg" {
			require.False(t, seen, "the clipboard was set twice in one action")
			found, seen = fmt.Sprint(msg), true
		}
	}
	walk(cmd)
	require.True(t, seen, "no tea.SetClipboard command was produced — the OSC 52 leg is missing")
	return found
}

// Both legs run on every copy, so a copy lands whether the user is local (the OS
// copier) or on the far side of an SSH session (OSC 52).
func TestCopyToClipboard_RunsBothLegs(t *testing.T) {
	fc := withFakeClipboard(t, nil)
	h := &home{}

	cmd := h.copyToClipboard("feature/login")

	require.True(t, fc.called, "the OS copier leg must run")
	require.Equal(t, "feature/login", fc.value, "the OS copier gets the payload")
	require.Equal(t, "feature/login", clipboardPayload(t, cmd), "so does the OSC 52 leg")
}

// The OS leg failing must not stop the OSC 52 leg — that is the whole reason a
// missing xclip is no longer an error. Mutation guard: return nil instead of the
// command and this fails while the OS-leg assertion above stays green.
func TestCopyToClipboard_OSLegFailureStillEmitsOSC52(t *testing.T) {
	fc := withFakeClipboard(t, errors.New("exec: xclip: not found"))
	h := &home{}

	cmd := h.copyToClipboard("main")

	require.True(t, fc.called)
	require.Equal(t, "main", clipboardPayload(t, cmd),
		"the escape must go out even when the host has no clipboard binary")
}

// The seam the app tests rely on is the exported package var, so pin that
// copyToClipboard actually goes through it rather than the concrete copier.
func TestCopyToClipboard_UsesTheSwappableSeam(t *testing.T) {
	orig := actions.CopyToClipboard
	t.Cleanup(func() { actions.CopyToClipboard = orig })
	calls := 0
	actions.CopyToClipboard = func(string) error { calls++; return nil }

	h := &home{}
	_ = h.copyToClipboard("x")

	require.Equal(t, 1, calls, "the OS leg runs exactly once per copy")
}
