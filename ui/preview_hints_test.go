// ui/preview_hints_test.go
package ui

import (
	"testing"

	"github.com/ZviBaratz/atrium/session"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newHintTestInstance(t *testing.T) *session.Instance {
	t.Helper()
	inst, err := session.NewInstance(session.InstanceOptions{
		Title: "hints", Path: t.TempDir(), Program: "echo",
	})
	require.NoError(t, err)
	return inst
}

// The overlay freezes the pane: String renders the decorated content and
// UpdateContent for the same instance becomes a no-op, so the 100ms poll
// can't repaint over the hints.
func TestPreviewHintOverlay_FreezesAndRenders(t *testing.T) {
	p := NewPreviewPane()
	p.SetSize(40, 10)
	inst := newHintTestInstance(t)

	p.SetHintOverlay(inst, "DECORATED CONTENT")
	assert.True(t, p.InHintMode())
	assert.Contains(t, p.String(), "DECORATED CONTENT")

	require.NoError(t, p.UpdateContent(inst))
	assert.True(t, p.InHintMode(), "same-instance tick must not drop the overlay")
	assert.Contains(t, p.String(), "DECORATED CONTENT")
}

// The overlay belongs to one instance: rendering any other instance drops it
// (the scroll-snapshot ownership rule — a frozen mode must never outlive its
// trigger conditions).
func TestPreviewHintOverlay_DroppedOnInstanceChange(t *testing.T) {
	p := NewPreviewPane()
	p.SetSize(40, 10)
	inst := newHintTestInstance(t)

	p.SetHintOverlay(inst, "DECORATED")
	require.NoError(t, p.UpdateContent(nil))
	assert.False(t, p.InHintMode())
}

// LiveContent gates hint-mode entry: only a live, non-fallback, non-scrolling
// pane with text qualifies.
func TestPreviewLiveContent(t *testing.T) {
	p := NewPreviewPane()
	p.SetSize(40, 10)

	_, ok := p.LiveContent()
	assert.False(t, ok, "empty pane has nothing to hint")

	p.previewState = previewState{fallback: false, text: "some output"}
	got, ok := p.LiveContent()
	assert.True(t, ok)
	assert.Equal(t, "some output", got)

	p.previewState = previewState{fallback: true, text: "splash"}
	_, ok = p.LiveContent()
	assert.False(t, ok, "fallback splash is not hintable")
}
