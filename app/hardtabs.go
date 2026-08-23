//go:build linux || solaris || aix || darwin || freebsd || dragonfly

package app

import (
	"os"

	"golang.org/x/sys/unix"
)

// suppressHardTabs stops Bubble Tea's renderer writing literal tab bytes into
// the frame, and returns the restore that puts the terminal back (#796, #696).
//
// The bytes are cursor movement, not content. Ultraviolet's relativeCursorMove
// moves the cursor forward with a run of '\t' rather than a CUF sequence when
// hard tabs are available, and Bubble Tea decides they are available in
// Program.checkOptimizedMovements, which reads TABDLY off the INPUT tty's
// termios as it stood before raw mode. A default terminal is TAB0, so the
// optimization is on and Atrium's frames reach the screen carrying tabs even
// though every frame the model produces is tab-free.
//
// That matters because a tab's drawn width is whatever the reader's tab stops
// say, not what Atrium's width math computed, and because tmux 3.6 records the
// skipped cells as tab cells: capture-pane and a user's copy-out both come back
// with '\t' where the screen shows alignment. Setting TABDLY to anything but
// TAB0 is the only input that switch reads, so it is the whole fix; the cost is
// a handful of extra bytes per frame.
//
// TABDLY is a multi-bit field, not a flag, so the write clears it before
// setting TAB3 — "the driver expands tabs", the one non-TAB0 value every
// supported platform spells the same way. OR-ing the bare mask in instead would
// be right only by accident: on Linux TABDLY and TAB3 are the same value, while
// on Darwin the field is several bits wide and TAB3 is one of them, so the mask
// would write a combination that is no delay setting at all. Raw mode clears
// OPOST and ignores the field either way; what reads it is Bubble Tea.
//
// Not a terminal, or an ioctl that fails, leaves the terminal alone and returns
// a no-op restore — a frame with tabs in it is a blemish, not a reason to
// refuse to start.
func suppressHardTabs(f *os.File) (restore func()) {
	noop := func() {}
	if f == nil {
		return noop
	}
	fd := int(f.Fd())
	before, err := unix.IoctlGetTermios(fd, getTermios)
	if err != nil {
		return noop
	}
	after := *before
	after.Oflag = after.Oflag&^tabdly | tab3
	if err := unix.IoctlSetTermios(fd, setTermios, &after); err != nil {
		return noop
	}
	return func() { _ = unix.IoctlSetTermios(fd, setTermios, before) }
}
