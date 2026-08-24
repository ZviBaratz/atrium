//go:build darwin || freebsd || dragonfly

package app

import "golang.org/x/sys/unix"

// The BSD/Darwin half of the split described in hardtabs_ioctl_linux.go.
//
// The build tag is narrower than "the BSDs" on purpose, and it is copied from
// Bubble Tea's rather than chosen: termios_unix.go covers darwin/linux/solaris/
// aix and termios_bsd.go covers dragonfly/freebsd, so those six are exactly the
// platforms where checkOptimizedMovements can turn hard tabs ON. NetBSD and
// OpenBSD fall through to its termios_other.go no-op and never enable them —
// which is just as well, since x/sys/unix defines neither TABDLY nor TAB3
// there, and naming them would not compile.
const (
	getTermios = unix.TIOCGETA
	setTermios = unix.TIOCSETA
	tabdly     = unix.TABDLY
	tab3       = unix.TAB3
)
