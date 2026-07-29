package app

import zone "github.com/lrstanley/bubblezone/v2"

// home.View().Content Scan()s the frame and the list Mark()s rows via the global
// bubblezone manager, which panics if it was never initialized. Production code
// does this in Run; tests render View directly, so initialize it here too.
func init() { zone.NewGlobal() }
