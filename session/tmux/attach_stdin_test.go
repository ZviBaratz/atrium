package tmux

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// readAttachStdin is the attach input loop extracted from Attach so it can be driven
// without a real terminal. Each test feeds a finite bytes.Reader (so the loop exits
// via io.EOF) and a bytes.Buffer as the pty, and calls the method synchronously: it
// returns on EOF / detach / a stale ctx, so no goroutine or -race synchronization is
// needed. The attachedSession helper supplies the per-attach ctx/wg/attachCh and a
// mock pty, so keyDetach runs its full teardown. A non-nil s.attachCh after the call
// means no detach happened (detachLocked closes and nils it).

// past returns a nuke deadline already elapsed, so input is classified, not dropped.
func past() time.Time { return time.Now().Add(-time.Hour) }

// future returns a nuke deadline far ahead, so input falls inside the drop window.
func future() time.Time { return time.Now().Add(time.Hour) }

func TestReadAttachStdinDropsNukeWindowBurst(t *testing.T) {
	s, _ := attachedSession(t)
	var out bytes.Buffer
	s.readAttachStdin(s.ctx, bytes.NewReader([]byte("hi")), &out, false, future())
	require.Empty(t, out.Bytes(), "bytes read inside the nuke window are dropped, never forwarded")
	require.NotNil(t, s.attachCh, "dropping the burst must not detach")
}

func TestReadAttachStdinForwardsAfterWindow(t *testing.T) {
	s, _ := attachedSession(t)
	var out bytes.Buffer
	s.readAttachStdin(s.ctx, bytes.NewReader([]byte("hi")), &out, false, past())
	require.Equal(t, "hi", out.String(), "ordinary input after the window is forwarded to the pty")
	require.NotNil(t, s.attachCh, "forwarding must not detach")
	require.Equal(t, DetachQuit, s.AttachExitReason(), "reason left untouched")
}

func TestReadAttachStdinCtrlQDetaches(t *testing.T) {
	s, _ := attachedSession(t)
	ch := s.attachCh
	var out bytes.Buffer
	s.readAttachStdin(s.ctx, bytes.NewReader([]byte{ctrlQ}), &out, false, past())
	assertClosed(t, ch)
	require.Equal(t, DetachQuit, s.AttachExitReason())
	require.False(t, s.KillRequested(), "Ctrl+Q detaches without requesting a kill")
	require.Empty(t, out.Bytes(), "the detach key is not forwarded")
}

func TestReadAttachStdinCtrlXKillsWhenAllowed(t *testing.T) {
	s, _ := attachedSession(t)
	ch := s.attachCh
	var out bytes.Buffer
	s.readAttachStdin(s.ctx, bytes.NewReader([]byte{ctrlX}), &out, true, past())
	assertClosed(t, ch)
	require.True(t, s.KillRequested(), "Ctrl+X with allowKill requests a kill")
	require.Equal(t, DetachQuit, s.AttachExitReason())
	require.Empty(t, out.Bytes(), "the kill key is not forwarded")
}

func TestReadAttachStdinCtrlXForwardedWhenNotAllowed(t *testing.T) {
	s, _ := attachedSession(t)
	var out bytes.Buffer
	s.readAttachStdin(s.ctx, bytes.NewReader([]byte{ctrlX}), &out, false, past())
	require.Equal(t, []byte{ctrlX}, out.Bytes(), "Ctrl+X stays a normal key when kill is disallowed")
	require.NotNil(t, s.attachCh, "Ctrl+X must not detach when kill is disallowed")
	require.False(t, s.KillRequested())
}

// Separate top-level tests rather than t.Run subtests: attachedSession's MockPtyFactory
// derives its temp-file name from t.Name(), and a subtest's "Parent/child" name embeds a
// slash that breaks the path.
func TestReadAttachStdinCtrlPageDownNexts(t *testing.T) {
	assertNavDetach(t, []byte("\x1b[6;5~"), DetachNext)
}

func TestReadAttachStdinCtrlPageUpPrevs(t *testing.T) {
	assertNavDetach(t, []byte("\x1b[5;5~"), DetachPrev)
}

func assertNavDetach(t *testing.T, seq []byte, want DetachReason) {
	t.Helper()
	s, _ := attachedSession(t)
	ch := s.attachCh
	var out bytes.Buffer
	s.readAttachStdin(s.ctx, bytes.NewReader(seq), &out, false, past())
	assertClosed(t, ch)
	require.Equal(t, want, s.AttachExitReason())
	require.False(t, s.KillRequested(), "navigation detaches without requesting a kill")
}

func TestReadAttachStdinStaleCtxReturnsWithoutActing(t *testing.T) {
	s, _ := attachedSession(t)
	var out bytes.Buffer
	s.cancel() // a detach already cancelled this attach's ctx: the reader is stale
	s.readAttachStdin(s.ctx, bytes.NewReader([]byte{'a'}), &out, true, past())
	require.Empty(t, out.Bytes(), "a stale reader forwards nothing")
	require.NotNil(t, s.attachCh, "a stale reader never touches the detach state")
	require.False(t, s.KillRequested())
}
