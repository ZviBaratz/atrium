// ui/menu_hints_test.go
package ui

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// While hint mode is up the bar must teach its three gestures; it is the only
// in-frame documentation the mode has.
func TestMenu_StateHintsLine(t *testing.T) {
	m := NewMenu()
	m.SetSize(120, 1)
	m.SetState(StateHints)
	out := m.String()
	assert.Contains(t, out, "copy")
	assert.Contains(t, out, "copy + open")
	assert.Contains(t, out, "cancel")
}
