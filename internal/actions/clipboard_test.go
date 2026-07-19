package actions

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// errWriter is an io.Writer that always returns a write error, simulating a
// broken pipe or closed fd without touching a real terminal.
type errWriter struct{}

func (errWriter) Write(_ []byte) (int, error) { return 0, errors.New("broken pipe") }

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

// TestEmitClipboardOSC52_WriteError: a write error (broken pipe, closed fd) is
// indistinguishable from "no terminal" from the caller's perspective — the OSC 52
// leg reports false so copyToClipboard falls back to execCopy.
func TestEmitClipboardOSC52_WriteError(t *testing.T) {
	orig := clipboardOutput
	clipboardOutput = errWriter{}
	t.Cleanup(func() { clipboardOutput = orig })
	require.False(t, emitClipboardOSC52("x"), "a write error must report the OSC 52 leg as not delivered")
}

// TestCopyToClipboard_WriteFaultyOutputFallsBackToExec: when the wired terminal
// writer errors (broken pipe), the exec leg still runs and a clean exec reports
// overall success — the write error is not exposed to the caller.
func TestCopyToClipboard_WriteFaultyOutputFallsBackToExec(t *testing.T) {
	orig, origExec := clipboardOutput, execCopy
	execCalled := false
	clipboardOutput = errWriter{}
	execCopy = func(string) error { execCalled = true; return nil }
	t.Cleanup(func() { clipboardOutput, execCopy = orig, origExec })

	require.NoError(t, copyToClipboard("fallback"), "a successful exec leg must report overall success")
	require.True(t, execCalled, "the exec leg must run when the OSC 52 write errors")
}

// TestCopyToClipboard_WriteFaultyOutputAndExecFail: when both legs fail — the
// terminal writer errors and execCopy returns an error — the reported error names
// the install hint and the OSC 52 alternative, keeping a stuck copy actionable.
func TestCopyToClipboard_WriteFaultyOutputAndExecFail(t *testing.T) {
	orig, origExec := clipboardOutput, execCopy
	clipboardOutput = errWriter{}
	execCopy = func(string) error { return errors.New("xclip: not found") }
	t.Cleanup(func() { clipboardOutput, execCopy = orig, origExec })

	err := copyToClipboard("x")
	require.Error(t, err)
	msg := strings.ToLower(err.Error())
	require.Contains(t, msg, "install", "the error must name the next step (install a clipboard utility)")
	require.Contains(t, msg, "osc 52", "the error must name the terminal alternative")
}
