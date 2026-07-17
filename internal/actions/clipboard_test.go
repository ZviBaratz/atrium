package actions

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// swapClipboardLegs points both clipboard legs at test doubles and restores the
// real ones when the test ends, keeping the suite hermetic (no host clipboard,
// no terminal). out is the OSC 52 sink; exec is the exec-copier stand-in.
func swapClipboardLegs(t *testing.T, out *bytes.Buffer, exec func(string) error) {
	t.Helper()
	origOut, origExec := clipboardOutput, execCopy
	t.Cleanup(func() { clipboardOutput, execCopy = origOut, origExec })
	if out == nil {
		clipboardOutput = nil
	} else {
		clipboardOutput = out
	}
	execCopy = exec
}

// wantOSC52 builds the exact OSC 52 system-clipboard escape for payload, so the
// test pins the wire format independently of the code under test.
func wantOSC52(payload string) string {
	return "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(payload)) + "\x07"
}

// The pure escape builder is the ground truth every other assertion leans on.
func TestClipboardOSC52_ExactBytes(t *testing.T) {
	require.Equal(t, wantOSC52("feature/login"), ClipboardOSC52("feature/login"))
	require.Equal(t, "\x1b]52;c;"+base64.StdEncoding.EncodeToString([]byte("dev"))+"\x07",
		ClipboardOSC52("dev"))
}

// Acceptance (1): from a plain SSH session with no clipboard binary on the
// remote, a copy still lands via OSC 52 — and the exact bytes reach the TUI's
// output for the given payload.
func TestCopyToClipboard_EmitsExactOSC52OverSSH(t *testing.T) {
	var buf bytes.Buffer
	swapClipboardLegs(t, &buf, func(string) error { return errors.New("exec: xclip: not found") })

	err := copyToClipboard("feature/login")

	require.NoError(t, err, "OSC 52 delivered the copy even though the exec copier failed")
	require.Equal(t, wantOSC52("feature/login"), buf.String(),
		"the exact OSC 52 bytes for the payload are emitted to the TUI output")
}

// Belt-and-braces: both legs fire on every copy, so a terminal that ignores
// OSC 52 is covered by the exec copier and vice versa.
func TestCopyToClipboard_RunsBothLegs(t *testing.T) {
	var buf bytes.Buffer
	execCalled := false
	swapClipboardLegs(t, &buf, func(string) error { execCalled = true; return nil })

	require.NoError(t, copyToClipboard("dev"))
	require.Equal(t, wantOSC52("dev"), buf.String(), "OSC 52 leg ran")
	require.True(t, execCalled, "exec leg ran too (belt-and-braces)")
}

// Acceptance (2): when no terminal is wired (OSC 52 unavailable), the exec
// copier is the fallback and a successful copy reports success.
func TestCopyToClipboard_FallsBackToExecWhenNoTerminal(t *testing.T) {
	execCalled := false
	swapClipboardLegs(t, nil, func(string) error { execCalled = true; return nil })

	require.NoError(t, copyToClipboard("main"))
	require.True(t, execCalled, "the exec copier must run as the local fallback")
}

// Acceptance (2): when neither leg can deliver, the failure names the next step
// instead of vanishing.
func TestCopyToClipboard_BothLegsFailNamesNextStep(t *testing.T) {
	swapClipboardLegs(t, nil, func(string) error { return errors.New("xclip: not found") })

	err := copyToClipboard("x")

	require.Error(t, err)
	require.ErrorContains(t, err, "xclip", "the underlying cause is wrapped")
	msg := strings.ToLower(err.Error())
	require.Contains(t, msg, "install", "the error names the next step")
	require.Contains(t, msg, "osc 52", "and points at the terminal alternative")
}

// Mutation guard for emitClipboardOSC52's nil-writer check: without it,
// io.WriteString(nil, …) panics. A nil output with a working exec copier must
// copy cleanly, never panic.
func TestCopyToClipboard_NilOutputDoesNotPanic(t *testing.T) {
	swapClipboardLegs(t, nil, func(string) error { return nil })
	require.NotPanics(t, func() {
		require.NoError(t, copyToClipboard("safe"))
	})
}

// emitClipboardOSC52 reports false when there is no writer, so the caller knows
// to lean on the exec leg (the discriminator the success test above relies on).
func TestEmitClipboardOSC52_NoWriter(t *testing.T) {
	swapClipboardLegs(t, nil, func(string) error { return nil })
	require.False(t, emitClipboardOSC52("x"), "a nil writer means the OSC 52 leg did not deliver")
}
