package session

// termname.go — the owned name of the terminal tab's shell (#708).
//
// The shell itself lives in ui: the pane creates it, captures it, attaches to it and reaps
// it. Only its NAME lives here, for the same reason runcmd.go keeps `_run`'s: a name that
// is re-derived on each use stops resolving the moment a deep rename moves the agent's tmux
// session, and the sibling is left where it was — running, holding whatever the user
// started in it, and reachable by nothing.
//
// So the name is minted from the `<tmux name>_term` convention exactly once, at the point
// the pane commits to creating a shell, and owned from then on: persisted with the instance
// (InstanceData.TermSession) so a restart finds the same shell rather than minting a second
// one beside it, and released when the shell is reaped so the next mint follows the title
// the session has by then.
//
// The pane consumes it through terminalKey (ui/terminal.go), the one production reader that
// still falls back to the mint — for an instance whose shell has never been created, where
// there is no cached state for a moving key to strand.

// TerminalSessionName returns the tmux name of the terminal shell this session OWNS, or ""
// when it has never had one created.
//
// "" is a report, not a safety claim: unlike RunSessionName it does not mean "nothing on
// that name is ours to touch" — see Instance.termName for why the shell's contract differs.
func (i *Instance) TerminalSessionName() string {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.termName
}

// MintTerminalSessionName is the name a first claim would take: this session's own tmux name
// plus the reserved suffix. "" when the instance has no tmux name yet — built but never
// started, so there is nothing to derive from and no shell to host.
//
// In production only two callers consult it: ClaimTerminalSessionName below, and
// terminalKey's pre-claim fallback (tests probe it to name what a derivation WOULD produce,
// which is the point they are making). Everything else reads TerminalSessionName, which is
// what makes "the name it was created under is the name it is reaped under" true rather
// than usually true.
func (i *Instance) MintTerminalSessionName() string {
	name := i.TmuxSessionName()
	if name == "" {
		return ""
	}
	return name + TermSessionSuffix
}

// ClaimTerminalSessionName returns the owned name, minting and storing one when this session
// does not have one yet. It is idempotent: a second claim returns the first claim's name,
// including after a rename has moved the tmux name it was minted from.
//
// The pane calls it BEFORE its tmux round trip rather than at install time, so the key it
// files the finished shell under is the key every later lookup and every reap computes —
// even for a rename that lands mid-create.
func (i *Instance) ClaimTerminalSessionName() string {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.termName == "" {
		if name := i.tmuxName; name != "" {
			i.termName = name + TermSessionSuffix
		}
	}
	return i.termName
}

// ReleaseTerminalSessionName forgets the owned name, so the next claim mints a fresh one
// from whatever this session is called by then.
//
// Called wherever the shell is reaped, and unconditionally there — the same shape as the
// reap generation it sits beside, and for the same reason: a reap that found nothing cached
// still means no shell of ours is on that name. Holding a name past its shell would keep a
// renamed session squatting on a title someone else may since have taken (see
// OwnedSiblingCollides), and would name a resumed session's shell after the title it used
// to have.
func (i *Instance) ReleaseTerminalSessionName() {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.termName = ""
}
