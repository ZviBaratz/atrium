// Package daemon implements autoyes mode as a separate background process (not
// a goroutine): the TUI launches `atrium --daemon`, which polls all stored
// instances and taps Enter on pending prompts, and the TUI kills it again on
// startup and exit.
//
// The daemon is a strict fill-the-gap process: it runs only while no TUI is
// alive. The TUI kills any daemon before app.Run (main.go) and relaunches one
// only on exit, and the TUI is the sole creator of sessions — so no process
// adds sessions during the daemon's lifetime in the supported single-TUI
// workflow. The daemon therefore snapshots the instance list once at startup
// and treats that snapshot as authoritative for its whole (short) life; newly
// created sessions are picked up automatically at the next relaunch, not via an
// in-daemon refresh. See the RunDaemon load site for the invariant in full.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/session"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// Tunables for graceful daemon shutdown, overridable in tests.
// gracefulStopTimeout bounds how long StopDaemon waits for a SIGTERM'd daemon to
// persist its instances and exit before escalating to SIGKILL; gracefulStopPoll
// is the interval between liveness probes while waiting; daemonStartupGrace
// bounds how long StopDaemon gives a live, just-launched daemon — whose PID is
// recorded (LaunchDaemon) before RunDaemon reaches acquireDaemonLock — to take
// its lock before a present-but-unheld lock is trusted as proof of death.
var (
	gracefulStopTimeout = 3 * time.Second
	gracefulStopPoll    = 25 * time.Millisecond
	daemonStartupGrace  = time.Second
)

// daemonLockFilename is the advisory-lock file the running daemon holds for its
// entire lifetime, sitting next to daemon.pid in the data dir. The kernel
// releases an flock when its owning process dies (cleanly or by crash), so the
// lock is a liveness+ownership signal that survives reboots — unlike the PID
// file, whose number the OS may have recycled onto an unrelated process.
const daemonLockFilename = "daemon.lock"

// errDaemonAlreadyRunning is returned by acquireDaemonLock when another daemon
// already holds the lock, so RunDaemon can decline to start a second one.
var errDaemonAlreadyRunning = errors.New("another atrium daemon is already running")

// errDaemonLockAbsent is returned by isDaemonLockHeld when no lock file exists.
// This is deliberately distinct from a present-but-free lock: an absent file is
// NOT proof the daemon is dead, because a daemon from a build predating the lock
// never created one. Callers must treat it as "can't confirm dead" and fall back
// to a direct signal/probe rather than assuming the recorded PID is stale —
// otherwise upgrading from a pre-lock binary would orphan a live legacy daemon.
var errDaemonLockAbsent = errors.New("daemon lock file does not exist")

// daemonLockPath returns the path to the daemon's advisory lock file in the data
// dir. Shared by RunDaemon (which holds the lock) and StopDaemon (which checks it).
func daemonLockPath() (string, error) {
	dir, err := config.GetConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, daemonLockFilename), nil
}

// effectivePollInterval converts a configured poll interval in milliseconds into
// a positive ticker duration. A non-positive value — the field absent from a
// legacy or hand-edited config.json, or explicitly set <= 0 — would panic
// time.NewTicker, so it falls back to the built-in default and keeps the daemon
// running instead of crashing the autoyes process.
func effectivePollInterval(ms int) time.Duration {
	if ms <= 0 {
		ms = config.DefaultDaemonPollIntervalMs
	}
	return time.Duration(ms) * time.Millisecond
}

// RunDaemon runs the daemon process which iterates over all sessions and runs AutoYes mode on them.
// It's expected that the main process kills the daemon when the main process starts.
// ctx carries the daemon's shutdown signal (main installs signal.NotifyContext for
// SIGINT/SIGTERM); cancelling it stops the poll loop and kills in-flight subprocesses.
func RunDaemon(ctx context.Context, cfg *config.Config) error {
	log.InfoLog.Printf("starting daemon")

	// Hold an exclusive advisory lock for the daemon's whole lifetime. It serves
	// two purposes: a single-instance guard (a second daemon declines to start so
	// two never double-tap the same prompts) and the liveness signal StopDaemon
	// checks before signaling a PID — the kernel frees the lock on this process's
	// death, so a recycled PID can never masquerade as a live daemon. Failure to
	// resolve or open the lock is non-fatal: log and run unlocked, matching the
	// pre-lock behavior.
	if lockPath, err := daemonLockPath(); err != nil {
		log.WarningLog.Printf("could not resolve daemon lock path: %v; running without single-instance lock", err)
	} else if release, err := acquireDaemonLock(lockPath); err != nil {
		if errors.Is(err, errDaemonAlreadyRunning) {
			log.InfoLog.Printf("another daemon already holds %s; exiting", lockPath)
			return nil
		}
		log.WarningLog.Printf("could not acquire daemon lock %s: %v; running without it", lockPath, err)
	} else {
		defer release()
	}

	state := config.LoadState()
	storage, err := session.NewStorage(state)
	if err != nil {
		return fmt.Errorf("failed to initialize storage: %w", err)
	}

	// Load the instance list once for the daemon's whole lifetime. This is
	// correct, not a missed refresh (issue #210): the daemon and TUI are mutually
	// exclusive — main.go's RunE calls StopDaemon() before app.Run and only
	// LaunchDaemon()s on TUI exit (gated by shouldLaunchDaemonOnExit; covered by
	// TestShouldLaunchDaemonOnExit) — and the TUI is the only thing that creates
	// sessions. So nothing adds sessions while this daemon is alive; any created
	// later are covered by the next relaunch.
	//
	// This rests on there being one TUI per data dir, which is now enforced: the
	// interactive atrium holds an flock (tui.lock, taken by acquireTUILock in
	// main.go's RunE) for its whole life, so a second TUI on the same data dir
	// refuses to start rather than racing this snapshot. That closes #230 — which
	// tracked the read- and write-side staleness two concurrent TUIs would
	// otherwise cause (the write side being the whole-file SaveInstances below, run
	// from this startup snapshot) — by prevention, making both hazards impossible,
	// rather than by a concurrency-sensitive in-daemon refresh that would not have
	// made the write side safe anyway.
	instances, err := storage.LoadInstances(ctx)
	if err != nil {
		return fmt.Errorf("failed to load instances: %w", err)
	}
	for _, instance := range instances {
		// Assume AutoYes is true if the daemon is running.
		instance.AutoYes = true
	}

	pollInterval := effectivePollInterval(cfg.DaemonPollInterval)

	// If we get an error for a session, it's likely that we'll keep getting the error. Log every 30 seconds.
	everyN := log.NewEvery(60 * time.Second)

	// pollOnce sweeps every active instance once: shared pane-state → status/prompt
	// mapping with the TUI metadata loop (see Instance.ApplyPaneState), so the
	// headless autoyes path can't drift from what the UI does. AutoYes is true on
	// every instance here (set above), so an auto-answerable prompt taps Enter; a
	// destructive manual prompt (claude's plan approval) instead surfaces NeedsInput,
	// persisted at shutdown so the TUI shows the blocked row.
	pollOnce := func() {
		for _, instance := range instances {
			// We only store started instances, but check anyway.
			if instance.Started() && !instance.Paused() {
				if instance.ApplyPaneState(instance.Poll()) {
					// Auto-answered a prompt: refresh the diff so the post-answer
					// state is reflected in the persisted snapshot.
					if err := instance.UpdateDiffStats(); err != nil {
						if everyN.ShouldLog() {
							log.WarningLog.Printf("could not update diff stats for %s: %v", instance.Title, err)
						}
					}
				}
				// Keep the persisted model current so the TUI shows the right
				// chip after relaunch (parity with app_poll.go tickUpdateMetadataCmd).
				if model, stamp, ok := instance.ComputeModel(); ok {
					instance.SetModelMeta(model, stamp)
				}
			}
		}
	}

	wg := &sync.WaitGroup{}
	wg.Add(1)
	stopCh := make(chan struct{})
	go func() {
		defer wg.Done()
		pollOnce() // first sweep immediately, then on each tick
		ticker := time.NewTicker(pollInterval)
		defer ticker.Stop()
		for {
			// One blocking select so shutdown (stopCh close or ctx cancel) is observed
			// immediately rather than after the remaining tick interval, and the ticker
			// is always stopped on exit.
			select {
			case <-stopCh:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				pollOnce()
			}
		}
	}()

	// Block until the lifecycle context is cancelled (SIGINT/SIGTERM via main's
	// signal.NotifyContext). Save instances before exiting.
	<-ctx.Done()
	log.InfoLog.Printf("shutting down: %v", context.Cause(ctx))

	// Stop the goroutine so we don't race.
	close(stopCh)
	wg.Wait()

	if err := storage.SaveInstances(instances); err != nil {
		log.ErrorLog.Printf("failed to save instances when terminating daemon: %v", err)
	}
	return nil
}

// selfPath is the binary's path resolved once at process start. LaunchDaemon
// runs at TUI exit, and by then a completed self-update may have swapped the
// binary on disk: os.Executable is a live readlink of /proc/self/exe on Linux,
// which after the swap reports the deleted old inode (".../.atrium.old
// (deleted)") — exec'ing that fails. The startup path stays valid because the
// swap reuses it for the new binary.
var selfPath, selfPathErr = os.Executable()

// LaunchDaemon launches the daemon process.
func LaunchDaemon(ctx context.Context) error {
	// Find the atrium binary (resolved at process start, see selfPath).
	execPath, err := selfPath, selfPathErr
	if err != nil {
		return fmt.Errorf("failed to get executable path: %w", err)
	}

	// WithoutCancel is deliberate: the daemon is a detached successor that must
	// survive the launching TUI's shutdown (it has its own signal handling), so
	// its exec must never be killed by the parent's lifecycle context.
	cmd := exec.CommandContext(context.WithoutCancel(ctx), execPath, "--daemon")

	// Detach the process from the parent
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil

	// Set process group to prevent signals from propagating
	cmd.SysProcAttr = getSysProcAttr()

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start child process: %w", err)
	}

	log.InfoLog.Printf("started daemon child process with PID: %d", cmd.Process.Pid)

	// Save PID to a file for later management
	pidDir, err := config.GetConfigDir()
	if err != nil {
		return fmt.Errorf("failed to get config directory: %w", err)
	}

	pidFile := filepath.Join(pidDir, "daemon.pid")
	// Atomic write (temp → fsync → rename), matching config.json/state.json: a crash
	// mid-write must not leave a torn PID file that StopDaemon would misparse or skip,
	// orphaning the daemon.
	if err := config.WriteFileAtomic(pidFile, []byte(fmt.Sprintf("%d", cmd.Process.Pid)), 0644); err != nil {
		return fmt.Errorf("failed to write PID file: %w", err)
	}

	// Don't wait for the child to exit, it's detached
	return nil
}

// StopDaemon attempts to stop a running daemon process if it exists. Returns no error if the daemon is not found
// (assumes the daemon does not exist).
func StopDaemon() error {
	pidDir, err := config.GetConfigDir()
	if err != nil {
		return fmt.Errorf("failed to get config directory: %w", err)
	}

	pidFile := filepath.Join(pidDir, "daemon.pid")
	data, err := os.ReadFile(pidFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to read PID file: %w", err)
	}

	var pid int
	if _, err := fmt.Sscanf(string(data), "%d", &pid); err != nil {
		return fmt.Errorf("invalid PID file format: %w", err)
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("failed to find daemon process: %w", err)
	}

	// Verify a daemon is actually alive before signaling. The kernel holds the
	// daemon's flock only while the real process lives, so a present-but-free lock
	// proves the PID is stale — the daemon crashed, was killed, or the machine
	// rebooted and recycled the number onto an unrelated process — and signaling it
	// would hit an innocent victim. In that case drop the stale PID file and return
	// without signaling.
	//
	// An ABSENT lock file is treated differently: it is not proof of death (a daemon
	// from a pre-lock build never created one, so the file's absence can mean a live
	// legacy daemon), so we fall through to terminate rather than orphan it. A
	// lock-check error is handled the same way — both preserve the "ensure stopped"
	// guarantee. Once any post-lock daemon has run, the file persists on disk, so a
	// genuinely dead daemon is detected below as present-but-free, never absent.
	lockPath := filepath.Join(pidDir, daemonLockFilename)
	held, lockErr := isDaemonLockHeld(lockPath)
	switch {
	case errors.Is(lockErr, errDaemonLockAbsent):
		log.InfoLog.Printf("no daemon lock file at %s; stopping PID %d directly (possible pre-lock daemon)", lockPath, pid)
	case lockErr != nil:
		log.WarningLog.Printf("could not verify daemon liveness via lock: %v; proceeding to stop PID %d", lockErr, pid)
	case !held:
		// One live daemon hides behind a free lock: one so freshly launched (an
		// exiting TUI spawns its successor before releasing tui.lock, and reset
		// can run the moment that lock frees) that it has not reached
		// acquireDaemonLock yet. Grant it a startup grace before trusting the
		// free lock as proof of death: locking within the grace proves a live
		// daemon (stop it below); dying or outliving the grace without ever
		// locking proves the PID stale or recycled.
		if awaitDaemonStartupLock(proc, lockPath) {
			log.InfoLog.Printf("daemon PID %d took its lock during the startup grace; stopping it", pid)
			break
		}
		log.InfoLog.Printf("daemon PID %d is stale (lock present but unheld); skipping signal", pid)
		if err := os.Remove(pidFile); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("failed to remove stale PID file: %w", err)
		}
		return nil
	}

	// Graceful stop (SIGTERM, then SIGKILL fallback) so the daemon persists the
	// autoyes progress it made while the TUI was closed instead of having it
	// thrown away by an immediate kill. terminateProcess blocks until the daemon
	// is gone, so the caller can safely load state afterward (see main.go).
	if err := terminateProcess(proc, lockPath); err != nil {
		return fmt.Errorf("failed to stop daemon process: %w", err)
	}

	// Clean up PID file
	if err := os.Remove(pidFile); err != nil {
		return fmt.Errorf("failed to remove PID file: %w", err)
	}

	log.InfoLog.Printf("daemon process (PID: %d) stopped successfully", pid)
	return nil
}
