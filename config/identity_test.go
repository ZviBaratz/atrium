package config

import (
	"os"
	"path/filepath"
	"testing"
)

// claudeDirWith writes body as <tmp>/.claude.json and returns the (absolute) dir.
func claudeDirWith(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".claude.json"), []byte(body), 0o600); err != nil {
		t.Fatalf("write .claude.json: %v", err)
	}
	return dir
}

func TestReadAccountIdentity(t *testing.T) {
	cases := []struct {
		name string
		body string
		want AccountIdentity
		ok   bool
	}{{
		name: "full oauthAccount",
		body: `{"oauthAccount":{"emailAddress":"a@b.com","accountUuid":"u-1",
		        "organizationName":"Acme"}}`,
		want: AccountIdentity{Email: "a@b.com", UUID: "u-1", Org: "Acme"},
		ok:   true,
	}, {
		name: "unrelated keys are ignored",
		body: `{"numStartups":7,"cachedGrowthBookFeatures":{"x":true},
		        "oauthAccount":{"emailAddress":"a@b.com","accountUuid":"u-1"}}`,
		want: AccountIdentity{Email: "a@b.com", UUID: "u-1"},
		ok:   true,
	}, {
		name: "values are trimmed",
		body: `{"oauthAccount":{"emailAddress":"  a@b.com \n","accountUuid":" u-1 "}}`,
		want: AccountIdentity{Email: "a@b.com", UUID: "u-1"},
		ok:   true,
	}, {
		// A dir identified by only one field still identifies an account; the
		// caller decides what it can do with a partial identity.
		name: "email only",
		body: `{"oauthAccount":{"emailAddress":"a@b.com"}}`,
		want: AccountIdentity{Email: "a@b.com"},
		ok:   true,
	}, {
		name: "uuid only",
		body: `{"oauthAccount":{"accountUuid":"u-1"}}`,
		want: AccountIdentity{UUID: "u-1"},
		ok:   true,
	}, {
		name: "oauthAccount present but says nothing identifying",
		body: `{"oauthAccount":{"organizationName":"Acme"}}`,
		ok:   false,
	}, {
		name: "no oauthAccount key",
		body: `{"numStartups":7}`,
		ok:   false,
	}, {
		name: "oauthAccount null",
		body: `{"oauthAccount":null}`,
		ok:   false,
	}, {
		name: "oauthAccount reshaped to a non-object",
		body: `{"oauthAccount":"nope"}`,
		ok:   false,
	}, {
		name: "malformed json",
		body: `{"oauthAccount":`,
		ok:   false,
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ReadAccountIdentity(claudeDirWith(t, tc.body))
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v (identity %+v)", ok, tc.ok, got)
			}
			if !tc.ok {
				if got != (AccountIdentity{}) {
					t.Errorf("failed read returned %+v, want zero value", got)
				}
				return
			}
			if got != tc.want {
				t.Errorf("identity = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestReadAccountIdentityMissingFileAndDir(t *testing.T) {
	if _, ok := ReadAccountIdentity(t.TempDir()); ok {
		t.Error("a dir with no .claude.json read as identified")
	}
	if _, ok := ReadAccountIdentity(filepath.Join(t.TempDir(), "absent")); ok {
		t.Error("a nonexistent dir read as identified")
	}
}

// A relative or empty dir must be refused, not joined against the process's working
// directory. "" is the routing value for "inherit the ambient env", and resolving it
// to ./.claude.json would invent an identity for an account that deliberately has
// none — and could block a launch over a file no session ever reads.
//
// The cwd is stocked with a valid .claude.json so a regression has something to find:
// without it this would pass just as well against a broken guard.
func TestReadAccountIdentityRefusesRelativeDir(t *testing.T) {
	t.Chdir(claudeDirWith(t, `{"oauthAccount":{"emailAddress":"cwd@b.com","accountUuid":"u-cwd"}}`))

	// Control: the bait is real and readable when addressed absolutely.
	abs, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if _, ok := ReadAccountIdentity(abs); !ok {
		t.Fatal("bait .claude.json is not readable; the rest of this test proves nothing")
	}

	for _, dir := range []string{"", ".", "./", "relative/sub"} {
		if got, ok := ReadAccountIdentity(dir); ok {
			t.Errorf("ReadAccountIdentity(%q) read the working directory: %+v", dir, got)
		}
	}
}

func TestAccountIdentityMatchesPin(t *testing.T) {
	id := AccountIdentity{Email: "Zvi@Example.com", UUID: "u-1"}
	cases := []struct {
		pin  string
		want bool
	}{
		{"Zvi@Example.com", true},
		{"zvi@example.com", true},    // case is not a different account
		{"  zvi@example.com ", true}, // nor is a copied-in space
		{"other@example.com", false},
		{"", false}, // unpinned must never read as "matches"
		{"   ", false},
		{"u-1", false}, // the pin is an email, not a UUID
	}
	for _, tc := range cases {
		if got := id.MatchesPin(tc.pin); got != tc.want {
			t.Errorf("MatchesPin(%q) = %v, want %v", tc.pin, got, tc.want)
		}
	}

	// An identity with no email cannot satisfy any pin — including an empty one.
	uuidOnly := AccountIdentity{UUID: "u-1"}
	if uuidOnly.MatchesPin("a@b.com") || uuidOnly.MatchesPin("") {
		t.Error("an identity with no email matched a pin")
	}
}

func TestAccountIdentityCollisionKey(t *testing.T) {
	cases := []struct {
		name string
		id   AccountIdentity
		want string
	}{
		{"uuid wins when present", AccountIdentity{Email: "a@b.com", UUID: "u-1"}, "u-1"},
		{"falls back to email", AccountIdentity{Email: "A@B.com"}, "a@b.com"},
		{"empty identity keys nothing", AccountIdentity{}, ""},
	}
	for _, tc := range cases {
		if got := tc.id.CollisionKey(); got != tc.want {
			t.Errorf("%s: CollisionKey() = %q, want %q", tc.name, got, tc.want)
		}
	}

	// The same account read from two dirs must collide even if one spells the email
	// differently; two genuinely different accounts must not.
	a := AccountIdentity{Email: "zvi@x.com", UUID: "u-1"}
	b := AccountIdentity{Email: "ZVI@X.com", UUID: "u-1"}
	c := AccountIdentity{Email: "other@x.com", UUID: "u-2"}
	if a.CollisionKey() != b.CollisionKey() {
		t.Error("one account read twice produced two collision keys")
	}
	if a.CollisionKey() == c.CollisionKey() {
		t.Error("two distinct accounts share a collision key")
	}
}

// CheckIdentity is the one classifier behind both `atrium doctor`'s report and the
// launch gate, so every distinction it draws is one both consumers can rely on.
//
// IdentityNoDir and IdentityUnreadable are the pair worth being careful about: both
// mean "no login was read", and callers that collapse them warn about a directory
// that does not exist. They must stay distinguishable here even though neither
// blocks anything.
func TestClaudeAccountCheckIdentity(t *testing.T) {
	read := func(m map[string]AccountIdentity) IdentityReadFunc {
		return func(dir string) (AccountIdentity, bool) {
			id, ok := m[dir]
			return id, ok
		}
	}
	known := read(map[string]AccountIdentity{
		"/h/dir": {Email: "actual@corp.com", UUID: "u-1"},
	})

	cases := []struct {
		name  string
		acct  ClaudeAccount
		read  IdentityReadFunc
		want  IdentityCheck
		email string
	}{{
		name: "no config dir at all",
		acct: ClaudeAccount{Name: "ambient", ExpectAccount: "who@corp.com"},
		read: known,
		want: IdentityNoDir,
	}, {
		name: "dir names no login",
		acct: ClaudeAccount{Name: "a", ConfigDir: "/h/absent", ExpectAccount: "who@corp.com"},
		read: known,
		want: IdentityUnreadable,
	}, {
		// Unreadable is classified before the pin, so a failed read can never be
		// reported as a verified or a wrong account.
		name: "unreadable outranks an unset pin",
		acct: ClaudeAccount{Name: "a", ConfigDir: "/h/absent"},
		read: known,
		want: IdentityUnreadable,
	}, {
		name:  "readable but nothing to check it against",
		acct:  ClaudeAccount{Name: "a", ConfigDir: "/h/dir"},
		read:  known,
		want:  IdentityUnpinned,
		email: "actual@corp.com",
	}, {
		name:  "pin satisfied",
		acct:  ClaudeAccount{Name: "a", ConfigDir: "/h/dir", ExpectAccount: " ACTUAL@corp.com "},
		read:  known,
		want:  IdentityVerified,
		email: "actual@corp.com",
	}, {
		name:  "pin unsatisfied",
		acct:  ClaudeAccount{Name: "a", ConfigDir: "/h/dir", ExpectAccount: "someone@else.com"},
		read:  known,
		want:  IdentityWrongAccount,
		email: "actual@corp.com",
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, actual := tc.acct.CheckIdentity(tc.read)
			if got != tc.want {
				t.Errorf("state = %v, want %v", got, tc.want)
			}
			if actual.Email != tc.email {
				t.Errorf("actual email = %q, want %q", actual.Email, tc.email)
			}
		})
	}
}

// An account with no dir must not be handed to the reader at all: "" would otherwise
// reach a reader that resolves it against the working directory.
func TestCheckIdentityNeverReadsAnAbsentDir(t *testing.T) {
	called := false
	state, _ := ClaudeAccount{Name: "ambient", ExpectAccount: "x@y.com"}.CheckIdentity(
		func(string) (AccountIdentity, bool) { called = true; return AccountIdentity{}, false })

	if called {
		t.Error("CheckIdentity read a config dir for an inherit-env account")
	}
	if state != IdentityNoDir {
		t.Errorf("state = %v, want IdentityNoDir", state)
	}
}

// ReadIdentity must expand a leading ~ the same way routing does, or an account
// configured the normal way would report as unverifiable.
func TestClaudeAccountReadIdentity(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("home: %v", err)
	}
	dir := filepath.Join(home, ".claude-tilde-test")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := `{"oauthAccount":{"emailAddress":"tilde@b.com","accountUuid":"u-t"}}`
	if err := os.WriteFile(filepath.Join(dir, ".claude.json"), []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, ok := ClaudeAccount{Name: "x", ConfigDir: "~/.claude-tilde-test"}.ReadIdentity()
	if !ok {
		t.Fatal("a ~-prefixed config_dir did not resolve")
	}
	if got.Email != "tilde@b.com" {
		t.Errorf("email = %q, want tilde@b.com", got.Email)
	}

	// An inherit-env account names no dir, so it has no identity of its own.
	if _, ok := (ClaudeAccount{Name: "ambient"}).ReadIdentity(); ok {
		t.Error("an account with no config_dir reported an identity")
	}
}

// The invariant every downstream collision check relies on: a failed read contributes
// NO identity, whatever payload the reader handed back. Two dirs that merely could not
// be read must never look like two dirs holding one account — otherwise a machine
// where claude is not yet onboarded reports a collision.
//
// This is the single place that guarantee is enforced, so it is the single place it can
// be tested. The doctor report and the accounts overlay both group on CollisionKey and
// would silently inherit any leak from here — the overlay rests on it alone, while
// doctor's collisions() keeps a belt-and-braces IdentityUnreadable skip in front of it.
func TestCheckIdentityDiscardsPayloadFromAFailedRead(t *testing.T) {
	leaky := func(string) (AccountIdentity, bool) {
		return AccountIdentity{Email: "stale@corp.com", UUID: "u-stale"}, false
	}

	state, actual := ClaudeAccount{Name: "a", ConfigDir: "/h/dir"}.CheckIdentity(leaky)

	if state != IdentityUnreadable {
		t.Errorf("state = %v, want IdentityUnreadable", state)
	}
	if actual != (AccountIdentity{}) {
		t.Errorf("actual = %+v, want the zero identity", actual)
	}
	if key := actual.CollisionKey(); key != "" {
		t.Errorf("a failed read produced collision key %q; two unreadable dirs would group", key)
	}
}
