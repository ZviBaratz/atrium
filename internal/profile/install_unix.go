//go:build !windows

package profile

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/ZviBaratz/atrium/log"
)

// TriggerSignal is the signal that toggles a profile run.
//
// SIGUSR1 is chosen because nothing else in Atrium's world uses it: tmux does not
// send it to panes, Bubble Tea handles only SIGINT/SIGTERM/SIGWINCH, and Atrium's
// own quitSignals are SIGINT/SIGTERM/SIGHUP. So it cannot collide with a real
// event, and its default disposition (terminate) is overridden the moment Install
// registers — which is why Install must run before anyone would think to use it.
var TriggerSignal = syscall.SIGUSR1

// Install arms the profiling signal and returns a stop function that disarms it.
//
// Idle cost is one goroutine parked on a channel receive — the same "costs nothing
// when nobody needs it" standard the attach keeper holds itself to. There is no
// enable flag: registering the handler is the entire cost, and gating it behind an
// env var would recreate the problem this package exists to solve, namely that you
// cannot profile a process nobody thought to enable profiling on.
func Install(ctx context.Context) (stop func()) {
	d := NewDumper(Dir(), ParseSeconds(os.Getenv(SecondsEnv)))
	return installOn(ctx, d)
}

// installOn is Install with the Dumper injected, so a test can drive the signal
// path against a temp directory without profiling the test binary for 30 seconds.
func installOn(ctx context.Context, d *Dumper) (stop func()) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, TriggerSignal)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-ch:
				if !ok {
					return
				}
				d.Toggle()
			}
		}
	}()
	log.InfoLog.Printf("pprof: send %v to pid %d to capture a profile", TriggerSignal, os.Getpid())
	return func() {
		signal.Stop(ch)
		close(ch)
		<-done
	}
}
