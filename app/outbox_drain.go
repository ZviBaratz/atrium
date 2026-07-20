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

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ZviBaratz/atrium/internal/outbox"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui"
)

// outboxDrainBudget caps how many messages one tick delivers. A backlog — a
// script that spooled hundreds while the TUI was closed — is drained across
// successive ticks instead of blocking the UI goroutine on one of them.
const outboxDrainBudget = 50

// drainOutbox delivers spooled prompts to their sessions and returns a command
// surfacing what happened, or nil when the spool was empty.
//
// Delivery goes through QueueFollowupPrompt, the same call the TUI's own
// quick-send uses, so a spooled prompt inherits its guarantee for free: a
// zero-valued queue clock means strictly idle-only delivery, never an injection
// mid-turn.
func (m *home) drainOutbox() tea.Cmd {
	entries, err := outbox.List()
	if err != nil {
		log.ErrorLog.Printf("failed to read the outbox: %v", err)
		return nil
	}

	now := time.Now()
	var spent, queued int
	var consumed []string

	for _, e := range entries {
		if m.outboxPoisoned[e.Path] {
			continue
		}
		if spent >= outboxDrainBudget {
			break
		}
		spent++
		consumed = append(consumed, e.Path)

		switch {
		case e.Err != nil:
			// Unreadable, or from a newer atrium. Discarding is the only way out:
			// a file nobody can decode and nobody deletes would be re-read on
			// every tick forever. outbox.List only ever surfaces files matching
			// the spool's own name format, so this can only discard our own.
			log.ErrorLog.Printf("discarding an unreadable outbox message: %v", e.Err)

		case e.Message.Expired(now):
			log.WarningLog.Printf("discarding an outbox message for %q: spooled %s ago, past the %s horizon",
				e.Message.Title, now.Sub(e.Message.CreatedAt).Round(time.Minute), outbox.TTL)

		default:
			// Matched on the (Title, Path) pair, never the title alone: titles are
			// unique only within a repo group, so a same-titled session in another
			// repo must never be the one that receives this.
			inst := m.findInstanceByIdentity(e.Message.Title, e.Message.Path)
			if inst == nil {
				log.WarningLog.Printf("discarding an outbox message for %q (%s): no such session",
					e.Message.Title, e.Message.Path)
				continue
			}
			inst.QueueFollowupPrompt(e.Message.Text)
			queued++
		}
	}

	if len(consumed) == 0 {
		return nil
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
	for _, path := range consumed {
		if err := outbox.Remove(path); err != nil {
			// Poison it rather than retry. An unlink that keeps failing (a
			// read-only spool, a permissions problem) would otherwise re-deliver
			// the same prompt every tick, for as long as the TUI runs.
			log.ErrorLog.Printf("could not remove a drained outbox message, ignoring it for the rest of this run: %v", err)
			if m.outboxPoisoned == nil {
				m.outboxPoisoned = make(map[string]bool)
			}
			m.outboxPoisoned[path] = true
		}
	}

	if queued == 0 {
		return nil
	}
	return m.flashNotice(queuedPromptsNotice(queued), ui.NoticeInfo)
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
