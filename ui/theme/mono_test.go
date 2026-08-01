package theme

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// NoColorRequested implements no-color.org literally: colour is off when the
// variable is PRESENT AND NON-EMPTY, regardless of value. That is deliberately
// stricter than the dependency's own handling — colorprofile parses NO_COLOR with
// strconv.ParseBool, so NO_COLOR=yes, =x, =0 and =2 all leave colour ON, which is
// four spec violations Atrium would inherit by doing nothing.
//
// Re-measured against colorprofile v0.4.3 (the version in go.mod) at Stage D:
// colorprofile.Env returns Ascii only for 1/true/TRUE, and TrueColor for
// 0/false/yes/x/2/off.
func TestNoColorRequested(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  []string
		want bool
	}{
		{"absent", []string{"TERM=xterm-256color"}, false},
		{"empty is not a request", []string{"NO_COLOR="}, false},
		{"one", []string{"NO_COLOR=1"}, true},
		{"true", []string{"NO_COLOR=true"}, true},
		// The four the dependency gets wrong. Each of these is a spec-mandated
		// "off" that colorprofile.Env leaves at TrueColor.
		{"zero is still a request", []string{"NO_COLOR=0"}, true},
		{"false is still a request", []string{"NO_COLOR=false"}, true},
		{"yes", []string{"NO_COLOR=yes"}, true},
		{"arbitrary", []string{"NO_COLOR=x"}, true},
		// Not a prefix or suffix match: only the exact name counts.
		{"a different variable", []string{"NO_COLOR_EVER=1"}, false},
		{"later entry wins, as os.Environ semantics", []string{"NO_COLOR=1", "NO_COLOR="}, false},
		{"later entry wins, the other way", []string{"NO_COLOR=", "NO_COLOR=yes"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, NoColorRequested(tc.env))
		})
	}
}

// Mono is a global that renderers read, so it must be restorable the way Set and
// SetGlyphSet are — otherwise one test leaves every later one monochrome, which
// under -shuffle is a different suite every run.
func TestSetMonoRestores(t *testing.T) {
	require.False(t, Mono(), "colour is on by default")
	restore := SetMono(true)
	require.True(t, Mono())
	restore()
	require.False(t, Mono())
}

// Mono() is read OFF the bubbletea loop: barStyleColours reaches it from the
// tea.Cmd that restyles the fleet after a theme change, and app_layout.go's
// barStyleApplier requires every global that Cmd touches be safe there.
//
// This is the guard for that, and it only means anything under -race (CI runs the
// race detector as its own job; `just test-race` locally). Swap mono back to a
// plain bool and this goes red with a WRITE/READ data race on ui/theme/mono.go —
// which is the whole point, because with a plain bool every OTHER test in the
// package still passes, including TestSetMonoRestores.
func TestMonoIsSafeToReadOffTheUpdateThread(t *testing.T) {
	defer SetMono(false)()

	const reads = 1000
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range reads {
			_ = Mono()
		}
	}()
	for range reads {
		SetMono(true)
		SetMono(false)
	}
	<-done
}
