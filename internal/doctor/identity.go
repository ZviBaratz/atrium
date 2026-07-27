package doctor

// Reporting which Claude login each configured account's config dir actually holds.
//
// Routing selects a directory and trusts the name written beside it. Nothing has
// ever checked that the directory agrees. Re-running `/login` inside a config dir
// rewrites its credentials in place, so a dir named "personal" can quietly start
// answering as a work account: routes, badges, pools and rotation all keep saying
// "personal" while every session they place bills the other login. There is no error
// and no log line — the first and only symptom is a usage figure on a webpage, read
// hours later.
//
// So this section always prints its roster, the way RenderGates always prints its
// rows: "verified, and correct" and "never looked" must not render identically. The
// warnings below are the states worth acting on.

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ZviBaratz/atrium/config"
)

// IdentityState is one account's verification outcome.
type IdentityState int

const (
	// IdentityOK means expect_account is set and the config dir holds that login.
	IdentityOK IdentityState = iota
	// IdentityMismatch means expect_account is set and the dir holds a DIFFERENT
	// login: sessions routed here bill the account in Actual, not the pinned one.
	IdentityMismatch
	// IdentityUnpinned means the dir's login was read, but the account declares no
	// expectation, so there is nothing to verify it against.
	IdentityUnpinned
	// IdentityUnreadable means no login could be read from the dir (never onboarded,
	// or a file this build cannot parse). Never treated as a wrong login.
	IdentityUnreadable
)

// AccountIdentityRow is the reported state of one configured account.
type AccountIdentityRow struct {
	Account  string
	Dir      string
	Actual   config.AccountIdentity
	Expected string // the account's expect_account, "" when unpinned
	State    IdentityState
}

// IdentityCollision is two or more accounts whose config dirs hold the SAME real
// login. This is the failure that motivated the section: the accounts look separate
// everywhere in the UI, so work spread across them lands on one login, and the
// usage the user expected to see on the others never appears.
type IdentityCollision struct {
	Accounts []string // configured names, in config order
	Email    string   // the shared login's email, "" when no member recorded one
	Key      string   // what they matched on: an account UUID, or a lowercased email
	SameDir  bool     // true when they also share one config_dir
}

// Login names the shared account for a reader. It prefers the email, the form a user
// recognises and can act on, and falls back to the key — a UUID — so a group whose
// dirs all recorded only a UUID still names what they matched on. Rendering the empty
// string here would put nothing in exactly the place the warning is pointing at.
func (c IdentityCollision) Login() string {
	if c.Email != "" {
		return c.Email
	}
	return c.Key
}

// AccountIdentityReport is the whole section: one row per account that names a
// config dir, plus any collisions between them.
type AccountIdentityReport struct {
	Rows       []AccountIdentityRow
	Collisions []IdentityCollision
}

// identityReader reads the login recorded in one config dir. Unexported so only
// in-package fakes implement it, mirroring gateReader and Runner; ok=false means
// "no identity readable here" and collapses every distinct failure, none of which a
// user could act on differently.
type identityReader interface {
	identity(configDir string) (config.AccountIdentity, bool)
}

// fileIdentityReader is the production reader, delegating to the strictly
// read-only config.ReadAccountIdentity.
type fileIdentityReader struct{}

func (fileIdentityReader) identity(configDir string) (config.AccountIdentity, bool) {
	return config.ReadAccountIdentity(configDir)
}

// CheckAccountIdentity builds the report for cfg's Claude accounts. Pure apart from
// the injected reader, so tests never touch a real config dir.
//
// Accounts with no config_dir are skipped entirely: they inject no
// CLAUDE_CONFIG_DIR, so they have no directory of their own to verify and share
// whatever the ambient env supplies. Reporting them would attribute the ambient
// login to an account that never selected it.
//
// Each distinct dir is read once — compared by CLEANED path, since config_dir is
// hand-written and a trailing slash is not a different dir — so two accounts pointing
// at one directory cost one read and, more to the point, cannot be made to disagree
// with each other by a file rewritten between two reads.
//
// Dormant when no Claude accounts are configured, matching CheckAccountKeys: with no
// roster there is nothing to verify and an empty section is noise.
func CheckAccountIdentity(cfg *config.Config, r identityReader) AccountIdentityReport {
	if cfg == nil || len(cfg.ClaudeAccounts) == 0 {
		return AccountIdentityReport{}
	}

	var report AccountIdentityReport
	type readResult struct {
		id config.AccountIdentity
		ok bool
	}
	seen := map[string]readResult{}

	for _, a := range cfg.ClaudeAccounts {
		raw := a.ResolvedConfigDir()
		if raw == "" {
			continue // inherit-env account: no dir of its own
		}
		// Clean before the path becomes a cache key or a SameDir comparison, the way
		// gates.go's installedGateDirs does and for the same reason: config_dir is
		// hand-written, and "/h/x" and "/h/x/" are one directory that would otherwise
		// be read twice and then reported as two dirs that drifted onto one login.
		// After the empty check, never before it — filepath.Clean("") is ".".
		dir := filepath.Clean(raw)
		got, cached := seen[dir]
		if !cached {
			got.id, got.ok = r.identity(dir)
			seen[dir] = got
		}
		row := AccountIdentityRow{
			Account: a.Name, Dir: dir, Actual: got.id,
			Expected: strings.TrimSpace(a.ExpectAccount),
		}
		switch {
		case !got.ok:
			row.State = IdentityUnreadable
		case row.Expected == "":
			row.State = IdentityUnpinned
		case got.id.MatchesPin(row.Expected):
			row.State = IdentityOK
		default:
			row.State = IdentityMismatch
		}
		report.Rows = append(report.Rows, row)
	}

	report.Collisions = collisions(report.Rows)
	return report
}

// collisions groups readable rows by the login they resolve to and returns every
// group with more than one account. Groups are emitted in the config order of their
// first member and members keep config order, so the section never reorders between
// runs on an unchanged config.
func collisions(rows []AccountIdentityRow) []IdentityCollision {
	type group struct {
		accounts []string
		dirs     map[string]bool
		email    string
	}
	groups := map[string]*group{}
	var order []string

	for _, row := range rows {
		if row.State == IdentityUnreadable {
			continue // unknown is not evidence of sameness
		}
		key := row.Actual.CollisionKey()
		if key == "" {
			continue
		}
		g := groups[key]
		if g == nil {
			g = &group{dirs: map[string]bool{}}
			groups[key] = g
			order = append(order, key)
		}
		g.accounts = append(g.accounts, row.Account)
		g.dirs[row.Dir] = true
		// Take the first member that names an email, not the first member. Grouping
		// is by UUID, and a dir can record a UUID with no email at all — so keying
		// the label to arrival order lets one such dir blank out the login for a
		// group whose other members name it perfectly well.
		if g.email == "" {
			g.email = row.Actual.Email
		}
	}

	var out []IdentityCollision
	for _, key := range order {
		g := groups[key]
		if len(g.accounts) < 2 {
			continue
		}
		out = append(out, IdentityCollision{
			Accounts: g.accounts, Email: g.email, Key: key, SameDir: len(g.dirs) == 1,
		})
	}
	return out
}

// CheckAccountIdentityInstalled reports on the real environment. Like
// CheckGatesInstalled it is the only function here that loads config — LoadConfig
// seeds config.json when absent, which is a write and must stay out of the pure
// CheckAccountIdentity that tests call.
func CheckAccountIdentityInstalled() AccountIdentityReport {
	return CheckAccountIdentity(config.LoadConfig(), fileIdentityReader{})
}

// status renders one row's verification outcome, carrying the value that makes it
// actionable: a mismatch names what was expected, so the row is diagnosable without
// cross-referencing config.json.
func (r AccountIdentityRow) status() string {
	switch r.State {
	case IdentityOK:
		return "ok"
	case IdentityMismatch:
		return "⚠ expected " + r.Expected
	case IdentityUnpinned:
		return "unpinned"
	default:
		return "⚠ no login recorded"
	}
}

// identityLabel is what a row shows for the login it found — the email, or the dir
// when there is no login to name, so an unreadable row still says WHICH directory
// came up empty.
func (r AccountIdentityRow) identityLabel() string {
	if r.State == IdentityUnreadable {
		return r.Dir
	}
	if r.Actual.Email == "" {
		return r.Actual.UUID // identified only by UUID; better than a blank column
	}
	return r.Actual.Email
}

// RenderAccountIdentity formats the section for `atrium doctor` (empty string when
// no account names a config dir). Rows always print, matching RenderGates: a
// verified account and an unexamined one must be distinguishable.
//
// Collisions render after the roster because they are statements ABOUT the rows
// above, and they carry the consequence rather than just the fact — "same login" is
// a curiosity until it is spelled out as one account's work billing another's quota,
// which is the whole reason the section exists.
func RenderAccountIdentity(rep AccountIdentityReport) string {
	if len(rep.Rows) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Claude account identities:\n")
	for _, r := range rep.Rows {
		fmt.Fprintf(&b, "  %-14s %-32s %s\n", r.Account, r.identityLabel(), r.status())
	}
	for _, c := range rep.Collisions {
		names, login := quotedList(c.Accounts), c.Login()
		if c.SameDir {
			fmt.Fprintf(&b, "  ⚠ %s share one config_dir,\n    so they are one login (%s)\n",
				names, login)
		} else {
			fmt.Fprintf(&b, "  ⚠ %s are different config dirs\n    holding the SAME login (%s)\n",
				names, login)
		}
		fmt.Fprintf(&b, "    → work spread across them all bills %s; only\n", login)
		b.WriteString("      one of these accounts is really separate. Re-run /login in the wrong dir.\n")
	}
	// The hint offers only what the tree actually does. This report is the sole
	// consumer of expect_account: no launch path consults it, so promising to refuse
	// the wrong login would sell a guarantee nothing keeps — and the way a user finds
	// out is by not being stopped, which is the failure this section exists to catch.
	if unpinned := unpinnedNames(rep.Rows); len(unpinned) > 0 {
		fmt.Fprintf(&b, "    → set expect_account on an account to have this check verify its\n"+
			"      login rather than just report it (unpinned: %s)\n", strings.Join(unpinned, ", "))
	}
	return b.String()
}

// unpinnedNames lists accounts declaring no expect_account, sorted so the hint does
// not reorder between runs.
func unpinnedNames(rows []AccountIdentityRow) []string {
	var out []string
	for _, r := range rows {
		if r.State == IdentityUnpinned {
			out = append(out, r.Account)
		}
	}
	sort.Strings(out)
	return out
}

// quotedList renders names as `"a"`, `"a" and "b"`, or `"a", "b" and "c"`.
func quotedList(names []string) string {
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = fmt.Sprintf("%q", n)
	}
	switch len(q) {
	case 0:
		return ""
	case 1:
		return q[0]
	default:
		return strings.Join(q[:len(q)-1], ", ") + " and " + q[len(q)-1]
	}
}
