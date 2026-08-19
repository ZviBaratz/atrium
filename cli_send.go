package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"time"

	"github.com/ZviBaratz/atrium/internal/outbox"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/session"

	"github.com/spf13/cobra"
)

// drainPollInterval is how often --wait re-checks for the spooled file. The TUI
// drains on its ~500ms metadata tick, so a shorter interval only adds syscalls.
const drainPollInterval = 100 * time.Millisecond

var (
	sendWaitFlag time.Duration
	sendPathFlag string

	sendCmd = &cobra.Command{
		Use:   "send <session> [message]",
		Short: "Queue a prompt for a session",
		Long: "Queues a prompt for a session, delivered on the same terms as the TUI's own\n" +
			"quick-send: strictly when the agent next goes idle, never injected mid-turn.\n\n" +
			"Delivery is asynchronous. The message is spooled to the data directory and the\n" +
			"running Atrium picks it up within about a second; with no Atrium running it stays\n" +
			"queued and is delivered the next time one starts. An Atrium that is running but\n" +
			"has handed its terminal to a session is a third case: its poll loop is parked, so\n" +
			"the prompt waits for the detach rather than for a relaunch, and both the stderr\n" +
			"warning and --wait say so. Use --wait to block until it has actually been queued\n" +
			"on the session.\n\n" +
			"With no message argument, or with \"-\", the prompt is read from stdin.",
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			log.Initialize(logDir(), false)
			defer log.Close()

			text, err := messageText(args, cmd.InOrStdin())
			if err != nil {
				return err
			}
			return runSend(cmd.OutOrStdout(), cmd.ErrOrStderr(), args[0], sendPathFlag, text, sendWaitFlag)
		},
	}
)

// messageText returns the prompt to send: the second argument, or stdin when
// that argument is absent or "-". Reading from stdin is what makes multi-line
// prompts practical to pipe in from a script.
func messageText(args []string, stdin io.Reader) (string, error) {
	if len(args) > 1 && args[1] != "-" {
		return args[1], nil
	}
	data, err := io.ReadAll(stdin)
	if err != nil {
		return "", fmt.Errorf("failed to read the prompt from stdin: %w", err)
	}
	return string(data), nil
}

// runSend spools a prompt for the session named by selector.
//
// It never writes state.json. That file has exactly one writer at any instant —
// the TUI holding tui.lock, or the autoyes daemon in the window where no TUI is
// alive — and both rewrite it whole from their own view of the instance list, so
// an outside append would be clobbered rather than merged. Spooling instead
// keeps this process a pure producer and leaves delivery to whoever legitimately
// owns the write.
func runSend(out, errOut io.Writer, selector, path, text string, wait time.Duration) error {
	if wait < 0 {
		// Cobra parses "--wait -5s" happily. Left to the wait > 0 test below it would
		// silently mean "do not wait", so a caller that fat-fingered a sign would be told
		// the prompt was queued and never learn what became of it — and a CI job scripted
		// to gate on `send --wait` would pass without ever having waited. Same guard, same
		// reason, as runNew's.
		return fmt.Errorf("--wait %s is negative; pass a positive duration", wait)
	}
	instances, err := loadStoredInstances()
	if err != nil {
		return err
	}
	target, err := resolveSession(instances, selector, path)
	if err != nil {
		return err
	}

	// Trailing newlines are an artifact of how the text arrived (a heredoc, a
	// pipe); leading whitespace could be meaningful, so only the tail is trimmed.
	text = strings.TrimRight(text, "\r\n")
	if strings.TrimSpace(text) == "" {
		return errors.New("refusing to send an empty prompt")
	}

	spooled, err := outbox.Write(outbox.Message{
		Title:    target.Title,
		Path:     target.Path,
		TmuxName: target.TmuxName,
		Text:     text,
	})
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "queued for %s\n", target.Title)

	// Queuing onto a paused session is deliberate rather than an error: the queue
	// is persisted state, so the prompt survives and is delivered after a resume.
	// Say so, because "queued" alone would imply it is about to be answered.
	if target.Status == session.Paused {
		_, _ = fmt.Fprintf(errOut, "note: %q is paused — the prompt waits until you resume it\n", target.Title)
	}

	warnSpoolWaiting(errOut, "delivering", wait > 0)
	if wait > 0 {
		return waitForDrain(spooled, wait)
	}
	return nil
}

// waitForDrain blocks until the spooled message has been accounted for.
//
// The drain unlinks the file whether it queued the prompt or threw it away — a
// session killed between resolve and drain is the realistic case — so the file's
// disappearance alone only means some Atrium consumed it. A discard therefore
// leaves a receipt naming the reason; awaitSpool owns the order the two are
// sampled in, which is what keeps a discard from reading as a delivery.
func waitForDrain(path string, timeout time.Duration) error {
	// No in-flight companion: the prompt spool has no claim step, because queuing a
	// prompt IS the whole job and the drain unlinks as soon as it has done it.
	return awaitSpool(path, "", timeout, spoolWaitCopy{
		refused: "atrium did not deliver the prompt",
		// The old wording ended "and will be delivered the next time one runs", which is
		// false of the case that is easiest to be in: an atrium that is running but has
		// handed its terminal to a session delivers on the detach, not on a relaunch.
		// drainerClause says which of the three it actually is, or nothing, in which case
		// this enumerates rather than asserts.
		timedOut: func() string {
			return joinTimedOut(fmt.Sprintf("waited %s and no atrium TUI picked the prompt up; "+
				"it is still queued in the outbox", timeout),
				"A running atrium delivers it on its next tick, or on detach if its terminal is "+
					"handed to a session; otherwise the next one to start does")
		},
	})
}

// betweenSpoolSamples runs between awaitSpool's two samples. A no-op in production; a
// var on config.detectAgentCommand's precedent, because the ORDER of those samples is
// the whole invariant and nothing outside this function can force the window between
// them — the drain landing a Reject there is a race of microseconds, so a test that
// only sets up the resulting state proves nothing about which sample sees it first.
var betweenSpoolSamples = func() {}

// betweenSpoolStats is betweenSpoolSamples for the other ordering spoolSettled owns:
// the windows between its stats, where a drain claiming a request and a reconcile
// re-queuing one land. Same reason for existing — the interleaving is microseconds
// wide, and a test that only arranges the end state cannot tell a correct
// implementation from one that reads the files the other way round.
//
// It fires at both windows (record→companion, companion→record), so a test that stages
// exactly one rename must make its hook idempotent or count its calls; both directions
// are staged in cli_new_test.go.
var betweenSpoolStats = func() {}

// spoolSettled reports whether a spooled record has reached a terminal state: the record
// itself gone, and — for a create, whose drain renames the record to a claim for the
// whole of the session build — its in-flight companion gone too. inFlight is "" for a
// spool that has no such step.
//
// The stat order is the correctness, and it is the record FIRST. Once the record is
// absent the request is either claimed or finished, and the companion stat separates
// those two. Reversed, the race is lost outright: the companion is read absent (not
// claimed yet), the drain claims, the record is then read absent too, both look gone,
// and a build that has not begun is reported to `atrium new --wait` as a created
// session, exit 0. That window is real rather than theoretical because outbox.Claim
// commits the rename atomically within one directory — there is no instant where both
// paths are absent mid-claim, so record-first cannot see a false gap there.
//
// The rename runs the OTHER WAY TOO, and no ordering survives both. outbox.Requeue —
// the startup reconcile handing an interrupted build back to the drain — renames the
// claim onto the record, so record-first loses that one for the mirror-image reason:
// the record is read absent (still a claim), Requeue renames, the claim is read absent
// too, and a request that is queued and about to be built normally is reported as
// created. Companion-first would lose the Claim direction instead.
//
// So the record is read TWICE, and both gaps close. A rename in either direction leaves
// the file at one of the two names at every instant, so "record absent, companion
// absent, record still absent" cannot be satisfied while either rename is in flight:
// the Claim direction is ruled out by the first read (nothing to rename away), and the
// Requeue direction by the third (the record is back). The re-read costs one stat on
// the settled path only — the polling loop's other iterations return at the first
// switch.
//
// A stat that could not answer is returned rather than folded into either verdict; the
// caller carries it to its deadline for awaitSpool's reason.
func spoolSettled(record, inFlight string) (bool, error) {
	gone := func(path string) (bool, error) {
		switch _, err := os.Stat(path); {
		case err == nil:
			return false, nil
		case !errors.Is(err, fs.ErrNotExist):
			return false, err
		}
		return true, nil
	}

	switch ok, err := gone(record); {
	case err != nil:
		return false, err
	case !ok:
		return false, nil // still queued
	}
	if inFlight == "" {
		return true, nil
	}
	betweenSpoolStats()
	switch ok, err := gone(inFlight); {
	case err != nil:
		return false, err
	case !ok:
		return false, nil // claimed: being built right now
	}
	betweenSpoolStats()
	return gone(record)
}

// spoolWaitCopy is the wording awaitSpool cannot supply: what a refusal and a timeout
// are called for this particular command. Both are finished strings — the caller knows
// its own timeout, so nothing here has to be a format.
type spoolWaitCopy struct {
	refused string
	// timedOut is called at the deadline, not built before the wait. It names who is
	// draining (drainerClause), and that answer is only true of the instant it is
	// asked: a user who detached during the wait has changed it, and reporting the
	// state the wait STARTED in would blame a condition that has since cleared.
	timedOut func() string
}

// joinTimedOut completes a deadline message with what is actually draining, falling
// back to fallback when nothing can be determined.
//
// The fallback is an enumeration of every case rather than a claim about one, which is
// what both messages said unconditionally before there was any way to tell them apart
// — correct, and useless to a caller trying to work out whether to wait longer or go
// and detach.
func joinTimedOut(head, fallback string) string {
	clause := drainerClause()
	if clause == "" {
		clause = fallback
	}
	return head + ". " + clause
}

// awaitSpool implements the completion protocol both `send --wait` and `new --wait`
// use: a rejection receipt means refused, the record settling (see spoolSettled) means
// done, and the deadline means still queued.
//
// The sampling order is the correctness. Reject writes the receipt and then unlinks
// the record, which guarantees only "if the record is gone, the receipt is already
// there" — so the record's absence becomes conclusive only once the receipt has been
// checked AFTER it. Sampling the receipt once, up front, loses the race outright:
// receipt absent, then the drain completes both the write and the unlink, then Stat
// returns ENOENT and a refusal for a taken title or a full cap is reported as a created
// session, exit 0. The leading check below is only a fast path; the one that closes the
// window is the re-read inside the settled arm. spoolSettled owns the second ordering,
// between the record and its in-flight companion.
//
// Shared because two copies of an ordering drift. What the two commands differ about is
// what settling *means* — for a prompt, that some Atrium consumed it; for a create,
// that the session exists *and is recorded*, since the drain holds the request as a
// claim until Start has completed and the row has been persisted — but that difference
// is in the callers' wording, in the inFlight path they pass, and in what they do
// afterwards, not in the protocol.
//
// A Stat error other than "not found" is never read as either outcome. It is also not
// returned on the spot: persistence is the only thing that separates a bad data dir
// from a network mount's one-off ESTALE, and persistence is what the deadline already
// measures. So it is carried, and reported at the deadline in place of
// wording.timedOut — which would otherwise blame a TUI that was never the problem.
func awaitSpool(path, inFlight string, timeout time.Duration, wording spoolWaitCopy) error {
	refused := func() error {
		reason, ok := outbox.Rejection(path)
		if !ok {
			return nil
		}
		if err := outbox.ClearRejection(path); err != nil {
			log.ErrorLog.Printf("failed to clear an outbox rejection receipt: %v", err)
		}
		return fmt.Errorf("%s: %s", wording.refused, reason)
	}

	deadline := time.Now().Add(timeout)
	var lastStatErr error
	for {
		if err := refused(); err != nil {
			return err
		}
		betweenSpoolSamples()
		settled, err := spoolSettled(path, inFlight)
		switch {
		case settled:
			// The record went away between the two samples, and nothing is holding it
			// in flight; the receipt for it, if there is one, is on disk by now.
			if err := refused(); err != nil {
				return err
			}
			return nil
		default:
			// Either nil — the record is still there, or is claimed and being built —
			// or a Stat that could not answer, which is carried to the deadline rather
			// than returned from this sample.
			//
			// The two cannot be told apart at one sample, and only one of them should end
			// the wait. A data dir on NFS, sshfs or a container bind mount answers a
			// single Stat with ESTALE or EIO and the next one fine; returning on the first
			// would fail a CI job for a record the TUI drains half a second later. A real
			// permissions problem does not clear, so it survives to the deadline and is
			// reported there — with its own error rather than wording.timedOut, which
			// blames a TUI that was never the problem. Assigning nil here is what clears a
			// transient one, so a later honest timeout is not reported as a stale EIO.
			lastStatErr = err
		}
		if !time.Now().Before(deadline) {
			if lastStatErr != nil {
				return fmt.Errorf("failed to read the outbox: %w", lastStatErr)
			}
			return errors.New(wording.timedOut())
		}
		time.Sleep(drainPollInterval)
	}
}
