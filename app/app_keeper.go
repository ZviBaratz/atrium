package app

// The attach keeper keeps background sessions serviced while a tea.Exec attach
// suspends the Bubble Tea event loop (issue #264).

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/tmux"
)

// attachKeeperInterval is the keeper's polling cadence, matching the metadata tick.
// A var, not a const, so tests can shrink it.
var attachKeeperInterval = 500 * time.Millisecond

// attachKeeper keeps background sessions serviced while a tea.Exec attach suspends
// the event loop — and with it the metadata tick that delivers queued prompts
// (deliverReadyPrompts) and taps auto-yes prompts (ApplyPaneState): without it, a
// session created with a startup prompt sits idle for the whole attach.
//
// It runs one sequential goroutine (the daemon's pollOnce precedent), started inside
// attachCommand.Run — on the suspended event-loop goroutine, so everything the main
// loop did before happens-before the keeper — and stopped AND JOINED before Run
// returns, so everything the keeper did happens-before the resumed loop and the
// tea.Exec callback. That join placement is the correctness linchpin: messages queued
// mid-attach (a stale metadataUpdateDoneMsg, a parked promptDeliveredMsg) may be
// processed BEFORE attachFinishedMsg, so the keeper must not outlive Run. (Pane-state
// captures taken before the attach are separately retired by home.attachGen, so a
// parked capture can't replay an auto-yes tap onto a dialog the keeper advanced.)
//
// Scope is deliberately minimal: it writes only instance status (mu-guarded) and the
// promptMu-guarded prompt state. It never touches diff/PR/model/mode metadata
// (modelStamp forbids a second extraction site), never recovers lost sessions (the
// strike debounce is main-loop state), never writes AutoYes, and never persists —
// the detach handler persists when delivered is set. An instance whose pre-attach
// sendPromptCmd is still in flight keeps its guard raised for the whole attach (the
// settle message is parked), so the keeper skips it: that degrades to pre-keeper
// behavior for that one instance.
type attachKeeper struct {
	ctx       context.Context     // app lifecycle: SIGTERM during an attach still stops the keeper
	instances []*session.Instance // main-thread snapshot; membership is frozen while the loop is suspended
	excluded  *session.Instance   // the attached instance (nil for the terminal tab, whose agent panes are all detached)

	stopCh   chan struct{}
	done     chan struct{}
	stopOnce sync.Once
	// running records that start() launched the goroutine, so stop() knows whether
	// there is anything to join. start/stop are only called from attachCommand.Run's
	// goroutine, so the plain bool never races.
	running bool

	// hardFails counts consecutive-cycle hard SendPrompt failures per instance so
	// the keeper retries on its cadence up to the same promptSendAttempts budget as
	// sendWithRetry; a success or soft outcome resets it, matching the tick path
	// where every dispatch gets a fresh retry budget.
	hardFails map[*session.Instance]int

	// delivered and errs are written only by the keeper goroutine and read by
	// attachExec's callback after stop() has joined — ordered by the join, the same
	// pattern as attachCommand.rawModeFailed. errs may be seeded on the main thread
	// (before start) with losses carried across a sibling-cycle re-attach, so the
	// final plain detach of a cycling chain surfaces every lost prompt.
	delivered bool     // ≥1 prompt confirmed delivered → the detach handler persists
	errs      []string // hard delivery failures that exhausted the budget, surfaced post-detach
}

func newAttachKeeper(ctx context.Context, instances []*session.Instance, excluded *session.Instance) *attachKeeper {
	return &attachKeeper{
		ctx:       ctx,
		instances: instances,
		excluded:  excluded,
		stopCh:    make(chan struct{}),
		done:      make(chan struct{}),
		hardFails: make(map[*session.Instance]int),
	}
}

// start launches the keeper goroutine. Call only once, from attachCommand.Run, after
// the attach has succeeded. It is a no-op when nothing is serviceable: the suspended
// loop cannot queue prompts or toggle AutoYes mid-attach, so the common attach
// (nothing queued, autoyes off) costs no goroutine and no ticker at all.
func (k *attachKeeper) start() {
	if !k.hasServiceable() {
		return
	}
	k.running = true
	go k.run()
}

// hasServiceable reports whether any non-excluded instance could need the keeper:
// a queued prompt to deliver or AutoYes prompts to tap. Deliberately ignores
// Started/Paused — an instance still starting at attach time becomes serviceable
// mid-attach, and service() re-checks liveness per cycle.
func (k *attachKeeper) hasServiceable() bool {
	for _, inst := range k.instances {
		if inst == k.excluded {
			continue
		}
		if inst.AutoYes || inst.Prompt() != "" {
			return true
		}
	}
	return false
}

// stop signals the keeper and joins it. It is idempotent, safe on a never-started
// keeper, and MUST complete before attachCommand.Run returns: the join is what
// orders the keeper's writes before the resumed event loop.
func (k *attachKeeper) stop() {
	k.stopOnce.Do(func() { close(k.stopCh) })
	if k.running {
		<-k.done
	}
}

func (k *attachKeeper) run() {
	defer close(k.done)
	// Sweep immediately (daemon precedent): a prompt that became ready just before
	// the attach shouldn't wait a full interval.
	k.tick()
	ticker := time.NewTicker(attachKeeperInterval)
	defer ticker.Stop()
	for {
		select {
		case <-k.stopCh:
			return
		case <-k.ctx.Done():
			return
		case <-ticker.C:
			k.tick()
		}
	}
}

// tick services every snapshot instance once, checking for a stop between instances
// so a detach never waits on more than the one in-flight delivery (≤ ~1s).
func (k *attachKeeper) tick() {
	for _, inst := range k.instances {
		select {
		case <-k.stopCh:
			return
		case <-k.ctx.Done():
			return
		default:
		}
		k.service(inst)
	}
}

// service runs one keeper cycle for one instance: poll → apply (status + auto-yes
// tap) → deliver a claimable queued prompt. It mirrors one metadata-tick fan-out
// plus applyMetadataResults plus deliverReadyPrompts, minus everything out of the
// keeper's scope.
func (k *attachKeeper) service(inst *session.Instance) {
	// Re-check liveness per cycle, not at snapshot time: a session whose Start()
	// completes mid-attach becomes serviceable here (its instanceStartedMsg is
	// parked until detach), which is exactly the "create B, attach to A" case.
	if inst == k.excluded || !inst.Started() || inst.Paused() {
		return
	}
	// Scope gate: only touch instances the keeper can act on — a queued prompt to
	// deliver or AutoYes prompts to tap. Status freshness for the rest is the
	// post-detach sweep's job, and polling the full list every cycle would exceed
	// the tick loop's own light-tick budget (see pollTargets). One read suffices:
	// the keeper is the sole prompt writer while the loop is suspended.
	prompt := inst.Prompt()
	if prompt == "" && !inst.AutoYes {
		return
	}
	state := inst.Poll() // the attached session self-guards to PaneUnknown; excluded is defense in depth
	if state == tmux.PaneDead {
		return // lost-session recovery stays with the main loop's strike debounce
	}
	inst.ApplyPaneState(state) // status + auto-yes Enter tap, tick-identical

	if prompt == "" {
		return // only probe readiness while a prompt is queued, like collectMetadata
	}
	if !promptDeliveryReady(state, inst.AwaitingInput(), inst.PromptQueuedAt(), time.Now()) {
		return
	}
	prompt, ok := inst.ClaimPrompt()
	if !ok {
		return // a pre-attach sendPromptCmd still owns this send; skip for this attach
	}
	// One SendPrompt attempt per cycle: the keeper's cadence is the retry loop, and
	// a single bounded attempt keeps the detach join tail short (~1s worst case).
	err := inst.SendPrompt(prompt)
	switch {
	case err == nil:
		delete(k.hardFails, inst)
		inst.ClearPrompt(prompt)
		k.delivered = true
		log.InfoLog.Printf("delivered queued prompt to %q while attached", inst.Title)
	case session.IsSoftPromptError(err):
		// Pane not ready / unconfirmed: retry next cycle with the prompt queued. A
		// soft outcome also resets the hard budget so it matches the tick path,
		// where each dispatch runs a fresh sendWithRetry — only consecutive-cycle
		// hard failures should retire a prompt.
		delete(k.hardFails, inst)
		inst.ClearPromptSending()
	default:
		k.hardFails[inst]++
		if k.hardFails[inst] < promptSendAttempts {
			inst.ClearPromptSending() // transient hard failure: retry next cycle
			return
		}
		// Budget exhausted: retire the prompt so the keeper doesn't spin on a dead
		// pane, and surface the loss post-detach — mirroring promptSendErrorMsg.
		inst.ClearPrompt(prompt)
		msg := fmt.Sprintf("failed to deliver prompt to %q: %v", inst.Title, err)
		k.errs = append(k.errs, msg)
		log.ErrorLog.Printf("%s (after %d attempts while attached)", msg, promptSendAttempts)
	}
}
