package tmux

import (
	"context"
	"fmt"
	"math/rand"
	"testing"

	"github.com/ZviBaratz/atrium/internal/testutil"
)

// TestRealSessionsBindTheSandboxSocket states #581's headline property directly:
// a test that starts a real session must not bind the socket the developer's
// running Atrium is on.
//
// Nothing else asserts this on the production path. TestManagedConfigParsesUnderRealTmux
// builds its tmux commands by hand with a socket name of its own, so it can only
// prove tmux honours TMUX_TMPDIR for a socket no shipped code ever opens. This one
// goes through NewSession/Start — i.e. through tmuxCommand's `-L socketName()`,
// the path every other real-tmux test in the module uses — and checks where that
// socket actually landed. socketName() is "atrium" under a sandbox HOME exactly as
// it is for a real install (config.RuntimeName), so the directory is the only thing
// separating this session from the live fleet.
func TestRealSessionsBindTheSandboxSocket(t *testing.T) {
	testutil.RequireTmux(t)
	root := testutil.TmuxRoot(t)

	name := fmt.Sprintf("sockiso-%d", rand.Int31())
	session := NewSession(context.Background(), name, "sleep 300")
	if err := session.Start(t.TempDir()); err != nil {
		t.Fatalf("start session: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	if !containsFile(t, root, socketName()) {
		t.Fatalf("socket %q not found under TMUX_TMPDIR %q: real sessions are binding the "+
			"shared socket dir, where the developer's live Atrium fleet lives (#581)",
			socketName(), root)
	}
	// The socket file alone is not this test's property: every other real-tmux test in
	// the package binds the same name, so under -shuffle=on one of theirs may already
	// have created it. has-session goes through tmuxCommand's `-L socketName()` too,
	// so a session found there is a session on the socket just located under root.
	if !session.DoesSessionExist() {
		t.Fatalf("session %q is not on socket %q: the socket under the sandbox root belongs "+
			"to some other session, so this proves nothing about where NewSession/Start landed",
			name, socketName())
	}
}
