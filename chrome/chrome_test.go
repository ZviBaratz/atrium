package chrome

import (
	"bytes"
	"strings"
	"testing"

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

func TestApply_EmitsExactBytes(t *testing.T) {
	var buf bytes.Buffer
	e := New(&buf, true)
	e.Apply(2, 5, false)
	want := ansi.SetWindowTitle("atrium · 2 need you · 5 running") + progressIndeterminate
	if buf.String() != want {
		t.Errorf("Apply emitted %q, want %q", buf.String(), want)
	}
}

// A steady fleet produces no per-tick output: a second identical Apply writes
// nothing (mutation guard for the change-detection cache in writeTitle/writeProg).
func TestApply_ChangeDetectionSuppressesRepeat(t *testing.T) {
	var buf bytes.Buffer
	e := New(&buf, true)
	e.Apply(2, 5, false)
	buf.Reset()
	e.Apply(2, 5, false)
	if buf.Len() != 0 {
		t.Errorf("repeat Apply with unchanged counts emitted %q, want nothing", buf.String())
	}
	// A real change does emit.
	e.Apply(3, 5, false)
	if buf.Len() == 0 {
		t.Error("Apply with changed counts must emit")
	}
}

func TestApply_ProgressStates(t *testing.T) {
	for _, tc := range []struct {
		running int
		errored bool
		want    string
	}{
		{5, false, progressIndeterminate}, // working → busy
		{0, false, progressReset},         // idle → clear
		{5, true, progressError},          // death this tick → error, even while running
		{0, true, progressError},          // error wins over idle
	} {
		if got := progressSeq(tc.running, tc.errored); got != tc.want {
			t.Errorf("progressSeq(%d,%v) = %q, want %q", tc.running, tc.errored, got, tc.want)
		}
	}
}

func TestApply_DisabledWritesNothing(t *testing.T) {
	var buf bytes.Buffer
	e := New(&buf, false)
	e.Apply(2, 5, false)
	e.Reset()
	if buf.Len() != 0 {
		t.Errorf("disabled emitter wrote %q, want nothing", buf.String())
	}
}

func TestReset_ClearsTitleAndProgress(t *testing.T) {
	var buf bytes.Buffer
	e := New(&buf, true)
	e.Apply(2, 5, false)
	buf.Reset()
	e.Reset()
	want := ansi.SetWindowTitle("") + progressReset
	if buf.String() != want {
		t.Errorf("Reset emitted %q, want %q", buf.String(), want)
	}
}

// Turning the switch off clears any painted chrome so nothing stale lingers.
func TestSetEnabled_OffResets(t *testing.T) {
	var buf bytes.Buffer
	e := New(&buf, true)
	e.Apply(2, 5, false)
	buf.Reset()
	e.SetEnabled(false)
	if !strings.Contains(buf.String(), progressReset) {
		t.Errorf("SetEnabled(false) must clear the chrome, emitted %q", buf.String())
	}
	// And once off, Apply is silent.
	buf.Reset()
	e.Apply(9, 9, false)
	if buf.Len() != 0 {
		t.Errorf("disabled Apply wrote %q, want nothing", buf.String())
	}
}
