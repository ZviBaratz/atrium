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
	"github.com/ZviBaratz/atrium/internal/testutil"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/tmux"
	"github.com/ZviBaratz/atrium/ui"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
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

// newCaptureHome builds a home with one started, selected instance whose tmux
// session runs on spy, sized and parked in the default state on the preview tab.
//
// Named for the capture chain rather than "frames": app/frame_restore_test.go owns
// newFrameHome, where "frame" means the rendered layout, not a pane capture.
func newCaptureHome(t *testing.T, spy *frameSpy) (*home, *session.Instance) {
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
	h, _ := newCaptureHome(t, spy)

	before := spy.count()
	h.Update(previewTickMsg{})
	require.Equal(t, before, spy.count(),
		"the preview tick must not shell out on the update thread; saw %v", spy.seen()[before:])

	// A keypress takes the same path through instanceChanged.
	before = spy.count()
	h.Update(keyMsg("down"))
	require.Equal(t, before, spy.count(),
		"a keypress must not shell out either; saw %v", spy.seen()[before:])
}

// TestPaneFrameCmd_CapturesOffThreadAndPaints is the companion that stops the
// guard above from being satisfiable by simply not capturing at all: the work
// must still happen, just somewhere else.
func TestPaneFrameCmd_CapturesOffThreadAndPaints(t *testing.T) {
	spy := newFrameSpy("agent is working")
	h, inst := newCaptureHome(t, spy)

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
	h, instA := newCaptureHome(t, spy)

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
	h, _ := newCaptureHome(t, spy)
	defer close(spy.release)

	// Park a capture in a goroutine: it hangs inside the executor, exactly as it
	// would against a tmux server that has stopped answering.
	captured := make(chan tea.Msg, 1)
	cmd := h.armFrameCapture(0)
	require.NotNil(t, cmd)
	go func() { captured <- cmd() }()

	// The update thread stays live: keys route, tabs switch, the frame renders.
	h.Update(previewTickMsg{})
	h.Update(keyMsg("tab"))
	require.Equal(t, ui.DiffTab, h.tabbedWindow.GetActiveTab(), "input must still be handled")
	require.NotEmpty(t, h.View().Content, "the app must still render while a capture is wedged")

	select {
	case <-captured:
		t.Fatal("the capture must still be blocked — the test proves nothing otherwise")
	default:
	}
}

// TestTerminalTab_CreatesItsShellOffTheUpdateThread extends the anti-jank guard to
// the terminal tab, whose cold path was the worst of the three: opening the tab on
// a session with no shell yet ran `tmux new-session` inline inside Update, while
// holding the very mutex String() takes to render.
//
// It cannot use the executor counter the preview guards use. The pane builds its
// shell sessions with tmux.NewSessionWithName, which bakes in the real executor, so
// a test spy attached to the *instance* never sees them. What is observable — and
// what the old code did on the update thread — is the sessions map gaining an
// entry, so that is what this asserts: nothing appears during Update, and the entry
// shows up only once the returned Cmd runs.
func TestTerminalTab_CreatesItsShellOffTheUpdateThread(t *testing.T) {
	testutil.RequireTmux(t)
	spy := newFrameSpy("shell prompt $")
	h, inst := newCaptureHome(t, spy)
	h.tabbedWindow.SetActiveTab(ui.TerminalTab)
	t.Cleanup(h.tabbedWindow.CloseTerminal)

	require.False(t, h.tabbedWindow.HasTerminalSession(inst),
		"precondition: no shell session for this instance yet")

	// Update must return without creating anything...
	_, cmd := h.Update(paneFrameMsg{at: time.Now()})
	require.False(t, h.tabbedWindow.HasTerminalSession(inst),
		"opening the terminal tab must not create a tmux session on the update thread")

	// ...and the creation must happen in the Cmd it returned.
	require.NotNil(t, cmd)
	cmd()
	require.True(t, h.tabbedWindow.HasTerminalSession(inst),
		"the shell must be created by the background capture instead")
}

// TestTickPaths_LaunchNoSubprocessOnTheUpdateThread is the standing invariant for
// #380: no timer-driven message may shell out on the Bubble Tea event-loop
// goroutine, in its handler or in the View that follows it.
//
// What this test can and cannot see, stated plainly so it is not mistaken for more
// than it is:
//
//   - It observes tmux ONLY. session/git builds exec.CommandContext directly rather
//     than going through cmd.Executor (see session/git/cmdrec.go), so a git call added
//     to a tick path would be invisible here.
//   - It observes the SELECTED INSTANCE's tmux only. The terminal pane builds its
//     shell sessions with the real executor, so work done against a shell is invisible
//     to this spy — TestTerminalTab_CreatesItsShellOffTheUpdateThread covers that path
//     by observing the sessions map instead.
//   - It covers timer-driven messages only. Key paths legitimately still shell out —
//     attach, deep rename, and AcceptSuggestion's capture wait-loop — and are the
//     subject of the named-loading leg of #380, not this one.
//   - tea.WindowSizeMsg is excluded: SetSessionPreviewSize genuinely fires one
//     resize-window per instance on the update thread. That is a real main-thread
//     burst, but resize-triggered rather than tick-triggered, and out of scope here.
//   - Scroll mode is excluded: UpdateContent's lazy viewport refill still captures
//     synchronously on the rare path where the viewport was cleared.
//
// A new timer-driven message must be added to this table, or the loop quietly stops
// being covered.
func TestTickPaths_LaunchNoSubprocessOnTheUpdateThread(t *testing.T) {
	// Messages are built against the live instance, the way the loop emits them: a
	// zero-valued one would test a shape production never produces.
	cases := []struct {
		name string
		msg  func(*session.Instance) tea.Msg
	}{
		{"preview tick", func(*session.Instance) tea.Msg { return previewTickMsg{} }},
		{"splash tick", func(*session.Instance) tea.Msg { return splashTickMsg{} }},
		{"pane frame", func(i *session.Instance) tea.Msg {
			return paneFrameMsg{target: frameTarget{instance: i}, text: "frame", at: time.Now()}
		}},
		{"metadata done", func(i *session.Instance) tea.Msg {
			return metadataUpdateDoneMsg{results: []instanceMetaResult{{instance: i, state: tmux.PaneIdle}}}
		}},
		{"metadata sweep", func(i *session.Instance) tea.Msg {
			return metadataSweepDoneMsg{results: []instanceMetaResult{{instance: i, state: tmux.PaneIdle}}}
		}},
		{"instance polled", func(i *session.Instance) tea.Msg {
			return instancePolledMsg{instance: i, state: tmux.PaneIdle}
		}},
		{"context push failed", func(i *session.Instance) tea.Msg {
			return contextPushFailedMsg{instances: []*session.Instance{i}}
		}},
		{"spinner tick", func(*session.Instance) tea.Msg { return spinner.TickMsg{} }},
	}
	for _, tab := range []struct {
		name string
		idx  int
	}{{"preview", ui.PreviewTab}, {"diff", ui.DiffTab}, {"terminal", ui.TerminalTab}} {
		for _, c := range cases {
			t.Run(tab.name+"/"+c.name, func(t *testing.T) {
				spy := newFrameSpy("agent output")
				h, inst := newCaptureHome(t, spy)
				h.tabbedWindow.SetActiveTab(tab.idx)
				h.appConfig.SessionContextBar = boolPtr(true)

				before := spy.count()
				h.Update(c.msg(inst))
				_ = h.View()
				require.Equal(t, before, spy.count(),
					"%s on the %s tab shelled out on the update thread: %v",
					c.name, tab.name, spy.seen()[before:])
			})
		}
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
	h, inst := newCaptureHome(t, spy)

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
	h, inst := newCaptureHome(t, spy)

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

// TestMetadataPoll_WarmsTheFrameCacheForFree is the harvest guarantee.
//
// Poll captures the pane to classify it and used to discard the bytes. Keeping
// them means a session the user has never selected still has a frame to paint the
// moment they do — instead of the setup splash — and the assertion that makes it
// meaningful is the *count*: the sweep must not fork a second capture-pane to do it.
func TestMetadataPoll_WarmsTheFrameCacheForFree(t *testing.T) {
	spy := newFrameSpy("background session output")
	h, inst := newCaptureHome(t, spy)

	_, _, captured := inst.PaneFrame()
	require.False(t, captured, "precondition: no frame yet")

	before := countVerb(spy.seen(), "capture-pane")
	results := collectMetadata(h.ctx, []*session.Instance{inst}, nil, false, h.usagePolicy(), h.diffContentFloor())
	afterPoll := countVerb(spy.seen(), "capture-pane")
	h.applyMetadataResults(results, false)

	text, _, captured := inst.PaneFrame()
	require.True(t, captured, "the sweep must leave the session with a frame")
	require.Contains(t, text, "background session output")
	require.Equal(t, 1, afterPoll-before,
		"the sweep must reuse Poll's own capture, not fork a second one; saw %v", spy.seen()[before:])
}

// TestHarvestedFrame_NeverRewindsAFresherFrame: the 100ms chain and the 500ms
// sweep write the same cache, and the chain's frames are newer for the watched
// session. An unconditional harvest would rewind the visible pane by up to half a
// second, ten times a second.
func TestHarvestedFrame_NeverRewindsAFresherFrame(t *testing.T) {
	spy := newFrameSpy("older")
	_, inst := newCaptureHome(t, spy)

	now := time.Now()
	inst.SetPaneFrame("newer frame from the capture chain", now)

	applyHarvestedFrame(instanceMetaResult{
		instance:    inst,
		paneFrame:   "older frame from the sweep",
		paneFrameAt: now.Add(-500 * time.Millisecond),
		paneFrameOK: true,
	})

	text, at, _ := inst.PaneFrame()
	require.Equal(t, "newer frame from the capture chain", text, "an older harvest must not rewind the pane")
	require.Equal(t, now, at)
}

// TestMetadataApply_LaunchesNoSubprocessOnTheUpdateThread covers the offender the
// issue did not name and that turned out to be larger than the one it did.
//
// applyMetadataResults runs on the update thread and called pushSessionContexts,
// which fired inst.TmuxAlive() — a has-session subprocess — for EVERY instance,
// twice a second, plus a set-option batch for each changed bar. Liveness now comes
// from the verdict the poll already reached, and the writes move into a Cmd.
func TestMetadataApply_LaunchesNoSubprocessOnTheUpdateThread(t *testing.T) {
	spy := newFrameSpy("agent output")
	h, inst := newCaptureHome(t, spy)
	h.appConfig.SessionContextBar = boolPtr(true)

	results := collectMetadata(h.ctx, []*session.Instance{inst}, inst, false, h.usagePolicy(), h.diffContentFloor())

	before := spy.count()
	cmds := h.applyMetadataResults(results, false)
	require.Equal(t, before, spy.count(),
		"applying metadata must not shell out on the update thread; saw %v", spy.seen()[before:])

	// And the work must still happen — in the returned Cmd.
	require.NotEmpty(t, cmds, "a changed context bar must return a push Cmd")
	for _, c := range cmds {
		if c != nil {
			c()
		}
	}
	require.Contains(t, spy.seen(), "set-option", "the push must reach tmux from the goroutine")
}

// TestContextPush_UsesThePollsLivenessNotASecondProbe pins where liveness comes
// from. The poll's has-session already answered this question for every session;
// asking again per session was the whole cost.
func TestContextPush_UsesThePollsLivenessNotASecondProbe(t *testing.T) {
	spy := newFrameSpy("agent output")
	h, inst := newCaptureHome(t, spy)
	h.appConfig.SessionContextBar = boolPtr(true)

	require.True(t, inst.PaneLive(), "a started, never-polled session reads as live")

	inst.SetPaneLive(false) // what a PaneDead poll records
	before := spy.count()
	require.Nil(t, h.pushSessionContexts(), "a session the poll saw die must not be pushed to")
	require.Equal(t, before, spy.count(), "and must not be probed either")

	inst.SetPaneLive(true)
	require.NotNil(t, h.pushSessionContexts(), "a live session's bar is pushed")
}

func boolPtr(b bool) *bool { return &b }

func countVerb(calls []string, verb string) int {
	n := 0
	for _, c := range calls {
		if c == verb {
			n++
		}
	}
	return n
}

// TestFrameChain_NeverForksAndNeverDiesWhileThereIsSomethingToCapture pins the
// two failure modes of a self-chaining loop: a second arm while one is in flight
// would double the tmux load every tick, and dropping the re-arm while a real
// target is still resolved would leave the pane frozen forever with no way back.
//
// The qualifier is load-bearing. The chain deliberately DOES die on an empty
// target — that is the point of #546's last stage, since a dispatch that captures
// nothing still costs a full frame rebuild ten times a second. What replaces the
// old "always re-arms" guarantee is the preview tick's revive, which
// TestFrameChain_DiesOnAnEmptyTargetAndRevivesFromThePreviewTick covers per path.
func TestFrameChain_NeverForksAndNeverDiesWhileThereIsSomethingToCapture(t *testing.T) {
	spy := newFrameSpy("agent output")
	h, _ := newCaptureHome(t, spy)

	first := h.armFrameCapture(0)
	require.NotNil(t, first)
	require.Nil(t, h.armFrameCapture(0), "a second arm while one is in flight must be a no-op")

	_, next := h.Update(first().(paneFrameMsg))
	require.NotNil(t, next, "handling a frame must re-arm the chain, or the pane freezes forever")
	require.True(t, h.frameInFlight, "the re-arm claims the slot again — exactly one capture is ever in flight")

	// With nothing to capture the chain ends instead: no Cmd, no message, and — the
	// half that matters — no claim on the in-flight slot, or the preview tick's
	// revive would find it taken and never restart the loop.
	paused := newFrameSpy("unused")
	ph, pinst := newCaptureHome(t, paused)
	pinst.SetStatus(session.Paused)

	before := paused.count()
	require.Nil(t, ph.armFrameCapture(0), "a paused selection has nothing to capture, so the chain ends")
	require.False(t, ph.frameInFlight, "an arm that declined must not hold the slot, or the revive can never take it")
	require.Equal(t, before, paused.count(), "and it must not reach tmux at all; saw %v", paused.seen()[before:])
}

// TestFrameChain_ReArmsImmediatelyWhenTheTargetMoved: a frame that lands for a
// session the user already left is stored (switching back is then instant) but
// leaves the new selection without one, so the next capture must not sleep out
// another interval first.
func TestFrameChain_ReArmsImmediatelyWhenTheTargetMoved(t *testing.T) {
	spy := newFrameSpy("agent output")
	h, instA := newCaptureHome(t, spy)

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

// TestCaptureTerminalFrame_JustCreatedShellKeepsItsFallback pins the round that
// creates the shell but has no frame for it yet.
//
// Creating the shell is itself the background work (tmux new-session), so that round
// returns having captured nothing. The temptation is to report it against the new
// cache slot — but ApplyFrame stamps frameAt on any err-free apply, and a non-zero
// frameAt is precisely how UpdateContent decides a frame has ARRIVED. Reporting a
// blank one therefore retires the "Opening terminal…" fallback and paints an empty
// pane in its place: the pane looks finished, showing nothing, until the next capture
// lands. Naming no slot is what keeps the fallback up for that one round.
func TestCaptureTerminalFrame_JustCreatedShellKeepsItsFallback(t *testing.T) {
	spy := newFrameSpy("shell output")
	h, inst := newCaptureHome(t, spy)

	ensured := false
	ensure := func(*session.Instance, string) (string, error) {
		ensured = true
		return "freshly-created-key", nil
	}

	// The terminal tab with no shell yet: a nil session, which is the create path.
	msg := captureTerminalFrame(frameTarget{termInstance: inst}, ensure)

	require.True(t, ensured, "control: this is the round that creates the shell")
	require.Empty(t, msg.target.termKey,
		"a round that captured nothing must name no cache slot, or the blank it carries "+
			"stamps frameAt and retires the fallback")

	// Through the handler, the pane must still offer the fallback rather than a blank.
	h.tabbedWindow.SetActiveTab(ui.TerminalTab)
	h.handlePaneFrame(msg)
	require.Contains(t, h.tabbedWindow.String(), "Opening terminal",
		"the pane must say it is opening, not render an empty frame")
}

// TestFrameTargetSnapshotsTheTitleForTheCaptureGoroutine is the delivery half of #718.
//
// ui's TestCaptureGoroutineTakesItsIdentityByParameter proves the shell creator does not read
// Instance.Title itself; nothing there proves the title it needs actually ARRIVES. Both
// halves are needed: pass an empty string through and the AST guard still passes while every
// shell is created with the window name "term: " and the legacy reap probes "term_".
//
// The snapshot has to be taken HERE, in resolveFrameTarget, because this is the update
// thread — the thread AdoptRename runs on, so this is the one place the title is the
// frame's. Since #795 reading it inside the ensurer would be safe rather than a race, and
// still wrong: it would be whichever title a concurrent rename left, one hop or two away
// from the frame being captured.
func TestFrameTargetSnapshotsTheTitleForTheCaptureGoroutine(t *testing.T) {
	spy := newFrameSpy("shell prompt $")
	h, inst := newCaptureHome(t, spy)
	h.tabbedWindow.SetActiveTab(ui.TerminalTab)

	target := h.resolveFrameTarget()
	require.False(t, target.empty(), "control: an idle terminal tab must resolve a target")
	require.Equal(t, inst.Title(), target.termTitle,
		"resolveFrameTarget must snapshot the title on the update thread")

	// Rename AFTER the target is resolved, and then run the capture. The order is what
	// makes the next assertion falsifiable: `target.termTitle` is a string field of a
	// value copy, so it cannot follow inst.Title however hard the test tries — asserting
	// that it did not is a tautology. What CAN differ is the value the ensurer receives,
	// which is the old snapshot if the capture goroutine is handed one and the new title
	// if it re-derives from target.termInstance. Mutate captureTerminalFrame to
	// `ensure(target.termInstance, target.termInstance.Title)` — the #718 defect, moved
	// one hop — and this goes red.
	before := target.termTitle
	renamedTo := inst.Title() + "-renamed"
	inst.AdoptRename(session.RenamedIdentity{
		Title: renamedTo, Branch: inst.Branch(), TmuxName: inst.TmuxSessionName(),
	})

	var got string
	called := false
	ensure := func(_ *session.Instance, title string) (string, error) {
		called, got = true, title
		return "created-key", nil
	}
	// A nil terminal is the create path — the only round that calls the ensurer.
	captureTerminalFrame(frameTarget{termInstance: inst, termTitle: before}, ensure)
	require.True(t, called, "control: the create path must reach the ensurer")
	// One assertion, not two: `got != renamedTo` would be entailed by this one and could
	// never fail on its own, since require.Equal halts the test first and renamedTo is
	// before+"-renamed" by construction. What it would have claimed is said here instead —
	// passing means the create ran on the PRE-rename title, which is the staleness
	// EnsureSession's doc prices in.
	require.Equal(t, before, got,
		"the capture goroutine must be handed the snapshot (%q), not re-derive the title "+
			"from the instance, which now reads %q", before, renamedTo)

	// The next round resolves fresh, so the new title is picked up one frame later.
	require.Equal(t, renamedTo, h.resolveFrameTarget().termTitle,
		"the following round must resolve the renamed title")
}
