package main

import (
	"os"

	"github.com/ZviBaratz/atrium/config"
)

// logDir resolves the directory log.Initialize writes atrium.log into: the data
// dir, which is the one thing that already tells one Atrium instance from another
// (#566). Before this the log was a single fixed name in the shared OS temp dir,
// so an isolated instance — separate HOME, separate tmux socket, its own data dir
// — still wrote into the real fleet's log, interleaved and unmarked.
//
// It lives here, in package main, rather than in package log, because config
// imports log: resolving the destination inside log would be an import cycle.
// main already imports both, so it is the one place that can decide.
//
// A home directory that cannot be resolved leaves nothing to derive a data dir
// from, and such an environment has no state, no worktrees and no second instance
// to be confused with. Falling back to the temp dir keeps diagnostics in a case
// where the collision this change prevents cannot arise anyway; dropping them
// would trade a real log for a hazard that is not present.
func logDir() string {
	if dir, err := config.GetConfigDir(); err == nil {
		return dir
	}
	return os.TempDir()
}
