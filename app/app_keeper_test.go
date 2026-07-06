package app

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/cmd/cmd_test"
	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/tmux"
	"github.com/ZviBaratz/atrium/ui"

	xansi "github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeKeeperPane models an agent pane for keeper tests (a port of the session
// package's fakeAgentPane): capture-pane renders either the composer with the
// current box text or a fixed dialog, send-keys/paste mutate the box, and every
// subprocess is counted so exclusion tests can assert zero contact. All state is
// mutex-guarded because the keeper goroutine drives the executor while tests read
// the counters (after stop(), but the race detector watches the fields regardless).
type fakeKeeperPane struct {
	mu           sync.Mutex
	created      bool   // has-session fails until new-session ran, so Start can create the session
	dialog       string // when non-empty, capture-pane renders this instead of the composer
	box          string // current composer text ("" = empty/submitted)
	pending      string // text staged by set-buffer, applied on paste-buffer
	failSendKeys bool   // hard-fail typing/tapping (exec error), for the hard-failure budget
	noLand       bool   // drop typed text on the floor (a soft not-landed outcome)

	typed  []string // recorded send-keys -l payloads
	enters int      // recorded submitting Enter taps
	calls  int      // every subprocess spawned against this pane
}

func (f *fakeKeeperPane) render() string {
	if f.dialog != "" {
		return f.dialog
	}
	var b strings.Builder
	b.WriteString("╭──────────────────────────────────────────────╮\n")
	if f.box == "" {
		b.WriteString("│ ❯                                              │\n")
	} else {
		for i, ln := range strings.Split(f.box, "\n") {
			if i == 0 {
				b.WriteString("│ ❯ " + ln + " │\n")
			} else {
				b.WriteString("│   " + ln + " │\n")
			}
		}
	}
	b.WriteString("╰──────────────────────────────────────────────╯\n")
	b.WriteString("  ? for shortcuts\n")
	return b.String()
}

func (f *fakeKeeperPane) snapshot() (typed []string, enters, calls int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.typed...), f.enters, f.calls
}

func (f *fakeKeeperPane) setDialog(dialog string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dialog = dialog
}

func (f *fakeKeeperPane) exec() cmd_test.MockCmdExec {
	return cmd_test.MockCmdExec{
		RunFunc: func(cmd *exec.Cmd) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.calls++
			args := cmd.Args
			switch {
			case slices.Contains(args, "new-session"):
				f.created = true
			case slices.Contains(args, "has-session"):
				if !f.created {
					return fmt.Errorf("no session")
				}
			case slices.Contains(args, "send-keys") && slices.Contains(args, "Enter"):
				if f.failSendKeys {
					return fmt.Errorf("send-keys failed")
				}
				f.enters++
				f.box = "" // a submitting Enter clears the composer
			case slices.Contains(args, "send-keys") && slices.Contains(args, "-l"):
				if f.failSendKeys {
					return fmt.Errorf("send-keys failed")
				}
				text := args[len(args)-1]
				f.typed = append(f.typed, text)
				if !f.noLand {
					f.box += text
				}
			case slices.Contains(args, "set-buffer"):
				f.pending = args[len(args)-1]
			case slices.Contains(args, "paste-buffer"):
				f.box += f.pending
			}
			return nil // has-session etc.: alive
		},
		OutputFunc: func(cmd *exec.Cmd) ([]byte, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.calls++
			args := strings.Join(cmd.Args, " ")
			switch {
			case strings.Contains(args, "capture-pane"):
				return []byte(f.render()), nil
			default:
				return []byte("%7\n"), nil
			}
		},
	}
}

// keeperPtyFactory is the ui/preview_test MockPtyFactory pattern: hand Start a
// throwaway file as its pty and run the command through the mock executor.
type keeperPtyFactory struct {
	t    *testing.T
	exec cmd_test.MockCmdExec
	n    int
}

func (p *keeperPtyFactory) Start(cmd *exec.Cmd) (*os.File, error) {
	p.n++
	f, err := os.OpenFile(
		filepath.Join(p.t.TempDir(), fmt.Sprintf("pty-%d", p.n)), os.O_CREATE|os.O_RDWR, 0o644)
	if err == nil {
		_ = p.exec.Run(cmd)
	}
	return f, err
}

func (p *keeperPtyFactory) Close() {}

// newKeeperInstance builds an unstarted direct instance whose tmux session is backed
// by fake. Callers that need a live agent call startKeeperInstance next.
func newKeeperInstance(t *testing.T, name string, fake *fakeKeeperPane) *session.Instance {
	t.Helper()
	inst, err := session.NewInstance(session.InstanceOptions{
		Title: name, Path: t.TempDir(), Program: "claude", Direct: true,
	})
	require.NoError(t, err)
	inst.SetTmuxSession(tmux.NewSessionWithDeps(
		context.Background(), name, "claude", &keeperPtyFactory{t: t, exec: fake.exec()}, fake.exec()))
	return inst
}

func startKeeperInstance(t *testing.T, inst *session.Instance) {
	t.Helper()
	require.NoError(t, inst.Start(true))
}

func TestKeeperServiceDeliversQueuedPrompt(t *testing.T) {
	fake := &fakeKeeperPane{}
	inst := newKeeperInstance(t, "deliver", fake)
	startKeeperInstance(t, inst)
	inst.QueuePrompt("do the thing")

	k := newAttachKeeper(context.Background(), []*session.Instance{inst}, nil)
	k.service(inst)

	typed, enters, _ := fake.snapshot()
	require.Equal(t, []string{"do the thing"}, typed, "the queued prompt must be typed into the composer")
	require.Equal(t, 1, enters, "the prompt must be submitted exactly once")
	require.Equal(t, "", inst.Prompt(), "a delivered prompt must be cleared so it is never re-sent")
	require.False(t, inst.PromptSending(), "the in-flight guard must be settled before detach")
	require.True(t, k.delivered, "the keeper must record the delivery so the detach handler persists it")
	require.Empty(t, k.errs)
}

func TestKeeperServiceNeverTouchesExcludedInstance(t *testing.T) {
	fake := &fakeKeeperPane{}
	inst := newKeeperInstance(t, "excluded", fake)
	startKeeperInstance(t, inst)
	inst.QueuePrompt("do the thing")
	inst.AutoYes = true
	_, _, before := fake.snapshot()

	k := newAttachKeeper(context.Background(), []*session.Instance{inst}, inst)
	k.service(inst)

	_, enters, after := fake.snapshot()
	require.Equal(t, before, after, "the attached instance must never be polled or probed")
	require.Equal(t, 0, enters)
	require.Equal(t, "do the thing", inst.Prompt(), "the attached instance's prompt must stay queued")
}

func TestKeeperServiceSkipsIdleInstanceWithNothingToDo(t *testing.T) {
	// The scope gate: no queued prompt and no AutoYes means the keeper has nothing it
	// could act on, so it must not spend subprocesses polling (status staleness while
	// attached is covered by the post-detach sweep).
	fake := &fakeKeeperPane{}
	inst := newKeeperInstance(t, "idle", fake)
	startKeeperInstance(t, inst)
	_, _, before := fake.snapshot()

	k := newAttachKeeper(context.Background(), []*session.Instance{inst}, nil)
	k.service(inst)

	_, _, after := fake.snapshot()
	require.Equal(t, before, after, "an instance with no prompt and no AutoYes must not be polled")
}

func TestKeeperServiceAutoYesTap(t *testing.T) {
	permissionDialog := "Do you want to proceed?\n  Yes\n  No, and tell Claude what to do\ndifferently"

	t.Run("AutoYes answers an auto-answerable prompt", func(t *testing.T) {
		fake := &fakeKeeperPane{}
		inst := newKeeperInstance(t, "autoyes", fake)
		startKeeperInstance(t, inst)
		fake.setDialog(permissionDialog)
		inst.AutoYes = true

		k := newAttachKeeper(context.Background(), []*session.Instance{inst}, nil)
		k.service(inst)

		typed, enters, _ := fake.snapshot()
		require.Equal(t, 1, enters, "AutoYes must tap Enter on a pending permission dialog")
		require.Empty(t, typed, "a tap must not type any text")
	})

	t.Run("without AutoYes the prompt surfaces as NeedsInput", func(t *testing.T) {
		fake := &fakeKeeperPane{}
		inst := newKeeperInstance(t, "manual", fake)
		startKeeperInstance(t, inst)
		fake.setDialog(permissionDialog)
		inst.QueuePrompt("queued but blocked") // passes the scope gate without AutoYes

		k := newAttachKeeper(context.Background(), []*session.Instance{inst}, nil)
		k.service(inst)

		_, enters, _ := fake.snapshot()
		require.Equal(t, 0, enters, "no AutoYes, no tap")
		require.Equal(t, session.NeedsInput, inst.GetStatus())
		require.Equal(t, "queued but blocked", inst.Prompt(),
			"a prompt must not be delivered while a blocking dialog is up (AwaitingInput gate)")
	})
}

func TestKeeperServiceSkipsInFlightSend(t *testing.T) {
	// A sendPromptCmd dispatched just before the attach still holds the in-flight
	// guard (its settle message is parked until detach); the keeper must not race it.
	fake := &fakeKeeperPane{}
	inst := newKeeperInstance(t, "inflight", fake)
	startKeeperInstance(t, inst)
	inst.QueuePrompt("do the thing")
	_, ok := inst.ClaimPrompt() // the pre-attach tick's dispatch raised the guard
	require.True(t, ok)

	k := newAttachKeeper(context.Background(), []*session.Instance{inst}, nil)
	k.service(inst)

	typed, enters, _ := fake.snapshot()
	require.Empty(t, typed, "an in-flight prompt must not be typed a second time")
	require.Equal(t, 0, enters)
	require.Equal(t, "do the thing", inst.Prompt(), "the prompt must stay queued for the async send")
	require.True(t, inst.PromptSending(), "the keeper must not settle a send it does not own")
	require.False(t, k.delivered)
}

func TestKeeperServiceSkipsUnstartedAndPaused(t *testing.T) {
	t.Run("unstarted instance is skipped, then picked up once started", func(t *testing.T) {
		fake := &fakeKeeperPane{}
		inst := newKeeperInstance(t, "starting", fake) // not started yet
		inst.QueuePrompt("do the thing")
		_, _, before := fake.snapshot()

		k := newAttachKeeper(context.Background(), []*session.Instance{inst}, nil)
		k.service(inst)
		_, _, after := fake.snapshot()
		require.Equal(t, before, after, "an unstarted instance must not be touched")
		require.Equal(t, "do the thing", inst.Prompt())

		// Start() completes mid-attach on its background goroutine; the per-cycle
		// re-check must pick the instance up on the next tick.
		startKeeperInstance(t, inst)
		k.service(inst)
		require.Equal(t, "", inst.Prompt(), "a session that finished starting mid-attach must get its prompt")
		require.True(t, k.delivered)
	})

	t.Run("paused instance is skipped", func(t *testing.T) {
		fake := &fakeKeeperPane{}
		inst := newKeeperInstance(t, "paused", fake)
		startKeeperInstance(t, inst)
		inst.QueuePrompt("do the thing")
		inst.SetStatus(session.Paused)
		_, _, before := fake.snapshot()

		k := newAttachKeeper(context.Background(), []*session.Instance{inst}, nil)
		k.service(inst)

		_, _, after := fake.snapshot()
		require.Equal(t, before, after, "a paused instance must not be touched")
		require.Equal(t, "do the thing", inst.Prompt())
	})
}

func TestKeeperServiceHardFailureBudget(t *testing.T) {
	fake := &fakeKeeperPane{}
	inst := newKeeperInstance(t, "hardfail", fake)
	startKeeperInstance(t, inst)
	fake.mu.Lock()
	fake.failSendKeys = true
	fake.mu.Unlock()
	inst.QueuePrompt("do the thing")

	k := newAttachKeeper(context.Background(), []*session.Instance{inst}, nil)
	for i := 0; i < promptSendAttempts-1; i++ {
		k.service(inst)
		require.Equal(t, "do the thing", inst.Prompt(),
			"a hard failure below the retry budget must keep the prompt queued (cycle %d)", i+1)
		require.False(t, inst.PromptSending(),
			"the guard must be released between cycles so the next one can retry")
		require.Empty(t, k.errs)
	}

	k.service(inst)
	require.Equal(t, "", inst.Prompt(),
		"exhausting the retry budget must retire the prompt, mirroring promptSendErrorMsg")
	require.False(t, inst.PromptSending())
	require.Len(t, k.errs, 1)
	require.Contains(t, k.errs[0], "hardfail", "the error must name the session")
	require.False(t, k.delivered)
}

func TestKeeperServiceHardFailureBudgetResetsOnSoftOutcome(t *testing.T) {
	// The tick path gives every dispatch a fresh sendWithRetry budget, so sporadic
	// transient hard failures spread across a long attach must not accumulate into
	// a retirement — only consecutive-cycle hard failures should.
	fake := &fakeKeeperPane{}
	inst := newKeeperInstance(t, "flappy", fake)
	startKeeperInstance(t, inst)
	inst.QueuePrompt("do the thing")

	k := newAttachKeeper(context.Background(), []*session.Instance{inst}, nil)
	set := func(fail, noLand bool) {
		fake.mu.Lock()
		fake.failSendKeys, fake.noLand = fail, noLand
		fake.mu.Unlock()
	}

	for round := 0; round < 2; round++ { // 2 hard failures, then a soft not-landed outcome, repeated
		set(true, false)
		k.service(inst)
		k.service(inst)
		set(false, true) // typing "works" but never lands → errPromptNotLanded (soft) → budget resets
		k.service(inst)
		require.Equal(t, "do the thing", inst.Prompt(),
			"interleaved transient failures must never retire the prompt (round %d)", round+1)
	}
	require.Empty(t, k.errs)

	set(false, false) // healthy pane again → delivers
	k.service(inst)
	require.Equal(t, "", inst.Prompt())
	require.True(t, k.delivered)
}

func TestKeeperStartSkipsWhenNothingServiceable(t *testing.T) {
	prev := attachKeeperInterval
	attachKeeperInterval = time.Millisecond
	defer func() { attachKeeperInterval = prev }()

	fake := &fakeKeeperPane{}
	inst := newKeeperInstance(t, "nothing-to-do", fake) // no prompt, no AutoYes
	startKeeperInstance(t, inst)
	_, _, before := fake.snapshot()

	k := newAttachKeeper(context.Background(), []*session.Instance{inst}, nil)
	k.start()
	time.Sleep(10 * time.Millisecond)
	k.stop() // must not hang: the goroutine was never launched

	select {
	case <-k.done:
		t.Fatal("the keeper must not launch when no instance is serviceable")
	default:
	}
	_, _, after := fake.snapshot()
	require.Equal(t, before, after, "an idle keeper must spawn no subprocesses")
}

func TestKeeperStartStopJoins(t *testing.T) {
	prev := attachKeeperInterval
	attachKeeperInterval = time.Millisecond
	defer func() { attachKeeperInterval = prev }()

	fake := &fakeKeeperPane{}
	inst := newKeeperInstance(t, "lifecycle", fake)
	startKeeperInstance(t, inst)
	inst.QueuePrompt("do the thing")

	k := newAttachKeeper(context.Background(), []*session.Instance{inst}, nil)
	k.start()
	require.Eventually(t, func() bool { return inst.Prompt() == "" }, 5*time.Second, time.Millisecond,
		"the running keeper must deliver the queued prompt")
	k.stop()

	select {
	case <-k.done:
	default:
		t.Fatal("stop() must join the keeper goroutine")
	}
	require.True(t, k.delivered)
	k.stop() // stop is idempotent (stopOnce)
}

func TestAttachCommandRunRunsKeeper(t *testing.T) {
	origIsTerminal := isTerminal
	t.Cleanup(func() { isTerminal = origIsTerminal })
	isTerminal = func(int) bool { return false }

	prev := attachKeeperInterval
	attachKeeperInterval = time.Millisecond
	defer func() { attachKeeperInterval = prev }()

	fake := &fakeKeeperPane{}
	inst := newKeeperInstance(t, "attached-run", fake)
	startKeeperInstance(t, inst)
	inst.QueuePrompt("do the thing")

	k := newAttachKeeper(context.Background(), []*session.Instance{inst}, nil)
	detach := make(chan struct{})
	cmd := &attachCommand{attach: func() (chan struct{}, error) { return detach, nil }, keeper: k}

	runDone := make(chan error, 1)
	go func() { runDone <- cmd.Run() }()

	require.Eventually(t, func() bool { return inst.Prompt() == "" }, 5*time.Second, time.Millisecond,
		"the keeper must deliver while the attach is still blocking")
	close(detach) // the user detaches
	require.NoError(t, <-runDone)

	select {
	case <-k.done:
	default:
		t.Fatal("Run must stop and join the keeper before returning")
	}
	require.True(t, k.delivered, "the callback reads the result fields after Run returns")
}

func TestAttachCommandRunFailedAttachNeverStartsKeeper(t *testing.T) {
	origIsTerminal := isTerminal
	t.Cleanup(func() { isTerminal = origIsTerminal })
	isTerminal = func(int) bool { return false }

	k := newAttachKeeper(context.Background(), nil, nil)
	cmd := &attachCommand{
		attach: func() (chan struct{}, error) { return nil, fmt.Errorf("attach failed") },
		keeper: k,
	}
	require.Error(t, cmd.Run())

	select {
	case <-k.done:
		t.Fatal("the keeper must not run when the attach itself failed")
	default:
	}
	k.stop() // must not hang on a never-started keeper
}

func TestAttachCommandRunNilKeeperTolerated(t *testing.T) {
	origIsTerminal := isTerminal
	t.Cleanup(func() { isTerminal = origIsTerminal })
	isTerminal = func(int) bool { return false }

	ch := make(chan struct{})
	close(ch)
	cmd := &attachCommand{attach: func() (chan struct{}, error) { return ch, nil }}
	require.NoError(t, cmd.Run())
}

// TestKeeperStragglerRace pins the promptMu contract end-to-end under the race
// detector: the keeper delivers and settles prompt state while a straggler
// pre-attach tick goroutine keeps reading it (collectMetadata/pollTargets do
// exactly these reads off-thread).
func TestKeeperStragglerRace(t *testing.T) {
	prev := attachKeeperInterval
	attachKeeperInterval = time.Millisecond
	defer func() { attachKeeperInterval = prev }()

	fake := &fakeKeeperPane{}
	inst := newKeeperInstance(t, "straggler", fake)
	startKeeperInstance(t, inst)
	inst.QueuePrompt("do the thing")

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // the last pre-attach tick's fan-out goroutine
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_ = inst.Prompt()
				_ = inst.PromptQueuedAt()
				_ = inst.Poll()
			}
		}
	}()

	k := newAttachKeeper(context.Background(), []*session.Instance{inst}, nil)
	k.start()
	require.Eventually(t, func() bool { return inst.Prompt() == "" }, 5*time.Second, time.Millisecond)
	k.stop()
	close(stop)
	wg.Wait()
}

func TestAttachFinished_KeeperDeliveredPersists(t *testing.T) {
	h, inst := newUnreadHome(t)

	statePath := filepath.Join(mustConfigDir(t), "state.json")
	_ = os.Remove(statePath)

	_, _ = h.Update(attachFinishedMsg{killTarget: inst, keeperDelivered: true})

	_, err := os.Stat(statePath)
	require.NoError(t, err,
		"a keeper delivery must be persisted on detach, mirroring promptDeliveredMsg's persist")
}

func TestAttachFinished_KeeperAbandonedPromptPersists(t *testing.T) {
	// A budget-exhausted prompt is cleared in memory by the keeper; the detach must
	// persist that clear too, or state.json resurrects the abandoned prompt on the
	// next launch.
	h, inst := newUnreadHome(t)
	h.errBox = ui.NewErrBox() // the same message also routes through error surfacing

	statePath := filepath.Join(mustConfigDir(t), "state.json")
	_ = os.Remove(statePath)

	_, _ = h.Update(attachFinishedMsg{killTarget: inst, keeperErrs: []string{"lost"}})

	_, err := os.Stat(statePath)
	require.NoError(t, err,
		"a keeper-abandoned prompt must be persisted on detach so it is not resurrected")
}

func TestAttachFinished_KeeperErrsSurfaced(t *testing.T) {
	h, inst := newUnreadHome(t)
	h.errBox = ui.NewErrBox()
	h.errBox.SetSize(10, 1) // too narrow for the message, forcing the persistent-modal route

	_, _ = h.Update(attachFinishedMsg{killTarget: inst, keeperErrs: []string{
		`failed to deliver prompt to "b": send-keys failed`,
	}})

	assert.Equal(t, stateInfo, h.state, "a lost prompt must be surfaced, not silently logged")
	require.NotNil(t, h.textOverlay)
	plain := xansi.Strip(h.textOverlay.Render())
	assert.Contains(t, plain, "failed to deliver prompt")
}

func TestAttachFinished_KeeperErrsSurfacedOnFailedReattach(t *testing.T) {
	// A sibling-cycle re-attach that fails still carries the previous keeper's
	// losses (attachExecCarry seeds them before Run can fail); the err branch must
	// surface them alongside the attach error, not drop them to log-only.
	h, inst := newUnreadHome(t)
	h.errBox = ui.NewErrBox()

	_, _ = h.Update(attachFinishedMsg{
		err:        fmt.Errorf("tmux attach failed"),
		killTarget: inst,
		keeperErrs: []string{`failed to deliver prompt to "b": send-keys failed`},
	})

	require.Equal(t, stateInfo, h.state, "carried keeper losses must be surfaced even when the re-attach fails")
	require.NotNil(t, h.textOverlay)
	plain := xansi.Strip(h.textOverlay.Render())
	assert.Contains(t, plain, "tmux attach failed")
	assert.Contains(t, plain, "failed to deliver prompt")
}

func TestAttachCommandRunCallsOnAttached(t *testing.T) {
	origIsTerminal := isTerminal
	t.Cleanup(func() { isTerminal = origIsTerminal })
	isTerminal = func(int) bool { return false }

	t.Run("successful attach bumps once, before the keeper could act", func(t *testing.T) {
		calls := 0
		ch := make(chan struct{})
		close(ch)
		cmd := &attachCommand{
			attach:     func() (chan struct{}, error) { return ch, nil },
			onAttached: func() { calls++ },
		}
		require.NoError(t, cmd.Run())
		require.Equal(t, 1, calls, "a successful attach must record the generation bump exactly once")
	})

	t.Run("failed attach does not bump", func(t *testing.T) {
		calls := 0
		cmd := &attachCommand{
			attach:     func() (chan struct{}, error) { return nil, fmt.Errorf("attach failed") },
			onAttached: func() { calls++ },
		}
		require.Error(t, cmd.Run())
		require.Zero(t, calls, "no keeper ran, so pre-attach captures are still valid")
	})
}

func mustConfigDir(t *testing.T) string {
	t.Helper()
	dir, err := config.GetConfigDir()
	require.NoError(t, err)
	return dir
}
