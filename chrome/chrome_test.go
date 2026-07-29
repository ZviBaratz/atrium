package chrome

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestTitle(t *testing.T) {
	for _, tc := range []struct {
		needYou, running int
		want             string
	}{
		{0, 0, "atrium"},
		{2, 0, "atrium · 2 need you"},
		{0, 5, "atrium · 5 running"},
		{2, 5, "atrium · 2 need you · 5 running"},
		{1, 1, "atrium · 1 need you · 1 running"},
	} {
		if got := Title(tc.needYou, tc.running); got != tc.want {
			t.Errorf("Title(%d,%d) = %q, want %q", tc.needYou, tc.running, got, tc.want)
		}
	}
}

// A zero segment is omitted, never rendered as "0 running" (mutation guard for the
// omit-zero condition in Title).
func TestTitle_OmitsZeroSegments(t *testing.T) {
	if got := Title(0, 5); strings.Contains(got, "0 need you") {
		t.Errorf("Title(0,5) = %q, must omit the zero need-you segment", got)
	}
	if got := Title(2, 0); strings.Contains(got, "0 running") {
		t.Errorf("Title(2,0) = %q, must omit the zero running segment", got)
	}
}

// The precedence Progress documents: an error this tick outranks a working session,
// which outranks idle. Both "error wins" cases are covered, since a swap of the
// switch arms would still satisfy the other three rows.
func TestProgress_States(t *testing.T) {
	for _, tc := range []struct {
		name    string
		running int
		errored bool
		want    tea.ProgressBarState
	}{
		{"idle", 0, false, tea.ProgressBarNone},
		{"working", 3, false, tea.ProgressBarIndeterminate},
		{"error wins over running", 3, true, tea.ProgressBarError},
		{"error wins over idle", 0, true, tea.ProgressBarError},
	} {
		if got := Progress(tc.running, tc.errored); got != tc.want {
			t.Errorf("%s: Progress(%d,%v) = %v, want %v", tc.name, tc.running, tc.errored, got, tc.want)
		}
	}
}

// The wire contract this package's doc promises. Bubble Tea owns the mapping now,
// so this pins that the states chosen above still resolve to the OSC 9;4 sequences
// the terminals in that doc implement — a Bubble Tea or x/ansi bump that changed
// them (or a state renumbered under us) fails here rather than silently in a
// terminal nobody is watching. Reproduces cursed_renderer.go's setProgressBar arms.
func TestProgress_StatesMapToOSC94Sequences(t *testing.T) {
	for _, tc := range []struct {
		state tea.ProgressBarState
		want  string
	}{
		{tea.ProgressBarNone, "\x1b]9;4;0\x07"},
		{tea.ProgressBarIndeterminate, "\x1b]9;4;3\x07"},
		{tea.ProgressBarError, "\x1b]9;4;2;0\x07"},
	} {
		var got string
		switch tc.state {
		case tea.ProgressBarNone:
			got = ansi.ResetProgressBar
		case tea.ProgressBarIndeterminate:
			got = ansi.SetIndeterminateProgressBar
		case tea.ProgressBarError:
			got = ansi.SetErrorProgressBar(0)
		case tea.ProgressBarDefault, tea.ProgressBarWarning:
			t.Fatalf("Progress never returns %v", tc.state)
		}
		if got != tc.want {
			t.Errorf("%v emits %q, want %q", tc.state, got, tc.want)
		}
	}
}
