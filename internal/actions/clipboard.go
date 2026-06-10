package actions

import "github.com/atotto/clipboard"

// CopyToClipboard writes text to the system clipboard. It is a package var so
// tests can substitute a fake without touching the host clipboard.
var CopyToClipboard = clipboard.WriteAll
