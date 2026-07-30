package actions

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// swapExecCopy points the OS-copier leg at a test double and restores the real one
// when the test ends, keeping the suite hermetic (no host clipboard).
func swapExecCopy(t *testing.T, exec func(string) error) {
	t.Helper()
	orig := execCopy
	t.Cleanup(func() { execCopy = orig })
	execCopy = exec
}

// A working host copier is a clean copy, and it receives the payload verbatim.
func TestCopyToClipboard_RunsTheOSCopier(t *testing.T) {
	var got string
	called := false
	swapExecCopy(t, func(s string) error { called = true; got = s; return nil })

	require.NoError(t, copyToClipboard("feature/login"))
	require.True(t, called, "the OS copier must run")
	require.Equal(t, "feature/login", got, "the payload reaches the copier unchanged")
}

// With no clipboard utility installed, the error names the missing dependency and
// the next step — the caller decides whether that is worth surfacing, since the
// OSC 52 leg (emitted by Bubble Tea, see home.copyToClipboard) covers the copy.
func TestCopyToClipboard_MissingUtilityNamesNextStep(t *testing.T) {
	swapExecCopy(t, func(string) error { return errors.New("exec: xclip: executable file not found in $PATH") })

	err := copyToClipboard("x")

	require.Error(t, err)
	require.ErrorContains(t, err, "xclip", "the underlying cause is wrapped")
	require.Contains(t, strings.ToLower(err.Error()), "install", "the error names the next step")
}
