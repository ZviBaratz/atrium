package ui

import zone "github.com/lrstanley/bubblezone/v2"

// The list and tabbed window Mark() clickable regions via the global bubblezone
// manager, which panics if it was never initialized. Production code does this in
// app.Run; tests render these components directly, so initialize it here too.
func init() { zone.NewGlobal() }
