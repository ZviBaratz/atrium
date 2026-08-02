// Package profile writes pprof profiles of a running Atrium on demand.
//
// It exists because there was no supported way to attribute Atrium's own CPU. The
// investigation behind #546 could report that `atr` held ~37% of a core at 14
// sessions but not what inside it was spending that, because the process offers no
// profiling surface and ptrace is commonly restricted (kernel.yama.ptrace_scope=1
// blocks attaching to a process that is not a descendant of the diagnosing shell).
//
// A signal is the trigger rather than an HTTP endpoint or a launch flag for one
// reason: the process worth profiling is the one already running. An env-var-gated
// listener can only profile a process someone thought to start with the var set,
// and restarting Atrium destroys exactly the state that makes it interesting — warm
// sessions, a real transcript backlog, an accumulated fleet. `kill -USR1` reaches
// the instance that is misbehaving right now.
//
// It profiles Atrium's own goroutines only. The subprocess half of the cost —
// measured at 19.4% of a core against 37.2% in-process — is invisible here by
// construction, because Atrium is blocked in wait4 while a child runs. That half is
// accounted for separately, per command verb, by package cmdlog.
package profile

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"strconv"
	"sync"
	"time"

	"github.com/ZviBaratz/atrium/log"
)

// SecondsEnv names the environment variable that overrides the CPU sampling
// window.
const SecondsEnv = "ATRIUM_PPROF_SECONDS"

// Sampling window bounds. The default is long enough to catch several full
// metadata sweeps (500ms) and many pane-frame rounds (100ms), so a profile
// describes steady state rather than one tick. The maximum keeps a fat-fingered
// value from holding the profiler open for the rest of the session.
const (
	defaultWindow = 30 * time.Second
	minWindow     = 1 * time.Second
	maxWindow     = 300 * time.Second
)

// ParseSeconds resolves the sampling window from a raw environment value.
//
// Split from the env read so it is testable without a subprocess: anything
// unparseable, non-positive, or out of range falls back to a sane window rather
// than failing, because a diagnostic knob that refuses to run is worse than one
// that runs with the default.
func ParseSeconds(raw string) time.Duration {
	if raw == "" {
		return defaultWindow
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return defaultWindow
	}
	d := time.Duration(n) * time.Second
	if d < minWindow {
		return minWindow
	}
	if d > maxWindow {
		return maxWindow
	}
	return d
}

// Dumper serialises profile runs. Exactly one CPU profile may be open at a time —
// runtime/pprof rejects a second StartCPUProfile with an error, and a diagnostic
// must never be able to take the app down, so a repeated signal is refused rather
// than propagated.
type Dumper struct {
	mu      sync.Mutex
	running bool
	stop    func()
	dir     string
	window  time.Duration
}

// NewDumper builds a Dumper writing into dir with the given sampling window.
func NewDumper(dir string, window time.Duration) *Dumper {
	return &Dumper{dir: dir, window: window}
}

// Dir is the directory profiles are written to: the OS temp dir.
//
// Deliberately not the data dir, even though the log moved there in #566. That
// directory holds live state a running TUI owns (state.json, the worktrees tree),
// `atrium doctor` is under standing orders not to mutate it, and `atrium reset`
// does not clear it — so a stray profiles directory there would accumulate
// forever with nothing to remove it. The log can live there because it is capped
// and rolls itself over; profiles are not, and nothing prunes them. That
// asymmetry is the whole reason the two artifacts sit in different places.
func Dir() string { return os.TempDir() }

// Running reports whether a CPU profile is currently open.
func (d *Dumper) Running() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.running
}

// Toggle starts a CPU profile, or stops one already running.
//
// The signal is a toggle rather than start-only so a long window can be cut short
// once the interesting moment has passed, and so a second press is a useful action
// instead of an error.
func (d *Dumper) Toggle() {
	if d.Running() {
		d.finish()
		return
	}
	d.start()
}

// start opens a CPU profile and arms the window timer. Errors are logged, never
// returned: the caller is a signal handler with nobody to report to, and a failed
// profile must not disturb the app.
func (d *Dumper) start() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.running {
		return
	}
	stamp := time.Now().Format("20060102-150405")
	path := filepath.Join(d.dir, fmt.Sprintf("atrium-cpu-%s-%d.pprof", stamp, os.Getpid()))
	f, err := os.Create(path)
	if err != nil {
		log.ErrorLog.Printf("pprof: could not create %s: %v", path, err)
		return
	}
	if err := pprof.StartCPUProfile(f); err != nil {
		log.ErrorLog.Printf("pprof: could not start CPU profile: %v", err)
		_ = f.Close()
		return
	}
	d.running = true
	// The timer is cancelled by a manual stop, so a toggled-off profile does not
	// get finished twice.
	timer := time.AfterFunc(d.window, d.finish)
	d.stop = func() {
		timer.Stop()
		pprof.StopCPUProfile()
		if cerr := f.Close(); cerr != nil {
			log.ErrorLog.Printf("pprof: could not close %s: %v", path, cerr)
		}
		log.InfoLog.Printf("pprof: wrote CPU profile to %s", path)
	}
	log.InfoLog.Printf("pprof: sampling CPU for %s into %s", d.window, path)
}

// finish stops the open CPU profile and writes the companion snapshots.
func (d *Dumper) finish() {
	d.mu.Lock()
	if !d.running {
		d.mu.Unlock()
		return
	}
	d.running = false
	stop := d.stop
	d.stop = nil
	dir := d.dir
	d.mu.Unlock()

	stop()
	writeSnapshots(dir)
}

// writeSnapshots dumps the point-in-time profiles that pair with the CPU sample.
//
// The goroutine dump is the one that earns its place here: the tick, the pane
// capture chain, the splash animation and the attach keeper are all self-arming
// loops, so "did one of them fork, or fail to die?" is a question this answers and
// a CPU profile does not.
func writeSnapshots(dir string) {
	stamp := time.Now().Format("20060102-150405")
	runtime.GC() // so the heap profile reflects live data, per pprof's own guidance
	for _, name := range []string{"heap", "goroutine"} {
		p := pprof.Lookup(name)
		if p == nil {
			continue
		}
		path := filepath.Join(dir, fmt.Sprintf("atrium-%s-%s-%d.pprof", name, stamp, os.Getpid()))
		f, err := os.Create(path)
		if err != nil {
			log.ErrorLog.Printf("pprof: could not create %s: %v", path, err)
			continue
		}
		if err := p.WriteTo(f, 0); err != nil {
			log.ErrorLog.Printf("pprof: could not write %s: %v", path, err)
		} else {
			log.InfoLog.Printf("pprof: wrote %s profile to %s", name, path)
		}
		if cerr := f.Close(); cerr != nil {
			log.ErrorLog.Printf("pprof: could not close %s: %v", path, cerr)
		}
	}
}
