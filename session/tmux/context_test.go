package tmux

import (
	"os/exec"
	"strings"
	"testing"

	cmd2 "github.com/ZviBaratz/atrium/cmd"
	"github.com/ZviBaratz/atrium/cmd/cmd_test"

	"github.com/stretchr/testify/require"
)

// SetContext pushes the @atrium_* user options in a batched command and caches the
// payload: an unchanged push issues no tmux command, while a changed value pushes
// again. This keeps the per-second metadata tick from spawning subprocesses when
// nothing moved.
func TestSetContext_CachesUnchanged(t *testing.T) {
	var ran []string
	cmdExec := cmd_test.MockCmdExec{
		RunFunc: func(c *exec.Cmd) error {
			ran = append(ran, cmd2.ToString(c))
			return nil
		},
		OutputFunc: func(c *exec.Cmd) ([]byte, error) { return nil, nil },
	}
	sess := NewTmuxSessionWithDeps("alpha", "claude", NewMockPtyFactory(t), cmdExec)

	require.NoError(t, sess.SetContext("alpha", "left"))
	require.Len(t, ran, 1, "first push should issue one batched command")

	// The batched command carries both options and a status refresh.
	for _, sub := range []string{"@atrium_name", "@atrium_left", "refresh-client"} {
		require.True(t, strings.Contains(ran[0], sub), "batched command missing %q: %s", sub, ran[0])
	}

	require.NoError(t, sess.SetContext("alpha", "left"))
	require.Len(t, ran, 1, "identical push should be a no-op")

	require.NoError(t, sess.SetContext("alpha", "left-CHANGED"))
	require.Len(t, ran, 2, "changed push should issue a new command")
}
