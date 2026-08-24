//go:build linux || solaris || aix

package app

import "golang.org/x/sys/unix"

// The termios ioctls and the tab-delay field, named per platform family. Split
// from hardtabs.go because x/sys/unix spells the ioctls TCGETS/TCSETS only here
// and TIOCGETA/TIOCSETA on the BSDs; charmbracelet/x/termios makes the same
// split and exports only the getter, which is why this is written out rather
// than borrowed.
//
// Both stay untyped: Termios.Oflag is uint32 on Linux and uint64 on Darwin, and
// an untyped constant is what lets one expression serve both. The values differ
// between the platforms sharing this file, which is the reason the field is
// referred to by name and never by number.
const (
	getTermios = unix.TCGETS
	setTermios = unix.TCSETS
	tabdly     = unix.TABDLY
	tab3       = unix.TAB3
)
