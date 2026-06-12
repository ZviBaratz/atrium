package session

import "github.com/ZviBaratz/atrium/session/agent"

// PinnedPermissionMode returns the --permission-mode flag value parsed from
// Program ("" = none). Claude-only: other agents don't use this flag.
// Derived on demand — Program is immutable and already persisted.
func (i *Instance) PinnedPermissionMode() string {
	if agent.Resolve(i.Program).Key != agent.KeyClaude {
		return ""
	}
	return agent.PermissionModeFlag(i.Program)
}
