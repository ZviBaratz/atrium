package overlay

import "github.com/ZviBaratz/atrium/config"

// AccountSelection is the create-form's account choice handed to the session
// spawner. A non-nil Member pins that exact account, bypassing rotation and
// availability (a deliberate pin is the escape hatch even for a limited
// account). Otherwise Pool names the pool to rotate within. Pool is also the
// list cluster key and is set even for a member pin, so a pinned session still
// groups under its pool.
type AccountSelection struct {
	Pool   string
	Member *config.ClaudeAccount
}
