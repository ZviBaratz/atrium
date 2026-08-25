package app

// repotrust.go — the create-time half of #814's per-repo trust ledger: the
// prompt. A repo whose create ref — the start point the session's worktree
// will actually check out, not literal HEAD — declares executable config
// (.atrium.json → repo_scripts) that the ledger does not grant gets asked
// ONCE, at create time, on the update thread — never mid-Start, where there is
// no surface to ask on. The answer only writes (or does not write) a grant;
// enforcement is session/repoconfig.go's, which re-hashes the worktree's own
// bytes at every use, so this dialog can be skipped (headless drain),
// declined, or raced without anything untrusted ever executing.
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
	"unicode"

	"github.com/ZviBaratz/atrium/internal/repotrust"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/repocfg"
	"github.com/ZviBaratz/atrium/session/git"
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

// repoTrustSeedPreview is how many entries of each seed list the dialog spells
// out. The lists are capped at repocfg.MaxRepoLocalSeedEntries, but that cap
// bounds git forks per materialization and is far past what fits a frame — so the
// dialog needs its own, much smaller one. Unlike the script
// entry there is no one-entry rule to lean on here, so the overflow is counted
// rather than hidden: the remedy for reading the rest is the file itself, which is
// in the user's own checkout and is the thing being granted.
const repoTrustSeedPreview = 3

// repoTrustSeedWidth is the cell budget for one whole rendered seed line, verb and
// overflow marker included. TestRepoTrustSeedLineFitsItsBudget measures the returned
// string against it, because prose here has now been wrong twice: a draft that capped
// each entry bounded n entries at n times the cap, and its replacement capped the
// joined sample while the verb and the overflow marker were appended outside the cap
// — 91 cells against a 48-cell box. Both wrapped, and two wrapped lists walked the
// centred dialog up until its top border cut the tab bar and, further down, until
// the decline hint left the frame. A confirmation the user cannot answer is worse
// than none, so the number is asserted rather than described.
// 46 is the dialog's usable text column at the 80-column floor, measured: the
// confirmation box renders 50 cells of inner width there, two of which are padding
// on each side. A wider budget wraps, and a wrapped seed line is a row the dialog's
// height budget never counted.
const repoTrustSeedWidth = 46

// repoTrustAssessment is the pure half, shared by the form and autoDispatch
// (the allExhausted split's pattern, #703: a headless create must not stage a
// modal to learn the answer): should creating at path stage the trust prompt,
// and with what material.
//
// base is the create's base branch ("" for the repo's current), because the
// file is read at the ref the worktree will actually check out — the form can
// pick a base outright, and update_base_on_create (default on) freshens to
// origin's tip, so literal HEAD can hold a different .atrium.json than the
// session materializes. Hashing HEAD there would grant one version and then
// immediately report the session's as "changed" (or skip the prompt for a
// file HEAD does not carry yet).
//
// False for a direct target (out of #814's scope — no worktree materializes
// anything), for a path git cannot assess, and for a repo whose config is
// absent, already granted, or declares nothing usable. The enforcement gate
// below the TUI refuses anything unproven regardless of what this says.
func (m *home) repoTrustAssessment(path string, direct bool, base string) (repotrust.Assessment, bool) {
	if direct {
		return repotrust.Assessment{}, false
	}
	ref := git.StartPointPreview(m.ctx, path, base, m.appConfig.GetUpdateBaseOnCreate())
	a, err := repotrust.AssessRepo(m.ctx, path, ref)
	if err != nil {
		return repotrust.Assessment{}, false
	}
	return a, a.WantsPrompt()
}

// confirmRepoTrust stages plan behind the trust prompt and dismisses the create
// form (when one is open — autoDispatch has none), stashing it as a restorable
// draft first, exactly as confirmOverCap does. Both trust answers proceed
// toward the spawn, but the spawn is not yet committed: finishSpawnGates can
// stage the over-cap/exhausted confirms next, whose DECLINE spawns nothing —
// without the stash, that decline (or a first-variant spawn failure) would
// have consumed the whole form for a session that never existed. The stash
// cannot double-create: every path that actually spawns ends in
// closeCreateForm, which clears it.
func (m *home) confirmRepoTrust(plan spawnPlan, a repotrust.Assessment) tea.Cmd {
	m.pendingTrust = &plan
	m.stashDirtyCreateForm()
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
	m.confirmationOverlay.SetConfirmLabel(repoTrustConfirmLabel(a))
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
// what a grant means. The verbs live in the key hint (see repoTrustConfirmLabel),
// per the voice rule in app_feedback.go.
//
// It says "runs" only when something runs. A file declaring nothing but
// carry_files/link_paths (#815) executes no command, and describing it as setup the
// user is about to run misstates the decision in BOTH directions: someone who
// declines because they will not run a stranger's script has actually declined a
// file copy they would have allowed, and someone who accepts thinks they approved
// one script when they approved the repo choosing which of their own gitignored
// files an agent reads and which of their trees it may write through. Those are
// different grants and the sentence has to be the one the file earns.
func repoTrustMessage(a repotrust.Assessment) string {
	what, consequence := "its own setup", "Trusting runs it, as you, in"
	if !repoTrustRuns(a) {
		what = "what its worktrees start with"
		consequence = "Trusting lets it copy and link those paths into"
	}
	if a.HasGrant {
		return fmt.Sprintf(
			"%s's %s has CHANGED since you trusted it:\n\n%s\n\n%s every new worktree of this repo until it changes again.",
			a.Root, repocfg.RepoLocalFileName, repoTrustSummary(a), consequence)
	}
	return fmt.Sprintf(
		"%s declares %s in %s:\n\n%s\n\n%s every new worktree of this repo until the file changes.",
		a.Root, what, repocfg.RepoLocalFileName, repoTrustSummary(a), consequence)
}

// repoTrustRuns reports whether granting this file would let a command execute.
// It is the entry's presence and nothing else: repocfg refuses an entry that
// configures nothing, so an entry that survived the parse declares at least one of
// setup_script / run_command / session_env / port_range — and the first two run
// while the other two are execution-adjacent enough that #814 withholds them
// together. The seed lists move files and make symlinks; no command comes from them.
func repoTrustRuns(a repotrust.Assessment) bool {
	return len(a.Local.Entries) > 0
}

// repoTrustConfirmLabel is the confirm key's hint, and it carries the same
// distinction the body does: offering to "run setup" for a file that runs nothing
// asks for consent to the wrong thing, and it is the half a user who reads only the
// key hints will act on.
func repoTrustConfirmLabel(a repotrust.Assessment) string {
	if repoTrustRuns(a) {
		return "trust and run setup"
	}
	return "trust this repo"
}

// repoTrustSummary renders what the file declares: the entry's name and the
// surfaces it configures, the first line of its setup script, and the two seed
// lists (#815). The surface names come from repocfg.RepoLocalSurfaces — the same
// list `atrium trust allow` prints and enforcement requires non-empty, so the
// dialog cannot describe a file the gate would treat as absent, nor stay silent
// about a half of it the grant would apply.
//
// The entry has no "+N more": ParseRepoLocal's one-entry rule means the entry
// shown here IS the entry that runs. The seed lists have no such rule, so they
// name their overflow instead of hiding it — the count in the surface line is
// exact, and every entry is readable in the file the grant is over.
func repoTrustSummary(a repotrust.Assessment) string {
	surfaces := repocfg.RepoLocalSurfaces(a.Local)
	if len(surfaces) == 0 {
		return ""
	}
	// A seed-only file has no entry, so there is no name slot to fill and leading
	// with the surfaces is the honest shape. "unnamed entry" would send the reader
	// hunting for an entry that is not there, and echoing the filename — which the
	// sentence two lines above already names — reads both as a stutter and as an
	// entry CALLED .atrium.json.
	line := strings.Join(surfaces, " + ")
	if len(a.Local.Entries) > 0 {
		name := sanitizeRepoText(a.Local.Entries[0].Name, 24)
		if name == "" {
			name = "unnamed entry"
		}
		line = name + " · " + line
	}
	if len(a.Local.Entries) > 0 {
		if script := strings.TrimSpace(a.Local.Entries[0].SetupScript); script != "" {
			first := script
			if idx := strings.IndexByte(first, '\n'); idx >= 0 {
				first = strings.TrimSpace(first[:idx]) + " …"
			}
			line += "\n" + sanitizeRepoText(first, repoTrustPreviewWidth)
		}
	}
	line += repoTrustSeedLine("copies in", a.Local.CarryFiles)
	line += repoTrustSeedLine("links in", a.Local.LinkPaths)
	return line
}

// repoTrustSeedLine spells out one seed list, bounded in both directions: each
// entry sanitized and width-capped, and the list itself truncated to
// repoTrustSeedPreview with the remainder counted rather than dropped. The verbs
// say the direction that matters — a copy is private to the session, a link is
// the user's own tree under another name, writable by the agent.
func repoTrustSeedLine(verb string, entries []string) string {
	if len(entries) == 0 {
		return ""
	}
	shown := entries
	suffix := ""
	if len(shown) > repoTrustSeedPreview {
		shown = shown[:repoTrustSeedPreview]
		// Short on purpose: the exact total is already in the surfaces line above and
		// the file is named twice in the body, so this only has to say the sample is a
		// sample. A longer marker buys nothing and costs the sample its room.
		suffix = fmt.Sprintf(" +%d more", len(entries)-len(shown))
	}
	prefix := verb + ": "
	// The sample gets what is left after the fixed parts, so the WHOLE line is what
	// repoTrustSeedWidth bounds. The floor keeps a degenerate budget from producing a
	// line that is all marker and no content; it can exceed the width only if the verb
	// and marker alone already do, which no caller here comes close to and
	// TestRepoTrustSeedLineFitsItsBudget would catch.
	budget := max(repoTrustSeedWidth-runewidth.StringWidth(prefix)-runewidth.StringWidth(suffix), 8)
	// Sanitize the JOIN, not each entry: n entries each inside their own cap are n
	// times the cap.
	return "\n" + prefix + sanitizeRepoText(strings.Join(shown, ", "), budget) + suffix
}

// sanitizeRepoText makes repo-authored text safe to interpolate into a frame:
// any rune that is not plainly printable becomes '·', and the result is
// truncated to a cell budget. Width-bounding alone is not enough, and
// sanitizing alone is not enough; this is both, in that order, so the
// truncation measures what will actually render.
//
// "Not plainly printable" is customcmd's user-text rule (!IsPrint, plus the
// Mn/Me combining marks), not a bare C0 check, because the runes that defeat
// each half of this function are wider than C0: ESC and the C1 set (U+009B is
// an 8-bit CSI) would write straight through lipgloss into the terminal; and
// Cf runes — U+202E RIGHT-TO-LEFT OVERRIDE visually reversing the one command
// this dialog exists to let the user read, zero-width spaces/joiners — measure
// ZERO cells, so runewidth.Truncate's budget bounds nothing on a string made
// of them. Replacing with a 1-cell '·' makes every byte of hostile input
// measurable again, which is what makes the truncation a real bound.
func sanitizeRepoText(s string, width int) string {
	cleaned := strings.Map(func(r rune) rune {
		if !unicode.IsPrint(r) || unicode.In(r, unicode.Mn, unicode.Me) {
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
