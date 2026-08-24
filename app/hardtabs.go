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
// would write a combination that is no delay setting at all.
//
// The suppressed state is not one a child may inherit — see yieldHardTabs.
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
	hardTabsAsFound = before
	if err := expandTabs(fd); err != nil {
		hardTabsAsFound = nil
		return noop
	}
	return func() {
		_ = restoreTabDelay(fd)
		hardTabsAsFound = nil
	}
}

// hardTabsAsFound is the tty as app.Run found it, kept so the delay field can be
// put back — by the restore above, and for the span of a handover by
// yieldHardTabs. nil means nothing is suppressed and there is nothing to yield.
//
// Written once by suppressHardTabs before tea.NewProgram, and read afterwards only
// from the suspended event-loop goroutine, which cannot be running until the
// program that owns it exists. So the two never overlap and this needs no lock,
// the same ordering attachCommand's outcome relies on.
var hardTabsAsFound *unix.Termios

// yieldHardTabs hands the tab-delay field back for as long as a child owns the
// terminal, and returns the call that takes it again.
//
// Asking the driver to expand tabs is harmless while Atrium renders: Bubble Tea
// reads the field, stops emitting tabs, and writes its frames through a tty in raw
// mode, where OPOST is off and the field is not consulted. It is not harmless for a
// child Atrium hands the terminal to in COOKED mode — attachCommand.raw false, which
// is what an `output: terminal` custom command asks for. There OPOST is on, so the
// driver expands that child's tabs itself, and it counts the bytes of the child's
// ANSI escapes as printable columns: a colourized, tab-aligned `git diff` or `go
// test` lands its columns in the wrong place. Measured on a pty: with OPOST|TAB3 set,
// "\x1b[31mab\tX" reaches the master as two spaces where the terminal would have
// moved six.
//
// So the child gets the terminal as the user's shell had it. The resuppress has to
// land before Bubble Tea's RestoreTerminal re-reads the field on the way back in,
// which a defer in attachCommand.Run guarantees — Program.exec re-captures the
// terminal only after Run returns.
func yieldHardTabs(f *os.File) (resuppress func()) {
	noop := func() {}
	if f == nil || hardTabsAsFound == nil {
		return noop
	}
	fd := int(f.Fd())
	if err := restoreTabDelay(fd); err != nil {
		return noop
	}
	return func() { _ = expandTabs(fd) }
}

// expandTabs and restoreTabDelay move the TABDLY field alone, leaving the rest of
// the termios as they find it. That matters on the way back from a handover: the
// child had the terminal, and whatever else it changed is not ours to revert. They
// read-modify-write separately rather than share a helper because Termios.Oflag is
// uint32 on Linux and the BSDs and uint64 on Darwin, so the field has no one name to
// pass it under.
func expandTabs(fd int) error {
	t, err := unix.IoctlGetTermios(fd, getTermios)
	if err != nil {
		return err
	}
	t.Oflag = t.Oflag&^tabdly | tab3
	return unix.IoctlSetTermios(fd, setTermios, t)
}

func restoreTabDelay(fd int) error {
	if hardTabsAsFound == nil {
		return nil
	}
	t, err := unix.IoctlGetTermios(fd, getTermios)
	if err != nil {
		return err
	}
	t.Oflag = t.Oflag&^tabdly | hardTabsAsFound.Oflag&tabdly
	return unix.IoctlSetTermios(fd, setTermios, t)
}
