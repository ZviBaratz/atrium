package app

// repotrust.go — the create-time half of #814's per-repo trust ledger: the
// prompt. A repo whose HEAD declares executable config (.atrium.json →
// repo_scripts) that the ledger does not grant gets asked ONCE, at create
// time, on the update thread — never mid-Start, where there is no surface to
// ask on. The answer only writes (or does not write) a grant; enforcement is
// session/repoconfig.go's, which re-hashes the worktree's own bytes at every
// use, so this dialog can be skipped (headless drain), declined, or raced
// without anything untrusted ever executing.
//
// Declining is NOT a cancel: both answers create the session, and only the
// grant differs — an untrusted repo is still a workable cold worktree (#629's
// Q4). That inversion of every other create-staged dialog's decline is why
// the overlay's cancel hint is relabeled and why this is the one caller of
// armOnDecline.

import (
	"fmt"
	"strings"
	"time"

	"github.com/ZviBaratz/atrium/internal/repotrust"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/repocfg"
	"github.com/ZviBaratz/atrium/ui"

	tea "charm.land/bubbletea/v2"
	"github.com/mattn/go-runewidth"
)

// proceedRepoTrustMsg is emitted when the repo-trust prompt is answered — by
// EITHER key: confirming granted first (armOnConfirm), declining granted
// nothing. Its Update handler runs the remaining spawn gates on the staged
// pendingTrust plan.
type proceedRepoTrustMsg struct{}

// repoTrustPreviewWidth bounds the script preview interpolated into the
// dialog. Nothing caps a setup_script in the repo's file, so the ceiling has
// to be here — the same argument, and a tighter figure, than
// customCommandDialogDescWidth's: this dialog carries the repo path and three
// more sentences, so its budget for repo-authored text is smaller.
const repoTrustPreviewWidth = 120

// repoTrustAssessment is the pure half, shared by the form, autoDispatch and
// the headless drain (the allExhausted split's pattern, #703: a headless
// create must not stage a modal to learn the answer): should creating at path
// stage the trust prompt, and with what material.
//
// False for a direct target (out of #814's scope — no worktree materializes
// anything), for a path git cannot assess, and for a repo whose config is
// absent, already granted, or declares nothing usable. The enforcement gate
// below the TUI refuses anything unproven regardless of what this says.
func (m *home) repoTrustAssessment(path string, direct bool) (repotrust.Assessment, bool) {
	if direct {
		return repotrust.Assessment{}, false
	}
	a, err := repotrust.AssessRepo(m.ctx, path)
	if err != nil {
		return repotrust.Assessment{}, false
	}
	return a, a.WantsPrompt()
}

// confirmRepoTrust stages plan behind the trust prompt and dismisses the create
// form (when one is open — autoDispatch has none). Unlike confirmOverCap
// nothing is stashed as a draft: BOTH answers spawn, and a restorable draft of
// an already-created session would double-create on restore.
func (m *home) confirmRepoTrust(plan spawnPlan, a repotrust.Assessment) tea.Cmd {
	m.pendingTrust = &plan
	m.textInputOverlay = nil
	m.menu.SetState(ui.StateDefault)
	m.resetTitleCheck()

	proceed := func() tea.Msg { return proceedRepoTrustMsg{} }
	cmd := m.confirmAction(repoTrustMessage(a), instantAction, proceed)
	m.armOnConfirm(func() {
		if err := repotrust.Grant(a.Key, a.Hash, a.Remote, time.Now()); err != nil {
			// The create still proceeds. Enforcement re-reads the ledger, finds no
			// grant, and keeps the config inert WITH its notice — a failed write is
			// loud downstream rather than silently trusted here.
			log.ErrorLog.Printf("repo-trust grant for %s failed: %v", a.Root, err)
		}
	})
	m.armOnDecline(proceed)
	m.confirmationOverlay.SetConfirmLabel("trust and run setup")
	// Decline proceeds, so the stock "cancel" would promise an abort this dialog
	// does not perform.
	m.confirmationOverlay.SetCancelLabel("create without it")
	// Deliberately no armDoubleTap, for both reasons doubleTapDialogs' docstring
	// enumerates this dialog under: it is staged by a message rather than a key
	// (nothing to echo), and it exists to state a fact — what the repo wants to
	// run — that a reflex confirm would grant unread.
	return cmd
}

// repoTrustMessage is the dialog body: whose config, what it declares (bounded
// — repo-authored text never reaches the frame unsanitized or unmeasured), and
// what a grant means. The verbs live in the key hint (trust and run setup /
// create without it), per the voice rule in app_feedback.go.
func repoTrustMessage(a repotrust.Assessment) string {
	if a.HasGrant {
		return fmt.Sprintf(
			"%s's %s has CHANGED since you trusted it:\n\n%s\n\nTrusting runs the new version in every new worktree of this repo until it changes again.",
			a.Root, repocfg.RepoLocalFileName, repoTrustSummary(a))
	}
	return fmt.Sprintf(
		"%s declares its own setup in %s:\n\n%s\n\nTrusting runs it, as you, in every new worktree of this repo until the file changes.",
		a.Root, repocfg.RepoLocalFileName, repoTrustSummary(a))
}

// repoTrustSummary renders what the file's governing entry declares: its name,
// the surfaces it configures, and the first line of the setup script.
func repoTrustSummary(a repotrust.Assessment) string {
	if len(a.Entries) == 0 {
		return ""
	}
	e := a.Entries[0]
	name := sanitizeRepoText(e.Name, 24)
	if name == "" {
		name = "unnamed entry"
	}
	var declares []string
	if strings.TrimSpace(e.SetupScript) != "" {
		declares = append(declares, "setup script")
	}
	if strings.TrimSpace(e.RunCommand) != "" {
		declares = append(declares, "run command")
	}
	if len(e.SessionEnv) > 0 {
		declares = append(declares, "session env")
	}
	if strings.TrimSpace(e.PortRange) != "" {
		declares = append(declares, "port range")
	}
	line := name + " · " + strings.Join(declares, " + ")
	if extra := len(a.Entries) - 1; extra > 0 {
		line += fmt.Sprintf(" (+%d more)", extra)
	}
	if script := strings.TrimSpace(e.SetupScript); script != "" {
		first := script
		if idx := strings.IndexByte(first, '\n'); idx >= 0 {
			first = strings.TrimSpace(first[:idx]) + " …"
		}
		line += "\n" + sanitizeRepoText(first, repoTrustPreviewWidth)
	}
	return line
}

// sanitizeRepoText makes repo-authored text safe to interpolate into a frame:
// control runes (ESC first among them — an escape sequence in a script would
// otherwise write straight through lipgloss into the terminal) become '·', and
// the result is truncated to a cell budget. Width-bounding alone is not
// enough, and sanitizing alone is not enough; this is both, in that order, so
// the truncation measures what will actually render.
func sanitizeRepoText(s string, width int) string {
	cleaned := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return '·'
		}
		return r
	}, s)
	return runewidth.Truncate(cleaned, width, "…")
}

// finishSpawnGates runs the remaining pre-spawn gates — all-exhausted, then
// the host-capacity soft cap — and spawns. It is the shared tail of the create
// paths: called in line when no trust prompt staged, and from the
// proceedRepoTrustMsg handler when one resolved. The order is
// createSessionFromForm's, for its recorded reason: neither accept path
// re-checks the other gate, so the hard cap must already be settled and the
// exhausted gate must stage ahead of the soft cap.
//
// The cap is recomputed here rather than carried through the trust stage, so a
// fleet that changed while that dialog sat open is measured as it is now. That
// gives the hard cap a second look too: a block that appeared meanwhile
// refuses with a notice (the form is already gone) rather than spawning past
// the user's explicit limit.
func (m *home) finishSpawnGates(plan spawnPlan) tea.Cmd {
	sc := m.sessionCap()
	count := m.capCount(sc)
	verdict := capVerdict(sc, count, len(plan.programs))
	if verdict == capBlock {
		free := max(0, sc.Limit-count)
		return m.flashNotice(fmt.Sprintf("need %d, %d free (max_sessions)", len(plan.programs), free), ui.NoticeError)
	}
	if cmd, gated := m.gateAllExhausted(plan); gated {
		return cmd
	}
	if verdict == capConfirm {
		return m.confirmOverCap(plan, sc.Limit, count)
	}
	return m.spawnVariants(plan)
}
