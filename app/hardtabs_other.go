//go:build !(linux || solaris || aix || darwin || freebsd || dragonfly)

package app

import "os"

// suppressHardTabs is a no-op where the termios knob it drives does not exist.
//
// Two different reasons land here, and only one is a gap. NetBSD, OpenBSD and
// the rest reach Bubble Tea's termios_other.go, which leaves useHardTabs false:
// hard tabs are already off and there is nothing to suppress. Windows is the
// real gap — termios_windows.go sets useHardTabs = true unconditionally, with
// no terminal state to read, so a Windows build still emits the tab bytes #796
// removes everywhere else. Atrium drives tmux, which Windows has no port of, so
// that gap has no user today; it is still a gap, not coverage.
func suppressHardTabs(*os.File) (restore func()) { return func() {} }

// yieldHardTabs is a no-op for the same reason: with nothing suppressed there is
// nothing to hand back for a cooked child's span.
func yieldHardTabs(*os.File) (resuppress func()) { return func() {} }
