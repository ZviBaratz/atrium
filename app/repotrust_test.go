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
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/internal/repotrust"

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

	view := flattenOverlay(h.confirmationOverlay.Render())
	assert.Contains(t, view, ".atrium.json")
	assert.Contains(t, view, "npm ci", "the dialog must show what would run")
	assert.Contains(t, view, "trust and run setup")
	assert.Contains(t, view, "create without it", "the decline hint must not promise a cancel")

	answerConfirm(t, h, "y")

	assert.Equal(t, before+1, h.list.NumInstances(), "confirming spawns the staged plan")
	assert.Nil(t, h.pendingTrust)
	a, err := repotrust.AssessRepo(context.Background(), repo)
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
	a, err := repotrust.AssessRepo(context.Background(), repo)
	require.NoError(t, err)
	assert.False(t, a.Granted, "declining must write nothing")
	assert.True(t, a.WantsPrompt(), "the next create must ask again")
}

func TestCreateSessionFromForm_GrantedRepoSkipsThePrompt(t *testing.T) {
	repo := gitInitRepo(t)
	commitRepoLocal(t, repo, testRepoLocal)
	a, err := repotrust.AssessRepo(context.Background(), repo)
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
