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

	"github.com/ZviBaratz/atrium/internal/repotrust"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/repocfg"
	"github.com/ZviBaratz/atrium/session/git"
	"github.com/ZviBaratz/atrium/ui"
	"github.com/ZviBaratz/atrium/ui/theme"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
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
// — 91 cells against the 46-cell column below. Both wrapped, and two wrapped lists
// walked the centred dialog up until its top border cut the tab bar and, further
// down, until the decline hint left the frame. A confirmation the user cannot answer is worse
// than none, so the number is asserted rather than described.
// 46 is the dialog's usable text column at the 80-column floor, measured: the
// confirmation box renders 50 cells of inner width there, two of which are padding
// on each side. A wider budget wraps, and a wrapped seed line is a row the dialog's
// height budget never counted.
const repoTrustSeedWidth = 46

// repoTrustPathWidth bounds the repo path the body names. The path is the user's
// own, not repo-authored, so it needs no sanitizing — but it is unbounded, and a
// deep worktree path is routinely long enough to wrap the header to six rows. That
// cost the dialog its bottom border once #815's two seed lines took the last of
// the height margin. Truncated from the LEFT, because the distinctive part of a
// path is its tail.
const repoTrustPathWidth = 46

// repoTrustPath fits a repo path into repoTrustPathWidth, keeping the tail.
func repoTrustPath(p string) string {
	if ansi.StringWidth(p) <= repoTrustPathWidth {
		return p
	}
	// Truncate from the left by taking the widest suffix that fits alongside the
	// marker, measured with the layout's own measurer.
	runes := []rune(p)
	for i := range runes {
		if suffix := string(runes[i:]); ansi.StringWidth(suffix)+1 <= repoTrustPathWidth {
			return "…" + suffix
		}
	}
	return "…"
}

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
	what := strings.Join(repoTrustDeclares(a), " and ")
	const untilFile = "every new worktree of this repo until the file changes."
	switch {
	case a.ScopeUpgrade:
		// NOT "changed": the bytes match the grant exactly. What changed is this
		// atrium, which now reads two keys the prompt that granted it could not
		// describe. Saying the file changed would send the reader to `git log` for an
		// edit nobody made.
		return fmt.Sprintf(
			"%s's %s is the file you trusted, but it also declares %s — which the version of atrium you trusted it on ignored:\n\n%s\n\n%s",
			repoTrustPath(a.Root), repocfg.RepoLocalFileName, strings.Join(repoTrustNewPowers(a), " and "),
			repoTrustSummary(a), repoTrustConsequence(a, untilFile))
	case a.HasGrant:
		return fmt.Sprintf(
			"%s's %s has CHANGED since you trusted it:\n\n%s\n\n%s",
			repoTrustPath(a.Root), repocfg.RepoLocalFileName, repoTrustSummary(a),
			repoTrustConsequence(a, "every new worktree of this repo until it changes again."))
	}
	return fmt.Sprintf(
		"%s declares %s in %s:\n\n%s\n\n%s",
		repoTrustPath(a.Root), what, repocfg.RepoLocalFileName, repoTrustSummary(a),
		repoTrustConsequence(a, untilFile))
}

// repoTrustRuns reports whether granting this file would let a COMMAND execute.
// It is not the entry's presence: repocfg admits an entry declaring only
// port_range or session_env (DeclaredSurfaces accepts four surfaces and #814
// withholds all of them together), and describing such a file as setup the user is
// about to run asks for consent to something that never happens. Only
// setup_script and run_command run.
func repoTrustRuns(a repotrust.Assessment) bool {
	if len(a.Local.Entries) == 0 {
		return false
	}
	e := a.Local.Entries[0].RepoScript
	return strings.TrimSpace(e.SetupScript) != "" || strings.TrimSpace(e.RunCommand) != ""
}

// repoTrustSeeds reports whether granting this file would let the repo decide which
// of the user's own gitignored files are copied into a worktree and which of their
// trees it may write through (#815). Independent of repoTrustRuns: a file can do
// both, and the mixed case is the one a binary got wrong — it named the script and
// left the seeding unmentioned.
func repoTrustSeeds(a repotrust.Assessment) bool {
	return len(a.Local.CarryFiles) > 0 || len(a.Local.LinkPaths) > 0
}

// repoTrustDeclares names what the file declares, for the "declares X in
// .atrium.json" clause. Composed rather than chosen, so a file that does two
// things is described as doing two things.
func repoTrustDeclares(a repotrust.Assessment) []string {
	var out []string
	if len(a.Local.Entries) > 0 {
		if repoTrustRuns(a) {
			out = append(out, "its own setup")
		} else {
			out = append(out, "its own session settings")
		}
	}
	if repoTrustSeeds(a) {
		out = append(out, "what its worktrees start with")
	}
	if len(out) == 0 {
		// Unreachable through the prompt (WantsPrompt requires a non-empty surface
		// list), but a message with an empty clause reads as a bug rather than as an
		// empty file, and repoTrustMessage is called directly by tests.
		out = append(out, "its own Atrium config")
	}
	return out
}

// repoTrustSeedClause names the seeding half of a grant, and only the halves this
// file actually has. The verbs say the direction that matters: a copy is private to
// the session, a link is the user's own tree under another name, writable by the
// agent and shared with every sibling session at once.
func repoTrustSeedClause(a repotrust.Assessment) string {
	var parts []string
	if len(a.Local.CarryFiles) > 0 {
		parts = append(parts, "copy those files in")
	}
	if len(a.Local.LinkPaths) > 0 {
		parts = append(parts, "link those paths through to your checkout")
	}
	return strings.Join(parts, " and ")
}

// repoTrustConsequence is what a grant actually DOES — the sentence the user is
// answering — ending on the preposition its caller's "every new worktree of this
// repo" completes.
//
// Written as whole sentences per combination rather than as clauses joined with
// commas. That is not style: a binary over three states is what let a mixed
// script+seed-list file (the shape the README's own example shows) be granted
// after being told only about the script, and the first fix for it produced
// "run it, as you,, and copy those files in … in in every new worktree" —
// fragment-gluing traded one wrong sentence for an unreadable one.
// tail is "every new worktree of this repo until …", which differs between a
// first grant and a re-grant, so each case reads its own grammar onto the same
// noun phrase instead of every case sharing one preposition.
func repoTrustConsequence(a repotrust.Assessment, tail string) string {
	carry, link := len(a.Local.CarryFiles) > 0, len(a.Local.LinkPaths) > 0
	seeds := repoTrustSeedClause(a)
	hasEntry := len(a.Local.Entries) > 0
	switch {
	case repoTrustRuns(a) && seeds != "":
		return "Trusting runs it as you, and lets it " + seeds + ", in " + tail
	case repoTrustRuns(a):
		return "Trusting runs it, as you, in " + tail
	case seeds != "" && hasEntry:
		return "Trusting applies it, and lets it " + seeds + ", in " + tail
	case carry && link:
		return "Trusting lets it " + seeds + ", in " + tail
	case carry:
		return "Trusting lets it copy those files into " + tail
	case link:
		return "Trusting lets it link those paths through to your checkout in " + tail
	}
	// An entry declaring only port_range/session_env: withheld with the rest (#814),
	// but nothing executes, so neither "runs" nor "copies" is the true verb.
	return "Trusting applies it to " + tail
}

// repoTrustNewPowers names only the powers a ScopeUpgrade grant does not yet
// cover, which is what that prompt is asking about. Today that is the seed lists
// alone — GrantVersionSeeds is the one version step — so this reads as a list for
// the next one rather than pretending the whole file is new.
func repoTrustNewPowers(a repotrust.Assessment) []string {
	var out []string
	if len(a.Local.CarryFiles) > 0 {
		out = append(out, "files to copy into every worktree")
	}
	if len(a.Local.LinkPaths) > 0 {
		out = append(out, "paths to link through to your checkout")
	}
	if len(out) == 0 {
		out = append(out, "settings")
	}
	return out
}

// repoTrustConfirmLabel is the confirm key's hint, and it carries the same
// distinction the body does: offering to "run setup" for a file that runs nothing
// asks for consent to the wrong thing, and it is the half a user who reads only the
// key hints will act on. The mixed case leads with the script, because that is the
// larger of the two powers — but it no longer implies the script is all of it.
func repoTrustConfirmLabel(a repotrust.Assessment) string {
	switch {
	case repoTrustRuns(a) && repoTrustSeeds(a):
		return "trust: run setup, seed files"
	case repoTrustRuns(a):
		return "trust and run setup"
	case repoTrustSeeds(a):
		return "trust and seed files"
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
		name := sanitizeRepoText(a.Local.Entries[0].Name, 16)
		if name == "" {
			name = "unnamed entry"
		}
		line = name + " · " + line
	}
	// Bound the WHOLE surfaces line, not just the repo-authored name inside it.
	// #815 lengthened it — a maximal entry contributes four surfaces and the two
	// seed counts add two more — and at the 80-column floor an unbounded ~117-cell
	// line is three rows the dialog's height budget never counted. It is the same
	// argument as repoTrustSeedWidth's, applied to the line that grew.
	line = sanitizeRepoText(line, repoTrustSeedWidth)
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
	budget := max(repoTrustSeedWidth-ansi.StringWidth(prefix)-ansi.StringWidth(suffix), 8)
	// Sanitize the JOIN, not each entry: n entries each inside their own cap are n
	// times the cap.
	return "\n" + prefix + sanitizeRepoText(strings.Join(shown, ", "), budget) + suffix
}

// sanitizeRepoText is this dialog's name for theme.SanitizeUntrusted — the one
// display-boundary rule for text somebody else authored, shared with the settings
// panel's provenance line. It lives in theme rather than here because the panel
// needs the identical rule and a second copy of it would drift: the panel's copy is
// the one that had none, and the class it would have missed (long runs of combining
// marks, which the parse deliberately ALLOWS so macOS's decomposed filenames work)
// is the class only the display boundary can judge.
func sanitizeRepoText(s string, width int) string {
	return theme.SanitizeUntrusted(s, width)
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
