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
			"queued and is delivered the next time one starts. Use --wait to block until it\n" +
			"has actually been queued on the session.\n\n" +
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

	if wait > 0 {
		return waitForDrain(spooled, wait)
	}
	if running, known := tuiRunning(); known && !running {
		_, _ = fmt.Fprintf(errOut,
			"warning: no atrium TUI is running, so nothing is delivering this yet; "+
				"it stays queued and is picked up the next time one starts\n")
	}
	return nil
}

// waitForDrain blocks until the spooled message has been accounted for.
//
// The drain unlinks the file whether it queued the prompt or threw it away — a
// session killed between resolve and drain is the realistic case — so the file's
// disappearance alone only means some Atrium consumed it. A discard therefore
// leaves a receipt naming the reason, and that is checked first: it is written
// before the message is unlinked, so a rejection can never be observed as a
// successful delivery.
func waitForDrain(path string, timeout time.Duration) error {
	return awaitSpool(path, timeout, spoolWaitCopy{
		refused: "atrium did not deliver the prompt",
		timedOut: fmt.Sprintf("waited %s and no atrium TUI picked the prompt up; it is still queued "+
			"in the outbox and will be delivered the next time one runs", timeout),
	})
}

// spoolWaitCopy is the wording awaitSpool cannot supply: what a refusal and a timeout
// are called for this particular command. Both are finished strings — the caller knows
// its own timeout, so nothing here has to be a format.
type spoolWaitCopy struct {
	refused  string
	timedOut string
}

// awaitSpool implements the completion protocol both `send --wait` and `new --wait`
// use. A rejection receipt is checked first — it is written before the request is
// unlinked, so a refusal can never be observed as a success — then the file's
// disappearance, then the deadline.
//
// Shared because the ordering is the correctness, and two copies of an ordering drift.
// What the two commands do differ about is what the disappearance *means* — for a
// prompt, that some Atrium consumed it; for a create, that the session exists (the
// drain holds the file until Start returns) — but that difference is in the callers'
// wording and in what they do afterwards, not in the protocol.
//
// A Stat error other than "not found" is a bad data dir, not a completion: it is
// reported rather than read as either outcome. Retrying to the deadline would turn a
// permissions problem into a timeout that blames the TUI.
func awaitSpool(path string, timeout time.Duration, wording spoolWaitCopy) error {
	deadline := time.Now().Add(timeout)
	for {
		if reason, ok := outbox.Rejection(path); ok {
			if err := outbox.ClearRejection(path); err != nil {
				log.ErrorLog.Printf("failed to clear an outbox rejection receipt: %v", err)
			}
			return fmt.Errorf("%s: %s", wording.refused, reason)
		}
		_, err := os.Stat(path)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return nil
		case err != nil:
			return fmt.Errorf("failed to read the outbox: %w", err)
		}
		if !time.Now().Before(deadline) {
			return errors.New(wording.timedOut)
		}
		time.Sleep(drainPollInterval)
	}
}
