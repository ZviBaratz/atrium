//go:build !windows

package tmux

import (
	"context"
	"os"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// runWindowSizeMonitor is the debounce+dispatch core extracted from monitorWindowSize.
// These tests drive it directly with a local signal channel and a recording callback, so
// they exercise the SIGWINCH→resize, debounce, and teardown paths without registering a
// real OS signal handler or touching a pty (the term.GetSize/pty.Setsize wrapper around it
// is a thin OS adapter that can't be unit-tested hermetically).

// resizeRecorder is a mutex-guarded counter: the handler goroutine increments it while the
// test goroutine reads it, so the access must be synchronized to stay clean under -race.
type resizeRecorder struct {
	mu    sync.Mutex
	count int
}

func (r *resizeRecorder) onResize() {
	r.mu.Lock()
	r.count++
	r.mu.Unlock()
}

func (r *resizeRecorder) calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.count
}

// waitWG fails the test if the monitor's goroutines don't all exit promptly.
func waitWG(t *testing.T, wg *sync.WaitGroup) {
	t.Helper()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("window-size monitor goroutines did not exit on ctx cancel")
	}
}

func TestWindowSizeMonitorResizesOnSignal(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	winch := make(chan os.Signal, 1)
	rec := &resizeRecorder{}

	runWindowSizeMonitor(ctx, &wg, winch, 5*time.Millisecond, rec.onResize, func() {})

	winch <- syscall.SIGWINCH
	require.Eventually(t, func() bool { return rec.calls() == 1 }, time.Second, 2*time.Millisecond,
		"a single winch triggers one debounced resize")

	cancel()
	waitWG(t, &wg)
}

func TestWindowSizeMonitorCoalesces(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	winch := make(chan os.Signal, 1)
	rec := &resizeRecorder{}

	// A generous debounce so all five sends land within one window before the timer fires.
	runWindowSizeMonitor(ctx, &wg, winch, 50*time.Millisecond, rec.onResize, func() {})

	for i := 0; i < 5; i++ {
		winch <- syscall.SIGWINCH
	}
	require.Eventually(t, func() bool { return rec.calls() == 1 }, time.Second, 2*time.Millisecond,
		"a burst of winch signals collapses to a single resize")
	require.Never(t, func() bool { return rec.calls() > 1 }, 100*time.Millisecond, 10*time.Millisecond,
		"the debounced burst produces no extra resizes")

	cancel()
	waitWG(t, &wg)
}

func TestWindowSizeMonitorStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	winch := make(chan os.Signal, 1)
	rec := &resizeRecorder{}
	stopped := make(chan struct{})

	runWindowSizeMonitor(ctx, &wg, winch, 50*time.Millisecond, rec.onResize, func() { close(stopped) })

	cancel()
	waitWG(t, &wg) // both goroutines exit on ctx.Done
	select {
	case <-stopped:
	default:
		t.Fatal("onStop (signal unsubscribe) not run when the handler exits")
	}
	require.Zero(t, rec.calls(), "no resize fires after an immediate cancel")
}
