package app

import (
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/transcript"
	"github.com/ZviBaratz/atrium/ui/overlay"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// forkCheckpoints is a three-checkpoint enumeration in the reader's own order —
// oldest first — with the oldest carrying no ForkAtID, as a real one does.
func forkCheckpoints() transcript.Checkpoints {
	base := time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC)
	return transcript.Checkpoints{
		SessionID: "5e551111-1111-4111-8111-111111111111",
		Path:      "/cfg/projects/-home-zvi-src/5e551111-1111-4111-8111-111111111111.jsonl",
		List: []transcript.Checkpoint{
			{MessageID: "0001aaaa-1111-4111-8111-111111111111", Label: "oldest", At: base, ForkAtID: ""},
			{MessageID: "0002aaaa-1111-4111-8111-111111111111", Label: "middle", At: base.Add(time.Minute), ForkAtID: "a001bbbb-1111-4111-8111-111111111111"},
			{MessageID: "0003aaaa-1111-4111-8111-111111111111", Label: "newest", At: base.Add(2 * time.Minute), ForkAtID: "a002bbbb-1111-4111-8111-111111111111"},
		},
	}
}

// openForkTimeline opens the timeline on a claude session and loads the fixture,
// leaving the cursor on row 0 (the newest checkpoint).
func openForkTimeline(t *testing.T) *home {
	t.Helper()
	h, inst := checkpointHome(t)
	_, _ = h.openCheckpoints()
	require.NotNil(t, h.checkpointOverlay)
	h.handleCheckpointsLoaded(checkpointsLoadedMsg{target: inst, result: forkCheckpoints()})
	return h
}

// TestSelectedCheckpoint_ReadsTheCursorThroughTheReversal is the guard for the one
// index inversion in the checkpoint path. The overlay is pushed rows newest-first
// while the enumeration is held in file order, so cursor 0 is the LAST element.
//
// Getting it backwards is invisible on screen — every row looks alike, and a fork
// seeded from the wrong end still starts, still answers, and still goes Ready. It
// is the same class of failure as the ignored flag, arrived at from the other side.
func TestSelectedCheckpoint_ReadsTheCursorThroughTheReversal(t *testing.T) {
	h := openForkTimeline(t)
	list := forkCheckpoints().List

	for cursor, want := range []string{list[2].MessageID, list[1].MessageID, list[0].MessageID} {
		for h.checkpointOverlay.SelectedIndex() < cursor {
			h.checkpointOverlay.HandleKeyPress(runeKey("j"))
		}
		require.Equal(t, cursor, h.checkpointOverlay.SelectedIndex())

		got, ok := h.selectedCheckpoint()
		require.True(t, ok, "cursor %d selected nothing", cursor)
		assert.Equalf(t, want, got.MessageID,
			"cursor %d (rows are newest-first, the enumeration is oldest-first) selected %q",
			cursor, got.Label)
	}
}

// Out-of-range and no-overlay both answer false rather than an arbitrary row: the
// caller spawns a session off this, so a zero Checkpoint would fork from "".
func TestSelectedCheckpoint_RefusesWhenThereIsNothingSelected(t *testing.T) {
	h, _ := checkpointHome(t)
	if _, ok := h.selectedCheckpoint(); ok {
		t.Error("selectedCheckpoint answered with no timeline open")
	}

	h = openForkTimeline(t)
	h.checkpointSource = transcript.Checkpoints{} // a result dropped between press and read
	if _, ok := h.selectedCheckpoint(); ok {
		t.Error("selectedCheckpoint answered from an empty enumeration")
	}
}

// TestForkKeyOpensTheSeededForm presses the key rather than calling the handler.
// Nothing else in the suite proves `f` is wired: it is not a keys.Registry entry,
// so no dispatch-coverage test reaches it, and the overlay's own tests stop at the
// intent flag. A key can be handled, documented in the footer, and dead.
func TestForkKeyOpensTheSeededForm(t *testing.T) {
	h := openForkTimeline(t)
	source := h.checkpointTarget.Title

	h.handleCheckpointsState(runeKey("f"))

	require.Equal(t, statePrompt, h.state, "f did not open the create form")
	require.NotNil(t, h.textInputOverlay)
	require.NotNil(t, h.pendingFork, "the form opened without the fork armed")
	assert.Nil(t, h.checkpointOverlay, "the timeline should be gone once the form is up")

	// The seed is the newest checkpoint's, since the cursor opens there.
	newest := forkCheckpoints().List[2]
	assert.Equal(t, newest.ForkAtID, h.pendingFork.cutEntryID)
	assert.Equal(t, newest.MessageID, h.pendingFork.droppedMessageID,
		"the dropped marker must be the checkpoint's own prompt — it is what proves the cut happened")
	assert.Equal(t, forkCheckpoints().Path, h.pendingFork.sourceTranscript,
		"a fork resumes the transcript by path; the new session's project dir is not the source's")

	// The title is derived from the source and free; the heading says it will fork.
	assert.Equal(t, source+forkTitleSuffix, h.textInputOverlay.GetTitle())
	assert.Contains(t, h.textInputOverlay.Title, "Fork",
		"the form is otherwise identical to a plain create — the heading is the only thing that says otherwise")
	assert.Contains(t, h.textInputOverlay.Title, newest.At.Format("Jan _2 15:04"),
		"the heading must name the checkpoint, so a mis-aimed cursor is visible before a worktree is built")
}

// The oldest checkpoint has nothing before it to keep, so claude would exit 1 on
// an empty --resume-session-at. Refused with a reason, and — the part that matters
// — the timeline stays up, exactly as a refused attach leaves it up rather than
// costing the user the list and a second whole-transcript scan.
func TestForkRefusesTheOldestCheckpoint(t *testing.T) {
	h := openForkTimeline(t)
	for h.checkpointOverlay.SelectedIndex() < 2 {
		h.checkpointOverlay.HandleKeyPress(runeKey("j"))
	}
	cp, ok := h.selectedCheckpoint()
	require.True(t, ok)
	require.Empty(t, cp.ForkAtID, "the fixture's oldest row must have no fork point")

	h.handleCheckpointsState(runeKey("f"))

	assert.Equal(t, stateCheckpoints, h.state, "a refused fork must leave the timeline standing")
	assert.NotNil(t, h.checkpointOverlay)
	assert.Nil(t, h.pendingFork)
	// Behind an overlay a notice falls back to the errBox row, which the centred box
	// does not cover — so the refusal is both spoken and visible, the same split the
	// refused-attach path makes.
	require.True(t, h.errBox.HasContent(), "and it must say why")
	assert.Contains(t, xansi.Strip(h.errBox.String()), "start of the conversation")
}

// f is inert while the read is in flight, all the way through the app: the overlay
// declines to arm it, so no form opens and no fork is stored. Asserted here as
// well as in the overlay because this is the layer that would build a worktree.
func TestForkIsInertWhileTheTimelineIsLoading(t *testing.T) {
	h, _ := checkpointHome(t)
	_, _ = h.openCheckpoints() // loading; no result delivered

	h.handleCheckpointsState(runeKey("f"))

	assert.Equal(t, stateCheckpoints, h.state)
	assert.Nil(t, h.pendingFork)
	assert.NotNil(t, h.checkpointOverlay)
}

// TestForkDoesNotLeakIntoAnUnrelatedCreate is the invariant that keeps the fork
// from being spawned by a form that says nothing about it.
//
// The create form is the ordinary one, so an armed fork left on the model would be
// picked up by the next `n` — same fields, same heading, and a session seeded from
// someone else's conversation. Every route into the form disarms; only openForkForm
// re-arms, and only for its own open.
func TestForkDoesNotLeakIntoAnUnrelatedCreate(t *testing.T) {
	h := openForkTimeline(t)
	h.handleCheckpointsState(runeKey("f"))
	require.NotNil(t, h.pendingFork)

	// Abandon it and open a fresh, unrelated form the way smart dispatch does.
	h.textInputOverlay = nil
	h.state = stateDefault
	h.openCreateFormSeeded(t.TempDir(), false, &PrefillResult{Title: "something-else"})

	assert.Nil(t, h.pendingFork, "an abandoned fork survived into an unrelated create form")
	require.NotNil(t, h.textInputOverlay)
	assert.NotContains(t, h.textInputOverlay.Title, "Fork")
}

// A stashed draft still shows the fork heading when it comes back, so the fork has
// to come back with it — otherwise the restored form promises a fork and performs
// a plain create, which is worse than either.
func TestForkTravelsWithAStashedDraft(t *testing.T) {
	h := openForkTimeline(t)
	h.handleCheckpointsState(runeKey("f"))
	require.NotNil(t, h.pendingFork)
	armed := h.pendingFork

	h.textInputOverlay.SetPrompt("try it another way")
	h.stashDirtyCreateForm()
	require.NotNil(t, h.stashedDraft, "the fixture must actually stash")
	require.Nil(t, h.pendingFork, "a stashed fork must not stay armed")

	h.textInputOverlay = nil
	h.state = stateDefault
	h.openCreateFormSeeded("", false, nil) // the bare `n` restore path

	require.Same(t, armed, h.pendingFork, "the restored draft came back without its fork")
	assert.Contains(t, h.textInputOverlay.Title, "Fork")
}

// firstFreeTitle suffixes from 2, because the bare stem is the 1. It goes through
// the same conflict predicate a variant title does, so an existing session — or an
// orphan branch from a killed one — disqualifies a name here too.
func TestFirstFreeTitle(t *testing.T) {
	h := openForkTimeline(t)
	path := t.TempDir()

	assert.Equal(t, "alpha-fork", h.firstFreeTitle("alpha-fork", path, true))

	// titleConflict is scoped to the target's repo group, so the fixture has to sit
	// in the one being submitted to — an instance in another group is not a conflict,
	// which is the whole point of the scoping.
	inst := h.list.GetSelectedInstance()
	require.NotNil(t, inst)
	inst.Title = "alpha-fork"
	h.newSessionGroup = inst.GroupKey()
	require.NotEmpty(t, h.variantTitleConflict("alpha-fork", path, true),
		"the fixture must actually collide, or the suffix step proves nothing")
	assert.Equal(t, "alpha-fork-2", h.firstFreeTitle("alpha-fork", path, true),
		"a taken stem must step to -2, not collide and not jump to -1")
}

// A fork's print run is what materializes the truncated conversation, and it has
// to ask something. An ordinary create's prompt is explicitly optional, so the
// requirement is the fork's alone — and it is refused with the form still open,
// rather than discovered as a failed start after a worktree was built.
func TestForkSubmitRequiresAPrompt(t *testing.T) {
	h := openForkTimeline(t)
	h.handleCheckpointsState(runeKey("f"))
	require.NotNil(t, h.pendingFork)
	h.textInputOverlay.Submitted = true

	h.createSessionFromForm("   ")

	require.NotNil(t, h.textInputOverlay, "the form must stay open to correct")
	assert.False(t, h.textInputOverlay.Submitted)
	assert.NotNil(t, h.pendingFork, "the fork must survive a refused submit")
	assert.Equal(t, 1, h.list.NumInstances(), "nothing may have been spawned")
}

// forkSeedForSpawn mints a fresh id per submit. claude refuses a --session-id
// already in use, so an id minted when `f` was pressed and reused across an
// abandoned submit would fail the second fork with an error about the first.
func TestForkSeedForSpawn_MintsAFreshID(t *testing.T) {
	h := openForkTimeline(t)
	h.handleCheckpointsState(runeKey("f"))

	first, err := h.forkSeedForSpawn()
	require.NoError(t, err)
	require.NotNil(t, first)
	second, err := h.forkSeedForSpawn()
	require.NoError(t, err)

	assert.NotEqual(t, first.NewSessionID, second.NewSessionID)
	assert.Equal(t, first.CutEntryID, second.CutEntryID, "only the id is fresh")

	h.pendingFork = nil
	none, err := h.forkSeedForSpawn()
	require.NoError(t, err)
	assert.Nil(t, none, "an ordinary create must carry no seed")
}

// A fan-out cannot be a fork: N sessions would each need their own --session-id
// and their own print run, and claude refuses an id already in use. Refused on the
// variant row, which is where the counts that caused it live, and nothing spawns.
//
// A git target, deliberately: on a direct one the fan-out is refused earlier for
// wanting worktrees at all, so the fork refusal would never be reached and this
// would pass while proving nothing. Its width is guarded separately, by
// TestVariantRefusals_SurviveAn80ColRender.
func TestForkRefusesAFanOut(t *testing.T) {
	h := newFanOutHome(t, gitInitRepo(t))
	h.pendingFork = &pendingFork{
		sourceTitle:      "alpha",
		sourceTranscript: "/cfg/projects/-src/s.jsonl",
		cutEntryID:       "aaaa1111-1111-4111-8111-111111111111",
		droppedMessageID: "bbbb2222-2222-4222-8222-222222222222",
	}
	typeString(h, "race")
	h.textInputOverlay.FocusVariants()
	plusKey(h) // claude 1 -> 2
	require.Greater(t, len(h.textInputOverlay.GetVariants()), 1, "the fixture must request a fan-out")

	before := h.list.NumInstances()
	ctrlS(h)

	require.Equal(t, statePrompt, h.state, "the form must stay open to correct")
	assert.Contains(t, h.textInputOverlay.VariantError(), "one session")
	assert.Equal(t, before, h.list.NumInstances(), "no session may have been spawned")
	assert.NotNil(t, h.pendingFork, "the fork survives a refused submit")
}

// TestForkHeading_SurvivesAn80ColRender is the width guard for the one piece of
// user-visible copy this feature adds to the create form.
//
// The heading is the only thing on that form that says the submit will fork, and
// it is the widest string the form's title row has ever carried — every other
// fixture in ui/overlay's width sweep uses the 11-cell default "New session", so
// nothing measured a title until now. fitOverlay truncates silently, and a heading
// cut to "Fork from checkp…" would still look deliberate.
//
// It drives `f` rather than pushing a string in, so what is measured is the copy
// the app actually builds — timestamp and all — not a literal restated here.
func TestForkHeading_SurvivesAn80ColRender(t *testing.T) {
	h := openForkTimeline(t)
	h.handleCheckpointsState(runeKey("f"))
	require.NotNil(t, h.textInputOverlay)
	heading := h.textInputOverlay.Title

	// 80x24 is the terminal the copy has to survive; SetSize takes the overlay's
	// 0.6 share of it, which is the trap in tests that pass 80 to the overlay.
	h.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: 80, Height: 24})

	frame := xansi.Strip(h.View().Content)
	assert.Containsf(t, frame, heading,
		"the fork heading is cut at 80 cols — fitOverlay trimmed it.\n  heading (%d cells): %q",
		lipgloss.Width(heading), heading)

	// The checkpoint's own time is the half that identifies WHICH checkpoint, so a
	// truncation that kept "Fork from checkpoint" and dropped the stamp would leave
	// the heading looking whole while losing the only thing a user could check the
	// cursor against.
	newest := forkCheckpoints().List[2]
	assert.Contains(t, frame, newest.At.Format(overlay.CheckpointTimeFormat),
		"the 80-column heading lost the checkpoint's timestamp")
}

// TestForkDoesNotAlsoQueueThePrompt is the regression guard for a defect that
// every other test in this file was blind to, and that only pressing the key
// found: the fork's prompt was delivered twice.
//
// The print run that materializes the truncated conversation asks the prompt
// headlessly — that is what makes it the session's real first turn rather than a
// throwaway. Queuing it as well hands it to deliverReadyPrompts, which types it
// into the pane once the agent is up, and the fork answers the same question a
// second time. Nothing failed, nothing was logged, and the session looked correct.
//
// The nil-fork arm is the control, and it is what makes this a guard rather than
// an assertion that spawning queues nothing: without it, deleting the QueuePrompt
// call outright would pass.
func TestForkDoesNotAlsoQueueThePrompt(t *testing.T) {
	const prompt = "carry on from the checkpoint"
	for _, tc := range []struct {
		name       string
		fork       *session.ForkSeed
		wantQueued int
	}{
		{"forked session", &session.ForkSeed{
			SourceTranscript: "/cfg/projects/-src/s.jsonl",
			CutEntryID:       "aaaa1111-1111-4111-8111-111111111111",
			DroppedMessageID: "bbbb2222-2222-4222-8222-222222222222",
			NewSessionID:     "cccc3333-3333-4333-8333-333333333333",
		}, 0},
		{"ordinary session", nil, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := newGateHome(t, gateScenarios()[0])
			before := h.list.NumInstances()

			// The returned cmd is deliberately not run: it is the background Start, and
			// what is under test is what startNewSession put on the instance before it.
			_, err := h.startNewSession("queued", t.TempDir(), true, "echo", "", prompt, nil, false, tc.fork)
			require.NoError(t, err)
			require.Equal(t, before+1, h.list.NumInstances(), "the session was not created")

			inst := h.list.GetInstances()[h.list.NumInstances()-1]
			assert.Equalf(t, tc.wantQueued, inst.QueueLen(),
				"%s: queued %d prompts, want %d — a fork's prompt is asked by the print run, "+
					"so queuing it as well types it into the pane a second time",
				tc.name, inst.QueueLen(), tc.wantQueued)
		})
	}
}

// TestForkFormSaysThePromptIsRequired guards the form against contradicting its own
// submit.
//
// Every other route into the create form leaves the prompt genuinely optional — it
// is queued and typed into the pane, and skipping it just leaves the agent idle. A
// fork's is not optional: the print run that materializes the truncated conversation
// exits 1 without one, so createSessionFromForm refuses the submit. A field still
// reading "Optional" at the moment the user decides whether to type is the form
// telling them something the submit will then contradict.
//
// The ordinary-form arm is the control: without it, making every placeholder say
// "Required" would pass.
func TestForkFormSaysThePromptIsRequired(t *testing.T) {
	h := openForkTimeline(t)
	h.handleCheckpointsState(runeKey("f"))
	require.NotNil(t, h.textInputOverlay)

	forked := h.textInputOverlay.PromptPlaceholder()
	assert.Equal(t, overlay.PromptPlaceholderFork, forked)
	assert.Contains(t, forked, "Required")
	assert.NotContains(t, forked, "Optional",
		"the fork form still calls its prompt optional, but the submit refuses an empty one")

	// The control: an ordinary create is unchanged.
	h.textInputOverlay = nil
	h.state = stateDefault
	h.openCreateFormSeeded(t.TempDir(), false, nil)
	require.NotNil(t, h.textInputOverlay)
	assert.Equal(t, overlay.PromptPlaceholderOptional, h.textInputOverlay.PromptPlaceholder(),
		"an ordinary session's prompt really is optional and must keep saying so")
}

// TestForkBaseBranch_DefaultsToTheConversationsOwnBranch is #657.
//
// A fork inherits a conversation ABOUT some branch's code. Basing its worktree on
// the repo's default hands it a transcript discussing files the worktree does not
// contain — which is how the very first real fork went, and the forked agent
// noticed before a human did.
//
// The assertion is deliberately on GetSelectedBranch, the value
// createSessionFromForm reads to build the worktree, not on what the picker
// renders: a preference that only moved a highlight would look identical on screen
// and branch off the wrong commit.
func TestForkBaseBranch_DefaultsToTheConversationsOwnBranch(t *testing.T) {
	repo := gitInitRepo(t)
	runGitIn(t, repo, "branch", "zvi/issue-644")
	runGitIn(t, repo, "branch", "zvi/unrelated")

	h := openForkTimelineIn(t, repo, "zvi/issue-644")
	h.handleCheckpointsState(runeKey("f"))
	require.NotNil(t, h.textInputOverlay)
	require.Equal(t, "zvi/issue-644", h.pendingFork.sourceBranch)

	// The filter is seeded to the branch name, which is what guarantees it is in the
	// capped result set at all — SearchBranches returns at most MaxBranchSearchResults,
	// newest first, so an unfiltered search in a busy repo need not contain it.
	assert.Equal(t, "zvi/issue-644", h.textInputOverlay.BranchFilter(),
		"the search was not narrowed to the branch, so the preference may never see it")

	// Results the seeded search would produce, ordered with the fork's own sibling
	// first: "<title>-fork" is a name this feature generates, so a repo running it
	// routinely holds a branch whose name contains another's.
	h.textInputOverlay.SetBranchResults(
		[]string{"zvi/issue-644-fork", "zvi/unrelated", "zvi/issue-644"},
		h.textInputOverlay.BranchFilterVersion())

	assert.Equal(t, "zvi/issue-644", h.textInputOverlay.GetSelectedBranch(),
		"the fork's base is not the conversation's branch — it would branch off the repo default")
}

// The reissued search has to run under the version PreferBranch left behind.
// Seeding the filter bumps that version, which invalidates the search
// openCreateFormSeeded already queued — so without the reissue the picker sits empty
// forever, and with a stale version the results are dropped on arrival. Neither
// failure is visible on screen: an empty picker looks like a slow one.
func TestForkBaseBranch_ResultsFromTheStaleSearchAreRejected(t *testing.T) {
	repo := gitInitRepo(t)
	runGitIn(t, repo, "branch", "zvi/issue-644")

	h := openForkTimelineIn(t, repo, "zvi/issue-644")
	staleVersion := h.textInputOverlay0(t).BranchFilterVersion()
	h.handleCheckpointsState(runeKey("f"))
	ov := h.textInputOverlay
	require.NotNil(t, ov)

	require.NotEqual(t, staleVersion, ov.BranchFilterVersion(),
		"seeding the filter must bump the version, or the in-flight search wins the race")

	ov.SetBranchResults([]string{"zvi/issue-644"}, staleVersion)
	assert.Empty(t, ov.GetSelectedBranch(), "results from the pre-seed search were accepted")

	ov.SetBranchResults([]string{"zvi/issue-644"}, ov.BranchFilterVersion())
	assert.Equal(t, "zvi/issue-644", ov.GetSelectedBranch(),
		"the fresh search's results did not apply the preference")
}

// TestForkBaseBranch_TheReissuedSearchRunsUnderTheLiveVersion runs the command the
// fork open actually returns, rather than hand-delivering results the way the tests
// above do.
//
// That distinction is the whole point. Seeding the filter bumps the picker's
// version, so a search issued under the version captured *before* the seed is
// rejected on arrival — and every symptom of that is invisible: the picker just
// stays empty, which looks like a slow search, and the base silently falls back to
// HEAD. Only executing the returned command and reading the version off the message
// can tell the two apart.
func TestForkBaseBranch_TheReissuedSearchRunsUnderTheLiveVersion(t *testing.T) {
	repo := gitInitRepo(t)
	runGitIn(t, repo, "branch", "zvi/issue-644")

	h := openForkTimelineIn(t, repo, "zvi/issue-644")
	_, cmd := h.handleCheckpointsState(runeKey("f"))
	ov := h.textInputOverlay
	require.NotNil(t, ov)

	var searches []branchSearchResultMsg
	var walk func(tea.Cmd)
	walk = func(c tea.Cmd) {
		if c == nil {
			return
		}
		msg := c()
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, sub := range batch {
				walk(sub)
			}
			return
		}
		if r, ok := msg.(branchSearchResultMsg); ok {
			searches = append(searches, r)
		}
	}
	walk(cmd)

	require.NotEmpty(t, searches, "opening the fork form issued no branch search at all")
	live := ov.BranchFilterVersion()
	var accepted *branchSearchResultMsg
	for i := range searches {
		if searches[i].version == live {
			accepted = &searches[i]
		}
	}
	require.NotNilf(t, accepted,
		"every search ran under a stale version (live=%d, issued=%v) — the results would "+
			"be dropped on arrival and the picker would sit empty", live, searches)
	assert.Contains(t, accepted.branches, "zvi/issue-644",
		"the accepted search did not look for the conversation's branch")

	// And feeding it through the real handler selects it, which is the property the
	// hand-delivered tests assert directly.
	ov.SetBranchResults(accepted.branches, accepted.version)
	assert.Equal(t, "zvi/issue-644", ov.GetSelectedBranch())
}

// textInputOverlay0 is the create form's filter version before the fork opens one,
// captured from a throwaway form so the "stale" version in the test above is a real
// prior value rather than a guess at what the counter started on.
func (m *home) textInputOverlay0(t *testing.T) *overlay.TextInputOverlay {
	t.Helper()
	ov, _ := m.newSessionFormOverlay()
	return ov
}

// A branch that is gone by fork time — merged and deleted is the ordinary end of a
// session's life, and this repo squash-merges with delete-branch-on-merge — must
// fall back to the form's default rather than naming a base the worktree build
// would then fail on.
func TestForkBaseBranch_FallsBackWhenTheBranchIsGone(t *testing.T) {
	repo := gitInitRepo(t)
	h := openForkTimelineIn(t, repo, "zvi/long-since-merged")
	h.handleCheckpointsState(runeKey("f"))
	require.NotNil(t, h.textInputOverlay)

	assert.Empty(t, h.pendingFork.sourceBranch,
		"a branch that no longer exists must not be offered as a base")
	h.textInputOverlay.SetBranchResults([]string{"main"}, h.textInputOverlay.BranchFilterVersion())
	assert.Empty(t, h.textInputOverlay.GetSelectedBranch(),
		"with no source branch the form keeps its ordinary default (branch off HEAD)")
}

// The preference is a default, not a pin: it applies to the first result set and is
// then gone, so a user who filters afterwards is not dragged back to it on every
// keystroke. Without the one-shot bound the picker fights the person using it.
func TestForkBaseBranch_IsADefaultNotAPin(t *testing.T) {
	repo := gitInitRepo(t)
	runGitIn(t, repo, "branch", "zvi/issue-644")
	runGitIn(t, repo, "branch", "zvi/other")

	h := openForkTimelineIn(t, repo, "zvi/issue-644")
	h.handleCheckpointsState(runeKey("f"))
	ov := h.textInputOverlay
	require.NotNil(t, ov)

	ov.SetBranchResults([]string{"zvi/issue-644", "zvi/other"}, ov.BranchFilterVersion())
	require.Equal(t, "zvi/issue-644", ov.GetSelectedBranch())

	// A second delivery, as a filter edit would produce, must not re-snap.
	ov.SetBranchResults([]string{"zvi/other", "zvi/issue-644"}, ov.BranchFilterVersion())
	assert.NotEqual(t, "zvi/issue-644", ov.GetSelectedBranch(),
		"the preference survived its first result set and moved the cursor again")
}

// openForkTimelineIn opens the timeline on a claude session that lives in repo and
// claims branch, so the fork's base-branch derivation has something real to read.
func openForkTimelineIn(t *testing.T, repo, branch string) *home {
	t.Helper()
	h, inst := checkpointHome(t)
	inst.Path = repo
	inst.Branch = branch
	h.newSessionPath = repo
	_, _ = h.openCheckpoints()
	require.NotNil(t, h.checkpointOverlay)
	h.handleCheckpointsLoaded(checkpointsLoadedMsg{target: inst, result: forkCheckpoints()})
	return h
}
