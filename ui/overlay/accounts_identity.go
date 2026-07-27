package overlay

// Showing which Claude login each account's config dir actually holds.
//
// The account list has always shown a name and a directory, both of which come
// straight back out of config.json — so the panel could only ever confirm what the
// user already typed. The one fact it could not show is the one that goes wrong:
// which login that directory is signed in as. A dir named "personal" can be
// re-authenticated to a work account in place, and until something reads the login,
// every row keeps saying "personal" while the sessions bill someone else.
//
// Two constraints shape how it is surfaced. Rows are already at their width budget
// (#478 has one wrapping at 91 columns), so the login goes where there is room — the
// routing preview and a list-level note — rather than into a fourth column. And the
// overlay redraws on every keystroke, so the logins are read ONCE when it opens and
// cached; nothing here touches the filesystem during a render.

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ZviBaratz/atrium/config"
	"github.com/charmbracelet/lipgloss"
)

// acctIdentity is one account's verification outcome, computed on demand from the
// cached reads rather than stored per row.
type acctIdentity struct {
	state  config.IdentityCheck
	actual config.AccountIdentity
}

// acctLogin is one directory's cached read: what it held, and whether it could be
// read at all. The two are kept together because a failed read must stay
// distinguishable from a dir holding nothing.
type acctLogin struct {
	id config.AccountIdentity
	ok bool
}

// loadIdentities reads the login behind every Claude config dir once, memoising per
// directory so two accounts naming one dir cost a single read. Called when the
// overlay is constructed — i.e. when the user opens it — and never during a render.
//
// A nil reader clears the cache, which renders exactly as an unconfigured feature:
// no note, no preview line. That is the honest rendering of "not looked", and it is
// what keeps every existing overlay test unaffected by this file.
func (o *AccountsOverlay) loadIdentities(read config.IdentityReadFunc) {
	o.logins, o.readIdentity = nil, read
	if read == nil || o.cfg == nil {
		return
	}
	o.logins = map[string]acctLogin{}
	o.cacheLogins()
}

// cacheLogins reads any account dir not already cached, and is idempotent. Called
// when the panel opens and again after commit(), which is the one mutation that can
// introduce a directory nobody named before; reorder and delete need no refresh
// because nothing here is keyed on a row's position.
func (o *AccountsOverlay) cacheLogins() {
	if o.logins == nil || o.readIdentity == nil || o.cfg == nil {
		return
	}
	for _, a := range o.cfg.ClaudeAccounts {
		dir := a.NormalizedConfigDir()
		if dir == "" {
			continue // inherit-env: no dir of its own to read
		}
		if _, seen := o.logins[dir]; seen {
			continue
		}
		id, ok := o.readIdentity(dir)
		o.logins[dir] = acctLogin{id: id, ok: ok}
	}
}

// cachedRead answers CheckIdentity out of the cache alone, never touching the
// filesystem, so classification is safe during a render. An uncached dir reads as
// unreadable — "not looked" and "read and found nothing" render the same, and both
// are honest here.
func (o *AccountsOverlay) cachedRead(dir string) (config.AccountIdentity, bool) {
	got, seen := o.logins[dir]
	if !seen {
		return config.AccountIdentity{}, false
	}
	return got.id, got.ok
}

// identityFor classifies one account against the cached reads. Recomputed per call
// rather than stored per row: the pin it checks lives on the account, the login it
// checks lives on the directory, and both move when the user edits or reorders the
// list. Cheap — CheckIdentity is a map lookup and a string compare once the read is
// cached.
func (o *AccountsOverlay) identityFor(a config.ClaudeAccount) (acctIdentity, bool) {
	if o.logins == nil {
		return acctIdentity{}, false
	}
	state, actual := a.CheckIdentity(o.cachedRead)
	return acctIdentity{state: state, actual: actual}, true
}

// identityForDir returns the outcome for the account at a config dir, and whether one
// could be computed. Keyed on the directory rather than the name because the preview
// resolves routing to a directory, and because a renamed account keeps its directory
// (the #470 anchor).
//
// Both sides are compared CLEANED, the spelling everything else here already uses:
// the login cache is keyed by NormalizedConfigDir and CheckIdentity looks up the same.
// This lookup is the one place that was still matching raw against raw — correct only
// because every caller happens to arrive raw today, and silent when it is not, since a
// miss drops the login line rather than reporting anything.
func (o *AccountsOverlay) identityForDir(dir string) (acctIdentity, bool) {
	if dir == "" || o.logins == nil || o.cfg == nil {
		return acctIdentity{}, false
	}
	want := filepath.Clean(dir)
	for _, a := range o.cfg.ClaudeAccounts {
		if a.NormalizedConfigDir() == want {
			return o.identityFor(a)
		}
	}
	return acctIdentity{}, false
}

// previewIdentityLine is the "signed in as …" line under the preview's Claude row —
// the answer to which login a session created here would actually bill. Empty when
// nothing is cached for the dir, or when no login could be read at all, so the preview
// gains a line only when it has something to say.
//
// It names the UUID when a dir records one with no email, the same fallback and for
// the same reason as doctor's IdentityCollision.Login: a dir identified only by a UUID
// is still a login being billed, and naming it in the report while staying silent here
// would leave the two surfaces disagreeing about whether the account is known at all.
//
// A mismatch leads with the warning glyph: the login being billed is the fact, and
// the glyph is what makes it read as wrong rather than merely unfamiliar.
func (o *AccountsOverlay) previewIdentityLine(dir string, width int) string {
	got, ok := o.identityForDir(dir)
	if !ok {
		return ""
	}
	login := got.actual.Email
	if login == "" {
		login = got.actual.UUID
	}
	if login == "" {
		return ""
	}
	if got.state == config.IdentityWrongAccount {
		return fitOneOf(width, "⚠ signed in as "+login, "⚠ "+login, "⚠ wrong login")
	}
	return fitOneOf(width, "signed in as "+login, login)
}

// identityNote is the list-level warning under the account rows, in the same place
// and the same shape as splitPoolNote: one line, never wider than width, degrading
// through shorter wordings rather than wrapping the box.
//
// It reports the two states a user must act on, collisions first. A collision is the
// worse of the two because it needs no misconfiguration to happen and produces no
// error — two accounts that look separate everywhere in this panel are one login,
// and the work spread across them lands on a single quota. A mismatch at least means
// someone wrote down an expectation that can be compared.
func (o *AccountsOverlay) identityNote(width int) string {
	if o.cfg == nil || o.logins == nil {
		return "" // nothing was read: the honest rendering is no note at all
	}
	var wrong []string
	byLogin := map[string][]string{}
	var loginOrder []string

	for _, a := range o.cfg.ClaudeAccounts {
		got, _ := o.identityFor(a)
		if got.state == config.IdentityWrongAccount {
			wrong = append(wrong, a.Name)
		}
		// An unreadable dir keys to "" — CheckIdentity discards a failed read's
		// payload — so it groups with nothing. Unknown is not evidence of sameness.
		key := got.actual.CollisionKey()
		if key == "" {
			continue
		}
		if _, seen := byLogin[key]; !seen {
			loginOrder = append(loginOrder, key)
		}
		byLogin[key] = append(byLogin[key], a.Name)
	}

	// Groups stay groups. Flattening them into one list of names would let two
	// separate pairs render as "all four are the same login", which is false and
	// points the user at a merge that isn't there.
	var groups [][]string
	for _, key := range loginOrder {
		if len(byLogin[key]) > 1 {
			groups = append(groups, byLogin[key])
		}
	}

	if note := collisionNote(groups, width); note != "" {
		return note
	}
	return wrongLoginNote(wrong, width)
}

// collisionNote words the "these are one login" warning, longest form first. One
// group names its members, because naming them is what makes the warning actionable.
// Several groups can only be counted: the panel has one line, and there is no wording
// that names two separate sets of accounts without either overflowing the box or
// implying they share a login. `atrium doctor` prints one line per group and is where
// the detail lives, so the note says so rather than pretending to be complete.
func collisionNote(groups [][]string, width int) string {
	switch len(groups) {
	case 0:
		return ""
	case 1:
		names := groups[0]
		if len(names) == 2 {
			return fitOneOf(width,
				fmt.Sprintf("'%s' and '%s' are the same login — both bill one account", names[0], names[1]),
				fmt.Sprintf("'%s' and '%s' are the same login", names[0], names[1]),
				"2 accounts are the same login",
				"same login")
		}
		return fitOneOf(width,
			fmt.Sprintf("%s are the same login — all bill one account", quotedNames(names)),
			fmt.Sprintf("%d accounts are the same login", len(names)),
			"same login")
	default:
		return fitOneOf(width,
			fmt.Sprintf("%d sets of accounts each share one login — atrium doctor names them", len(groups)),
			fmt.Sprintf("%d sets of accounts each share one login", len(groups)),
			fmt.Sprintf("%d duplicated logins", len(groups)),
			"duplicated logins")
	}
}

// wrongLoginNote words the expect_account-violated warning, longest form first.
func wrongLoginNote(names []string, width int) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return fitOneOf(width,
			fmt.Sprintf("'%s' is signed in as the wrong account", names[0]),
			"1 account has the wrong login",
			"wrong login")
	default:
		return fitOneOf(width,
			fmt.Sprintf("%d accounts are signed in as the wrong account", len(names)),
			fmt.Sprintf("%d wrong logins", len(names)),
			"wrong logins")
	}
}

// fitOneOf returns the first wording that fits width, callers passing them
// longest-first. When even the shortest does not fit it is clipped rather than
// returned oversize: this line renders inside a bordered box, and one string wider
// than its budget wraps the whole panel (#478). A degradation ladder that can still
// overflow at its last rung is not a guarantee, so the clip is the guarantee.
func fitOneOf(width int, candidates ...string) string {
	var last string
	for _, c := range candidates {
		if lipgloss.Width(c) <= width {
			return c
		}
		last = c
	}
	return clipWidth(last, width)
}

// clipWidth hard-truncates s to at most width display cells, marking the cut with an
// ellipsis. Unlike truncTail (which keeps the tail of a path, where the leaf is the
// informative part) this keeps the head, because these are sentences.
func clipWidth(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= width {
		return s
	}
	r := []rune(s)
	for len(r) > 0 && lipgloss.Width(string(r))+1 > width {
		r = r[:len(r)-1]
	}
	return string(r) + "…"
}

// quotedNames renders names as 'a', 'b' and 'c', matching splitPoolNote's quoting.
func quotedNames(names []string) string {
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = "'" + n + "'"
	}
	if len(q) < 2 {
		return strings.Join(q, "")
	}
	return strings.Join(q[:len(q)-1], ", ") + " and " + q[len(q)-1]
}
