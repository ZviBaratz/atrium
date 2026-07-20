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

func spooled(t *testing.T) []outbox.Entry {
	t.Helper()
	entries, err := outbox.List()
	require.NoError(t, err)
	return entries
}

func send(t *testing.T, selector, path, text string, wait time.Duration) (stdout, stderr string, err error) {
	t.Helper()
	var out, errOut bytes.Buffer
	err = runSend(&out, &errOut, selector, path, text, wait)
	return out.String(), errOut.String(), err
}

// TestSendSpoolsMessage is the core contract: one message, addressed by the
// (Title, Path) pair the drain matches on.
func TestSendSpoolsMessage(t *testing.T) {
	sandboxDataDir(t)
	d := inst("fix-auth", "/repo/web")
	d.TmuxName = "atrium_web_fix-auth"
	seedInstances(t, d)

	stdout, _, err := send(t, "fix-auth", "", "rebase on main", 0)
	require.NoError(t, err)
	assert.Contains(t, stdout, "fix-auth")

	entries := spooled(t)
	require.Len(t, entries, 1)
	require.NoError(t, entries[0].Err)
	assert.Equal(t, "fix-auth", entries[0].Message.Title)
	assert.Equal(t, "/repo/web", entries[0].Message.Path)
	assert.Equal(t, "atrium_web_fix-auth", entries[0].Message.TmuxName)
	assert.Equal(t, "rebase on main", entries[0].Message.Text)
}

// TestSendNeverWritesState is the invariant the whole spool design exists to
// protect. state.json has exactly one writer at any instant; if send ever
// appended to it directly, a concurrent TUI save — or the autoyes daemon's exit
// save from its startup snapshot — would silently clobber the queued prompt.
// Asserted on the bytes, so any write at all fails this.
func TestSendNeverWritesState(t *testing.T) {
	dir := sandboxDataDir(t)
	seedInstances(t, inst("fix-auth", "/repo/web"))

	statePath := filepath.Join(dir, config.StateFileName)
	before, err := os.ReadFile(statePath)
	require.NoError(t, err)

	_, _, err = send(t, "fix-auth", "", "hello", 0)
	require.NoError(t, err)

	after, err := os.ReadFile(statePath)
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after), "send must not touch state.json")
}

// TestSendToPausedSessionQueues: the queue is persisted state, so a prompt for a
// paused session survives and delivers after a resume. Saying only "queued"
// would imply it is about to be answered.
func TestSendToPausedSessionQueues(t *testing.T) {
	sandboxDataDir(t)
	d := inst("fix-auth", "/repo/web")
	d.Status = session.Paused
	seedInstances(t, d)

	_, stderr, err := send(t, "fix-auth", "", "hello", 0)
	require.NoError(t, err)
	assert.Len(t, spooled(t), 1, "a paused target still gets its message spooled")
	assert.Contains(t, stderr, "paused")
	assert.Contains(t, stderr, "resume")
}

// TestSendUnknownSessionSpoolsNothing: an undeliverable message must not be left
// in the spool to be discarded later — fail at send time, while the user is here.
func TestSendUnknownSessionSpoolsNothing(t *testing.T) {
	sandboxDataDir(t)
	seedInstances(t, inst("fix-auth", "/repo/web"))

	_, _, err := send(t, "nope", "", "hello", 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"nope"`)
	assert.Empty(t, spooled(t))
}

// TestSendAmbiguousSpoolsNothing: same, for the cross-repo title collision.
func TestSendAmbiguousSpoolsNothing(t *testing.T) {
	sandboxDataDir(t)
	seedInstances(t, inst("api", "/repo/web"), inst("api", "/repo/svc"))

	_, _, err := send(t, "api", "", "hello", 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ambiguous")
	assert.Empty(t, spooled(t))
}

// TestSendPathDisambiguates addresses one of two same-titled sessions.
func TestSendPathDisambiguates(t *testing.T) {
	sandboxDataDir(t)
	seedInstances(t, inst("api", "/repo/web"), inst("api", "/repo/svc"))

	_, _, err := send(t, "api", "/repo/svc", "hello", 0)
	require.NoError(t, err)

	entries := spooled(t)
	require.Len(t, entries, 1)
	assert.Equal(t, "/repo/svc", entries[0].Message.Path)
}

// TestSendRejectsEmptyPrompt: an empty prompt would be a no-op inside
// QueueFollowupPrompt, so it would vanish with no explanation.
func TestSendRejectsEmptyPrompt(t *testing.T) {
	sandboxDataDir(t)
	seedInstances(t, inst("fix-auth", "/repo/web"))

	for _, text := range []string{"", "\n\n", "   \t  "} {
		_, _, err := send(t, "fix-auth", "", text, 0)
		require.Error(t, err, "text %q", text)
		assert.Empty(t, spooled(t))
	}
}

// TestSendTrimsTrailingNewlineOnly: a heredoc or a pipe adds a trailing newline,
// but leading whitespace can be meaningful (an indented code block).
func TestSendTrimsTrailingNewlineOnly(t *testing.T) {
	sandboxDataDir(t)
	seedInstances(t, inst("fix-auth", "/repo/web"))

	_, _, err := send(t, "fix-auth", "", "  indented\nsecond line\n\n", 0)
	require.NoError(t, err)

	entries := spooled(t)
	require.Len(t, entries, 1)
	assert.Equal(t, "  indented\nsecond line", entries[0].Message.Text)
}

// TestSendWaitReturnsWhenDrained: the spooled file disappearing is the delivery
// receipt, so --wait watches for exactly that.
func TestSendWaitReturnsWhenDrained(t *testing.T) {
	sandboxDataDir(t)
	seedInstances(t, inst("fix-auth", "/repo/web"))

	// Stand in for the TUI's drain: remove the file shortly after it appears.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 100 {
			if entries, err := outbox.List(); err == nil && len(entries) == 1 {
				_ = outbox.Remove(entries[0].Path)
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()

	_, _, err := send(t, "fix-auth", "", "hello", 5*time.Second)
	<-done
	require.NoError(t, err)
	assert.Empty(t, spooled(t))
}

// TestSendWaitTimesOut fails loudly when nothing drained, while making clear the
// message was not lost.
func TestSendWaitTimesOut(t *testing.T) {
	sandboxDataDir(t)
	seedInstances(t, inst("fix-auth", "/repo/web"))

	_, _, err := send(t, "fix-auth", "", "hello", 150*time.Millisecond)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "still queued", "the message survives a timeout; say so")
	assert.Len(t, spooled(t), 1, "a timeout must leave the message in the spool")
}

// TestMessageText covers the three ways a prompt arrives.
func TestMessageText(t *testing.T) {
	got, err := messageText([]string{"sess", "inline text"}, strings.NewReader("ignored"))
	require.NoError(t, err)
	assert.Equal(t, "inline text", got)

	got, err = messageText([]string{"sess"}, strings.NewReader("from stdin"))
	require.NoError(t, err)
	assert.Equal(t, "from stdin", got)

	got, err = messageText([]string{"sess", "-"}, strings.NewReader("explicit stdin"))
	require.NoError(t, err)
	assert.Equal(t, "explicit stdin", got)
}
