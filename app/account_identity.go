package app

// Refusing to launch a session onto the wrong Claude login.
//
// Routing selects a config DIRECTORY, and until a session actually runs, nothing has
// checked that the directory holds the account whose name is written beside it. It
// can stop holding it at any time: `/login` inside a config dir rewrites its
// credentials in place, so the dir a route calls "personal" can start answering as a
// work account with no error, no log line and no change to any config Atrium reads.
// Sessions then bill an account nobody chose, and the first symptom is a usage figure
// on a webpage, read hours later, after the quota is gone.
//
// An account that sets expect_account is asserting which login its directory holds.
// This file is where that assertion is enforced, at the last moment before the
// CLAUDE_CONFIG_DIR is injected — because a wrong launch cannot be undone by noticing
// it afterwards.

import (
	"fmt"
	"strings"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/session"
)

// accountIdentityError reports why acct must not be launched, or nil to proceed.
// It decides on config.ClaudeAccount.CheckIdentity — the same classifier
// `atrium doctor` reports from — so the section can never call an account fine that
// this gate then refuses.
//
// Only IdentityWrongAccount refuses. The other outcomes proceed, for reasons that
// differ:
//
//	IdentityUnpinned     No expectation was declared, so there is nothing to verify
//	                     against. Verification is opt-in; an account that says
//	                     nothing is not thereby wrong.
//	IdentityNoDir        An inherit-env account injects no CLAUDE_CONFIG_DIR and has
//	                     no directory of its own to be wrong about. Nothing is
//	                     logged: there is no dir to name, and a warning here would
//	                     fire on every launch forever.
//	IdentityUnreadable   claude will prompt for login in the pane, which cannot
//	                     silently mis-bill — the harm this gate exists to prevent is
//	                     absent, and refusing would strand anyone mid-onboarding.
//	                     Logged only when the account is pinned, because only then
//	                     did someone assert something that could not be checked.
func accountIdentityError(acct config.ClaudeAccount, read config.IdentityReadFunc) error {
	state, actual := acct.CheckIdentity(read)
	switch state {
	case config.IdentityWrongAccount:
		// No trailing period: Go error strings are composable fragments, and
		// staticcheck's ST1005 enforces it even on prose as long as this.
		return fmt.Errorf(
			"account %q expects %s, but %s is signed in as %s.\n"+
				"Launching would bill %s — re-run /login in that directory, "+
				"or correct expect_account in config.json",
			acct.Name, strings.TrimSpace(acct.ExpectAccount),
			acct.ResolvedConfigDir(), actual.Email, actual.Email)
	case config.IdentityUnreadable:
		if pin := strings.TrimSpace(acct.ExpectAccount); pin != "" {
			log.WarningLog.Printf(
				"claude account %q expects %s but no login is recorded in %s; launching unverified",
				acct.Name, pin, acct.ResolvedConfigDir())
		}
	}
	return nil
}

// identityRead returns the reader to verify with, defaulting to the real one so a
// home built without the seam (or in a test that does not care) still checks.
func (m *home) identityRead() config.IdentityReadFunc {
	if m.readIdentity != nil {
		return m.readIdentity
	}
	return config.ReadAccountIdentity
}

// verifyAccountIdentity is the creation-side gate: the resolved account is checked
// before the session exists, so a refusal leaves nothing behind to clean up. A nil
// account is the dormant case (no claude_accounts configured) and always proceeds.
func (m *home) verifyAccountIdentity(acct *config.ClaudeAccount) error {
	if acct == nil {
		return nil
	}
	return accountIdentityError(*acct, m.identityRead())
}

// verifyResumeIdentity is the resume-side gate. A paused session re-injects the
// CLAUDE_CONFIG_DIR it was born with, so it mis-bills exactly as a new session would
// and is gated the same way.
//
// This is deliberately unlike the session cap, which exempts resume (#463): that
// exemption rests on a hard cap being uncrossable by resuming, since every creation
// already held total ≤ Limit. No such argument exists here. The invariant is "never
// run on the wrong login", and the reasoning does not transfer just because both are
// launch-time gates.
//
// The account is found by config-dir anchor (#470), not by name: the dir is what the
// session actually injected, so a renamed or re-pooled account still resolves to the
// expectation its sessions are running under. An instance whose dir matches no
// configured account has no declared expectation and proceeds.
func (m *home) verifyResumeIdentity(inst *session.Instance) error {
	if m.appConfig == nil || inst == nil {
		return nil
	}
	acct, ok := m.appConfig.FindClaudeAccount(inst.ClaudeAccountName(), inst.ClaudeConfigDir())
	if !ok {
		return nil
	}
	return accountIdentityError(acct, m.identityRead())
}
