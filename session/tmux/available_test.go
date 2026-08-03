package tmux

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
)

func TestAvailable(t *testing.T) {
	orig := lookPath
	t.Cleanup(func() { lookPath = orig })

	// Pin the version verdict to "no answer" rather than inheriting whatever the host's
	// tmux is. Init probes the real binary (barstyle_test calls Init), so without this
	// the "present tmux returns nil" case below would depend on the developer's tmux
	// version and on test order — and would fail outright on any host whose tmux is
	// below MinVersion, which a distro-packaged 3.1c still is.
	resetVersionVerdict(t)

	t.Run("missing tmux returns ErrNotInstalled", func(t *testing.T) {
		lookPath = func(string) (string, error) { return "", exec.ErrNotFound }
		if err := Available(); !errors.Is(err, ErrNotInstalled) {
			t.Fatalf("Available() = %v, want ErrNotInstalled", err)
		}
	})

	t.Run("present tmux returns nil", func(t *testing.T) {
		lookPath = func(string) (string, error) { return "/usr/bin/tmux", nil }
		if err := Available(); err != nil {
			t.Fatalf("Available() = %v, want nil", err)
		}
	})
}

// Available must refuse a tmux too old for `new-session -e`, and must keep failing open
// on everything short of a confident below-floor verdict.
func TestAvailableVersionGate(t *testing.T) {
	origLookPath := lookPath
	t.Cleanup(func() { lookPath = origLookPath })
	lookPath = func(string) (string, error) { return "/usr/bin/tmux", nil }

	t.Run("a tmux below the floor returns ErrTooOld", func(t *testing.T) {
		resetVersionVerdict(t)
		versionProbe = func(context.Context) (string, error) { return "tmux 1.8\n", nil }
		probeVersionOnce()

		err := Available()
		if !errors.Is(err, ErrTooOld) {
			t.Fatalf("Available() = %v, want ErrTooOld", err)
		}
		// The message has to carry both numbers, or the user learns nothing actionable.
		for _, want := range []string{"1.8", MinVersion} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("Available() message %q does not name %q", err, want)
			}
		}
	})

	t.Run("a tmux at the floor returns nil", func(t *testing.T) {
		resetVersionVerdict(t)
		versionProbe = func(context.Context) (string, error) { return "tmux 3.2a\n", nil }
		probeVersionOnce()
		if err := Available(); err != nil {
			t.Fatalf("Available() = %v, want nil", err)
		}
	})

	t.Run("an unprobed tmux returns nil", func(t *testing.T) {
		// The zero value of the verdict is "no answer", which must not refuse: the daemon
		// and any caller that never ran Init land here.
		resetVersionVerdict(t)
		if err := Available(); err != nil {
			t.Fatalf("Available() = %v, want nil when no probe has run", err)
		}
	})
}

// A missing tmux must short-circuit: ErrNotInstalled names the actionable problem, and
// pairing it with a version complaint would be noise. Also pins that Available itself
// never execs — the verdict is read, not probed, because both callers sit on the Bubble
// Tea update thread.
func TestAvailableDoesNotProbeWhenTmuxIsMissing(t *testing.T) {
	origLookPath := lookPath
	t.Cleanup(func() { lookPath = origLookPath })
	lookPath = func(string) (string, error) { return "", exec.ErrNotFound }

	resetVersionVerdict(t)
	calls := 0
	versionProbe = func(context.Context) (string, error) {
		calls++
		return "tmux 1.8\n", nil
	}

	if err := Available(); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("Available() = %v, want ErrNotInstalled", err)
	}
	if calls != 0 {
		t.Fatalf("Available() ran the version probe %d times; it must never exec", calls)
	}
}
