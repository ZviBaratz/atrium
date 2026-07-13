package app

import (
	"context"
	"testing"

	"github.com/ZviBaratz/atrium/ui"

	"github.com/stretchr/testify/require"
)

// TestAdvanceSplashFrameFrozenWhileOverlayUp locks the "don't animate behind a
// dialog" behavior: the empty-state splash advances only in the default state, so
// the welcome dialog (and any other overlay) the user is reading has a still
// background, not churning motion.
func TestAdvanceSplashFrameFrozenWhileOverlayUp(t *testing.T) {
	tw := ui.NewTabbedWindow(ui.NewPreviewPane(), ui.NewDiffPane(), ui.NewTerminalPane(context.Background()))
	m := &home{tabbedWindow: tw}

	// While an overlay owns the screen, the animation is frozen.
	m.state = stateWelcome
	m.advanceSplashFrame()
	m.advanceSplashFrame()
	require.Zero(t, m.splashFrame, "splash must not advance behind an overlay")

	// Back in the default state it ticks again.
	m.state = stateDefault
	m.advanceSplashFrame()
	require.Equal(t, 1, m.splashFrame, "splash advances in the default state")
}
