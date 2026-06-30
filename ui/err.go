package ui

import (
	"strings"

	"github.com/ZviBaratz/atrium/ui/theme"

	"github.com/mattn/go-runewidth"
)

// ErrBox is the single-row error banner rendered beneath the help bar when an
// action fails.
type ErrBox struct {
	height, width int
	err           error
}

// NewErrBox returns an empty ErrBox.
func NewErrBox() *ErrBox {
	return &ErrBox{}
}

// SetError sets the error to display; nil clears it.
func (e *ErrBox) SetError(err error) {
	e.err = err
}

// Clear removes the displayed error.
func (e *ErrBox) Clear() {
	e.err = nil
}

// HasError reports whether an error is currently set. The layout uses this to
// decide whether to allot the error box a row.
func (e *ErrBox) HasError() bool {
	return e.err != nil
}

// SetSize sets the box's render dimensions; long errors are truncated to fit.
func (e *ErrBox) SetSize(width, height int) {
	e.width = width
	e.height = height
}

// Fits reports whether the toast can convey err without losing content: a
// single line that survives String()'s truncation threshold intact. Multi-line
// errors never fit (String flattens them with "//"); over-wide ones don't
// either, unless the box has no measured width yet (startup, tests), where the
// toast is the safe default. Callers route non-fitting errors to a persistent
// modal instead.
func (e *ErrBox) Fits(err error) bool {
	if err == nil {
		return true
	}
	msg := err.Error()
	if strings.Contains(msg, "\n") {
		return false
	}
	return e.width <= 0 || runewidth.StringWidth(msg) <= e.width-3
}

func (e *ErrBox) String() string {
	// No error means no row: returning "" keeps the caller from joining a blank
	// line beneath the help bar (lipgloss.JoinVertical counts "" as one line).
	if e.err == nil {
		return ""
	}
	err := e.err.Error()
	lines := strings.Split(err, "\n")
	err = strings.Join(lines, "//")
	if runewidth.StringWidth(err) > e.width-3 && e.width-3 >= 0 {
		err = runewidth.Truncate(err, e.width-3, "…")
	}
	return centerInBox(e.width, e.height, theme.Current().DangerStyle().Render(err))
}
