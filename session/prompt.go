package session

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ZviBaratz/atrium/session/tmux"
)

// Prompt delivery: the type → confirm-landed → Enter → confirm-cleared state
// machine that delivers a queued initial prompt into the agent, with idempotent
// retry via the soft-error sentinels (see IsSoftPromptError).

// Soft prompt-delivery outcomes: the pane was not (yet) in a state to accept or to confirm
// the prompt. These are not failures — the prompt stays queued and the next metadata tick
// retries — so the app layer distinguishes them (via IsSoftPromptError) from a hard tmux
// error, which it surfaces to the user.
var (
	errPromptNotReady     = errors.New("agent not awaiting input")
	errPromptNotLanded    = errors.New("prompt did not land in the input box")
	errPromptNotSubmitted = errors.New("prompt was typed but not submitted")
)

// IsSoftPromptError reports whether err from SendPrompt is a retryable soft outcome (the
// pane was not ready, or delivery could not be confirmed) rather than a hard send failure.
// On a soft outcome the caller should keep the prompt queued and let the next tick retry;
// SendPrompt is idempotent, so a retry never doubles the prompt.
func IsSoftPromptError(err error) bool {
	return errors.Is(err, errPromptNotReady) ||
		errors.Is(err, errPromptNotLanded) ||
		errors.Is(err, errPromptNotSubmitted)
}

// promptSignatureMax caps the landing-confirmation anchor (see promptSignature) at a length
// that comfortably fits one composer row on any reasonable pane width, so the anchor itself
// never wraps and the squashed-whitespace match stays exact.
const promptSignatureMax = 40

// promptVerifyInterval spaces the post-type and post-submit pane re-captures while
// confirming delivery. A var so tests can zero it and stay fast.
var promptVerifyInterval = 100 * time.Millisecond

// promptLandAttempts / promptSubmitAttempts bound how long SendPrompt waits (in
// promptVerifyInterval steps) for the typed text to appear in the box, and for it to clear
// after Enter, before returning a soft error that defers to the next tick.
const (
	promptLandAttempts   = 5
	promptSubmitAttempts = 5
)

// squashSpace removes all whitespace from s. The input-box readback flattens the composer's
// wrapped rows by joining them with spaces, and a terminal wrap can fall mid-word; comparing
// both the readback and the signature with all whitespace removed makes the landing check
// immune to wherever the box wrapped the text.
func squashSpace(s string) string {
	return strings.Join(strings.Fields(s), "")
}

// promptSignature is the recognizable anchor used to confirm a queued prompt actually
// reached the composer: the first non-empty line, whitespace-squashed and capped. It is
// matched (as a substring) against the squashed input-box readback. Empty only for an
// all-blank prompt, which is never queued.
func promptSignature(prompt string) string {
	for _, line := range strings.Split(prompt, "\n") {
		sq := squashSpace(line)
		if sq == "" {
			continue
		}
		if r := []rune(sq); len(r) > promptSignatureMax {
			sq = string(r[:promptSignatureMax])
		}
		return sq
	}
	return ""
}

// boxHoldsPrompt reports whether the composer currently holds this prompt: either its first-line
// signature is visible (inline text — single-line, or a multi-line paste the agent did not
// collapse), or — for a multi-line prompt the agent collapsed into a placeholder chip — a
// collapsed-paste chip is present. Without this, a long prompt that claude renders as
// "[Pasted text +N lines]" never confirms as landed, so it is never submitted and is re-pasted on
// every retry (see SendPrompt).
//
// A collapsed-paste chip carries no prompt text to match, so it is trusted only for a multi-line
// prompt (the bracketed-paste path) and only because it is our own staged paste: SendPrompt is the
// sole writer of an awaiting-input composer, which starts empty when delivery begins, so a chip
// seen mid-delivery is the paste this call — or a prior not-yet-confirmed retry — placed, never a
// stray unrelated one. A single-line send always uses the exact signature.
//
// It reads the box once and classifies that single readback for both signals, so a poll costs one
// capture rather than a signature capture plus a separate chip capture.
func boxHoldsPrompt(ts *tmux.Session, prompt, sig string) bool {
	text, ok := ts.InputBoxText()
	if !ok {
		return false
	}
	if sig != "" && strings.Contains(squashSpace(text), sig) {
		return true
	}
	return strings.Contains(prompt, "\n") && ts.IsCollapsedPaste(text)
}

// confirmBox polls pred up to attempts times, spaced by promptVerifyInterval, returning
// true on the first satisfied check. It gives the agent's TUI a moment to repaint after a
// paste or an Enter before SendPrompt concludes whether delivery was confirmed.
func confirmBox(pred func() bool, attempts int) bool {
	for k := 0; k < attempts; k++ {
		if pred() {
			return true
		}
		time.Sleep(promptVerifyInterval)
	}
	return false
}

// typePrompt enters the prompt text into the composer without submitting it. A multi-line
// prompt is pasted as one bracketed-paste block (so the agent does not submit on the first
// newline and drop the rest); a single-line prompt is typed literally.
func (i *Instance) typePrompt(ts *tmux.Session, prompt string) error {
	if strings.Contains(prompt, "\n") {
		if err := ts.SendPasted(prompt); err != nil {
			return fmt.Errorf("error pasting prompt to tmux session: %w", err)
		}
		return nil
	}
	if err := ts.SendKeys(prompt); err != nil {
		return fmt.Errorf("error sending keys to tmux session: %w", err)
	}
	return nil
}

// SendPrompt delivers a queued initial prompt into the agent and submits it, verifying each
// step so the prompt is never silently dropped onto the wrong screen. It:
//
//  1. confirms the agent is awaiting input (else returns a soft error to retry next tick);
//  2. types the prompt — unless a prior attempt already staged it in the box — as a paste
//     for multi-line text or literal keys for a single line;
//  3. confirms the text landed in the box before submitting (soft error if it never does);
//  4. presses Enter and confirms the box cleared (soft error if it did not submit).
//
// It is idempotent across the common soft-failure paths: step 2 is skipped when the box
// already holds the prompt, so a retry after a not-yet-submitted attempt re-submits rather
// than re-types. The one residual doubling window is a submit that actually succeeded but
// whose post-Enter confirmation (step 4) timed out before the box repainted as cleared: the
// next attempt then sees an empty box and re-types. That needs the box to clear later than
// promptSubmitAttempts*promptVerifyInterval after a successful Enter, which the agents we
// target do effectively instantly, so it is accepted rather than guarded. Hard tmux failures
// (dead pane, send-keys error) are returned wrapped for the caller to surface.
func (i *Instance) SendPrompt(prompt string) error {
	ts := i.tmux()
	if !i.isStarted() {
		return fmt.Errorf("instance not started")
	}
	if ts == nil {
		return fmt.Errorf("tmux session not initialized")
	}
	if !ts.AwaitingInput() {
		return errPromptNotReady
	}

	sig := promptSignature(prompt)
	// Skip typing if a previous attempt already staged this prompt in the box but could
	// not confirm its submission; retype only when the box does not already hold it.
	if !boxHoldsPrompt(ts, prompt, sig) {
		if err := i.typePrompt(ts, prompt); err != nil {
			return err
		}
		if !confirmBox(func() bool { return boxHoldsPrompt(ts, prompt, sig) }, promptLandAttempts) {
			return errPromptNotLanded
		}
	}

	if err := ts.TapEnter(); err != nil {
		return fmt.Errorf("error submitting prompt to tmux session: %w", err)
	}
	if !confirmBox(func() bool { return !boxHoldsPrompt(ts, prompt, sig) }, promptSubmitAttempts) {
		return errPromptNotSubmitted
	}
	return nil
}
