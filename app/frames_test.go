package app

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/cmd/cmd_test"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/tmux"
	"github.com/ZviBaratz/atrium/ui"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"
)

// frameSpy is a cmd.Executor that counts what the app runs and can hold a chosen
// subcommand open. Counting is what makes the anti-jank guard crisp: a wall-clock
// assertion would be flaky in CI, but "how many subprocesses did Update launch"
// is exact, and the executor seam is the only door tmux work goes through.
type frameSpy struct {
	mu      sync.Mutex
	calls   []string
	content string

	// blockOn holds any call whose subcommand matches until release is closed, so
	// a test can put a capture "in flight" deterministically.
	blockOn string
	release chan struct{}

	// live names the sessions new-session has created, so has-session answers the
	// way a real server does: absent before the launch, present after. A fake that
	// always reports "alive" makes Start refuse its own session.
	live map[string]bool
}

func newFrameSpy(content string) *frameSpy {
	return &frameSpy{content: content, release: make(chan struct{}), live: map[string]bool{}}
}

func (s *frameSpy) record(cmd *exec.Cmd) string {
	verb := tmuxVerb(cmd)
	s.mu.Lock()
	s.calls = append(s.calls, verb)
	s.mu.Unlock()
	if s.blockOn != "" && verb == s.blockOn {
		<-s.release
	}
	return verb
}

// sessionExists reports whether a has-session argv names a session new-session
// already created in this fake.
func (s *frameSpy) sessionExists(cmd *exec.Cmd) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for name := range s.live {
		if strings.Contains(strings.Join(cmd.Args, " "), name) {
			return true
		}
	}
	return false
}

func (s *frameSpy) noteCreated(cmd *exec.Cmd) {
	args := cmd.Args
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, a := range args {
		if a == "-s" && i+1 < len(args) {
			s.live[args[i+1]] = true
		}
	}
}

func (s *frameSpy) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

func (s *frameSpy) seen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

func (s *frameSpy) exec() cmd_test.MockCmdExec {
	return cmd_test.MockCmdExec{
		RunFunc: func(cmd *exec.Cmd) error {
			switch s.record(cmd) {
			case "has-session":
				if !s.sessionExists(cmd) {
					return fmt.Errorf("session not found")
				}
			case "new-session":
				s.noteCreated(cmd)
			}
			return nil
		},
		OutputFunc: func(cmd *exec.Cmd) ([]byte, error) {
			switch s.record(cmd) {
			case "capture-pane":
				return []byte(s.content), nil
			default:
				// Any pane-id probe answers with one pane, so paneTarget resolves.
				return []byte("%7\n"), nil
			}
		},
	}
}

// tmuxVerb is the tmux subcommand of an argv, skipping the binary and tmux's own
// -L/-f prelude flags.
func tmuxVerb(cmd *exec.Cmd) string {
	for i := 1; i < len(cmd.Args); i++ {
		a := cmd.Args[i]
		if strings.HasPrefix(a, "-") {
			i++ // skip the flag's value
			continue
		}
		return a
	}
	return ""
}

type framePtyFactory struct {
	t    *testing.T
	exec cmd_test.MockCmdExec
	n    int
}

func (p *framePtyFactory) Start(cmd *exec.Cmd) (*os.File, error) {
	p.n++
	f, err := os.OpenFile(filepath.Join(p.t.TempDir(), fmt.Sprintf("pty-%d", p.n)), os.O_CREATE|os.O_RDWR, 0o644)
	if err == nil {
		_ = p.exec.Run(cmd)
	}
	return f, err
}

func (p *framePtyFactory) Close() {}

// newFrameHome builds a home with one started, selected instance whose tmux
// session runs on spy, sized and parked in the default state on the preview tab.
func newFrameHome(t *testing.T, spy *frameSpy) (*home, *session.Instance) {
	t.Helper()
	h := newCreateFormHome(t)
	h.spinner = spinner.New()
	h.lostStrikes = map[*session.Instance]int{}

	inst, err := session.NewInstance(session.InstanceOptions{
		Title: "framed", Path: t.TempDir(), Program: "claude", Direct: true,
	})
	require.NoError(t, err)
	inst.SetTmuxSession(tmux.NewSessionWithDeps(
		h.ctx, "framed", "claude", &framePtyFactory{t: t, exec: spy.exec()}, spy.exec()))
	require.NoError(t, inst.Start(true))

	h.list.AddInstance(inst)()
	h.list.SetSelectedInstance(0)
	h.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	// The first size opens the first-run welcome modal, which swallows keys; these
	// tests are about the default view, so park there like newSmokeHome does.
	h.state = stateDefault
	return h, inst
}

// TestPreviewTick_LaunchesNoSubprocessOnTheUpdateThread is the anti-jank guard.
//
// Update runs on Bubble Tea's single event-loop goroutine: anything it does
// synchronously is time the app cannot spend handling a keypress or painting a
// frame. The preview tick used to run has-session + capture-pane there, so every
// repaint waited on a shared, single-threaded tmux server — and instanceChanged
// runs from ~60 key handlers besides, making it a latency floor under every
// keystroke. Counting executor calls across Update is exact where timing would be
// flaky: returned Cmds are never run here, so anything counted was inline.
func TestPreviewTick_LaunchesNoSubprocessOnTheUpdateThread(t *testing.T) {
	spy := newFrameSpy("agent output")
	h, _ := newFrameHome(t, spy)

	before := spy.count()
	h.Update(previewTickMsg{})
	require.Equal(t, before, spy.count(),
		"the preview tick must not shell out on the update thread; saw %v", spy.seen()[before:])

	// A keypress takes the same path through instanceChanged.
	before = spy.count()
	h.Update(tea.KeyMsg{Type: tea.KeyDown})
	require.Equal(t, before, spy.count(),
		"a keypress must not shell out either; saw %v", spy.seen()[before:])
}

// TestPaneFrameCmd_CapturesOffThreadAndPaints is the companion that stops the
// guard above from being satisfiable by simply not capturing at all: the work
// must still happen, just somewhere else.
func TestPaneFrameCmd_CapturesOffThreadAndPaints(t *testing.T) {
	spy := newFrameSpy("agent is working")
	h, inst := newFrameHome(t, spy)

	cmd := h.armFrameCapture(0)
	require.NotNil(t, cmd, "the chain must arm a capture for a live selected session")

	msg := cmd()
	frame, ok := msg.(paneFrameMsg)
	require.True(t, ok, "the capture Cmd must return a paneFrameMsg, got %T", msg)
	require.NoError(t, frame.err)
	require.Equal(t, inst, frame.target.instance, "the frame must name the instance it was captured for")
	require.Contains(t, spy.seen(), "capture-pane", "the capture must actually reach tmux")

	h.Update(frame)

	text, _, captured := inst.PaneFrame()
	require.True(t, captured)
	require.Contains(t, text, "agent is working")
	require.Contains(t, h.tabbedWindow.String(), "agent is working", "the applied frame must reach the pane")
}

// TestPaneFrame_NeverPaintsOneSessionsFrameIntoAnother guards the identity rule.
// A capture takes long enough for the selection to move under it, and the frame
// is stored against the instance it names — never against whatever is selected
// when it lands.
func TestPaneFrame_NeverPaintsOneSessionsFrameIntoAnother(t *testing.T) {
	spy := newFrameSpy("session A output")
	h, instA := newFrameHome(t, spy)

	instB, err := session.NewInstance(session.InstanceOptions{
		Title: "other", Path: t.TempDir(), Program: "claude", Direct: true,
	})
	require.NoError(t, err)
	h.list.AddInstance(instB)()

	cmd := h.armFrameCapture(0)
	frame := cmd().(paneFrameMsg)

	// The user moves to B before A's frame lands.
	h.list.SetSelectedInstance(1)
	h.Update(frame)

	textA, _, capturedA := instA.PaneFrame()
	require.True(t, capturedA)
	require.Contains(t, textA, "session A output")
	_, _, capturedB := instB.PaneFrame()
	require.False(t, capturedB, "B must not inherit A's frame just by being selected when it landed")
}

// TestUpdateLoopSurvivesAWedgedCapture is the "the interface never blocks"
// acceptance test. With a capture parked on an unresponsive tmux server, the
// update loop must keep handling input and rendering. Before this change the
// same scenario blocked Update itself for the full tmux operation timeout.
//
// It is channel-gated rather than timed, so there is no wall clock to be flaky
// about. Its honest failure mode: against the old inline capture, the first
// Update below would never return and the test would die on go test's timeout
// rather than on an assertion.
func TestUpdateLoopSurvivesAWedgedCapture(t *testing.T) {
	spy := newFrameSpy("agent output")
	spy.blockOn = "capture-pane"
	h, _ := newFrameHome(t, spy)
	defer close(spy.release)

	// Park a capture in a goroutine: it hangs inside the executor, exactly as it
	// would against a tmux server that has stopped answering.
	captured := make(chan tea.Msg, 1)
	cmd := h.armFrameCapture(0)
	require.NotNil(t, cmd)
	go func() { captured <- cmd() }()

	// The update thread stays live: keys route, tabs switch, the frame renders.
	h.Update(previewTickMsg{})
	h.Update(tea.KeyMsg{Type: tea.KeyTab})
	require.Equal(t, ui.DiffTab, h.tabbedWindow.GetActiveTab(), "input must still be handled")
	require.NotEmpty(t, h.View(), "the app must still render while a capture is wedged")

	select {
	case <-captured:
		t.Fatal("the capture must still be blocked — the test proves nothing otherwise")
	default:
	}
}

// TestPaneFrameFailures_NeverReachTheErrorBox guards the flood this change ends.
//
// A capture error used to return from UpdateContent into instanceChanged's
// handleError, which flashes the error row — ten times a second for as long as the
// failure lasted, while the pane it was complaining about kept rendering its last
// frame perfectly well. The failure is now recorded on the instance and reported by
// the pane's own staleness marker instead.
func TestPaneFrameFailures_NeverReachTheErrorBox(t *testing.T) {
	spy := newFrameSpy("agent output")
	h, inst := newFrameHome(t, spy)

	for range 20 {
		h.Update(paneFrameMsg{
			target: frameTarget{instance: inst},
			err:    fmt.Errorf("error capturing pane content: tmux server gone"),
			at:     time.Now(),
		})
	}

	require.False(t, h.errBox.HasError(), "a failing capture must not raise the error box")
	require.False(t, h.menu.HasNotice(), "nor flash a notice on the hint row")
}

// TestStaleMarker_SurvivesThePreviewTick pins how the marker's two clocks
// interact, which only an app-level test can see: the pane's own tests never run
// instanceChanged, and that is the caller that stamps the target clock.
//
// The rule is that the 10×/s tick refreshes the FRAME stamp and must never touch
// the TARGET stamp. If it did, every tick would restart the suppression window a
// target change opens and the marker could never appear at all — the pane would
// go silently stale, which is the one outcome this feature exists to prevent.
func TestStaleMarker_SurvivesThePreviewTick(t *testing.T) {
	spy := newFrameSpy("agent output line")
	h, inst := newFrameHome(t, spy)

	h.Update(paneFrameMsg{
		target: frameTarget{instance: inst},
		text:   "agent output line",
		at:     time.Now().Add(-5 * time.Second),
	})
	require.Contains(t, h.tabbedWindow.String(), "stale", "an overdue frame is marked when it lands")

	// The first tick adopts the selection, which legitimately restarts the clock —
	// a target the pane has just been pointed at is new, not stale.
	h.Update(previewTickMsg{})
	require.NotContains(t, h.tabbedWindow.String(), "stale", "a just-adopted target is not stale")

	// Age that adoption past the threshold and keep ticking. This is where the bug
	// lived: the per-tick frame stamp and the target-change restamp shared one
	// field, so the tick that runs 10×/s erased the marker every time — every pane
	// test passed while the running app never showed it once.
	h.tabbedWindow.NoteFrameTargetChange(time.Now().Add(-5 * time.Second))
	for range 5 {
		h.Update(previewTickMsg{})
	}
	require.Contains(t, h.tabbedWindow.String(), "stale", "the marker must survive steady-state ticks")
}

// TestFrameChain_NeverForksAndNeverDies pins the two failure modes of a
// self-chaining loop: a second arm while one is in flight would double the tmux
// load every tick, and an arm that returns no Cmd would leave the pane frozen
// forever with no way back.
func TestFrameChain_NeverForksAndNeverDies(t *testing.T) {
	spy := newFrameSpy("agent output")
	h, _ := newFrameHome(t, spy)

	first := h.armFrameCapture(0)
	require.NotNil(t, first)
	require.Nil(t, h.armFrameCapture(0), "a second arm while one is in flight must be a no-op")

	_, next := h.Update(first().(paneFrameMsg))
	require.NotNil(t, next, "handling a frame must re-arm the chain, or the pane freezes forever")
	require.True(t, h.frameInFlight, "the re-arm claims the slot again — exactly one capture is ever in flight")

	// Even with nothing to capture, the chain must keep ticking — otherwise a
	// session paused while selected would freeze the loop and the preview would
	// never come back when it resumes.
	paused := newFrameSpy("unused")
	ph, pinst := newFrameHome(t, paused)
	pinst.SetStatus(session.Paused)

	cmd := ph.armFrameCapture(0)
	require.NotNil(t, cmd, "the chain must arm even when there is nothing to capture")
	msg, ok := cmd().(paneFrameMsg)
	require.True(t, ok)
	require.True(t, msg.target.empty(), "a paused selection must resolve to a no-I/O target")
	require.NotContains(t, paused.seen()[len(paused.seen())-1:], "capture-pane",
		"an empty target must not reach tmux at all")

	_, again := ph.Update(msg)
	require.NotNil(t, again, "and the chain must still re-arm from that empty round")
}

// TestFrameChain_ReArmsImmediatelyWhenTheTargetMoved: a frame that lands for a
// session the user already left is stored (switching back is then instant) but
// leaves the new selection without one, so the next capture must not sleep out
// another interval first.
func TestFrameChain_ReArmsImmediatelyWhenTheTargetMoved(t *testing.T) {
	spy := newFrameSpy("agent output")
	h, instA := newFrameHome(t, spy)

	instB, err := session.NewInstance(session.InstanceOptions{
		Title: "other", Path: t.TempDir(), Program: "claude", Direct: true,
	})
	require.NoError(t, err)
	instB.SetTmuxSession(tmux.NewSessionWithDeps(
		h.ctx, "other", "claude", &framePtyFactory{t: t, exec: spy.exec()}, spy.exec()))
	require.NoError(t, instB.Start(true))
	h.list.AddInstance(instB)()

	stale := paneFrameMsg{target: frameTarget{instance: instA}, text: "old", at: time.Now()}
	h.list.SetSelectedInstance(1)

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, cmd := h.Update(stale)
		require.NotNil(t, cmd)
		cmd() // the re-armed capture: with a delay it would sit here for the interval
	}()

	select {
	case <-done:
	case <-time.After(paneFrameInterval):
		t.Fatal("a capture for a moved-on target must re-arm without waiting out an interval")
	}
}
