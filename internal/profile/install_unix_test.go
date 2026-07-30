//go:build !windows

package profile

import (
	"context"
	"syscall"
	"testing"
	"time"
)

// The signal really reaches the dumper.
//
// This is the structural claim the whole package rests on, and nothing else
// asserts it: Install can register a handler for the wrong signal, or spawn a
// goroutine that never reads the channel, and every other test in this package
// still passes. Sending the real signal to this process is the only way to find
// out — and it doubles as proof that registering the handler overrides SIGUSR1's
// default disposition, which is to kill us.
func TestTriggerSignalTogglesTheDumper(t *testing.T) {
	dir := t.TempDir()
	d := NewDumper(dir, maxWindow) // long window, so only the signals move it
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := installOn(ctx, d)
	defer func() {
		d.finish() // whatever the test left open
		stop()
	}()

	if err := syscall.Kill(syscall.Getpid(), TriggerSignal); err != nil {
		t.Fatalf("kill(self, %v): %v", TriggerSignal, err)
	}
	waitFor(t, func() bool { return d.Running() }, "a profile to open after the first signal")

	if err := syscall.Kill(syscall.Getpid(), TriggerSignal); err != nil {
		t.Fatalf("kill(self, %v): %v", TriggerSignal, err)
	}
	waitFor(t, func() bool { return !d.Running() }, "the profile to close after the second signal")

	if !hasPrefixedFile(t, dir, "atrium-cpu-") {
		t.Error("no CPU profile written, want the signal-driven run to have produced one")
	}
}

// Once stopped, a further signal is ignored — the handler is disarmed and the
// goroutine has exited, so a profile cannot be opened behind the app's back after
// shutdown.
func TestStopDisarmsTheTrigger(t *testing.T) {
	dir := t.TempDir()
	d := NewDumper(dir, maxWindow)
	stop := installOn(context.Background(), d)
	stop()

	// Nothing is listening now, so the default disposition would kill this process
	// if signal.Stop had actually unregistered it — which is why a decoy handler is
	// armed for the duration of the send. The decoy really does open a profile, so
	// it must be finished: an abandoned CPU profile is process-wide state, and it
	// would make every later test's StartCPUProfile fail.
	decoy := NewDumper(t.TempDir(), maxWindow)
	reset := installOn(context.Background(), decoy)
	defer func() {
		decoy.finish()
		reset()
	}()
	if err := syscall.Kill(syscall.Getpid(), TriggerSignal); err != nil {
		t.Fatalf("kill(self, %v): %v", TriggerSignal, err)
	}
	time.Sleep(50 * time.Millisecond)

	if d.Running() {
		t.Error("Running() = true, want the stopped dumper to be deaf to the signal")
	}
	if hasPrefixedFile(t, dir, "atrium-cpu-") {
		t.Error("the stopped dumper wrote a profile, want it disarmed")
	}
}

// waitFor polls cond until it holds, so the assertions do not race the signal
// goroutine. Delivery is asynchronous; a bare read would be flaky either way.
func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
