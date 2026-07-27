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
	"strings"

	"github.com/ZviBaratz/atrium/config"
	"github.com/charmbracelet/lipgloss"
)

// acctIdentity is one account's cached verification outcome, parallel by index to
// cfg.ClaudeAccounts.
type acctIdentity struct {
	state  config.IdentityCheck
	actual config.AccountIdentity
}

// loadIdentities reads the login behind every Claude account once, memoising per
// directory so two accounts naming one dir cost a single read. Called when the
// overlay is constructed — i.e. when the user opens it — and never during a render.
//
// A nil reader clears the cache, which renders exactly as an unconfigured feature:
// no note, no preview line. That is the honest rendering of "not looked", and it is
// what keeps every existing overlay test unaffected by this file.
func (o *AccountsOverlay) loadIdentities(read config.IdentityReadFunc) {
	o.identities = nil
	if read == nil || o.cfg == nil {
		return
	}
	cache := map[string]config.AccountIdentity{}
	okCache := map[string]bool{}
	memo := func(dir string) (config.AccountIdentity, bool) {
		if _, seen := okCache[dir]; !seen {
			cache[dir], okCache[dir] = read(dir)
		}
		return cache[dir], okCache[dir]
	}
	for _, a := range o.cfg.ClaudeAccounts {
		state, actual := a.CheckIdentity(memo)
		o.identities = append(o.identities, acctIdentity{state: state, actual: actual})
	}
}

// identityForDir returns the cached outcome for the account at a config dir, and
// whether one is cached. Keyed on the resolved dir rather than the name because the
// preview resolves routing to a directory, and because a renamed account keeps its
// directory (the #470 anchor).
func (o *AccountsOverlay) identityForDir(dir string) (acctIdentity, bool) {
	if dir == "" {
		return acctIdentity{}, false
	}
	for i, a := range o.cfg.ClaudeAccounts {
		if i >= len(o.identities) {
			break
		}
		if a.ResolvedConfigDir() == dir {
			return o.identities[i], true
		}
	}
	return acctIdentity{}, false
}

// previewIdentityLine is the "signed in as …" line under the preview's Claude row —
// the answer to which login a session created here would actually bill. Empty when
// nothing is cached for the dir or no login could be read, so the preview gains a
// line only when it has something to say.
//
// A mismatch leads with the warning glyph: the login being billed is the fact, and
// the glyph is what makes it read as wrong rather than merely unfamiliar.
func (o *AccountsOverlay) previewIdentityLine(dir string, width int) string {
	got, ok := o.identityForDir(dir)
	if !ok || got.actual.Email == "" {
		return ""
	}
	if got.state == config.IdentityWrongAccount {
		return fitOneOf(width,
			"⚠ signed in as "+got.actual.Email,
			"⚠ "+got.actual.Email,
			"⚠ wrong login")
	}
	return fitOneOf(width, "signed in as "+got.actual.Email, got.actual.Email)
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
	var wrong []string
	byLogin := map[string][]string{}
	var loginOrder []string

	for i, a := range o.cfg.ClaudeAccounts {
		if i >= len(o.identities) {
			break
		}
		got := o.identities[i]
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

	var collided []string
	for _, key := range loginOrder {
		if len(byLogin[key]) > 1 {
			collided = append(collided, byLogin[key]...)
		}
	}

	if note := collisionNote(collided, width); note != "" {
		return note
	}
	return wrongLoginNote(wrong, width)
}

// collisionNote words the "these are one login" warning, longest form first.
func collisionNote(names []string, width int) string {
	if len(names) < 2 {
		return ""
	}
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
