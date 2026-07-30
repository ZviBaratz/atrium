package theme

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// Current() is no longer read only on the bubbletea loop: the tmux bar push runs as
// a tea.Cmd — its own goroutine — and reads the active theme to colour the status
// band (session/tmux/barstyle.go). Selection still happens on the loop, so this
// models the real shape: one writer flipping the palette, many readers off-loop.
// Fails under -race the moment `current` goes back to a plain pointer.
func TestCurrent_IsSafeToReadOffTheLoop(t *testing.T) {
	t.Cleanup(Set(Current().Name))

	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // the loop: the only writer
		defer wg.Done()
		for range 50 {
			Set("tokyo-night")
			Set("catppuccin-mocha")
		}
	}()
	for range 4 { // bar pushes: readers on their own goroutines
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				require.NotNil(t, Current(), "Current() must never observe a nil theme")
			}
		}()
	}
	wg.Wait()
}

// A reader holds a consistent snapshot: compose() builds a fresh *Theme and the
// pointer is swapped whole, so a concurrent Set can never tear one theme's palette
// across another's glyphs.
func TestCurrent_ReturnsAWholeSnapshot(t *testing.T) {
	t.Cleanup(Set(Current().Name))

	Set("tokyo-night")
	snapshot := Current()
	Set("catppuccin-mocha")

	require.Equal(t, "tokyo-night", snapshot.Name,
		"a held snapshot must not mutate when the selection moves on")
	require.Equal(t, Get("tokyo-night").Palette.BarBg, snapshot.Palette.BarBg)
}
