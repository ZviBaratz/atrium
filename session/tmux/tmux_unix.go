//go:build !windows

package tmux

import (
	"context"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/ZviBaratz/atrium/log"

	"golang.org/x/term"
)

// winchDebounce is how long runWindowSizeMonitor waits after the last resize signal
// before applying the new size, collapsing a drag's burst of events into one resize.
// Tests drive runWindowSizeMonitor directly with their own debounce, so this is just
// the production value.
const winchDebounce = 50 * time.Millisecond

// monitorWindowSize applies the current terminal size to the attached pty and keeps
// it in sync with SIGWINCH for the life of the attach.
func (t *Session) monitorWindowSize() {
	winchChan := make(chan os.Signal, 1)
	signal.Notify(winchChan, syscall.SIGWINCH)
	// Send initial SIGWINCH to trigger the first resize
	_ = syscall.Kill(syscall.Getpid(), syscall.SIGWINCH)

	everyN := log.NewEvery(60 * time.Second)

	doUpdate := func() {
		// Use the current terminal height and width.
		cols, rows, err := term.GetSize(int(os.Stdin.Fd()))
		if err != nil {
			if everyN.ShouldLog() {
				log.ErrorLog.Printf("failed to update window size: %v", err)
			}
		} else {
			if err := t.updateWindowSize(cols, rows); err != nil {
				if everyN.ShouldLog() {
					log.ErrorLog.Printf("failed to update window size: %v", err)
				}
			}
		}
	}
	// Do one at the end of the function to set the initial size.
	defer doUpdate()

	runWindowSizeMonitor(t.ctx, t.wg, winchChan, winchDebounce, doUpdate, func() { signal.Stop(winchChan) })
}

// runWindowSizeMonitor collapses a burst of winch signals into a single resize event
// `debounce` after the last one, registering two ctx-scoped goroutines on wg: a
// debouncer and a handler that calls onResize per debounced event. Both exit on
// ctx.Done; onStop runs when the handler exits (production unsubscribes the OS signal
// there). Extracted from monitorWindowSize so the debounce and teardown are
// unit-testable without real signals or a real pty.
func runWindowSizeMonitor(ctx context.Context, wg *sync.WaitGroup, winch <-chan os.Signal, debounce time.Duration, onResize, onStop func()) {
	wg.Add(2)
	debounced := make(chan struct{}, 1)
	go func() {
		defer wg.Done()
		var resizeTimer *time.Timer
		for {
			select {
			case <-ctx.Done():
				// Stop a pending timer on teardown so it can't fire into a done ctx.
				if resizeTimer != nil {
					resizeTimer.Stop()
				}
				return
			case <-winch:
				if resizeTimer != nil {
					resizeTimer.Stop()
				}
				resizeTimer = time.AfterFunc(debounce, func() {
					select {
					case debounced <- struct{}{}:
					case <-ctx.Done():
					}
				})
			}
		}
	}()
	go func() {
		defer wg.Done()
		defer onStop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-debounced:
				onResize()
			}
		}
	}()
}
