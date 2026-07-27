package config

// Reading the Claude login a config dir actually holds.
//
// Routing picks a DIRECTORY (see ResolveClaudeAccount); the account name beside it
// in config.json is a label nothing has ever verified. The two can drift apart
// without a trace: re-running `/login` inside a config dir rewrites its credentials
// in place, so a dir named "personal" can start answering as a work account while
// every route, badge and pool keeps saying "personal". Sessions then bill an account
// the user never chose, and the only visible symptom is a usage figure on a webpage.
//
// This file closes that gap by reading the identity out of <configDir>/.claude.json,
// which claude maintains under oauthAccount. It is a pure, strictly READ-ONLY probe —
// deliberately unlike LoadConfig/LoadState in this same package, which seed and
// rewrite the data dir and so cannot run beside a live TUI.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// AccountIdentity is the Claude login recorded in one config dir. Email is what a
// user recognises and what expect_account pins against; UUID is the stable
// machine identity and what collision detection keys on (an email can be changed on
// an account that stays the same, and two dirs holding one account share the UUID
// whatever their labels say). Org is display-only context.
type AccountIdentity struct {
	Email string
	UUID  string
	Org   string
}

// CollisionKey is the value two accounts must share to be the same real login.
// It prefers UUID and falls back to the lowercased email so a future claude that
// reshapes one field still leaves the check something to compare; "" means this
// identity cannot participate in collision detection at all.
//
// The fallback cannot cross-match a UUID-keyed identity with an email-keyed one, so
// a dir missing its UUID is invisible to the check rather than wrongly matched. That
// is the safe direction: a missed warning costs a user the diagnosis they would have
// had anyway, while a false one accuses two legitimately distinct accounts.
func (id AccountIdentity) CollisionKey() string {
	if id.UUID != "" {
		return id.UUID
	}
	return strings.ToLower(id.Email)
}

// MatchesPin reports whether this identity satisfies an expect_account value.
// Comparison is on email, case-insensitively and after trimming: it is the field a
// user can read off the Claude UI and type into config.json, and neither its case
// nor a stray copied space is a different account. An empty pin matches nothing —
// callers must treat "unpinned" as "do not check", never as "check against nothing".
func (id AccountIdentity) MatchesPin(pin string) bool {
	pin = strings.TrimSpace(pin)
	if pin == "" {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(id.Email), pin)
}

// ReadAccountIdentity returns the login recorded in configDir, or ok=false when no
// identity can be read there. Every failure — a relative dir, a missing or
// unreadable file, malformed JSON, an absent or reshaped oauthAccount, an object
// carrying neither an email nor a UUID — collapses into ok=false. None of them are
// distinguishable to a user and none justify treating a dir as if it held a KNOWN
// but different account, which is the one conclusion that could wrongly block a
// launch.
//
// Conservative in the same way as doctor's fileGateReader and session/tmux/trust.go,
// the other readers of this file: a missing file means claude was never onboarded in
// that dir, and a malformed one is claude's file and claude's problem to repair.
//
// A relative (or empty) dir is refused rather than joined against the caller's
// working directory. "" is the routing value that means "inherit the ambient env",
// and resolving it to ./.claude.json would report on a file no session has any
// relationship to — inventing an identity for an account that deliberately has none.
func ReadAccountIdentity(configDir string) (AccountIdentity, bool) {
	if !filepath.IsAbs(configDir) {
		return AccountIdentity{}, false
	}
	data, err := os.ReadFile(filepath.Join(configDir, ".claude.json"))
	if err != nil {
		return AccountIdentity{}, false
	}
	var root struct {
		OAuthAccount *struct {
			EmailAddress     string `json:"emailAddress"`
			AccountUUID      string `json:"accountUuid"`
			OrganizationName string `json:"organizationName"`
		} `json:"oauthAccount"`
	}
	if err := json.Unmarshal(data, &root); err != nil {
		return AccountIdentity{}, false
	}
	if root.OAuthAccount == nil {
		return AccountIdentity{}, false
	}
	id := AccountIdentity{
		Email: strings.TrimSpace(root.OAuthAccount.EmailAddress),
		UUID:  strings.TrimSpace(root.OAuthAccount.AccountUUID),
		Org:   strings.TrimSpace(root.OAuthAccount.OrganizationName),
	}
	if id.Email == "" && id.UUID == "" {
		return AccountIdentity{}, false // present but says nothing identifying
	}
	return id, true
}

// ReadIdentity is ReadAccountIdentity for a configured account, resolving its
// config_dir (expanding ~) first. An inherit-env account — config_dir "" — reads
// nothing and reports ok=false: it injects no CLAUDE_CONFIG_DIR, so it has no dir of
// its own whose identity could be verified.
func (a ClaudeAccount) ReadIdentity() (AccountIdentity, bool) {
	return ReadAccountIdentity(a.ResolvedConfigDir())
}
