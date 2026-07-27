package overlay

import (
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

	assert.Nil(t, o.identities)
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

	require.Len(t, o.identities, 1, "the constructor read no identities")
	assert.Equal(t, "opened@corp.com", o.identities[0].actual.Email)
	assert.Equal(t, config.IdentityUnpinned, o.identities[0].state)
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
