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
// What this package does NOT do, so the next reader does not assume otherwise: the group
// holds more than Atrium and the command. Any subprocess in flight when the takeover starts
// is in it too — the keeper's `tmux send-keys` children (which app.killedByTerminalSignal
// handles), a metadata sweep's git/tmux children (harmless; they are re-polled), and a
// background action's child such as auto-naming or open-PR, which is NOT serialised against
// a custom command and will surface a spurious failure the user did not cause. Confining the
// signal properly means real job control — Setpgid, tcsetpgrp and SIGTTOU handling — which
// is deliberately out of scope here — see #619.
//
// Bubble Tea already solves its half — releaseTerminal sets ignoreSignals, so its own
// SIGINT handler stays quiet for the duration of a tea.Exec — and this is Atrium's half
// of the same idea, in the same shape.
//
// Three properties are deliberate:
//
// The suspension never touches a signal that means "shut down". SIGTERM and SIGHUP always
// cancel. SIGHUP especially:
// main.go registers it to override Go's "terminate without running defers" disposition,
// so losing the terminal cancels the context and lets the deferred autoyes-daemon
// handoff run. Swallowing that would leave the process alive with its terminal gone and
// the handoff skipped.
//
// A suspended SIGINT is DROPPED, not replayed on resume. The user aimed it at the child,
// which already received it; re-raising it as a shutdown once the command finishes would
// quit the app for a keypress that did exactly what was intended.
//
// Nothing is IGNORED at the OS level. An ignored signal is inherited across exec, so
// ignoring SIGQUIT would have made Ctrl+\ kill neither Atrium nor the command — the two
// signals are declined by Atrium and left to the child. See SuspendTerminalSignals.
package lifecycle

import (
	"context"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
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
//
// Hence TWO channels, a split that is load-bearing rather than tidy. os/signal DROPS a
// signal that arrives on a full channel, and a borrowable SIGINT sharing one buffer with
// the fatal signals can therefore cost the SIGHUP that runs the daemon handoff: the user
// holds Ctrl+C against a stubborn build, each SIGINT is swallowed, and the SIGHUP from
// the closing window lands on a buffer that has no room. signal.NotifyContext could
// never lose that way because its goroutine returns on the first signal it sees, so
// nothing was ever queued behind a swallowed one. Keeping the watcher alive is precisely
// what makes buffering matter, so the borrowable signal gets its own queue.
func Watch(parent context.Context, sigs ...os.Signal) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)

	var borrowable, fatal []os.Signal
	for _, s := range sigs {
		if s == os.Interrupt {
			borrowable = append(borrowable, s)
			continue
		}
		fatal = append(fatal, s)
	}

	// The SPLIT above is the guarantee; the buffer below is only politeness. A held Ctrl+C
	// repeats, and dropping some of those repeats costs nothing because every one of them
	// is swallowed anyway — what must never be dropped is the first fatal signal, and it
	// now has a queue no SIGINT can occupy.
	intCh := make(chan os.Signal, 8)
	fatalCh := make(chan os.Signal, len(fatal)+1)
	if len(borrowable) > 0 {
		signal.Notify(intCh, borrowable...)
	}
	if len(fatal) > 0 {
		signal.Notify(fatalCh, fatal...)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			case <-fatalCh:
				// SIGTERM and SIGHUP are never borrowed, so there is nothing to consult.
				cancel()
				return
			case s := <-intCh:
				if !cancels(s) {
					continue
				}
				cancel()
				return
			}
		}
	}()

	return ctx, func() {
		signal.Stop(intCh)
		signal.Stop(fatalCh)
		cancel()
		<-done // join, so a stopped watcher cannot outlive the call that stopped it
	}
}

// cancels reports whether s should cancel the lifecycle right now. Split out from the
// watcher goroutine so the decision is testable without raising process-global signals.
func cancels(s os.Signal) bool {
	return s != os.Interrupt || interruptSuspended.Load() == 0
}

// interruptGrace is how long a borrow outlives the child it was taken for. A var so
// tests can shrink it.
//
// It exists because a signal has no arrival time we can read. cancels() runs when the
// watcher goroutine OBSERVES the SIGINT, not when the kernel delivered it, and the two
// can straddle the resume: a Ctrl+C aimed at `sh -c "sleep 60"` kills the child at once,
// so Wait returns, Run's deferred resume fires, and the watcher may only then dequeue a
// signal that was queued while the borrow was still held. Reading the depth at that point
// sees zero and quits the TUI — the exact outcome this package exists to prevent, on the
// keypress it exists to make survivable.
//
// A short grace resolves it in the safe direction. The cost is bounded and statable: a
// Ctrl+C meant for Atrium itself, pressed within the grace of a command finishing, is
// swallowed and must be pressed again. That is strictly better than a coin flip on
// whether the app survives.
var interruptGrace = 500 * time.Millisecond

// swallowedDuringTakeover are the signals a cooked takeover registers a handler for and
// then discards, as opposed to SIGINT, which the lifecycle watcher already receives and
// merely declines to act on.
//
// REGISTERING is the mechanism, not ignoring, and the difference is the whole behaviour.
// POSIX resets a CAUGHT signal to SIG_DFL across exec but leaves an IGNORED one ignored —
// so signal.Ignore(SIGQUIT) is inherited by the `sh -c` child and Ctrl+\ then does nothing
// at all, killing neither Atrium nor the command the user was trying to stop. Registering
// it instead demotes it from Go's default for Atrium while leaving the child's default
// intact, which is exactly the split SIGINT already gets: the keypress reaches the command
// and not the app.
//
// SIGQUIT is the whole list, and it is here because `output: terminal` is
// the first Atrium state that runs with ISIG ON — every other takeover is raw, and so is
// the TUI — which arms Ctrl+\ for the first time. Its Go default is dump-every-goroutine
// -stack and exit(2), skipping main.go's deferred autoyes-daemon handoff, so a user who
// tries Ctrl+C, sees the build ignore it, and escalates one key to the left would kill
// Atrium in the one way that leaves the whole fleet unanswered (#264's symptom) with
// goroutine traces where their screen used to be.
//
// SIGTSTP (Ctrl+Z) is deliberately NOT here. It is armed the same way, but it stops the
// entire foreground process group — Atrium and the child together — and `fg` resumes
// both. That is coherent job control rather than a defect, and swallowing it would take
// away a shell feature the user asked for.
var swallowedDuringTakeover = []os.Signal{syscall.SIGQUIT}

// notifySignals and stopSignals seam os/signal's registration calls, for the reason
// app/app_attach.go seams term's tty calls: the deregistration half cannot be driven by
// raising the signal — a SIGQUIT delivered after a correct stop dumps stacks and exits(2),
// taking the test binary with it — so without a seam "we hand it back afterwards" is
// unassertable, and an unassertable claim here leaves Go's stack-dump aid disabled for the
// rest of the process.
var (
	notifySignals = signal.Notify
	stopSignals   = signal.Stop
)

// SuspendTerminalSignals hands the terminal's signals to a child that owns the terminal,
// until the returned function is called. Call it as
// `defer SuspendTerminalSignals()()` around the takeover.
//
// Neither signal is ignored at the OS level, and that is deliberate: an ignored signal is
// INHERITED across exec, so the child would stop receiving the keypress the user aimed at
// it. SIGINT is swallowed by the watcher (see cancels); SIGQUIT is registered here for the
// duration and handed back afterwards. Either way Atrium declines the signal and the
// child still gets it.
//
// It is inert when no Watch is active, which is what lets attachCommand.Run call it
// unconditionally: unit tests drive Run directly, with no watcher anywhere.
//
// The returned function is idempotent. A resume that could run twice would drive the
// depth negative and leave SIGINT suppressed for the rest of the process — a TUI that
// silently stops answering Ctrl+C, with nothing to explain it.
func SuspendTerminalSignals() func() {
	interruptSuspended.Add(1)
	// Buffered, with no reader on purpose: the registration itself is the effect, and a
	// signal that arrives simply lands in the buffer and is discarded. Nothing needs to
	// observe it — Atrium's job is only to stop dying from it.
	quitCh := make(chan os.Signal, 4)
	notifySignals(quitCh, swallowedDuringTakeover...)

	var once sync.Once
	return func() {
		once.Do(func() {
			// Hands SIGQUIT back to Go's default. The stack dump is a debugging aid outside
			// a takeover and must not be disabled process-wide.
			stopSignals(quitCh)
			// Released on a timer, not here — see interruptGrace. AfterFunc rather than a
			// parked goroutine so a takeover costs nothing once the grace has elapsed.
			time.AfterFunc(interruptGrace, func() { interruptSuspended.Add(-1) })
		})
	}
}
