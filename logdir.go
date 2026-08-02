package main

import (
	"os"
)

// logDir resolves the directory log.Initialize writes atrium.log into.
//
// It lives here, in package main, rather than in package log, because config
// imports log: resolving the destination inside log would be an import cycle.
// main already imports both, so it is the one place that can decide (#566).
func logDir() string {
	return os.TempDir()
}
