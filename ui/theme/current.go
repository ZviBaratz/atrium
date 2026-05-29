package theme

// current is the active theme. It is set once at startup via Set and must not
// be mutated afterward — rendering is single-threaded on the bubbletea loop, so
// no locking is needed.
var current = registry[DefaultThemeName]

// Current returns the active theme. Never nil.
func Current() *Theme { return current }

// Set activates the named theme (falling back to the default for unknown
// names) and returns a function that restores the previously active theme.
// Startup ignores the return value; tests use it for cleanup:
//
//	defer theme.Set("unicode")()
func Set(name string) (restore func()) {
	prev := current
	current = Get(name)
	return func() { current = prev }
}
