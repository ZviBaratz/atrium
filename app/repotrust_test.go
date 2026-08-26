package app

// repotrust_test.go — the create-time half of #814. The enforcement proof (an
// untrusted script spawns no process) lives in session/repoconfig_test.go;
// what belongs here is the PROMPT's contract: it stages before anything
// spawns, BOTH answers spawn, only "y" writes a grant, ordinary dialogs keep
// their decline-is-a-pure-cancel contract, and the dialog itself survives the
// 80×24 floor with repo-authored text in it.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/internal/repotrust"
	"github.com/ZviBaratz/atrium/repocfg"

	tea "charm.land/bubbletea/v2"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/muesli/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// commitRepoLocal commits content as repo's .atrium.json.
func commitRepoLocal(t *testing.T, repo, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".atrium.json"), []byte(content), 0o644))
	for _, args := range [][]string{{"add", ".atrium.json"}, {"commit", "-m", "repo config"}} {
		cmd := exec.CommandContext(context.Background(), "git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}
}

// submitCreateForm drives the create form to a submit over repo, exactly as
// TestCreateSessionFromForm_CreatesOneAndClearsOverlay does.
func submitCreateForm(t *testing.T, h *home, repo, title string) tea.Cmd {
	t.Helper()
	h.newSessionPath = repo
	h.state = statePrompt
	ov, _ := h.newSessionFormOverlay()
	h.textInputOverlay = ov
	ov.HandleKeyPress(keyMsg("tab"))
	ov.HandleKeyPress(keyMsg("tab"))
	ov.HandleKeyPress(textMsg(title))
	return h.createSessionFromForm("")
}

const testRepoLocal = `{"repo_scripts":[{"name":"web","setup_script":"npm ci"}]}`

// answerConfirm presses key on the open confirmation and runs the resulting
// message back through Update, returning home in its post-answer state.
func answerConfirm(t *testing.T, h *home, key string) {
	t.Helper()
	_, cmd := h.handleKeyPress(keyMsg(key))
	require.NotNil(t, cmd, "answering the trust prompt must produce its proceed message")
	msg := cmd()
	require.NotNil(t, msg)
	_, _ = h.Update(msg)
}

func TestCreateSessionFromForm_UntrustedRepoStagesTheTrustPrompt(t *testing.T) {
	repo := gitInitRepo(t)
	commitRepoLocal(t, repo, testRepoLocal)
	h := newCreateFormHome(t)

	before := h.list.NumInstances()
	cmd := submitCreateForm(t, h, repo, "feature")
	assert.Nil(t, cmd)

	require.Equal(t, stateConfirm, h.state, "an ungranted repo-local config must stage the prompt")
	require.NotNil(t, h.confirmationOverlay)
	require.NotNil(t, h.pendingTrust, "the plan must be staged, not spawned")
	assert.Equal(t, before, h.list.NumInstances(), "nothing spawns while the prompt is up")
	assert.True(t, h.stagedSpawnPlan(), "the headless drain must hold while the prompt is up")
	assert.Nil(t, h.textInputOverlay, "the form is consumed before the dialog opens")
	require.NotNil(t, h.stashedDraft,
		"the dismissed form must be stashed (confirmOverCap's contract): a later gate's decline or a spawn failure has to have something to restore")

	view := flattenOverlay(h.confirmationOverlay.Render())
	assert.Contains(t, view, ".atrium.json")
	assert.Contains(t, view, "npm ci", "the dialog must show what would run")
	assert.Contains(t, view, "trust and run setup")
	assert.Contains(t, view, "create without it", "the decline hint must not promise a cancel")

	answerConfirm(t, h, "y")

	assert.Equal(t, before+1, h.list.NumInstances(), "confirming spawns the staged plan")
	assert.Nil(t, h.pendingTrust)
	assert.Nil(t, h.stashedDraft, "the committed spawn consumes the stash — a restorable draft of a created session would double-create")
	a, err := repotrust.AssessRepo(context.Background(), repo, "")
	require.NoError(t, err)
	assert.True(t, a.Granted, "y must write the grant before the spawn proceeds")
}

func TestCreateSessionFromForm_DecliningTrustStillSpawnsUntrusted(t *testing.T) {
	repo := gitInitRepo(t)
	commitRepoLocal(t, repo, testRepoLocal)
	h := newCreateFormHome(t)

	before := h.list.NumInstances()
	_ = submitCreateForm(t, h, repo, "feature")
	require.Equal(t, stateConfirm, h.state)

	answerConfirm(t, h, "esc")

	assert.Equal(t, before+1, h.list.NumInstances(),
		"declining trust is not declining the create — the session spawns with the config inert")
	assert.Nil(t, h.pendingTrust)
	a, err := repotrust.AssessRepo(context.Background(), repo, "")
	require.NoError(t, err)
	assert.False(t, a.Granted, "declining must write nothing")
	assert.True(t, a.WantsPrompt(), "the next create must ask again")
}

func TestCreateSessionFromForm_GrantedRepoSkipsThePrompt(t *testing.T) {
	repo := gitInitRepo(t)
	commitRepoLocal(t, repo, testRepoLocal)
	a, err := repotrust.AssessRepo(context.Background(), repo, "")
	require.NoError(t, err)
	require.NoError(t, repotrust.Grant(a.Key, a.Hash, a.Remote, time.Now()))
	h := newCreateFormHome(t)

	before := h.list.NumInstances()
	_ = submitCreateForm(t, h, repo, "feature")

	assert.Equal(t, stateDefault, h.state, "a granted repo asks nothing")
	assert.Equal(t, before+1, h.list.NumInstances())
	assert.Nil(t, h.pendingTrust)
}

func TestCreateSessionFromForm_RepoWithoutConfigSkipsThePrompt(t *testing.T) {
	repo := gitInitRepo(t)
	h := newCreateFormHome(t)

	before := h.list.NumInstances()
	_ = submitCreateForm(t, h, repo, "feature")

	assert.Equal(t, stateDefault, h.state)
	assert.Equal(t, before+1, h.list.NumInstances())
}

// TestDeclineRunsNothingForOrdinaryDialogs pins the contract the decline slot
// (armOnDecline) must not erode: every dialog that does not arm one keeps
// decline as a pure cancel — no action, no message, no side effect. Without
// this, a leaked decline action from a prior dialog would fire on the next
// dialog's esc.
func TestDeclineRunsNothingForOrdinaryDialogs(t *testing.T) {
	h := newCreateFormHome(t)
	ran := false
	_ = h.confirmAction("sure?", instantAction, func() tea.Msg { ran = true; return nil })
	require.Equal(t, stateConfirm, h.state)

	_, cmd := h.handleKeyPress(keyMsg("esc"))

	assert.Nil(t, cmd, "declining an ordinary dialog must produce nothing")
	assert.False(t, ran)
	assert.Equal(t, stateDefault, h.state)
}

// TestTrustDeclineDoesNotLeakIntoTheNextDialog: the decline action is cleared
// with the rest of the pending set, so a trust prompt followed by an ordinary
// dialog leaves that dialog's esc a pure cancel.
func TestTrustDeclineDoesNotLeakIntoTheNextDialog(t *testing.T) {
	repo := gitInitRepo(t)
	commitRepoLocal(t, repo, testRepoLocal)
	h := newCreateFormHome(t)
	_ = submitCreateForm(t, h, repo, "feature")
	require.Equal(t, stateConfirm, h.state)
	answerConfirm(t, h, "esc") // consume the trust prompt (spawns untrusted)

	_ = h.confirmAction("sure?", instantAction, func() tea.Msg { return nil })
	_, cmd := h.handleKeyPress(keyMsg("esc"))

	assert.Nil(t, cmd, "the trust dialog's proceed-on-decline must not survive into the next dialog")
}

// TestRepoTrustDialogFitsTheFrame is the fits-the-frame guard
// (TestCustomCommandConfirmDialogFitsTheFrame's shape): repo-authored text —
// the entry name and the script — is bounded and sanitized before it reaches
// the dialog, so even a hostile file leaves a box the 80×24 floor can answer.
func TestRepoTrustDialogFitsTheFrame(t *testing.T) {
	for name, script := range map[string]string{
		"short":       "npm ci",
		"unbounded":   strings.Repeat("a very long single-line setup command ", 60),
		"wide runes":  strings.Repeat("日本語", 400),
		"multi-line":  "npm ci\n" + strings.Repeat("make step\n", 50),
		"ansi escape": "npm ci \x1b[31mRED\x1b[0m \x07",
	} {
		t.Run(name, func(t *testing.T) {
			repo := gitInitRepo(t)
			commitRepoLocal(t, repo,
				`{"repo_scripts":[{"name":"`+strings.Repeat("n", 60)+`","setup_script":`+jsonString(script)+`}]}`)
			h := newCreateFormHome(t)
			h.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: 80, Height: 24})

			_ = submitCreateForm(t, h, repo, "feature")
			require.Equal(t, stateConfirm, h.state)

			view := xansi.Strip(h.View().Content)
			assert.Contains(t, view, "trust and run setup",
				"a confirmation the user cannot answer is worse than none")
			lines := strings.Split(view, "\n")
			assert.LessOrEqual(t, len(lines), 24)
			for i, l := range lines {
				assert.Equalf(t, 80, ansi.PrintableRuneWidth(l), "line %d is the wrong width", i)
			}
			assert.NotContains(t, view, "\x1b[31m", "repo-authored escapes must never reach the frame")
		})
	}
}

// jsonString JSON-encodes a fixture string (escapes, control characters and all).
func jsonString(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// TestSanitizeRepoTextNeutralizesZeroWidthAndBidi: the frame test above cannot
// see these — ansi.PrintableRuneWidth scores a bidi override or a zero-width
// rune as 0 cells, so a frame full of them still measures 80 — which is
// exactly how they defeat runewidth.Truncate too (StringWidth <= budget
// returns the whole string). The guard is therefore direct: nothing
// non-printable survives sanitizeRepoText, and because every hostile rune
// becomes a 1-cell '·', the cell budget bounds the LENGTH again.
func TestSanitizeRepoTextNeutralizesZeroWidthAndBidi(t *testing.T) {
	for name, hostile := range map[string]string{
		"bidi override flood":  strings.Repeat("\u202e", 4000) + "rm -rf /", // Trojan Source, CVE-2021-42574
		"zero-width flood":     strings.Repeat("\u200b\u200d", 4000),
		"c1 controls":          "npm ci \u009b31m \u0085", // U+009B is an 8-bit CSI
		"combining-mark stack": "a" + strings.Repeat("\u0301", 4000),
		"esc and bel":          "npm ci \x1b[31mRED\x1b[0m\x07",
	} {
		t.Run(name, func(t *testing.T) {
			out := sanitizeRepoText(hostile, repoTrustPreviewWidth)
			for _, bad := range []string{"\u202e", "\u200b", "\u200d", "\u009b", "\u0085", "\u0301", "\x1b", "\x07"} {
				assert.NotContains(t, out, bad)
			}
			assert.LessOrEqual(t, ansi.PrintableRuneWidth(out), repoTrustPreviewWidth)
			assert.LessOrEqual(t, len([]rune(out)), repoTrustPreviewWidth+1,
				"every rune must be measurable, so the cell budget must bound the rune count too")
		})
	}
}

// TestRepoTrustAssessmentReadsTheCreateBase pins the app half of finding #1's
// fix: the assessment hashes the file at the ref the worktree will check out —
// the form's base branch resolved through git.StartPointPreview — not literal
// HEAD. A HEAD read here shows and grants one version while the session
// materializes another.
func TestRepoTrustAssessmentReadsTheCreateBase(t *testing.T) {
	repo := gitInitRepo(t)
	commitRepoLocal(t, repo, `{"repo_scripts":[{"name":"main-entry","setup_script":"make main"}]}`)
	for _, args := range [][]string{
		{"checkout", "-b", "setup-branch"},
		{"rm", "--cached", ".atrium.json"},
	} {
		cmd := exec.CommandContext(context.Background(), "git", args...)
		cmd.Dir = repo
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	commitRepoLocal(t, repo, `{"repo_scripts":[{"name":"branch-entry","setup_script":"make branch"}]}`)
	cmd := exec.CommandContext(context.Background(), "git", "checkout", "-")
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "checkout -: %s", out)
	h := newCreateFormHome(t)

	fromBranch, ok := h.repoTrustAssessment(repo, false, "setup-branch")
	require.True(t, ok)
	require.Len(t, fromBranch.Local.Entries, 1)
	assert.Equal(t, "branch-entry", fromBranch.Local.Entries[0].Name,
		"the prompt must describe the base branch's file — that is what the worktree checks out")

	fromHead, ok := h.repoTrustAssessment(repo, false, "")
	require.True(t, ok)
	assert.Equal(t, "main-entry", fromHead.Local.Entries[0].Name)
	assert.NotEqual(t, fromBranch.Hash, fromHead.Hash,
		"two bases, two hashes — a single HEAD hash would grant bytes the session never holds")
}

// TestAutoDispatchTrustPathLeavesParkedDraftAlone pins the dispatch plan's
// finalize split: the smart-dispatch path has no form, so a create that routes
// through the staged trust prompt must neither destroy a PARKED draft (an
// earlier Escape's unfinished create) nor write the dispatch line into the
// create form's prompt history — exactly what its ordinary, unprompted tail
// already does not do.
func TestAutoDispatchTrustPathLeavesParkedDraftAlone(t *testing.T) {
	repo := gitInitRepo(t)
	commitRepoLocal(t, repo, testRepoLocal)
	h := newCreateFormHome(t)

	// Park a draft, as an Escape from a half-filled create form would.
	h.newSessionPath = repo
	ov, _ := h.newSessionFormOverlay()
	h.textInputOverlay = ov
	ov.HandleKeyPress(keyMsg("tab"))
	ov.HandleKeyPress(keyMsg("tab"))
	ov.HandleKeyPress(textMsg("half-finished"))
	h.stashDirtyCreateForm()
	h.textInputOverlay = nil
	require.NotNil(t, h.stashedDraft)

	before := h.list.NumInstances()
	_, handled := h.autoDispatch(PrefillResult{Path: repo, Title: "dispatched", Prompt: "do the thing"})
	require.True(t, handled)
	require.Equal(t, stateConfirm, h.state, "an untrusted repo prompts on the dispatch path too")

	answerConfirm(t, h, "y")

	assert.Equal(t, before+1, h.list.NumInstances())
	require.NotNil(t, h.stashedDraft, "the parked draft belongs to a different create; the dispatch must not consume it")
	assert.Equal(t, "half-finished", h.stashedDraft.GetTitle())
	assert.Empty(t, h.appState.GetPromptHistory(),
		"the dispatch line is not create-form input; the ordinary dispatch tail records nothing and this path must match")
}

// TestRepoTrustDialogNamesTheSeedLists: the grant covers the whole file, so the
// dialog has to describe the whole file. A seed-only repo executes nothing and still
// decides which of the user's gitignored files reach the agent — a dialog that said
// only "setup script" (or nothing at all) would collect consent for something it
// never mentioned.
func TestRepoTrustDialogNamesTheSeedLists(t *testing.T) {
	repo := gitInitRepo(t)
	commitRepoLocal(t, repo, `{"carry_files":[".dev.vars",".other.env"]}`)
	h := newCreateFormHome(t)
	h.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: 80, Height: 24})

	_ = submitCreateForm(t, h, repo, "feature")
	require.Equal(t, stateConfirm, h.state, "a seed-only file must still stage the prompt")

	view := xansi.Strip(h.View().Content)
	assert.Contains(t, view, "2 carried files")

	assert.Contains(t, view, ".dev.vars", "the entries themselves, not just a count")

}

// TestRepoTrustDialogBoundsWideSeedLists: the entry cap
// (repocfg.MaxRepoLocalSeedEntries) bounds git forks per materialization, not frame
// lines, so the dialog needs its own bound. A file at that cap, with every entry as
// long and as wide as a LEGAL path can be, must still leave a box the 80x24 floor
// can answer.
//
// Legal is the operative word, and it is what makes this test's fixture different
// from the create form's: the unprintable classes that defeat a width budget
// outright are refused by repocfg.CanonicalSeedPath before a dialog ever sees them
// (see TestRepoTrustNeverPromptsForAnUnprintableSeedEntry below, and
// TestCanonicalSeedPathRefusesUnprintableRunes for the rule). What is left to bound
// here is sheer size: wide runes measure two cells and render two, so nothing lies
// about them - there is just far too much.
func TestRepoTrustDialogBoundsWideSeedLists(t *testing.T) {
	entries := make([]string, repocfg.MaxRepoLocalSeedEntries)
	for i := range entries {
		// Double-width CJK and a long ASCII tail: one defeats a naive rune count, the
		// other a naive cell count that forgot there are 64 of these.
		entries[i] = fmt.Sprintf("dep%d/%s%s", i, strings.Repeat("\u65e5\u672c\u8a9e", 30), strings.Repeat("x", 200))
	}
	raw, err := json.Marshal(entries)
	require.NoError(t, err)

	repo := gitInitRepo(t)
	commitRepoLocal(t, repo, `{"carry_files":`+string(raw)+`}`)
	h := newCreateFormHome(t)
	h.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: 80, Height: 24})

	_ = submitCreateForm(t, h, repo, "feature")
	require.Equal(t, stateConfirm, h.state)

	view := xansi.Strip(h.View().Content)
	// "trust and seed files", not "trust and run setup": this fixture declares no
	// repo_scripts, so nothing executes — see TestRepoTrustCopyOnlyPromisesWhatRuns.
	assert.Contains(t, view, "trust and seed files",
		"a confirmation the user cannot answer is worse than none")
	assert.Contains(t, view, "more",
		"a truncated sample must say it is a sample, never read as the whole list")
	lines := strings.Split(view, "\n")
	assert.LessOrEqual(t, len(lines), 24)
	for i, l := range lines {
		assert.Equalf(t, 80, ansi.PrintableRuneWidth(l), "line %d is the wrong width", i)
	}
}

// TestRepoTrustNeverPromptsForAnUnprintableSeedEntry is the other half of the pact
// above: a seed entry carrying a zero-width or bidi rune is refused at the parse, so
// no dialog is asked to describe it and no grant can cover it. Asserted through the
// create path rather than the parser because the parser test cannot see that the
// refusal actually reaches the prompt decision.
func TestRepoTrustNeverPromptsForAnUnprintableSeedEntry(t *testing.T) {
	raw, err := json.Marshal([]string{"node_modules/\u202egnp.exe"})
	require.NoError(t, err)

	repo := gitInitRepo(t)
	commitRepoLocal(t, repo, `{"carry_files":`+string(raw)+`}`)
	h := newCreateFormHome(t)
	h.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: 80, Height: 24})

	_ = submitCreateForm(t, h, repo, "feature")
	assert.NotEqual(t, stateConfirm, h.state,
		"a refused file has nothing grantable, so it must never stage a trust prompt")
}

// TestRepoTrustSeedLineFitsItsBudget is the assertion repoTrustSeedWidth's comment
// used to be. Prose there has been wrong twice — once bounding each entry (n entries
// at n times the cap) and once bounding only the joined sample while the verb and
// the overflow marker were appended outside it, 91 cells against a 46-cell column —
// so the number is measured here instead of described there.
func TestRepoTrustSeedLineFitsItsBudget(t *testing.T) {
	for name, entries := range map[string][]string{
		"one short":     {".env"},
		"three short":   {".env", ".env.local", ".dev.vars"},
		"over the cap":  longSeedList(repocfg.MaxRepoLocalSeedEntries, "dep"),
		"one very long": {strings.Repeat("verylongsegment/", 40)},
		"wide runes":    longSeedList(repocfg.MaxRepoLocalSeedEntries, strings.Repeat("日本語", 30)),
	} {
		t.Run(name, func(t *testing.T) {
			for _, verb := range []string{"copies in"} {
				line := strings.TrimPrefix(repoTrustSeedLine(verb, entries), "\n")
				require.NotEmpty(t, line)
				assert.LessOrEqualf(t, ansi.PrintableRuneWidth(line), repoTrustSeedWidth,
					"%q: %q is %d cells", verb, line, ansi.PrintableRuneWidth(line))
				assert.NotContainsf(t, line, "… …", "%q: doubled ellipsis reads as a typo: %q", verb, line)
			}
		})
	}
	assert.Empty(t, repoTrustSeedLine("copies in", nil), "an empty list contributes no line")
}

// longSeedList builds n entries whose stem repeats, for width fixtures.
func longSeedList(n int, stem string) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("%s%d", stem, i)
	}
	return out
}

// TestRepoTrustDialogHoldsTheFloorWithEverything is the margin guard: at the 80×24
// floor, a file declaring a script AND a seed list must still render an answerable
// box. #815's seed line spends part of that margin, and nothing else here would say
// when the rest is gone. Overlay geometry at tight heights is F2's (#802); this only
// refuses to lose ground silently.
//
// It does NOT pin how much margin is left. An earlier version claimed the margin was
// "now zero" and that the next line added would push an answer off the frame; that
// was measured wrong — injected rows showed several to spare — and a number here
// would have to be re-measured on every copy edit to stay true. The assertion is the
// property (the box holds), and the failure message is what tells you the margin
// went.
//
// The fixture is also narrower than the name: it declares a setup_script and
// carry_files, not the maximal surfaces line (run command, session env and port
// range are what repoTrustSummary can also name). That combination is measured
// nowhere.
func TestRepoTrustDialogHoldsTheFloorWithEverything(t *testing.T) {
	repo := gitInitRepo(t)
	commitRepoLocal(t, repo, `{
		"repo_scripts":[{"name":"web","setup_script":"npm ci && npm run db:migrate"}],
		"carry_files":[".dev.vars",".claude/settings.local.json",".env.local",".x"]
	}`)
	h := newCreateFormHome(t)
	h.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: 80, Height: 24})
	_ = submitCreateForm(t, h, repo, "feature")
	require.Equal(t, stateConfirm, h.state)

	view := xansi.Strip(h.View().Content)
	lines := strings.Split(view, "\n")
	assert.LessOrEqual(t, len(lines), 24)

	// Both answers, and the box's own bottom edge: a clipped box means the content
	// below the clip is gone, and the decline is the answer that must never vanish.
	assert.Contains(t, view, "trust: run setup, seed files")
	assert.Contains(t, view, "create without it")
	assert.Regexp(t, `╰─{10,}╯`, view, "the dialog's bottom border must be on screen")

	// Neither seed line may wrap: a wrapped line is a row the height budget never
	// counted, which is the mechanism that spent the margin in the first place.
	for _, verb := range []string{"copies in:"} {
		var found string
		for _, l := range lines {
			if strings.Contains(l, verb) {
				found = l
			}
		}
		require.NotEmptyf(t, found, "the dialog must carry a %q line", verb)
		assert.Containsf(t, found, "more", "%q wrapped: its overflow marker is on another row (%q)", verb, found)
	}
}

// TestRepoTrustCopyOnlyPromisesWhatRuns: the dialog asks for consent, so it has to
// name the thing being consented to. A seed-only file executes NOTHING — describing
// it as setup the user is about to run misstates the decision in both directions
// (declining "a stranger's script" actually declines a file copy they would have
// allowed; accepting it approves the repo choosing which of their gitignored files
// an agent reads, believing they approved one script).
func TestRepoTrustCopyOnlyPromisesWhatRuns(t *testing.T) {
	t.Run("a seed-only file never says run", func(t *testing.T) {
		repo := gitInitRepo(t)
		commitRepoLocal(t, repo, `{"carry_files":[".dev.vars"]}`)
		h := newCreateFormHome(t)
		h.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: 80, Height: 30})
		_ = submitCreateForm(t, h, repo, "feature")
		require.Equal(t, stateConfirm, h.state)

		view := xansi.Strip(h.View().Content)
		assert.NotContains(t, view, "run setup", "the key hint must not offer to run what cannot run")
		assert.NotContains(t, view, "runs it, as you", "the body must not promise execution")
		assert.NotContains(t, view, "its own setup")
		assert.Contains(t, view, "trust and seed files")
		consequence := trustConsequenceSentence(t, view)
		assert.Contains(t, consequence, "copy those files in", "it must name what a grant actually does here")
		assert.Contains(t, consequence, "copy those files into",
			"the sentence must name the power the grant actually confers")

		// And the entry-name slot is absent rather than filled with the filename the
		// sentence above already named, or with "unnamed entry" — either sends the
		// reader hunting for an entry that does not exist.
		assert.NotContains(t, view, "unnamed entry")
		assert.NotContains(t, view, ".atrium.json ·")
	})

	t.Run("an executable file still says run", func(t *testing.T) {
		// The positive control. Without it, a change that dropped the execution
		// wording everywhere would pass the subtest above.
		repo := gitInitRepo(t)
		commitRepoLocal(t, repo, `{"repo_scripts":[{"name":"web","setup_script":"npm ci"}],"carry_files":[".dev.vars"]}`)
		h := newCreateFormHome(t)
		h.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: 80, Height: 30})
		_ = submitCreateForm(t, h, repo, "feature")
		require.Equal(t, stateConfirm, h.state)

		view := xansi.Strip(h.View().Content)
		assert.Contains(t, view, "trust: run setup, seed files")
		// One grant, both halves named IN THE SENTENCE THE USER ANSWERS. Asserting
		// "1 carried file" instead is what let the incomplete wording ship: that string
		// is in the summary block, which lists what the file declares, and it appears
		// whether or not the consequence sentence admits the grant covers it. The
		// mixed shape is the README's own example, and it is the case a binary got
		// wrong — it named the script and left the seeding unmentioned.
		consequence := trustConsequenceSentence(t, view)
		assert.Contains(t, consequence, "runs it as you", "the script half must be named")
		assert.Contains(t, consequence, "copy those files in", "the seeding half must be named too")
	})
}

// trustConsequenceSentence pulls the dialog's "Trusting …" sentence out of a
// RENDERED frame, reflowed onto one line. It reads the frame rather than calling
// repoTrustMessage so the assertion covers what a user can actually see — but the
// sentence is wrapped across rows there, so a bare strings.Contains on the view
// silently fails for any phrase that straddles a row boundary. Normalizing first is
// what makes the assertion able to fail for the right reason instead of always.
func trustConsequenceSentence(t *testing.T, view string) string {
	t.Helper()
	flat := flattenFrame(view)
	i := strings.Index(flat, "Trusting")
	require.GreaterOrEqual(t, i, 0, "the dialog must carry a consequence sentence")
	rest := flat[i:]
	if end := strings.Index(rest, "."); end >= 0 {
		rest = rest[:end+1]
	}
	return rest
}

// flattenFrame reflows a rendered frame onto one line, dropping the box drawing, so
// an assertion about a PHRASE cannot fail merely because the phrase wrapped across
// two rows. Every prose assertion on this dialog needs it: the body is wrapped by
// the confirm overlay on purpose.
func flattenFrame(view string) string {
	var b strings.Builder
	for _, r := range view {
		switch {
		case r == '\n' || strings.ContainsRune("│─╭╮╰╯├┤┬┴┼└┘┌┐", r):
			b.WriteByte(' ')
		default:
			b.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// TestRepoTrustDialogHoldsTheFloorWithALongRepoPath is the margin guard the one
// above could not be: its fixture takes whatever short path t.TempDir() hands out,
// and the repo path is interpolated into the body uncapped. A real Atrium worktree
// path is long — the data dir, "worktrees", a user prefix, a session name, a hash —
// and a header that wraps to six rows is what actually spends the 80x24 height
// margin, on top of what #815's seed line already takes.
//
// It asserts the box's own bottom border, because that is the edge that goes first
// and the decline hint sits just above it: a confirmation whose "create without it"
// is off the frame is worse than no confirmation at all.
func TestRepoTrustDialogHoldsTheFloorWithALongRepoPath(t *testing.T) {
	repo := gitInitRepo(t)
	commitRepoLocal(t, repo, `{
		"repo_scripts":[{"name":"web-frontend-and-backend","setup_script":"npm ci && npm run db:migrate && npm run build && npm run seed && echo done"}],
		"carry_files":[".dev.vars",".claude/settings.local.json",".env.local",".x"]
	}`)

	h := newCreateFormHome(t)
	h.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: 80, Height: 24})
	_ = submitCreateForm(t, h, repo, "feature")
	require.Equal(t, stateConfirm, h.state)

	// Stage the same dialog again with a path the length a real worktree has. Going
	// through repoTrustMessage directly is the point: the fixture cannot choose its
	// own TempDir length, and the uncapped interpolation is what is under test.
	long := "/home/somebody/.atrium/worktrees/somebody/ux2-815-repo-local-config-and-trust_18cf01ef4c9cb403"
	require.Greater(t, len(long), repoTrustSeedWidth, "the fixture path must actually be long")
	body := repoTrustMessage(repotrust.Assessment{
		Root:     long,
		Present:  true,
		HasGrant: true,
		Local: repocfg.RepoLocal{
			Entries:    []repocfg.RepoLocalEntry{{Index: 0, RepoScript: config.RepoScript{Name: "web-frontend-and-backend", SetupScript: strings.Repeat("npm run build && ", 12) + "done"}}},
			CarryFiles: longSeedList(4, "carry/entry-with-a-long-name-"),
		},
	})
	// The PROSE is meant to wrap — the confirm overlay wraps it. What must be bounded
	// is every span interpolated from outside: the path, the entry name, the script
	// preview and the seed samples. The path is the one #815 left uncapped.
	assert.NotContains(t, body, long, "the repo path must be truncated, not interpolated whole")
	assert.Contains(t, body, "…", "and the truncation must say it happened")
	for i, line := range strings.Split(repoTrustSummary(repotrust.Assessment{
		Local: repocfg.RepoLocal{
			Entries:    []repocfg.RepoLocalEntry{{Index: 0, RepoScript: config.RepoScript{Name: strings.Repeat("n", 80), SetupScript: strings.Repeat("s", 400)}}},
			CarryFiles: longSeedList(40, "carry/very-long-entry-name-"),
		},
	}), "\n") {
		assert.LessOrEqualf(t, ansi.PrintableRuneWidth(line), repoTrustPreviewWidth,
			"summary line %d is unbounded (%q)", i, line)
	}

	h2 := newCreateFormHome(t)
	h2.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: 80, Height: 24})
	h2.confirmAction(body, instantAction, func() tea.Msg { return nil })
	h2.confirmationOverlay.SetConfirmLabel("trust: run setup, seed files")
	h2.confirmationOverlay.SetCancelLabel("create without it")
	view := xansi.Strip(h2.View().Content)
	lines := strings.Split(view, "\n")
	assert.LessOrEqual(t, len(lines), 24)
	flat := flattenFrame(view)
	assert.Contains(t, flat, "create without it", "the decline must never leave the frame")
	assert.Contains(t, flat, "trust: run setup, seed files", "and neither may the confirm")
	assert.Regexp(t, `╰─{10,}╯`, view, "the dialog's bottom border must be on screen")
	for i, l := range lines {
		assert.Equalf(t, 80, ansi.PrintableRuneWidth(l), "line %d is the wrong width", i)
	}
}

// TestRepoTrustDialogCallsSessionEnvExecution is the guard for repoTrustRuns'
// session_env clause, which shipped with none.
//
// session_env is rendered into `tmux new-session -e` independently of any script, and
// repocfg.reservedEnvName blocks only ATRIUM_*, CLAUDE_CONFIG_DIR and GH_CONFIG_DIR —
// so NODE_OPTIONS, GIT_SSH_COMMAND, LD_PRELOAD and BASH_ENV all reach the agent. A
// file carrying one of those and NO script is arbitrary code execution as the user,
// and the consent copy has to say "runs" for it or the user approves execution while
// reading a sentence about configuration.
//
// The fixture therefore declares session_env ALONE. A file that also carried a
// setup_script would say "runs" from the script clause whatever this one did, which is
// how the clause came to be deleted and re-added without any test noticing either way.
func TestRepoTrustDialogCallsSessionEnvExecution(t *testing.T) {
	repo := gitInitRepo(t)
	commitRepoLocal(t, repo, `{"repo_scripts":[{"name":"dev","session_env":{"NODE_OPTIONS":"--require /tmp/p.js"}}]}`)
	h := newCreateFormHome(t)
	h.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: 80, Height: 24})

	_ = submitCreateForm(t, h, repo, "feature")
	require.Equal(t, stateConfirm, h.state, "a session_env-only file must still stage the prompt")

	view := xansi.Strip(h.View().Content)
	assert.Contains(t, view, "runs it",
		"session_env alone is execution, so the consent copy must say so — it reaches the "+
			"agent through tmux new-session -e with no script in the file at all")
	assert.NotContains(t, view, "Trusting applies it to",
		"that is the verb for a file where NOTHING executes; session_env is not that file")
}
