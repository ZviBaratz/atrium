// Package lifecycle owns the root process lifecycle context: the one place a signal
// becomes cancellation.
//
// It is signal.NotifyContext plus one thing that function cannot express — SIGINT can
// be handed to a child process for the duration of a terminal takeover.
//
// That matters because of how a custom command in `output: terminal` mode runs (#375).
// A tmux attach puts the terminal in RAW mode, where ISIG is off and Ctrl+C is just a
// byte for tmux to forward. A `sh -c` child must run COOKED — raw mode also clears
// OPOST, which turns every newline a build prints into staircased output — and cooked
// mode leaves ISIG on. The child inherits Atrium's process group, so the kernel
// delivers that Ctrl+C to the whole group: to the command the user meant to abort, and
// to Atrium, whose root context is wired to SIGINT and passed to tea.WithContext. The
// result without this package is that pressing Ctrl+C to stop a three-minute `just ci`
// exits the TUI.
//
// Bubble Tea already solves its half — releaseTerminal sets ignoreSignals, so its own
// SIGINT handler stays quiet for the duration of a tea.Exec — and this is Atrium's half
// of the same idea, in the same shape.
//
// Two properties are deliberate:
//
// The suspension is SIGINT-SCOPED. SIGTERM and SIGHUP always cancel. SIGHUP especially:
// main.go registers it to override Go's "terminate without running defers" disposition,
// so losing the terminal cancels the context and lets the deferred autoyes-daemon
// handoff run. Swallowing that would leave the process alive with its terminal gone and
// the handoff skipped.
//
// A suspended SIGINT is DROPPED, not replayed on resume. The user aimed it at the child,
// which already received it; re-raising it as a shutdown once the command finishes would
// quit the app for a keypress that did exactly what was intended.
package lifecycle

import (
	"context"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
)

// interruptSuspended is the number of terminal takeovers currently asking for SIGINT to
// be left to the child. A depth rather than a flag: nesting is not reachable today (the
// event loop is suspended, so nothing can start a second takeover), but a flag that two
// paths could clear independently would end a suspension the other still holds.
//
// Package-level because there is exactly one lifecycle watcher per process, and the
// caller that needs to suspend it — attachCommand.Run, deep in app — is nowhere near
// the main.go call that created it. Threading a handle down to it would put a nil check
// on the path this exists to protect.
var interruptSuspended atomic.Int64

// Watch returns a context cancelled by any of sigs, and a stop function that
// unregisters them and cancels — the contract of signal.NotifyContext, which this
// replaces.
//
// The difference is the loop. signal.NotifyContext's goroutine returns after the first
// signal it sees, so a swallowed one would retire the watcher and leave the process
// unable to shut down on any later signal. This keeps waiting until a signal actually
// cancels.
func Watch(parent context.Context, sigs ...os.Signal) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, sigs...)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			case s := <-ch:
				if !cancels(s) {
					continue
				}
				cancel()
				return
			}
		}
	}()

	return ctx, func() {
		signal.Stop(ch)
		cancel()
		<-done // join, so a stopped watcher cannot outlive the call that stopped it
	}
}

// cancels reports whether s should cancel the lifecycle right now. Split out from the
// watcher goroutine so the decision is testable without raising process-global signals.
func cancels(s os.Signal) bool {
	return s != os.Interrupt || interruptSuspended.Load() == 0
}

// SuspendInterrupt hands SIGINT to a child that owns the terminal, until the returned
// function is called. Call it as `defer SuspendInterrupt()()` around the takeover.
//
// It is inert when no Watch is active, which is what lets attachCommand.Run call it
// unconditionally: unit tests drive Run directly, with no watcher anywhere.
//
// The returned function is idempotent. A resume that could run twice would drive the
// depth negative and leave SIGINT suppressed for the rest of the process — a TUI that
// silently stops answering Ctrl+C, with nothing to explain it.
func SuspendInterrupt() func() {
	interruptSuspended.Add(1)
	var once sync.Once
	return func() { once.Do(func() { interruptSuspended.Add(-1) }) }
}
