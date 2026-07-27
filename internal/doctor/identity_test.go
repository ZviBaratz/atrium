package doctor

import (
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/config"
)

// fakeReader maps a config dir to the login recorded there, and counts reads per dir.
// A dir absent from the map reads as "no identity", the same as a dir claude was
// never onboarded in.
func fakeReader(m map[string]config.AccountIdentity) (config.IdentityReadFunc, map[string]int) {
	reads := map[string]int{}
	return func(dir string) (config.AccountIdentity, bool) {
		reads[dir]++
		id, ok := m[dir]
		return id, ok
	}, reads
}

// leakyReader always fails, but returns a populated identity anyway — the contract
// violation CheckAccountIdentity must not be fooled by.
func leakyReader(leak config.AccountIdentity) config.IdentityReadFunc {
	return func(string) (config.AccountIdentity, bool) { return leak, false }
}

func acct(name, dir, expect string) config.ClaudeAccount {
	return config.ClaudeAccount{Name: name, ConfigDir: dir, ExpectAccount: expect}
}

func cfgWith(accounts ...config.ClaudeAccount) *config.Config {
	return &config.Config{ClaudeAccounts: accounts}
}

func id(email, uuid string) config.AccountIdentity {
	return config.AccountIdentity{Email: email, UUID: uuid}
}

// rowFor finds the row for an account name, failing the test when absent.
func rowFor(t *testing.T, rep AccountIdentityReport, name string) AccountIdentityRow {
	t.Helper()
	for _, r := range rep.Rows {
		if r.Account == name {
			return r
		}
	}
	t.Fatalf("no row for account %q (rows: %+v)", name, rep.Rows)
	return AccountIdentityRow{}
}

// The failure this whole section exists for: two accounts the user believes are
// separate, on two different config dirs, that turn out to be one login. Every route,
// badge and pool keeps them apart while the sessions bill one account.
func TestCheckAccountIdentityFlagsDistinctDirsOnOneLogin(t *testing.T) {
	r, _ := fakeReader(map[string]config.AccountIdentity{
		"/h/.claude-personal": id("work2@corp.com", "u-work2"),
		"/h/.claude-work":     id("work@corp.com", "u-work"),
		"/h/.claude-work2":    id("work2@corp.com", "u-work2"),
	})
	rep := CheckAccountIdentity(cfgWith(
		acct("personal", "/h/.claude-personal", ""),
		acct("work", "/h/.claude-work", ""),
		acct("work2", "/h/.claude-work2", ""),
	), r)

	if len(rep.Collisions) != 1 {
		t.Fatalf("got %d collisions, want 1: %+v", len(rep.Collisions), rep.Collisions)
	}
	c := rep.Collisions[0]
	if got := strings.Join(c.Accounts, ","); got != "personal,work2" {
		t.Errorf("colliding accounts = %q, want \"personal,work2\" (config order)", got)
	}
	if c.Email != "work2@corp.com" {
		t.Errorf("collision email = %q, want work2@corp.com", c.Email)
	}
	if c.SameDir {
		t.Error("SameDir = true for two genuinely different directories")
	}

	out := RenderAccountIdentity(rep)
	for _, want := range []string{`"personal"`, `"work2"`, "work2@corp.com", "SAME login"} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q:\n%s", want, out)
		}
	}
	// The consequence, not just the fact — this is what makes the report actionable.
	if !strings.Contains(out, "bills work2@corp.com") {
		t.Errorf("render does not state who gets billed:\n%s", out)
	}
}

// The negative control. Without it, a check that fired unconditionally would pass
// every test above.
func TestCheckAccountIdentityQuietWhenLoginsDiffer(t *testing.T) {
	r, _ := fakeReader(map[string]config.AccountIdentity{
		"/h/a": id("a@corp.com", "u-a"),
		"/h/b": id("b@corp.com", "u-b"),
	})
	rep := CheckAccountIdentity(cfgWith(acct("a", "/h/a", ""), acct("b", "/h/b", "")), r)

	if len(rep.Collisions) != 0 {
		t.Fatalf("distinct logins reported a collision: %+v", rep.Collisions)
	}
	if out := RenderAccountIdentity(rep); strings.Contains(out, "⚠") {
		t.Errorf("clean config rendered a warning glyph:\n%s", out)
	}
}

// Two dirs claude was never onboarded in are both "unknown". Unknown is not evidence
// that they are the same account, and grouping them on their shared empty key would
// accuse every fresh install of a collision.
func TestCheckAccountIdentityUnreadableDirsDoNotCollide(t *testing.T) {
	r, _ := fakeReader(nil)
	rep := CheckAccountIdentity(cfgWith(acct("a", "/h/a", ""), acct("b", "/h/b", "")), r)

	if len(rep.Collisions) != 0 {
		t.Fatalf("two unreadable dirs were reported as one login: %+v", rep.Collisions)
	}
	for _, name := range []string{"a", "b"} {
		if got := rowFor(t, rep, name).State; got != config.IdentityUnreadable {
			t.Errorf("%s state = %v, want IdentityUnreadable", name, got)
		}
	}
	if out := RenderAccountIdentity(rep); !strings.Contains(out, "/h/a") {
		t.Errorf("an unreadable row does not name its directory:\n%s", out)
	}
}

// The two guards in collisions() — skip unreadable rows, skip empty keys — overlap
// for the production reader, which returns a zero identity on failure and never
// returns a readable one with no fields. Each test below defeats one guard so the
// other is exercised alone; together they are why neither can be deleted as
// redundant.

// A reader that returns data alongside ok=false is violating the contract, but the
// cost of trusting it is accusing two accounts of sharing a login on the strength of
// a read that failed. State, not payload, decides.
func TestCheckAccountIdentityIgnoresPayloadFromFailedRead(t *testing.T) {
	rep := CheckAccountIdentity(cfgWith(acct("a", "/h/a", ""), acct("b", "/h/b", "")),
		leakyReader(id("stale@corp.com", "u-stale")))

	if len(rep.Collisions) != 0 {
		t.Fatalf("collided on an identity from a failed read: %+v", rep.Collisions)
	}
	for _, name := range []string{"a", "b"} {
		if got := rowFor(t, rep, name).State; got != config.IdentityUnreadable {
			t.Errorf("%s state = %v, want IdentityUnreadable", name, got)
		}
	}
}

// A successful read carrying nothing identifying cannot be compared to anything.
// Grouping such rows would collide every account a future claude stopped naming.
func TestCheckAccountIdentityIgnoresEmptyIdentity(t *testing.T) {
	r, _ := fakeReader(map[string]config.AccountIdentity{"/h/a": {}, "/h/b": {}})
	rep := CheckAccountIdentity(cfgWith(acct("a", "/h/a", ""), acct("b", "/h/b", "")), r)

	if len(rep.Collisions) != 0 {
		t.Fatalf("two contentless identities were grouped as one login: %+v", rep.Collisions)
	}
}

func TestCheckAccountIdentityPinStates(t *testing.T) {
	r, _ := fakeReader(map[string]config.AccountIdentity{
		"/h/ok":       id("right@corp.com", "u-1"),
		"/h/wrong":    id("actual@corp.com", "u-2"),
		"/h/unpinned": id("who@corp.com", "u-3"),
	})
	rep := CheckAccountIdentity(cfgWith(
		acct("ok", "/h/ok", "right@corp.com"),
		acct("wrong", "/h/wrong", "expected@corp.com"),
		acct("unpinned", "/h/unpinned", ""),
		acct("gone", "/h/gone", "pinned@corp.com"),
	), r)

	for _, tc := range []struct {
		name string
		want config.IdentityCheck
	}{
		{"ok", config.IdentityVerified},
		{"wrong", config.IdentityWrongAccount},
		{"unpinned", config.IdentityUnpinned},
		{"gone", config.IdentityUnreadable}, // pinned but unreadable is NOT a mismatch
	} {
		if got := rowFor(t, rep, tc.name).State; got != tc.want {
			t.Errorf("%s state = %v, want %v", tc.name, got, tc.want)
		}
	}

	out := RenderAccountIdentity(rep)
	// A mismatch must carry what was expected, or the row is not diagnosable.
	if !strings.Contains(out, "expected expected@corp.com") {
		t.Errorf("mismatch row omits the expected login:\n%s", out)
	}
	// And what it actually found.
	if !strings.Contains(out, "actual@corp.com") {
		t.Errorf("mismatch row omits the actual login:\n%s", out)
	}
	if !strings.Contains(out, "unpinned: unpinned") {
		t.Errorf("render does not nudge the unpinned account:\n%s", out)
	}
}

// A pin is satisfied by the login regardless of case, so a config written with
// different capitalisation is not a mismatch.
func TestCheckAccountIdentityPinIsCaseInsensitive(t *testing.T) {
	r, _ := fakeReader(map[string]config.AccountIdentity{"/h/a": id("Zvi@Example.com", "u-1")})
	rep := CheckAccountIdentity(cfgWith(acct("a", "/h/a", " zvi@example.com ")), r)
	if got := rowFor(t, rep, "a").State; got != config.IdentityVerified {
		t.Errorf("state = %v, want IdentityVerified", got)
	}
}

// Two accounts on ONE directory are trivially one login. It is still worth saying —
// CheckPools only covers pool members, so an unpooled pair is otherwise unreported —
// but it is a different sentence from two dirs that drifted onto one account.
func TestCheckAccountIdentitySameDirCollision(t *testing.T) {
	r, reads := fakeReader(map[string]config.AccountIdentity{
		"/h/shared": id("one@corp.com", "u-1"),
	})
	rep := CheckAccountIdentity(cfgWith(
		acct("a", "/h/shared", ""),
		acct("b", "/h/shared", ""),
	), r)

	if len(rep.Collisions) != 1 {
		t.Fatalf("got %d collisions, want 1", len(rep.Collisions))
	}
	if !rep.Collisions[0].SameDir {
		t.Error("SameDir = false for two accounts on one directory")
	}
	if out := RenderAccountIdentity(rep); !strings.Contains(out, "share one config_dir") {
		t.Errorf("same-dir collision used the drifted-dirs wording:\n%s", out)
	}

	// One directory is read once however many accounts name it: a file rewritten
	// between two reads must not be able to make an account disagree with itself.
	if got := reads["/h/shared"]; got != 1 {
		t.Errorf("read /h/shared %d times, want 1", got)
	}
}

// The hint advertises the launch gate, so it must say what the gate does. The
// promise is only half of the guard: TestDoctorHintMatchesGate, over in app, is what
// checks the sentence against accountIdentityError. Split across packages because
// that is where the two halves live — the copy here, the enforcement there — and a
// drift between them is exactly how #496 came to offer a refusal nothing performed.
func TestRenderAccountIdentityHintPromisesTheGate(t *testing.T) {
	r, _ := fakeReader(map[string]config.AccountIdentity{"/h/a": id("who@corp.com", "u-a")})
	out := RenderAccountIdentity(CheckAccountIdentity(cfgWith(acct("a", "/h/a", "")), r))

	if !strings.Contains(out, "unpinned: a") {
		t.Fatalf("no hint rendered for an unpinned account:\n%s", out)
	}
	if !strings.Contains(out, "refuse") {
		t.Errorf("hint does not offer the refusal the gate actually performs:\n%s", out)
	}
}

// config_dir is hand-written, so one directory reaches this check spelled two ways.
// Left unnormalised it is read twice — which the single-read guarantee exists to
// prevent — and then described with the wrong sentence: "different config dirs" and
// "re-run /login in the wrong dir" send the user hunting for a second directory that
// does not exist.
func TestCheckAccountIdentityNormalisesConfigDirSpelling(t *testing.T) {
	r, reads := fakeReader(map[string]config.AccountIdentity{
		"/h/shared": id("one@corp.com", "u-1"),
	})
	rep := CheckAccountIdentity(cfgWith(
		acct("a", "/h/shared", ""),
		acct("b", "/h/shared/", ""),
		acct("c", "/h/sub/../shared", ""),
	), r)

	if got := reads["/h/shared"]; got != 1 {
		t.Errorf("read /h/shared %d times, want 1", got)
	}
	if len(reads) != 1 {
		t.Errorf("reads = %v, want only the cleaned /h/shared", reads)
	}
	if len(rep.Collisions) != 1 {
		t.Fatalf("got %d collisions, want 1: %+v", len(rep.Collisions), rep.Collisions)
	}
	if !rep.Collisions[0].SameDir {
		t.Error("SameDir = false for one directory spelled three ways")
	}
	out := RenderAccountIdentity(rep)
	if !strings.Contains(out, "share one config_dir") {
		t.Errorf("one dir spelled three ways used the drifted-dirs wording:\n%s", out)
	}
	// Every row must show the cleaned path, or the roster itself reads as three dirs.
	for _, name := range []string{"a", "b", "c"} {
		if got := rowFor(t, rep, name).Dir; got != "/h/shared" {
			t.Errorf("%s dir = %q, want the cleaned \"/h/shared\"", name, got)
		}
	}
}

// The empty check must come BEFORE the clean: filepath.Clean("") is ".", which would
// turn every inherit-env account into a row reporting on the process's working
// directory — a login no session routed to it.
func TestCheckAccountIdentityDoesNotCleanTheEmptyDir(t *testing.T) {
	r, reads := fakeReader(nil)
	rep := CheckAccountIdentity(cfgWith(acct("ambient", "", "")), r)

	if len(rep.Rows) != 0 {
		t.Fatalf("inherit-env account produced rows: %+v", rep.Rows)
	}
	if _, read := reads["."]; read {
		t.Error(`the empty config dir was cleaned to "." and read`)
	}
}

// Grouping is by UUID, so members can disagree about the email — including a dir that
// records none. Taking the first member's email blanks the login out of the very
// sentence naming who gets billed, for a group that names it perfectly well.
func TestCollisionNamesLoginWhenFirstMemberHasNoEmail(t *testing.T) {
	r, _ := fakeReader(map[string]config.AccountIdentity{
		"/h/quiet": id("", "u-shared"), // UUID only: ReadAccountIdentity accepts this
		"/h/named": id("real@corp.com", "u-shared"),
	})
	rep := CheckAccountIdentity(cfgWith(
		acct("quiet", "/h/quiet", ""),
		acct("named", "/h/named", ""),
	), r)

	if len(rep.Collisions) != 1 {
		t.Fatalf("got %d collisions, want 1: %+v", len(rep.Collisions), rep.Collisions)
	}
	if got := rep.Collisions[0].Login(); got != "real@corp.com" {
		t.Errorf("collision login = %q, want real@corp.com from the member that has one", got)
	}
	out := RenderAccountIdentity(rep)
	if !strings.Contains(out, "bills real@corp.com") {
		t.Errorf("render does not name who gets billed:\n%s", out)
	}
	if strings.Contains(out, "()") || strings.Contains(out, "bills ;") {
		t.Errorf("render left the login blank:\n%s", out)
	}
}

// And when NO member records an email, the warning still has to say what they matched
// on. Empty parentheses would point at the answer and then not give it.
func TestCollisionFallsBackToUUIDWhenNoMemberHasAnEmail(t *testing.T) {
	r, _ := fakeReader(map[string]config.AccountIdentity{
		"/h/a": id("", "u-shared"),
		"/h/b": id("", "u-shared"),
	})
	rep := CheckAccountIdentity(cfgWith(acct("a", "/h/a", ""), acct("b", "/h/b", "")), r)

	if len(rep.Collisions) != 1 {
		t.Fatalf("got %d collisions, want 1: %+v", len(rep.Collisions), rep.Collisions)
	}
	if got := rep.Collisions[0].Login(); got != "u-shared" {
		t.Errorf("collision login = %q, want the UUID u-shared", got)
	}
	if out := RenderAccountIdentity(rep); !strings.Contains(out, "u-shared") {
		t.Errorf("render does not name the UUID the dirs matched on:\n%s", out)
	}
}

// An inherit-env account injects no CLAUDE_CONFIG_DIR, so it has no directory whose
// login could be verified. Reporting one would attribute the ambient login to an
// account that never selected it.
func TestCheckAccountIdentitySkipsInheritEnvAccounts(t *testing.T) {
	r, reads := fakeReader(map[string]config.AccountIdentity{"/h/a": id("a@corp.com", "u-a")})
	rep := CheckAccountIdentity(cfgWith(acct("ambient", "", ""), acct("a", "/h/a", "")), r)

	if len(rep.Rows) != 1 || rep.Rows[0].Account != "a" {
		t.Fatalf("rows = %+v, want only \"a\"", rep.Rows)
	}
	if _, read := reads[""]; read {
		t.Error("the empty config dir was read")
	}
}

func TestCheckAccountIdentityDormantWithoutAccounts(t *testing.T) {
	r, _ := fakeReader(nil)
	for _, cfg := range []*config.Config{nil, cfgWith()} {
		rep := CheckAccountIdentity(cfg, r)
		if len(rep.Rows) != 0 || len(rep.Collisions) != 0 {
			t.Errorf("report on %v is not empty: %+v", cfg, rep)
		}
		if out := RenderAccountIdentity(rep); out != "" {
			t.Errorf("empty report rendered %q", out)
		}
	}
}

// Rows follow config order and collisions follow their first member's, so an
// unchanged config renders an identical section every run (map iteration must not
// leak into the output).
func TestCheckAccountIdentityIsDeterministic(t *testing.T) {
	accounts := []config.ClaudeAccount{
		acct("z", "/h/z", ""), acct("m", "/h/m", ""),
		acct("a", "/h/a", ""), acct("m2", "/h/m2", ""),
	}
	ids := map[string]config.AccountIdentity{
		"/h/z": id("z@c.com", "u-z"), "/h/m": id("m@c.com", "u-m"),
		"/h/a": id("a@c.com", "u-a"), "/h/m2": id("m@c.com", "u-m"),
	}

	r, _ := fakeReader(ids)
	rep := CheckAccountIdentity(cfgWith(accounts...), r)
	if got := strings.Join(namesOf(rep), ","); got != "z,m,a,m2" {
		t.Errorf("rows = %q, want config order \"z,m,a,m2\"", got)
	}
	if len(rep.Collisions) != 1 {
		t.Fatalf("got %d collisions, want 1 (m and m2 share a login)", len(rep.Collisions))
	}
	if got := strings.Join(rep.Collisions[0].Accounts, ","); got != "m,m2" {
		t.Errorf("collision members = %q, want \"m,m2\"", got)
	}

	// Repeat enough times that Go's randomised map iteration would surface.
	first := RenderAccountIdentity(rep)
	for i := 0; i < 50; i++ {
		fresh, _ := fakeReader(ids)
		if got := RenderAccountIdentity(CheckAccountIdentity(cfgWith(accounts...), fresh)); got != first {
			t.Fatalf("render differed on run %d:\n%s\n--- vs ---\n%s", i, got, first)
		}
	}
}

func namesOf(rep AccountIdentityReport) []string {
	out := make([]string, len(rep.Rows))
	for i, r := range rep.Rows {
		out[i] = r.Account
	}
	return out
}
