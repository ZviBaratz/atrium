package overlay

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// A picker filter is a text buffer, so a paste belongs in it whole. handleKey
// cannot serve that: it switches on msg.Code, and a paste's first rune is a
// legitimate rune — a leading space is Code KeySpace, which appended one space
// and dropped the rest of the clipboard. Pasted paths and quoted text lead with a
// space often enough that this was the common case, not the exotic one.
func TestPickerPasteKeepsTheWholeText(t *testing.T) {
	for _, text := range []string{
		" leading space",
		"plain text",
		"\ttab-led",
		"trailing space ",
	} {
		t.Run(text, func(t *testing.T) {
			p := newPicker(false)

			consumed, changed := p.handlePaste(text)

			require.True(t, consumed, "a paste into a focused picker is consumed")
			require.True(t, changed, "a non-empty paste changes the filter")
			require.Equal(t, text, p.filter, "the whole paste must land in the filter")
		})
	}
}

// An empty paste must not be reported as an edit: a sync picker resets its cursor
// on every edit, so a no-op paste would silently move the selection.
func TestPickerEmptyPasteIsNotAnEdit(t *testing.T) {
	p := newPicker(false)
	p.filter = "keep"
	p.cursor = 3

	_, changed := p.handlePaste("")

	require.False(t, changed, "an empty paste is not a filter edit")
	require.Equal(t, "keep", p.filter)
	require.Equal(t, 3, p.cursor, "an empty paste must not reset the cursor")
}

// A paste appends to whatever the user already typed, exactly as typing would.
func TestPickerPasteAppendsToExistingFilter(t *testing.T) {
	p := newPicker(false)
	p.filter = "atr"

	_, changed := p.handlePaste("ium/app")

	require.True(t, changed)
	require.Equal(t, "atrium/app", p.filter)
}
