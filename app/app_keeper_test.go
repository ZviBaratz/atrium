package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/cmd"
	"github.com/ZviBaratz/atrium/cmd/cmd_test"
	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/notify"
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
	footer       string // mode line below the box ("" = the inert "? for shortcuts")
	pending      string // text staged by set-buffer, applied on paste-buffer
	failSendKeys bool   // hard-fail typing/tapping (exec error), for the hard-failure budget
	noLand       bool   // drop typed text on the floor (a soft not-landed outcome)
	// signalSendKeys fails a send the way a Ctrl+C does: the subprocess is killed by a
	// signal rather than running and refusing. Reachable since `output: terminal` (#375),
	// where a cooked takeover puts the keeper's tmux children in the interrupted
	// foreground process group.
	signalSendKeys error

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
	if f.footer != "" {
		b.WriteString("  " + f.footer + "\n")
	} else {
		b.WriteString("  ? for shortcuts\n")
	}
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
				if f.signalSendKeys != nil {
					return f.signalSendKeys
				}
				if f.failSendKeys {
					return fmt.Errorf("send-keys failed")
				}
				f.enters++
				f.box = "" // a submitting Enter clears the composer
			case slices.Contains(args, "send-keys") && slices.Contains(args, "-l"):
				if f.signalSendKeys != nil {
					return f.signalSendKeys
				}
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
	// Claude's network-permission dialog — the one prompt autoyes answers with Enter.
	// ABRIDGED from the live 2.1.210 capture (2026-07-15, #343), not a transcription of one:
	// the verbatim panes are session/agent's claudeFetchPane (width 100) and
	// claudeFetchNarrowPane (width 28), and that is where the matcher itself is pinned. This
	// test drives the keeper, not the matcher, so it keeps only the shape the keeper's path
	// needs — do not read the rule's width here as a captured one.
	// That shape is still not decoration: the matcher requires its title below the pane's last
	// box border, so a bare option list no longer reads as a live dialog. The "❯ 1. Yes" also
	// makes InputBoxVisible true, which is what the second subtest needs — its queued prompt
	// must be held back by DetectPrompt (the blocking-dialog gate it names), not by the
	// accidental absence of a box.
	permissionDialog := strings.Join([]string{
		"● Fetch(https://example.net)",
		strings.Repeat("─", 60),
		" Fetch",
		`   url: "https://example.net", prompt: "Summarize the content of this page."`,
		" Do you want to allow Claude to fetch this content?",
		" ❯ 1. Yes",
		"   2. Yes, and don't ask again for example.net",
		"   3. No, and tell Claude what to do differently (esc)",
	}, "\n")

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

// writeKeeperTranscript plants a claude transcript whose last assistant turn ends with
// text, and points inst's transcript root at it — the wiring the poll path uses.
func writeKeeperTranscript(t *testing.T, inst *session.Instance, workDir, text string) {
	t.Helper()
	root := t.TempDir()
	sanitized := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return '-'
	}, workDir)
	dest := filepath.Join(root, "projects", sanitized, "s.jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(dest), 0o755))
	line := `{"type":"assistant","isSidechain":false,"message":{"model":"claude-opus-4-7",` +
		`"content":[{"type":"text","text":"` + text + `"}]}}` + "\n"
	require.NoError(t, os.WriteFile(dest, []byte(line), 0o644))
	inst.SetClaudeAccount("work", root, false)
}

// TestKeeperServiceHoldsPromptOnUnansweredQuestion covers the #571 hold on the OTHER
// dispatcher. The attach keeper services every session the user is not attached to, so
// without its own gate a background session that stopped to ask still has its queued
// follow-up delivered as the answer — the tick-path guard cannot see this path at all.
//
// It also drives the release end to end: once MarkSeen clears unread (what
// markSeenAfterDwell does when the user dwells on the row), the same prompt delivers.
func TestKeeperServiceHoldsPromptOnUnansweredQuestion(t *testing.T) {
	fake := &fakeKeeperPane{}
	inst := newKeeperInstance(t, "asked", fake)
	startKeeperInstance(t, inst)
	writeKeeperTranscript(t, inst, inst.WorkingDir(), "Want me to open the PR, or will you?")
	inst.QueueFollowupPrompt("go ahead")

	// A finished turn the user has not visited: Running→Ready is the edge that flags
	// unread, exactly as the poll loop's ApplyPaneState would.
	inst.SetStatus(session.Running)
	inst.SetStatus(session.Ready)
	require.True(t, inst.Unread())

	k := newAttachKeeper(context.Background(), []*session.Instance{inst}, nil)
	k.service(inst)

	typed, enters, _ := fake.snapshot()
	require.Empty(t, typed, "a follow-up must not be typed as the answer to an unread question")
	require.Zero(t, enters)
	require.Equal(t, "go ahead", inst.Prompt(), "the prompt stays queued, not dropped")
	require.False(t, k.delivered)

	// The user looks at the row: the hold releases and the same prompt goes out.
	inst.MarkSeen()
	k.service(inst)

	typed, enters, _ = fake.snapshot()
	require.Equal(t, []string{"go ahead"}, typed, "a seen question releases the queued follow-up")
	require.Equal(t, 1, enters)
	require.Empty(t, inst.Prompt())
	require.True(t, k.delivered)
}

// The same hold, on a turn that ended by asking WHILE a background shell kept running.
//
// This is the shape #682's review caught. The hold is `asked && Unread()`, and the unread
// bit used to be raised only on entry to Ready — which a chip-held row never reaches, since
// it settles on Pending instead. So extending endedAskingNow to PaneBackground (which this
// still needs) computed `asked` for a conjunction that could never be true, and the queued
// follow-up went out as the answer to a question the user had not seen. Nothing else fails
// when the hold silently stops holding, which is why it needs a test of its own on both
// dispatchers.
func TestKeeperServiceHoldsPromptWhenTheQuestionEndedWithBackgroundWork(t *testing.T) {
	fake := &fakeKeeperPane{footer: "⏵⏵ auto mode on · 2 shells · ← for agents · ↓ to manage"}
	inst := newKeeperInstance(t, "asked-bg", fake)
	startKeeperInstance(t, inst)
	writeKeeperTranscript(t, inst, inst.WorkingDir(), "Want me to open the PR, or will you?")
	inst.QueueFollowupPrompt("go ahead")

	// The keeper's own poll must classify this pane as background work, or the test would
	// be driving the plain PaneIdle path the sibling test above already covers.
	require.Equal(t, tmux.PaneBackground, inst.Poll(), "chip up + turn over = background work")

	k := newAttachKeeper(context.Background(), []*session.Instance{inst}, nil)
	k.service(inst)

	require.Equal(t, session.Pending, inst.GetStatus(), "the shell is still running")
	require.True(t, inst.Unread(), "and the turn-end that asked still flags unread")
	typed, enters, _ := fake.snapshot()
	require.Empty(t, typed, "a follow-up must not answer a question the user has not seen")
	require.Zero(t, enters)
	require.Equal(t, "go ahead", inst.Prompt(), "the prompt stays queued")

	// The release valve is the same one: seeing the row lets the prompt through, even
	// though the background work is still running and the row is still Pending.
	inst.MarkSeen()
	k.service(inst)

	typed, enters, _ = fake.snapshot()
	require.Equal(t, []string{"go ahead"}, typed, "a seen question releases the queued follow-up")
	require.Equal(t, 1, enters)
	require.Equal(t, session.Pending, inst.GetStatus(), "delivery does not fake a finished turn")
}

// TestEndedAskingNow_MemoFallback pins the two paths that must answer from the memo
// rather than re-reading, and the one that must not.
//
// The fallback is load-bearing, not an optimisation: the transcript is only re-read on a
// settled pane and only when its stamp moved, so returning a bare false whenever
// ComputeAsked declines would drop the hold on every tick in between — the prompt would
// go out one tick after the question, not never. Returning false there passes every other
// test in this package, which is why it needs its own.
func TestEndedAskingNow_MemoFallback(t *testing.T) {
	fake := &fakeKeeperPane{}
	inst := newKeeperInstance(t, "memo", fake)
	startKeeperInstance(t, inst)
	writeKeeperTranscript(t, inst, inst.WorkingDir(), "Want me to open the PR?")

	// First settled look: reads the transcript and reports a fresh verdict to memoize.
	asked, stamp, ok := endedAskingNow(inst, tmux.PaneIdle)
	require.True(t, ok, "a settled pane with a changed transcript yields a memoizable result")
	require.True(t, asked)
	inst.SetAskedMeta(asked, stamp)

	// Same pane, unchanged transcript: no re-read, but the hold must survive.
	asked, _, ok = endedAskingNow(inst, tmux.PaneIdle)
	require.False(t, ok, "an unchanged transcript has nothing to apply")
	require.True(t, asked, "...but the memo must still report the outstanding question")

	// Mid-turn: never re-read (the turn is still being written), still hold. The
	// transcript is REPLACED with question-free prose first, so this also proves the
	// idle gate: without it the read would happen here, see no question, and report a
	// fresh false — the assertion below would then fail on `asked`, not just on `ok`.
	writeKeeperTranscript(t, inst, inst.WorkingDir(), "Working on it now.")
	asked, _, ok = endedAskingNow(inst, tmux.PaneWorking)
	require.False(t, ok, "a working pane must not re-read the transcript it is still writing")
	require.True(t, asked, "a working pane must answer from the memo, not report 'no question'")
}

// TestMetadataTick_MemoizesTheQuestionFlag covers the wiring between the two halves of
// the #571 signal on the TICK path: collectMetadata derives the flag off-thread and
// applyMetadataResults must store it on the instance.
//
// Dropping the store leaves the delivery hold working — each tick still gates on its own
// fresh read — so every other test here passes. What breaks is everything that reads the
// memo afterwards: the once-per-turn read budget, and the notification rung that reads
// EndedAsking() rather than re-deriving it.
func TestMetadataTick_MemoizesTheQuestionFlag(t *testing.T) {
	h := newCreateFormHome(t)
	h.lostStrikes = map[*session.Instance]int{}
	fake := &fakeKeeperPane{}
	inst := newKeeperInstance(t, "ticked", fake)
	startKeeperInstance(t, inst)
	writeKeeperTranscript(t, inst, inst.WorkingDir(), "Want me to open the PR?")
	h.list.AddInstance(inst)()

	require.False(t, inst.EndedAsking(), "precondition: nothing derived yet")

	results := collectMetadata(h.ctx, []*session.Instance{inst}, nil, false, h.usagePolicy(), h.diffContentFloor())
	require.Len(t, results, 1)
	require.Equal(t, tmux.PaneIdle, results[0].state, "precondition: the fake pane reads settled")
	require.True(t, results[0].askedOK, "a settled pane with a fresh transcript yields a result to apply")

	h.applyMetadataResults(results, false)

	require.True(t, inst.EndedAsking(), "the tick must memoize the question flag on the instance")
}

// TestTickPathHoldsPromptWhenQuestionFirstAppears is the regression test for the ordering
// the hold depends on. It drives the whole tick pipeline — collectMetadata (off-thread)
// → applyMetadataResults (ApplyPaneState, main thread) → the returned send commands —
// with Unread() FALSE going in, which is the ordinary case: the user was watching the row
// when they queued the follow-up, so nothing is unread until this very tick's Running→Ready
// edge raises it.
//
// That precondition is the whole test. Seeding Unread() true first (the obvious way, and
// what the keeper test does) passes even when the hold is evaluated a step too early,
// because the value it reads is then already correct. Verified by moving the check back
// into collectMetadata: this test fails and every other test in the package still passes.
func TestTickPathHoldsPromptWhenQuestionFirstAppears(t *testing.T) {
	h := newCreateFormHome(t)
	h.lostStrikes = map[*session.Instance]int{}
	fake := &fakeKeeperPane{}
	inst := newKeeperInstance(t, "tickhold", fake)
	startKeeperInstance(t, inst)
	writeKeeperTranscript(t, inst, inst.WorkingDir(), "Want me to open the PR, or will you?")
	h.list.AddInstance(inst)()

	// The agent is mid-turn and the user is looking at the row: nothing unread yet.
	inst.SetStatus(session.Running)
	require.False(t, inst.Unread(), "precondition: the row has been seen, nothing is unread")

	inst.QueueFollowupPrompt("go ahead")

	results := collectMetadata(h.ctx, []*session.Instance{inst}, nil, false, h.usagePolicy(), h.diffContentFloor())
	require.Len(t, results, 1)
	require.Equal(t, tmux.PaneIdle, results[0].state, "precondition: the turn ended")
	require.True(t, results[0].asked, "precondition: the question was detected off-thread")

	for _, cmd := range h.applyMetadataResults(results, false) {
		if cmd != nil {
			cmd()
		}
	}

	typed, enters, _ := fake.snapshot()
	require.Empty(t, typed, "the follow-up must not be delivered into the unanswered question")
	require.Zero(t, enters)
	require.Equal(t, "go ahead", inst.Prompt(), "the prompt stays queued, not dropped")
	require.True(t, inst.Unread(), "the same tick flagged the finished turn unread")

	// The release valve still works through the same path: once the user dwells on the
	// row, the very next tick delivers.
	inst.MarkSeen()
	results = collectMetadata(h.ctx, []*session.Instance{inst}, nil, false, h.usagePolicy(), h.diffContentFloor())
	for _, cmd := range h.applyMetadataResults(results, false) {
		if cmd != nil {
			cmd()
		}
	}
	typed, enters, _ = fake.snapshot()
	require.Equal(t, []string{"go ahead"}, typed, "a seen question releases the follow-up")
	require.Equal(t, 1, enters)
}

// TestTickPathNotifiesAsked is the wiring guard for #571's notification half.
//
// Every other notify test drives notifyEventFor or maybeNotify directly, handing in
// `asked` as an argument — so all of them pass while the one place production computes
// that argument passes a literal false. That is not hypothetical: this PR shipped exactly
// that, with a comment above the call claiming it passed r.asked. Type-correct, vet-clean,
// full suite green, and EventAsked silently retired in production.
//
// This drives the real tick — collectMetadata (off-thread verdict) → applyMetadataResults
// (ApplyPaneState, then maybeNotify) — and asserts the question rings THROUGH that path.
// The finished rung is set to off so a plain finish cannot produce the bell being asserted:
// what rings here can only be EventAsked.
func TestTickPathNotifiesAsked(t *testing.T) {
	var buf bytes.Buffer
	h := newCreateFormHome(t)
	h.lostStrikes = map[*session.Instance]int{}
	h.notifier = notify.New(&buf, cmd.MakeExecutor())
	h.notifySeen = make(map[*session.Instance]*notifyState)
	h.appConfig.Notifications = config.NotificationsBell
	h.appConfig.NotificationsFinished = config.NotificationsOff

	fake := &fakeKeeperPane{}
	inst := newKeeperInstance(t, "ticknotify", fake)
	startKeeperInstance(t, inst)
	writeKeeperTranscript(t, inst, inst.WorkingDir(), "Want me to open the PR, or will you?")
	h.list.AddInstance(inst)()

	// A second row is selected: maybeNotify stays silent on the selected instance.
	other := newNotifyInstance(t)
	h.list.AddInstance(other)()
	h.list.SetSelectedInstance(1)

	// Spend the first-observation gate without letting the edge itself ring.
	h.notifySeen[inst] = &notifyState{}
	inst.SetStatus(session.Running)
	buf.Reset()

	results := collectMetadata(h.ctx, []*session.Instance{inst}, nil, false, h.usagePolicy(), h.diffContentFloor())
	require.True(t, results[0].asked, "precondition: the question was detected off-thread")
	h.applyMetadataResults(results, true)

	require.NotEmpty(t, buf.String(),
		"the tick must notify for a question; with notifications_finished=off only EventAsked can ring here")
}

// TestTickPathPlainFinishStaysQuiet is TestTickPathNotifiesAsked's negative control, and
// it guards the opposite miswiring: passing a constant true where r.asked belongs.
//
// That direction is worse than it looks. It would classify EVERY finished turn as a
// question, routing all of them through the base mode and out of reach of
// notifications_finished — the "just raise the EventFinished rung" mistake #571 explicitly
// warns against, arrived at by accident. Without this test the constant passes: the
// asked-side test only gets louder.
//
// Same wiring as its sibling, one difference: the turn ends on a statement.
func TestTickPathPlainFinishStaysQuiet(t *testing.T) {
	var buf bytes.Buffer
	h := newCreateFormHome(t)
	h.lostStrikes = map[*session.Instance]int{}
	h.notifier = notify.New(&buf, cmd.MakeExecutor())
	h.notifySeen = make(map[*session.Instance]*notifyState)
	h.appConfig.Notifications = config.NotificationsBell
	h.appConfig.NotificationsFinished = config.NotificationsOff

	fake := &fakeKeeperPane{}
	inst := newKeeperInstance(t, "tickquiet", fake)
	startKeeperInstance(t, inst)
	writeKeeperTranscript(t, inst, inst.WorkingDir(), "Done. The PR is open and CI is green.")
	h.list.AddInstance(inst)()

	other := newNotifyInstance(t)
	h.list.AddInstance(other)()
	h.list.SetSelectedInstance(1)

	h.notifySeen[inst] = &notifyState{}
	inst.SetStatus(session.Running)
	buf.Reset()

	results := collectMetadata(h.ctx, []*session.Instance{inst}, nil, false, h.usagePolicy(), h.diffContentFloor())
	require.False(t, results[0].asked, "precondition: a statement is not a question")
	h.applyMetadataResults(results, true)

	require.Empty(t, buf.String(),
		"a plain finish must stay on the finished rung (off), not be reported as a question")
}

// TestKeeperInterruptedSendNeverRetiresThePrompt is the fix for what Ctrl+C does to the
// keeper during a cooked terminal takeover.
//
// `output: terminal` runs the user's command cooked precisely so Ctrl+C reaches it — and
// that SIGINT goes to the whole foreground process group, which includes the
// `tmux send-keys` children the keeper shells out to while the loop is suspended. Counted
// as hard failures, a few impatient presses exhaust promptSendAttempts and retire the
// prompt for good: handleCustomCommandTerminalDone then PERSISTS the cleared prompt, so it
// leaves state.json and is never delivered — for a keypress aimed at the user's own build.
//
// An interrupted send says nothing about whether the pane would have accepted the prompt,
// so the truthful reading is "try again next cycle".
func TestKeeperInterruptedSendNeverRetiresThePrompt(t *testing.T) {
	fake := &fakeKeeperPane{}
	inst := newKeeperInstance(t, "interrupted", fake)
	startKeeperInstance(t, inst)
	fake.mu.Lock()
	fake.signalSendKeys = signalDeathError(t)
	fake.mu.Unlock()
	inst.QueuePrompt("do the thing")

	k := newAttachKeeper(context.Background(), []*session.Instance{inst}, nil)
	// Well past the budget a hard failure would have spent.
	for i := 0; i < promptSendAttempts+2; i++ {
		k.service(inst)
		require.Equalf(t, "do the thing", inst.Prompt(),
			"an interrupted send must keep the prompt queued (cycle %d)", i+1)
		require.False(t, inst.PromptSending(),
			"the guard must be released so the next cycle can retry")
	}
	assert.Empty(t, k.errs,
		"a Ctrl+C aimed at the user's own command must not be reported as a lost prompt")
	assert.False(t, k.delivered)
	assert.Empty(t, k.hardFails, "and it must not accumulate against the retire budget")

	// And once the interruptions stop, the same prompt still delivers.
	fake.mu.Lock()
	fake.signalSendKeys = nil
	fake.mu.Unlock()
	k.service(inst)
	assert.Equal(t, "", inst.Prompt(), "the prompt survives to be delivered")
	assert.True(t, k.delivered)
}

// The other side of the same discrimination: a send that RAN and exited non-zero is a hard
// failure and must still retire the prompt on budget. Without this, "signalled" could be
// spelled as "any non-zero exit" and a genuinely dead pane would be retried forever — the
// keeper spinning on it for the whole takeover with nothing surfaced at the end.
// TestKeeperContextKilledSendIsStillAHardFailure is the case the predicate's first
// spelling silently reclassified.
//
// Every tmux op runs under exec.CommandContext with a 10s tmuxOpTimeout, and a context kill
// is SIGKILL — which reports ExitCode() == -1, exactly like the Ctrl+C this softening exists
// for. Keyed on the code alone, a genuinely wedged tmux stopped being a hard failure: the
// keeper re-sent every cycle for the whole takeover and the return path reported nothing,
// so a prompt that could never be delivered looked fine. Only the SIGNAL distinguishes them.
func TestKeeperContextKilledSendIsStillAHardFailure(t *testing.T) {
	fake := &fakeKeeperPane{}
	inst := newKeeperInstance(t, "wedged", fake)
	startKeeperInstance(t, inst)
	fake.mu.Lock()
	fake.signalSendKeys = contextKilledError(t) // a hung tmux reaped by its op timeout
	fake.mu.Unlock()
	inst.QueuePrompt("do the thing")

	require.False(t, killedByTerminalSignal(contextKilledError(t)),
		"a timeout kill is SIGKILL, not the terminal's interrupt — the code alone cannot tell")

	k := newAttachKeeper(context.Background(), []*session.Instance{inst}, nil)
	for range promptSendAttempts {
		k.service(inst)
	}
	assert.Equal(t, "", inst.Prompt(),
		"a wedged tmux must still exhaust the budget and retire the prompt")
	require.Len(t, k.errs, 1, "and the loss must be surfaced on return, not retried in silence")
}

func TestKeeperNonZeroExitIsStillAHardFailure(t *testing.T) {
	fake := &fakeKeeperPane{}
	inst := newKeeperInstance(t, "exited", fake)
	startKeeperInstance(t, inst)
	fake.mu.Lock()
	fake.signalSendKeys = exitErrorWithCode(t, 1) // ran and refused, ExitCode 1 — not a signal
	fake.mu.Unlock()
	inst.QueuePrompt("do the thing")

	k := newAttachKeeper(context.Background(), []*session.Instance{inst}, nil)
	for range promptSendAttempts {
		k.service(inst)
	}
	assert.Equal(t, "", inst.Prompt(),
		"a send that ran and failed must still exhaust the budget and retire the prompt")
	require.Len(t, k.errs, 1, "and the loss must be surfaced")
	assert.Contains(t, k.errs[0], "exited")
}

// TestKilledByTerminalSignalBoundaries pins the predicate directly, at both edges.
//
// It is the discrimination the keeper's classification rests on, and `ExitCode() == -1`
// cannot make it: a context kill (SIGKILL, from tmuxOpTimeout) and a Ctrl+C (SIGINT) report
// the same code. Too broad and a wedged tmux is retried in silence forever; too narrow and
// an impatient Ctrl+C retires a queued prompt for good.
func TestKilledByTerminalSignalBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"SIGINT — the Ctrl+C a cooked takeover delivers", signalDeathError(t), true},
		{"SIGQUIT — the Ctrl+\\ a cooked takeover also delivers", quitDeathError(t), true},
		{"SIGKILL — a tmux op reaped by its own timeout", contextKilledError(t), false},
		{"SIGTERM — an outside `kill`, which is not the terminal's", termDeathError(t), false},
		{"exited non-zero — ran and refused", exitErrorWithCode(t, 1), false},
		{"exited zero-ish plain error — not a subprocess outcome at all",
			errors.New("send-keys failed"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, killedByTerminalSignal(tc.err))
		})
	}
}
