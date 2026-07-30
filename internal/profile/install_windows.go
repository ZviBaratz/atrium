//go:build windows

package profile

import "context"

// Install is a no-op on Windows.
//
// The trigger is SIGUSR1, which Windows has no equivalent of — a process there
// cannot be signalled this way at all, so there is nothing to register and nothing
// to disarm. The package still builds and its pure parts stay testable; only the
// trigger is absent. A Windows profiling story would need a different door
// (a named pipe, or the HTTP endpoint), and should be built when someone needs it
// rather than guessed at now.
func Install(_ context.Context) (stop func()) { return func() {} }
