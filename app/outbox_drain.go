package app

// outbox_drain.go — delivering prompts spooled by `atrium send`.
//
// The external command is a pure producer: it drops a message into <data
// dir>/outbox and exits, never touching state.json, because that file has
// exactly one writer at any instant. This is the consumer side. It runs on the
// Bubble Tea update goroutine — the only place model state may be mutated — so
// the TUI remains that single writer and the message never races a save.

import (
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/ZviBaratz/atrium/internal/outbox"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui"
)

// outboxDrainBudget caps how many messages one tick delivers. A backlog — a
// script that spooled hundreds while the TUI was closed — is drained across
// successive ticks instead of blocking the UI goroutine on one of them.
const outboxDrainBudget = 50

// rejectionSweepInterval is how often the receipt GC actually walks the spools. The
// horizon it enforces is 24 hours, so running it on every ~500ms metadata tick — and
// twice per tick now that there are two spools to walk — spends around 350k directory
// reads a day (2/s × 2 dirs × 86400) to delete a file that may sit an extra minute
// (#546 is the standing reason idle work in this loop is worth a constant).
const rejectionSweepInterval = time.Minute

// sweepRejectionsOccasionally runs the receipt GC at most once per
// rejectionSweepInterval. The zero lastRejectionSweep makes the first tick after launch
// sweep, which is the one that matters: it is where a receipt left by a previous run,
// for a producer that never came back to read it, is finally collected.
func (m *home) sweepRejectionsOccasionally(now time.Time) {
	if now.Sub(m.lastRejectionSweep) < rejectionSweepInterval {
		return
	}
	m.lastRejectionSweep = now
	outbox.SweepRejections(now)
}

// drainOutbox delivers spooled prompts to their sessions and returns a command
// surfacing what happened, or nil when the spool was empty.
//
// Delivery goes through QueueFollowupPrompt, the same call the TUI's own
// quick-send uses, so a spooled prompt inherits its guarantee for free: a
// zero-valued queue clock means strictly idle-only delivery, never an injection
// mid-turn.
//
// One way this is a broader caller than quick-send: `s` can only target the
// selected session, which is necessarily past Loading, while a spooled message
// can name one that is still starting up or has never idled. That is still the
// right call rather than QueuePrompt's timeout valve — force-injecting into a
// startup banner is exactly what the idle-only rule exists to prevent — but it
// does mean a session whose agent never idles holds its prompt indefinitely.
// That prompt stays visible and cancelable: the queue overlay lists it, and
// `atrium ls` reports it as queued_prompts.
func (m *home) drainOutbox() tea.Cmd {
	// Before the read, not after it. The sweep collects receipts from BOTH spools, and
	// the create spool has no other collector — drainCreateRequests deliberately does
	// not sweep, on the grounds that this runs first on the same tick. Below the early
	// return that stops being true: one unreadable prompt directory would strand the
	// sweep for the life of the process while the create drain kept working normally,
	// leaking a receipt per refused `atrium new` with nothing left to collect them.
	now := time.Now()
	m.sweepRejectionsOccasionally(now)

	entries, err := outbox.List()
	if err != nil {
		log.ErrorLog.Printf("failed to read the outbox: %v", err)
		return nil
	}

	var spent, queued int
	var delivered []string
	rejected := map[string]string{} // path -> reason the sender should see

	for _, e := range entries {
		if m.outboxPoisoned[e.Path] {
			continue
		}
		if spent >= outboxDrainBudget {
			break
		}
		spent++

		switch {
		case e.Err != nil:
			// Unreadable, or from a newer atrium. Discarding is the only way out:
			// a file nobody can decode and nobody deletes would be re-read on
			// every tick forever. outbox.List only ever surfaces files matching
			// the spool's own name format, so this can only discard our own.
			log.ErrorLog.Printf("discarding an unreadable outbox message: %v", e.Err)
			rejected[e.Path] = "the message could not be read"

		case e.Message.Expired(now):
			age := now.Sub(e.Message.CreatedAt).Round(time.Minute)
			log.WarningLog.Printf("discarding an outbox message for %q: spooled %s ago, past the %s horizon",
				e.Message.Title, age, outbox.TTL)
			rejected[e.Path] = fmt.Sprintf("the message was spooled %s ago, past the %s horizon", age, outbox.TTL)

		default:
			// Matched on the (Title, Path) pair, never the title alone: titles are
			// unique only within a repo group, so a same-titled session in another
			// repo must never be the one that receives this.
			inst := m.findInstanceByIdentity(e.Message.Title, e.Message.Path)
			if inst == nil {
				log.WarningLog.Printf("discarding an outbox message for %q (%s): no such session",
					e.Message.Title, e.Message.Path)
				rejected[e.Path] = fmt.Sprintf("no session %q in %s — it may have been killed since the message was sent",
					e.Message.Title, e.Message.Path)
				continue
			}
			inst.QueueFollowupPrompt(e.Message.Text)
			delivered = append(delivered, e.Path)
			queued++
		}
	}

	// Persist before unlinking so a crash cannot lose a queued prompt that no
	// longer has a file behind it. A failure here is logged rather than retried:
	// the prompt is already live in the session's queue, and the TUI persists on
	// every subsequent mutation anyway, so leaving the file would only re-queue a
	// duplicate on the next tick.
	if queued > 0 {
		if err := m.persistInstances(); err != nil {
			log.ErrorLog.Printf("failed to persist prompts drained from the outbox: %v", err)
		}
	}
	for _, path := range delivered {
		m.discardSpoolFile(path, func() error { return outbox.Remove(path) })
	}
	for path, reason := range rejected {
		// Leaves a receipt so `send --wait` reports the failure instead of
		// reading the unlink as a successful delivery.
		m.discardSpoolFile(path, func() error { return outbox.Reject(path, reason) })
	}

	if queued == 0 {
		return nil
	}
	return m.flashNotice(queuedPromptsNotice(queued), ui.NoticeInfo)
}

// discardSpoolFile runs a spool-file removal and poisons the path if it fails.
//
// Poisoning rather than retrying is what keeps a persistent failure — a
// read-only spool, a permissions problem — from re-delivering the same prompt
// every tick for as long as the TUI runs. It is in-memory only, so the next
// launch re-tries the file rather than inheriting a verdict from what may have
// been transient; the cost is that a genuinely persistent failure re-delivers
// once per launch, which is the lesser of the two.
func (m *home) discardSpoolFile(path string, remove func() error) {
	if err := remove(); err != nil {
		log.ErrorLog.Printf("could not clear a drained outbox message, ignoring it for the rest of this run: %v", err)
		if m.outboxPoisoned == nil {
			m.outboxPoisoned = make(map[string]bool)
		}
		m.outboxPoisoned[path] = true
	}
}

func queuedPromptsNotice(n int) string {
	if n == 1 {
		return "queued 1 prompt from atrium send"
	}
	return fmt.Sprintf("queued %d prompts from atrium send", n)
}

// findInstanceByIdentity returns the loaded instance with this exact
// (Title, Path) pair, the composite key session.Storage matches on for the same
// reason: a title alone is ambiguous across repo groups.
func (m *home) findInstanceByIdentity(title, path string) *session.Instance {
	for _, inst := range m.list.GetInstances() {
		if inst.Title == title && inst.Path == path {
			return inst
		}
	}
	return nil
}
