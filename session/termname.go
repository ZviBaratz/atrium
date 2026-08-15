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
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.mintTermNameLocked()
}

// mintTermNameLocked is the mint expression itself, and the only place `<tmux name><suffix>`
// is spelled. Both public entry points go through it so they cannot drift: a claim that
// stored one spelling while the exported mint computed another would file a shell under a
// name terminalKey's fallback and CloseForInstance's bump do not agree with, which is #708
// with the derivation moved one level down. Any future qualifier — sanitizing, truncating to
// tmux's limits — lands here once.
//
// Caller holds i.mu in either mode.
func (i *Instance) mintTermNameLocked() string {
	if i.tmuxName == "" {
		return ""
	}
	return i.tmuxName + TermSessionSuffix
}

// ClaimTerminalSessionName returns the owned name, minting and storing one when this session
// does not have one yet, and reports whether THIS call is the one that minted it. It is
// idempotent: a second claim returns the first claim's name and minted=false, including
// after a rename has moved the tmux name it was minted from.
//
// The pane calls it BEFORE its tmux round trip rather than at install time, so the key it
// files the finished shell under is the key every later lookup and every reap computes —
// even for a rename that lands mid-create.
//
// minted is what lets a create that then fails put the name back. Without it the instance
// would own — and persist — a name no shell was ever started under, and the collision
// guards would go on reserving that title against new sessions on behalf of nothing. It is
// deliberately narrower than "the name is unused": a name this session already owned is
// left alone on failure, because a shell may still be sitting on it.
func (i *Instance) ClaimTerminalSessionName() (name string, minted bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.termName == "" {
		if fresh := i.mintTermNameLocked(); fresh != "" {
			i.termName = fresh
			minted = true
		}
	}
	return i.termName, minted
}

// ReleaseTerminalSessionName forgets the owned name, so the next claim mints a fresh one
// from whatever this session is called by then.
//
// Called once the shell on that name is known to be gone — reaped, or never started.
// Holding a name past its shell would keep a renamed session squatting on a title someone
// else may since have taken (see OwnedSiblingCollides), and would name a resumed session's
// shell after the title it used to have.
//
// The order matters and runs the other way from releaseRunTmux: kill first, forget second.
// The owned name is the ONLY record of a shell the pane has no entry for — nothing sweeps
// shells at exit, so one can outlive the process and be adopted by the next run — and
// forgetting it while its shell is still up is the same permanent orphan #708 is about.
func (i *Instance) ReleaseTerminalSessionName() {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.termName = ""
}
