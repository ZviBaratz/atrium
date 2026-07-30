package actions

import (
	"fmt"

	"github.com/atotto/clipboard"
)

// CopyToClipboard is the OS-clipboard leg of a copy: it shells out to the host
// copier (xclip/xsel/wl-copy/pbcopy) so a paste lands in apps that read the system
// selection rather than the terminal. It is one of two independent legs — the other
// is the OSC 52 escape Bubble Tea emits (see home.copyToClipboard), which is what
// carries a copy across SSH, where the remote has no clipboard binary. Either leg
// succeeding is a copy, so its error is a missing fallback, not a failed copy.
//
// A package var so tests (and alternate front ends) can substitute a fake without
// touching the host clipboard.
var CopyToClipboard = copyToClipboard

// execCopy is the underlying exec-based copier. A package var so tests can exercise
// the failure path without a real clipboard utility on PATH.
var execCopy = clipboard.WriteAll

// copyToClipboard runs the OS copier, naming the missing dependency when it is not
// installed so a caller that has no other leg can say something actionable.
func copyToClipboard(text string) error {
	if err := execCopy(text); err != nil {
		return fmt.Errorf(
			"no clipboard utility (%w) — install xclip/xsel/wl-clipboard for the OS clipboard leg",
			err)
	}
	return nil
}
