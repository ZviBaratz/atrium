package overlay

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/config"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// identityOverlay builds a Claude-tab overlay over accounts, with the logins their
// dirs "hold" supplied by logins (keyed by config_dir). A dir absent from logins
// reads as never onboarded.
func identityOverlay(t *testing.T, accounts []config.ClaudeAccount,
	logins map[string]string) *AccountsOverlay {
	t.Helper()
	o := NewAccountsOverlay(&config.Config{ClaudeAccounts: accounts}, config.DefaultState())
	o.SetSize(120, 40)
	o.loadIdentities(func(dir string) (config.AccountIdentity, bool) {
		addr, ok := logins[dir]
		if !ok {
			return config.AccountIdentity{}, false
		}
		return config.AccountIdentity{Email: addr, UUID: "u-" + addr}, true
	})
	return o
}

// The failure the panel could never show: two accounts that look separate in every
// row, on two different directories, that are one login.
func TestAccountsOverlay_IdentityNoteNamesCollidingAccounts(t *testing.T) {
	o := identityOverlay(t, []config.ClaudeAccount{
		{Name: "personal", ConfigDir: "/h/p"},
		{Name: "work", ConfigDir: "/h/w"},
		{Name: "work2", ConfigDir: "/h/w2"},
	}, map[string]string{"/h/p": "w2@corp.com", "/h/w": "w@corp.com", "/h/w2": "w2@corp.com"})

	note := o.identityNote(80)
	assert.Contains(t, note, "'personal'")
	assert.Contains(t, note, "'work2'")
	assert.NotContains(t, note, "'work'", "the account on its own login must not be named")

	assert.Contains(t, o.Render(), "'personal'", "the note never reached the rendered list")
}

// The negative control: a healthy roster must add no note at all, or the warning is
// decoration rather than a signal.
func TestAccountsOverlay_IdentityNoteSilentWhenHealthy(t *testing.T) {
	o := identityOverlay(t, []config.ClaudeAccount{
		{Name: "personal", ConfigDir: "/h/p", ExpectAccount: "me@home.com"},
		{Name: "work", ConfigDir: "/h/w", ExpectAccount: "me@corp.com"},
	}, map[string]string{"/h/p": "me@home.com", "/h/w": "me@corp.com"})

	assert.Empty(t, o.identityNote(80))
}

// An unread dir is unknown, not "the same as the other unread dir" — otherwise every
// machine where claude has not been onboarded reports a collision.
func TestAccountsOverlay_IdentityNoteIgnoresUnreadableDirs(t *testing.T) {
	o := identityOverlay(t, []config.ClaudeAccount{
		{Name: "a", ConfigDir: "/h/a"},
		{Name: "b", ConfigDir: "/h/b"},
	}, nil)

	assert.Empty(t, o.identityNote(80))
}

func TestAccountsOverlay_IdentityNoteReportsWrongLogin(t *testing.T) {
	o := identityOverlay(t, []config.ClaudeAccount{
		{Name: "personal", ConfigDir: "/h/p", ExpectAccount: "me@home.com"},
	}, map[string]string{"/h/p": "me@corp.com"})

	assert.Contains(t, o.identityNote(80), "'personal'")
	assert.Contains(t, o.identityNote(80), "wrong account")
}

// A collision outranks a mismatch: it needs no misconfiguration to happen, produces
// no error anywhere, and silently merges two quotas.
func TestAccountsOverlay_IdentityNotePrefersCollision(t *testing.T) {
	o := identityOverlay(t, []config.ClaudeAccount{
		{Name: "personal", ConfigDir: "/h/p", ExpectAccount: "me@home.com"},
		{Name: "work", ConfigDir: "/h/w"},
	}, map[string]string{"/h/p": "me@corp.com", "/h/w": "me@corp.com"})

	note := o.identityNote(80)
	assert.Contains(t, note, "same login")
	assert.NotContains(t, note, "wrong account")
}

// #478 is an overlay line that wraps the box at 91 columns. Every wording this file
// can emit must fit the width it is handed, at every width the box can be, or it
// reintroduces exactly that bug. Measured unstyled: lipgloss pads a rendered line to
// the box width, so a styled measurement passes regardless of the text.
func TestAccountsOverlay_IdentityNoteNeverExceedsItsWidth(t *testing.T) {
	rosters := map[string][]config.ClaudeAccount{
		"two colliding": {
			{Name: "personal-with-a-long-name", ConfigDir: "/h/p"},
			{Name: "work-with-a-long-name-too", ConfigDir: "/h/w"},
		},
		"three colliding": {
			{Name: "aaaaaaaaaaaaaaaaaaaaaaaaa", ConfigDir: "/h/a"},
			{Name: "bbbbbbbbbbbbbbbbbbbbbbbbb", ConfigDir: "/h/b"},
			{Name: "ccccccccccccccccccccccccc", ConfigDir: "/h/c"},
		},
		"one wrong login": {
			{Name: "an-extremely-long-account-name", ConfigDir: "/h/a",
				ExpectAccount: "expected@corp.com"},
		},
		"several wrong logins": {
			{Name: "aaaaaaaaaaaaaaaaaaaaaaaaa", ConfigDir: "/h/a", ExpectAccount: "x@corp.com"},
			{Name: "bbbbbbbbbbbbbbbbbbbbbbbbb", ConfigDir: "/h/b", ExpectAccount: "y@corp.com"},
		},
	}
	logins := map[string]string{
		"/h/p": "same@corp.com", "/h/w": "same@corp.com",
		"/h/a": "same@corp.com", "/h/b": "same@corp.com", "/h/c": "same@corp.com",
	}

	for name, accounts := range rosters {
		t.Run(name, func(t *testing.T) {
			o := identityOverlay(t, accounts, logins)
			// 16 is below the box's own 20-column floor; if a wording fits there it
			// fits anywhere the overlay can render.
			for w := 16; w <= 100; w++ {
				note := o.identityNote(w)
				require.LessOrEqual(t, lipgloss.Width(note), w,
					"width %d: %q overflows by %d", w, note, lipgloss.Width(note)-w)
			}
		})
	}
}

// The same guard for the preview's login line, which is held to the pool block's
// budget (inner minus the indent) rather than the full inner width.
func TestAccountsOverlay_PreviewIdentityLineNeverExceedsItsWidth(t *testing.T) {
	o := identityOverlay(t, []config.ClaudeAccount{
		{Name: "a", ConfigDir: "/h/a", ExpectAccount: "expected@corp.com"},
		{Name: "b", ConfigDir: "/h/b"},
	}, map[string]string{
		"/h/a": "a-very-long-actual-login-address@some-corporate-domain.example.com",
		"/h/b": "b-very-long-actual-login-address@some-corporate-domain.example.com",
	})

	for _, dir := range []string{"/h/a", "/h/b"} {
		for w := 4; w <= 100; w++ {
			line := o.previewIdentityLine(dir, w)
			require.LessOrEqual(t, lipgloss.Width(line), w,
				"dir %s at width %d: %q overflows", dir, w, line)
		}
	}
}

// The whole rendered panel must not grow past the box either — the note is one more
// line competing with rows the window budget already allocated.
func TestAccountsOverlay_IdentityNoteDoesNotWidenTheBox(t *testing.T) {
	o := identityOverlay(t, []config.ClaudeAccount{
		{Name: "personal", ConfigDir: "/h/p"},
		{Name: "work2", ConfigDir: "/h/w2"},
	}, map[string]string{"/h/p": "same@corp.com", "/h/w2": "same@corp.com"})

	for _, w := range []int{80, 91, 100, 120} {
		o.SetSize(w, 40)
		for _, line := range strings.Split(o.Render(), "\n") {
			require.LessOrEqual(t, lipgloss.Width(line), w,
				"terminal %d: line overflows: %q", w, line)
		}
	}
}

// The preview answers "who would a session created here bill". Without the login it
// could only name the directory, which is the thing that turned out to be lying.
func TestAccountsOverlay_PreviewNamesTheResolvedLogin(t *testing.T) {
	o := identityOverlay(t, []config.ClaudeAccount{
		{Name: "personal", ConfigDir: "/h/p"},
	}, map[string]string{"/h/p": "surprise@corp.com"})
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})

	assert.Contains(t, o.renderPreview(), "surprise@corp.com")
}

// A pinned account whose dir holds someone else must say so in the preview, not just
// print the login as if it were expected.
func TestAccountsOverlay_PreviewFlagsAWrongLogin(t *testing.T) {
	o := identityOverlay(t, []config.ClaudeAccount{
		{Name: "personal", ConfigDir: "/h/p", ExpectAccount: "me@home.com"},
	}, map[string]string{"/h/p": "me@corp.com"})

	line := o.previewIdentityLine("/h/p", 80)
	assert.Contains(t, line, "⚠")
	assert.Contains(t, line, "me@corp.com")
}

// Nothing readable means nothing shown — the preview gains a line only when it has
// something to say, and an inherit-env route has no dir at all.
func TestAccountsOverlay_PreviewIdentityLineSilentWithoutALogin(t *testing.T) {
	o := identityOverlay(t, []config.ClaudeAccount{{Name: "a", ConfigDir: "/h/a"}}, nil)

	assert.Empty(t, o.previewIdentityLine("/h/a", 80), "unreadable dir")
	assert.Empty(t, o.previewIdentityLine("", 80), "inherit-env route")
	assert.Empty(t, o.previewIdentityLine("/h/unknown", 80), "dir belonging to no account")
}

// Render must never touch the filesystem: the overlay redraws on every keystroke.
// The cache is filled once, when the panel opens.
func TestAccountsOverlay_RenderReadsNoIdentities(t *testing.T) {
	reads := 0
	o := NewAccountsOverlay(&config.Config{ClaudeAccounts: []config.ClaudeAccount{
		{Name: "a", ConfigDir: "/h/a"}, {Name: "b", ConfigDir: "/h/b"},
		{Name: "b2", ConfigDir: "/h/b"},
	}}, config.DefaultState())
	o.SetSize(100, 40)
	o.loadIdentities(func(string) (config.AccountIdentity, bool) {
		reads++
		return config.AccountIdentity{Email: "x@y.com", UUID: "u"}, true
	})

	require.Equal(t, 2, reads, "one read per DISTINCT dir, not per account")
	for i := 0; i < 5; i++ {
		o.Render()
	}
	assert.Equal(t, 2, reads, "Render performed identity IO")
}

// A nil reader is the "never looked" state, and must render as an unconfigured
// feature rather than as a clean bill of health or a crash.
func TestAccountsOverlay_NoIdentitiesRendersNothingExtra(t *testing.T) {
	o := NewAccountsOverlay(&config.Config{ClaudeAccounts: []config.ClaudeAccount{
		{Name: "a", ConfigDir: "/h/a"},
	}}, config.DefaultState())
	o.SetSize(100, 40)
	o.loadIdentities(nil)

	assert.Nil(t, o.logins)
	assert.Empty(t, o.identityNote(80))
	assert.Empty(t, o.previewIdentityLine("/h/a", 80))
	assert.NotPanics(t, func() { o.Render() })
}

// The production wiring: NewAccountsOverlay must read the logins itself. Every other
// test here injects a fake reader, so without this one the constructor's call could
// be deleted and the panel would silently show nothing — the exact blindness this
// feature exists to remove.
func TestNewAccountsOverlay_ReadsIdentitiesOnOpen(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".claude-real")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".claude.json"),
		[]byte(`{"oauthAccount":{"emailAddress":"opened@corp.com","accountUuid":"u-o"}}`), 0o600))

	o := NewAccountsOverlay(&config.Config{ClaudeAccounts: []config.ClaudeAccount{
		{Name: "real", ConfigDir: "~/.claude-real"},
	}}, config.DefaultState())

	require.Len(t, o.logins, 1, "the constructor read no identities")
	got, ok := o.identityFor(o.cfg.ClaudeAccounts[0])
	require.True(t, ok)
	assert.Equal(t, "opened@corp.com", got.actual.Email)
	assert.Equal(t, config.IdentityUnpinned, got.state)
}

// The cache holds READS, keyed by directory — not a verdict per row position. The
// overlay reorders (J/K), edits and deletes accounts in place while it is open, so a
// snapshot taken at construction and indexed by position names another account's
// login the moment any of those runs. Every account must keep reporting its own dir's
// login across all three mutations.
func TestAccountsOverlay_IdentitySurvivesInPlaceMutations(t *testing.T) {
	logins := map[string]string{"/h/p": "personal@corp.com", "/h/w": "work@corp.com"}
	// A fresh slice per subtest: moveAccount swaps in place, so a shared backing
	// array would leak one subtest's reorder into the next.
	open := func(t *testing.T) *AccountsOverlay {
		return identityOverlay(t, []config.ClaudeAccount{
			{Name: "personal", ConfigDir: "/h/p"},
			{Name: "work", ConfigDir: "/h/w"},
		}, logins)
	}

	t.Run("reorder", func(t *testing.T) {
		o := open(t)
		o.selectTab(tabClaude)
		require.True(t, o.moveAccount(1), "J/K reorder is reachable with the panel open")

		assert.Contains(t, o.previewIdentityLine("/h/p", 80), "personal@corp.com")
		assert.Contains(t, o.previewIdentityLine("/h/w", 80), "work@corp.com")
	})

	t.Run("delete", func(t *testing.T) {
		o := open(t)
		o.selectTab(tabClaude)
		o.cfg.ClaudeAccounts = o.cfg.ClaudeAccounts[1:] // 'personal' deleted

		assert.Contains(t, o.previewIdentityLine("/h/w", 80), "work@corp.com")
	})

	t.Run("edit retargets a dir", func(t *testing.T) {
		o := open(t)
		o.cfg.ClaudeAccounts = []config.ClaudeAccount{{Name: "personal", ConfigDir: "/h/w"}}

		assert.Contains(t, o.previewIdentityLine("/h/w", 80), "work@corp.com",
			"the login must follow the dir the account now names")
	})
}

// An edit can name a dir nobody had when the panel opened. commit() is a keypress, not
// a render, so it may read — otherwise the newly named dir stays permanently blank.
func TestAccountsOverlay_CommitReadsANewlyNamedDir(t *testing.T) {
	o := identityOverlay(t, []config.ClaudeAccount{{Name: "a", ConfigDir: "/h/a"}},
		map[string]string{"/h/a": "a@corp.com", "/h/late": "late@corp.com"})

	o.selectTab(tabClaude)
	o.editIndex = 0
	o.form = newAccountForm(false, true, "a", "/h/late", "", "", "", "")
	o.commit()

	assert.Contains(t, o.previewIdentityLine("/h/late", 80), "late@corp.com")
}

// Two accounts sharing one login is one collision; two SEPARATE pairs are two, and
// saying "all four are the same login" is simply false. The doctor report words each
// group on its own line; the panel has one line, so it must count groups rather than
// flatten them into a single claim.
func TestAccountsOverlay_IdentityNoteSeparatesDistinctCollisionGroups(t *testing.T) {
	o := identityOverlay(t, []config.ClaudeAccount{
		{Name: "a", ConfigDir: "/h/a"}, {Name: "b", ConfigDir: "/h/b"},
		{Name: "c", ConfigDir: "/h/c"}, {Name: "d", ConfigDir: "/h/d"},
	}, map[string]string{
		"/h/a": "x@corp.com", "/h/b": "x@corp.com",
		"/h/c": "y@corp.com", "/h/d": "y@corp.com",
	})

	for w := 16; w <= 120; w++ {
		note := o.identityNote(w)
		require.LessOrEqual(t, lipgloss.Width(note), w, "width %d: %q overflows", w, note)
		assert.NotContains(t, note, "'a', 'b', 'c' and 'd'",
			"width %d: two separate pairs reported as one shared login", w)
	}
	assert.Contains(t, o.identityNote(120), "2", "the note must say how many groups there are")
}

// One group keeps naming its members — the count wording is for several groups only.
func TestAccountsOverlay_IdentityNoteStillNamesASingleGroup(t *testing.T) {
	o := identityOverlay(t, []config.ClaudeAccount{
		{Name: "a", ConfigDir: "/h/a"}, {Name: "b", ConfigDir: "/h/b"},
		{Name: "c", ConfigDir: "/h/c"},
	}, map[string]string{
		"/h/a": "x@corp.com", "/h/b": "x@corp.com", "/h/c": "lonely@corp.com",
	})

	note := o.identityNote(80)
	assert.Contains(t, note, "'a'")
	assert.Contains(t, note, "'b'")
	assert.NotContains(t, note, "'c'")
}

// rowWindow's chrome must budget the identity note the same way it budgets the
// split-pool note (see TestAccountsOverlay_RowWindowChromeConditionalOnSplitPoolNote
// and commit 9b25662): a note printed by renderList but uncounted by rowWindow makes
// the box one line taller than the terminal it was measured against.
func TestAccountsOverlay_RowWindowChromeConditionalOnIdentityNote(t *testing.T) {
	mk := func(collide bool) *AccountsOverlay {
		var accts []config.ClaudeAccount
		logins := map[string]string{}
		for i := 0; i < 30; i++ {
			dir := fmt.Sprintf("/h/d%02d", i)
			accts = append(accts, config.ClaudeAccount{
				Name: fmt.Sprintf("acct%02d", i), ConfigDir: dir,
				// Route rules mean no catch-all, so the "unmatched repos" hint
				// renders too — that is the line whose slack the note ate.
				RemoteMatches: []string{dir},
			})
			logins[dir] = dir + "@corp.com"
		}
		if collide {
			logins["/h/d01"] = logins["/h/d00"]
		}
		return identityOverlay(t, accts, logins)
	}

	clean := mk(false)
	clean.SetSize(80, 24)
	start, end := clean.rowWindow(clean.activeLen())
	assert.Equal(t, 12, end-start, "a healthy config keeps the full 12-row budget")

	dirty := mk(true)
	dirty.SetSize(80, 24)
	start, end = dirty.rowWindow(dirty.activeLen())
	assert.Equal(t, 11, end-start, "the identity note costs exactly the one row it occupies")

	for _, h := range []int{20, 24, 30} {
		dirty.SetSize(80, h)
		for _, line := range strings.Split(dirty.Render(), "\n") {
			require.LessOrEqual(t, lipgloss.Width(line), 80, "height %d: line overflows", h)
		}
		require.LessOrEqual(t, len(strings.Split(dirty.Render(), "\n")), h,
			"height %d: the box is taller than the terminal", h)
	}
}

// On a fully rate-limited pool, creation does NOT use SelectPoolMember's defensive
// cursor fallback — it pins SoonestResetMember on confirm (the member the "← on
// confirm" marker points at). The login line answers "who would this bill", so it
// has to follow the same member, or the preview names one account while the create
// spends another.
//
// work-1 sits at the rotation cursor and would win a naive read; work-2 resets
// first, so it is the one creation actually pins.
func TestAccountsOverlay_PreviewLoginFollowsTheConfirmedMemberWhenAllLimited(t *testing.T) {
	accounts := []config.ClaudeAccount{
		{Name: "work-1", ConfigDir: "/h/w1", Pool: "work"},
		{Name: "work-2", ConfigDir: "/h/w2", Pool: "work"},
	}
	st := config.DefaultState()
	require.NoError(t, st.SetAccountLimited("work-1", "2099-01-02T00:00:00Z"))
	require.NoError(t, st.SetAccountLimited("work-2", "2099-01-01T00:00:00Z")) // resets first

	o := NewAccountsOverlay(&config.Config{ClaudeAccounts: accounts}, st)
	o.SetSize(120, 40)
	o.loadIdentities(func(dir string) (config.AccountIdentity, bool) {
		return config.AccountIdentity{Email: filepath.Base(dir) + "@corp.com",
			UUID: "u-" + dir}, true
	})
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})

	out := o.renderPreview()
	assert.Contains(t, out, "w2@corp.com",
		"the preview names a login other than the member creation would pin")
	assert.NotContains(t, out, "w1@corp.com")
}

// config_dir is hand-written, and everything else in this file compares on the
// normalised spelling — the login cache is keyed by it, and CheckIdentity looks it up.
// The dir lookup has to agree, or a caller arriving with one spelling misses an
// account written with the other, and misses by rendering NOTHING: the preview simply
// loses its login line rather than reporting a problem.
func TestAccountsOverlay_IdentityLookupIgnoresDirSpelling(t *testing.T) {
	o := identityOverlay(t, []config.ClaudeAccount{
		{Name: "trailing", ConfigDir: "/h/p/"},
		{Name: "dotted", ConfigDir: "/h/sub/../q"},
	}, map[string]string{"/h/p": "p@corp.com", "/h/q": "q@corp.com"})

	for _, tc := range []struct{ dir, want string }{
		{"/h/p", "p@corp.com"},        // raw caller, trailing-slash account
		{"/h/p/", "p@corp.com"},       // both spelled with the slash
		{"/h/q", "q@corp.com"},        // raw caller, dotted account
		{"/h/sub/../q", "q@corp.com"}, // both spelled with the dots
	} {
		assert.Contains(t, o.previewIdentityLine(tc.dir, 80), tc.want,
			"lookup for %q found nothing", tc.dir)
	}
}

// A dir can record a UUID and no email — ReadAccountIdentity accepts exactly that, and
// doctor's IdentityCollision.Login names the UUID rather than render a blank. The
// preview has to agree: staying silent about a login the report names would leave the
// two surfaces disagreeing about whether the account is known at all.
func TestAccountsOverlay_PreviewNamesAUUIDOnlyLogin(t *testing.T) {
	o := NewAccountsOverlay(&config.Config{ClaudeAccounts: []config.ClaudeAccount{
		{Name: "quiet", ConfigDir: "/h/quiet"},
	}}, config.DefaultState())
	o.SetSize(120, 40)
	o.loadIdentities(func(string) (config.AccountIdentity, bool) {
		return config.AccountIdentity{UUID: "u-shared"}, true // no email at all
	})

	assert.Contains(t, o.previewIdentityLine("/h/quiet", 80), "u-shared")
}

// A readable dir recording neither still renders nothing: there is no login to name,
// and "signed in as " with a blank after it points at an answer it does not have.
func TestAccountsOverlay_PreviewSilentWithoutAnyLogin(t *testing.T) {
	o := NewAccountsOverlay(&config.Config{ClaudeAccounts: []config.ClaudeAccount{
		{Name: "blank", ConfigDir: "/h/blank"},
	}}, config.DefaultState())
	o.SetSize(120, 40)
	o.loadIdentities(func(string) (config.AccountIdentity, bool) {
		return config.AccountIdentity{}, true // readable, but says nothing
	})

	assert.Empty(t, o.previewIdentityLine("/h/blank", 80))
}
