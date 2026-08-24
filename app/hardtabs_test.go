//go:build linux || solaris || aix || darwin || freebsd || dragonfly

// This file carries hardtabs.go's build tag rather than none, because its positive
// control is only true where the fix is. On NetBSD and OpenBSD suppressHardTabs is the
// no-op in hardtabs_other.go and bubbletea never enables hard tabs, so an untagged
// TestHardTabSuppressionIsLoadBearing would require a total that is structurally zero
// and fail there for no defect, telling its reader to re-sweep frameStates() over a
// platform that has nothing to sweep.

package app

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/creack/pty"
	"github.com/stretchr/testify/require"
)

// hardtabs_test.go is the no-raw-tab oracle (#796, #696), and it has to run the
// real renderer over a real pty because the frame it guards is not where the
// bytes come from. home.View().Content is tab-free at every size and in every
// state — measured — while the stream Bubble Tea writes to the terminal is not:
// ultraviolet moves the cursor forward with literal '\t' when hard tabs are
// enabled. A test reading View() would pass on both sides of the fix, which is
// exactly the shape of guard #696 was already stuck behind.
//
// tmux 3.6 is what turns that into a visible defect: it records the skipped
// cells as tab cells, so capture-pane and a user's copy-out return '\t' where
// the screen shows alignment, at a column the reader's tab stops decide rather
// than the one Atrium laid out.

// tabFrameModel replays one already-built frame. The subject is the renderer,
// not the model, so the frame is captured from a real home beforehand and fed
// in as a constant — which also keeps the pty program free of Atrium's timers,
// tmux calls and pollers.
type tabFrameModel struct{ frame string }

func (m tabFrameModel) Init() tea.Cmd { return nil }

func (m tabFrameModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if _, ok := msg.(tea.KeyPressMsg); ok {
		return m, tea.Quit
	}
	return m, nil
}

func (m tabFrameModel) View() tea.View { return tea.View{Content: m.frame} }

// renderThroughPTY runs frame through a real Bubble Tea program attached to a
// real pty and returns every byte the program wrote. suppress selects whether
// the PRODUCTION suppressHardTabs runs against the same tty Bubble Tea will
// read — calling the shipped function rather than restating it is what makes
// this a guard on the fix instead of a guard on a copy of it.
//
// The wait is on the bytes, not on a clock: the reader signals as soon as the
// program has written something, and only then is the quit key sent. A sleep
// long enough to be safe on a loaded CI runner would dominate the suite.
func renderThroughPTY(t *testing.T, frame string, w, h int, suppress bool) string {
	t.Helper()

	ptmx, tty, err := pty.Open()
	require.NoError(t, err, "opening a pty: this guard cannot run without one")
	defer func() { _ = ptmx.Close() }()
	require.NoError(t, pty.Setsize(tty, &pty.Winsize{Cols: uint16(w), Rows: uint16(h)}))

	// Not deferred: the restore has to run while tty is still open, and the explicit
	// Close below is what ends the reader. A defer here would fire after it, ioctl-ing
	// an fd this process has already released — and would exercise only the set half.
	restoreTabs := func() {}
	if suppress {
		restoreTabs = suppressHardTabs(tty)
	}

	var mu sync.Mutex
	var out strings.Builder
	drained := make(chan struct{})
	wrote := make(chan struct{})
	var once sync.Once
	go func() {
		defer close(drained)
		buf := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				mu.Lock()
				out.Write(buf[:n])
				mu.Unlock()
				once.Do(func() { close(wrote) })
			}
			if err != nil {
				return
			}
		}
	}()

	p := tea.NewProgram(tabFrameModel{frame},
		tea.WithInput(tty), tea.WithOutput(tty),
		tea.WithEnvironment(append(os.Environ(), "TERM=xterm-256color")))
	go func() {
		select {
		case <-wrote:
		case <-time.After(30 * time.Second):
		}
		p.Send(tea.KeyPressMsg{Code: 'q', Text: "q"})
	}()
	_, err = p.Run()
	require.NoError(t, err)

	restoreTabs()
	// Closing the slave is what ends the reader's Read loop; without it the
	// goroutine blocks and the returned bytes are whatever raced in.
	require.NoError(t, tty.Close())
	<-drained

	mu.Lock()
	defer mu.Unlock()
	return out.String()
}

// tabCase is one frame to render: a UI state from the shared frameStates table
// plus the geometry to render it at.
type tabCase struct {
	state string
	w, h  int
}

// rawTabCases are the (state, size) pairs measured to emit tab bytes at HEAD
// without the fix. They are not a sample of the UI — they are the reproducers,
// which is the only thing that makes a "no tabs" assertion mean anything.
//
// diffComment leads because it is the frame #696 reported: a styled patch with
// '--- ', '+++ ' and '@@' lines. It appears twice — at the 46x14 the report was
// taken at, which yields a single tab byte, and again at 64x18, where the same
// frame yields 28. Keeping both is deliberate: the narrow one is the case on
// record, and a case that reproduces by one byte is the first to fall silent.
//
// The rest are here because the defect was never diff-specific — the sweep that
// found them covered every state in frameStates() at five sizes — and a guard
// resting on one frame would go quiet the moment that frame's layout moved.
//
// TestHardTabSuppressionIsLoadBearing is what holds this table honest; see its
// comment for what silently rots without it.
var rawTabCases = []tabCase{
	{"diffComment", 46, 14},
	{"diffComment", 64, 18},
	{"checkpoints", 120, 40},
	{"customCommands", 100, 18},
	{"imagePreview", 80, 24},
	{"screensaver", 60, 14},
}

// frameForCase builds the case's frame through the same helper the parity
// goldens use, so the bytes under test are the bytes Atrium draws.
func frameForCase(t *testing.T, c tabCase) string {
	t.Helper()
	for _, fs := range frameStates() {
		if fs.name != c.state {
			continue
		}
		frame := newParityHome(t, fs, c.w, c.h).View().Content
		require.NotContains(t, frame, "\t",
			"%s: the frame itself must be tab-free — fit() expands tabs before styling, "+
				"so a tab here is a different defect from the one this file guards", c.state)
		return frame
	}
	t.Fatalf("no state named %q in frameStates()", c.state)
	return ""
}

// TestFrameEmitsNoRawTab is the guard: no frame Atrium renders may reach the
// terminal carrying a literal tab byte.
func TestFrameEmitsNoRawTab(t *testing.T) {
	for _, c := range rawTabCases {
		t.Run(fmt.Sprintf("%s_%dx%d", c.state, c.w, c.h), func(t *testing.T) {
			raw := renderThroughPTY(t, frameForCase(t, c), c.w, c.h, true)
			require.Equal(t, 0, strings.Count(raw, "\t"),
				"%s at %dx%d emitted %d literal tab byte(s); suppressHardTabs is the fix",
				c.state, c.w, c.h, strings.Count(raw, "\t"))
		})
	}
}

// TestHardTabSuppressionIsLoadBearing is the positive control, and without it
// the guard above is one layout change away from proving nothing: every case in
// rawTabCases was chosen because it reproduces, and a frame that stops
// reproducing keeps reporting green while covering nothing at all. So this
// renders the same table WITHOUT the fix and requires the defect to still be
// there.
//
// It asserts on the table, not on each row: which individual frame crosses a
// tab stop is a property of that frame's layout and moves with any UI change,
// while the class itself does not. A row going quiet is fine; the table going
// quiet means rawTabCases needs new reproducers, and that is what fails here.
func TestHardTabSuppressionIsLoadBearing(t *testing.T) {
	var total int
	for _, c := range rawTabCases {
		n := strings.Count(renderThroughPTY(t, frameForCase(t, c), c.w, c.h, false), "\t")
		t.Logf("%s %dx%d: %d tab byte(s) unsuppressed", c.state, c.w, c.h, n)
		total += n
	}
	require.Positive(t, total,
		"no case in rawTabCases reproduces the defect any more, so TestFrameEmitsNoRawTab "+
			"is vacuous: re-sweep frameStates() against a range of sizes and replace the table")
}
