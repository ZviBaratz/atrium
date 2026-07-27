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

// NormalizedConfigDir is ResolvedConfigDir cleaned — the spelling identity work
// reads, caches and compares on. config_dir is hand-written, so one directory
// reaches these checks written several ways, and gates.go's installedGateDirs
// already cleans for exactly this reason: a trailing slash is not a different dir.
// Left raw, "/h/x" and "/h/x/" would be read twice, defeating the single-read
// guarantee, and then reported as two dirs that had drifted onto one login — sending
// the user after a second directory that does not exist.
//
// An account naming no dir returns "" rather than a cleaned one, because
// filepath.Clean("") is ".": every inherit-env account would otherwise acquire a
// config dir pointing at the process's working directory, and be reported and gated
// on a login no session ever routed there.
func (a ClaudeAccount) NormalizedConfigDir() string {
	dir := a.ResolvedConfigDir()
	if dir == "" {
		return ""
	}
	return filepath.Clean(dir)
}

// ReadIdentity is ReadAccountIdentity for a configured account, resolving and
// normalizing its config_dir (expanding ~) first. An inherit-env account — config_dir
// "" — reads nothing and reports ok=false: it injects no CLAUDE_CONFIG_DIR, so it has
// no dir of its own whose identity could be verified.
func (a ClaudeAccount) ReadIdentity() (AccountIdentity, bool) {
	return ReadAccountIdentity(a.NormalizedConfigDir())
}

// IdentityReadFunc reads the login recorded in a config dir. A function rather than
// an interface because two packages inject it — doctor's report and the app's
// launch gate — and both only ever need the one call.
type IdentityReadFunc func(configDir string) (AccountIdentity, bool)

// IdentityCheck is how an account's declared expectation compares with the login its
// config dir actually holds.
type IdentityCheck int

const (
	// IdentityNoDir means the account injects no CLAUDE_CONFIG_DIR, so it has no
	// directory of its own to verify and rides whatever the ambient env supplies.
	// Distinct from IdentityUnreadable: there is no dir here to be wrong about, so
	// callers must not report or warn about one.
	IdentityNoDir IdentityCheck = iota
	// IdentityUnreadable means the dir names no login (claude was never onboarded
	// there, or wrote a file this build cannot parse). Never evidence of a WRONG
	// login — only of an unanswered question.
	IdentityUnreadable
	// IdentityUnpinned means the login was read but the account declares no
	// expect_account, so there is nothing to verify it against.
	IdentityUnpinned
	// IdentityVerified means expect_account is set and the dir holds that login.
	IdentityVerified
	// IdentityWrongAccount means expect_account is set and the dir holds a
	// DIFFERENT login: work sent here bills someone the user did not choose. This
	// is the only state that justifies refusing to launch.
	IdentityWrongAccount
)

// CheckIdentity classifies a against the login its config dir actually holds,
// returning that login too (zero when none could be read).
//
// This is the single classifier behind both `atrium doctor`'s report and the
// launch-time gate. They must never disagree — a report saying "ok" beside an
// account the gate then refuses would send the user hunting the wrong problem — and
// the only way to guarantee that is for there to be one implementation of the rules
// rather than two that look alike.
//
// Order matters: an unreadable dir is classified before the pin is consulted, so an
// account is never called verified or wrong on the strength of a read that failed.
func (a ClaudeAccount) CheckIdentity(read IdentityReadFunc) (IdentityCheck, AccountIdentity) {
	dir := a.NormalizedConfigDir()
	if dir == "" {
		return IdentityNoDir, AccountIdentity{}
	}
	actual, ok := read(dir)
	if !ok {
		return IdentityUnreadable, AccountIdentity{}
	}
	if strings.TrimSpace(a.ExpectAccount) == "" {
		return IdentityUnpinned, actual
	}
	if actual.MatchesPin(a.ExpectAccount) {
		return IdentityVerified, actual
	}
	return IdentityWrongAccount, actual
}
