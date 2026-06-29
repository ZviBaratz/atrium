// Package daemon implements autoyes mode as a separate background process (not
// a goroutine): the TUI launches `atrium --daemon`, which polls all stored
// instances and taps Enter on pending prompts, and the TUI kills it again on
// startup and exit.
package daemon

import (
	"context"
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
// is the interval between liveness probes while waiting.
var (
	gracefulStopTimeout = 3 * time.Second
	gracefulStopPoll    = 25 * time.Millisecond
)

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
	state := config.LoadState()
	storage, err := session.NewStorage(state)
	if err != nil {
		return fmt.Errorf("failed to initialize storage: %w", err)
	}

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

	// Graceful stop (SIGTERM, then SIGKILL fallback) so the daemon persists the
	// autoyes progress it made while the TUI was closed instead of having it
	// thrown away by an immediate kill. terminateProcess blocks until the daemon
	// is gone, so the caller can safely load state afterward (see main.go).
	if err := terminateProcess(proc); err != nil {
		return fmt.Errorf("failed to stop daemon process: %w", err)
	}

	// Clean up PID file
	if err := os.Remove(pidFile); err != nil {
		return fmt.Errorf("failed to remove PID file: %w", err)
	}

	log.InfoLog.Printf("daemon process (PID: %d) stopped successfully", pid)
	return nil
}
