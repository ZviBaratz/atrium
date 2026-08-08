package app

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ZviBaratz/atrium/ui/imageview"
	"github.com/ZviBaratz/atrium/ui/overlay"
	"github.com/ZviBaratz/atrium/ui/theme"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeFixtureImage copies the committed tiny PNG to path, creating parents, and
// returns path. Copied rather than encoded for the reason image_load_test.go's
// header gives: encoding here would register the PNG decoder for the whole test
// binary.
func writeFixtureImage(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "images", "tiny.png"))
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, b, 0o644))
	return path
}

// runCmdInto runs cmd the way the runtime would — flattening a batch — and feeds
// every message it produces back through Update.
//
// Tests that stop at "a command was returned" prove only that a branch was
// taken. What matters here is what the command's message does when it lands,
// which is a different Update from the keypress that asked for it.
func runCmdInto(t *testing.T, h *home, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		return
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			runCmdInto(t, h, c)
		}
		return
	}
	if msg != nil {
		h.Update(msg)
	}
}

// Half blocks are the default, and each of the three fallbacks is a case where
// they would fail silently rather than loudly.
func TestImageRenderMode(t *testing.T) {
	cases := []struct {
		name             string
		glyphSet         string
		mono             bool
		blocksMeasureOne bool
		want             imageview.Mode
	}{
		{"default", theme.GlyphSetPlain, false, true, imageview.HalfBlock},
		{"nerd fonts still use blocks", theme.GlyphSetNerd, false, true, imageview.HalfBlock},
		{"ascii glyph rung", theme.GlyphSetASCII, false, true, imageview.ASCII},
		// Without colour a half block is the same glyph in every cell: a solid
		// rectangle that renders "successfully" and shows nothing.
		{"NO_COLOR", theme.GlyphSetPlain, true, true, imageview.ASCII},
		// A block that measures two cells while rendering one is the divergence
		// that desyncs the alt-screen renderer into ghost rows — here on every
		// row of the picture at once.
		{"blocks measure two cells", theme.GlyphSetPlain, false, false, imageview.ASCII},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, imageRenderMode(tc.glyphSet, tc.mono, tc.blocksMeasureOne))
		})
	}
}

// The live resolver must read all three inputs, not just the one it was written
// for. Each is flipped on its own against the same home.
//
// The width input is the one that cannot be flipped from here — both libraries
// decide at package init — so it is asserted against the measurement instead,
// which is the same wire currentImageRenderMode passes on. The environments that
// change that answer are covered where the predicate lives, by
// imageview's TestHalfBlocksMeasureOneCell_AcrossWidthEnvironments.
func TestCurrentImageRenderMode_ReadsTheLiveEnvironment(t *testing.T) {
	h := newHintsHome(t, newBranchInstance(t, "a", "b1"))
	baseline := imageview.HalfBlock
	if !imageview.HalfBlocksMeasureOneCell() {
		baseline = imageview.ASCII // a developer whose shell asks for east-asian widths
	}
	require.Equal(t, baseline, h.currentImageRenderMode(), "the measured width is not consulted")

	restore := theme.SetMono(true)
	assert.Equal(t, imageview.ASCII, h.currentImageRenderMode(), "NO_COLOR is not consulted")
	restore()

	h.appConfig.GlyphSet = theme.GlyphSetASCII
	assert.Equal(t, imageview.ASCII, h.currentImageRenderMode(), "glyph_set is not consulted")
	h.appConfig.GlyphSet = theme.GlyphSetPlain
}

// A load that failed explains itself and leaves no box behind. A silent failure
// here reads as "the key did nothing".
func TestHandleImageLoaded_ErrorNoticesAndOpensNothing(t *testing.T) {
	h := newHintsHome(t, newBranchInstance(t, "a", "b1"))

	_, cmd := h.handleImageLoaded(imageLoadedMsg{path: "/tmp/x.png", err: errors.New("nope")})

	assert.Equal(t, stateDefault, h.state)
	assert.Nil(t, h.imageOverlay)
	assert.NotNil(t, cmd, "a failed load must say so")
}

// The box swallows every key but esc. q in particular: falling through to the
// global keys would quit the app out from under an open overlay.
func TestImagePreviewState_SwallowsEverythingButEsc(t *testing.T) {
	h := newHintsHome(t, newBranchInstance(t, "a", "b1"))
	h.windowWidth, h.windowHeight = 100, 30
	h.openImagePreview(overlay.Image{Path: "/tmp/x.png", Pixels: parityImage(), Width: 30, Height: 20})
	require.Equal(t, stateImagePreview, h.state)

	for _, key := range []string{"q", "j", "n", "?", "!"} {
		_, _ = h.handleKeyPress(textMsg(key))
		require.Equalf(t, stateImagePreview, h.state, "%q left the preview", key)
	}
	_, _ = h.handleKeyPress(keyMsg("ctrl+c"))
	require.Equal(t, stateImagePreview, h.state, "ctrl+c left the preview")

	_, _ = h.handleKeyPress(keyMsg("esc"))
	assert.Equal(t, stateDefault, h.state)
	assert.Nil(t, h.imageOverlay, "closing must drop the decoded image, not pin it")
}

// The box takes the hint bar's row, like every other modal.
//
// Pinned explicitly because the guard that would otherwise cover it — the
// frame-restore walk — SKIPS any state whose bar is visible. Dropping this state
// from menuVisible's list would therefore not fail a test; it would quietly stop
// one from running, and leave a key hint stranded under a full-bleed picture.
func TestImagePreview_HidesTheHintBar(t *testing.T) {
	h := newHintsHome(t, newBranchInstance(t, "a", "b1"))
	h.state = stateImagePreview
	assert.False(t, h.menuVisible())
}

// The resolution root is the agent's own cwd. A session with no worktree — direct
// or unstarted — falls back to the repository, and a selection-less list gives up
// rather than resolving against Atrium's working directory.
func TestHintResolveRoot(t *testing.T) {
	h := newHintsHome(t)
	assert.Equal(t, "", h.hintResolveRoot(), "no selection means no root")

	h = newHintsHome(t, newBranchInstance(t, "a", "b1"))
	inst := h.list.GetSelectedInstance()
	require.NotNil(t, inst)
	// A session with no worktree — direct, or git before Start — falls back to
	// the directory it was created for.
	//
	// Asserted against inst.Path, NOT against inst.GetRepoPath(): that resolves
	// through the same nil-worktree guard, so it returns "" here and comparing
	// against it would compare "" to "" and pass however the fallback is written.
	// That is exactly what the previous version of this assertion did.
	require.Empty(t, inst.GetWorktreePath(), "fixture must have no worktree, or this proves nothing")
	require.NotEmpty(t, inst.Path)
	assert.Equal(t, inst.Path, h.hintResolveRoot())
	assert.Empty(t, inst.GetRepoPath(), "GetRepoPath is dead here — that is why it is not the fallback")
}

// A decode that lands after the user has moved on is DROPPED, not applied.
//
// Opening unconditionally seizes whatever state the next keypress reached. The
// hint-mode case is the damaging one: overwriting stateHints without
// exitHintMode leaves the preview pane frozen on its dimmed snapshot, and the
// tick's self-heal is gated on the state it just lost.
func TestHandleImageLoaded_DroppedWhenTheUserMovedOn(t *testing.T) {
	for _, st := range []state{stateHints, statePrompt, stateHelp, stateVisual} {
		h := newHintsHome(t, newBranchInstance(t, "a", "b1"))
		h.windowWidth, h.windowHeight = 100, 30
		h.state = st

		_, _ = h.handleImageLoaded(imageLoadedMsg{
			path: "/tmp/x.png", img: parityImage(), width: 30, height: 20,
		})

		assert.Equalf(t, st, h.state, "a late load seized state %d", st)
		assert.Nilf(t, h.imageOverlay, "a late load armed the overlay from state %d", st)
	}

	// Control: from the state the gesture actually leaves behind, it opens.
	h := newHintsHome(t, newBranchInstance(t, "a", "b1"))
	h.windowWidth, h.windowHeight = 100, 30
	_, _ = h.handleImageLoaded(imageLoadedMsg{
		path: "/tmp/x.png", img: parityImage(), width: 30, height: 20,
	})
	assert.Equal(t, stateImagePreview, h.state)
	assert.NotNil(t, h.imageOverlay)
}
