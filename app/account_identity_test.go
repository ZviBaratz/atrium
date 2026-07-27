package app

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/internal/doctor"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui/overlay"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// idByBase fakes the identity reader, keyed on the config dir's base name so tests
// need not know the sandboxed HOME that ResolvedConfigDir expands against. A base
// absent from the map reads as "no login recorded".
func idByBase(m map[string]config.AccountIdentity) config.IdentityReadFunc {
	return func(dir string) (config.AccountIdentity, bool) {
		id, ok := m[filepath.Base(dir)]
		return id, ok
	}
}

func email(addr string) config.AccountIdentity {
	return config.AccountIdentity{Email: addr, UUID: "uuid-" + addr}
}

func TestAccountIdentityError(t *testing.T) {
	read := idByBase(map[string]config.AccountIdentity{
		"dir-right": email("right@corp.com"),
		"dir-wrong": email("actual@corp.com"),
	})

	cases := []struct {
		name    string
		acct    config.ClaudeAccount
		wantErr bool
	}{{
		name: "pinned and the dir holds that login",
		acct: config.ClaudeAccount{Name: "a", ConfigDir: "/h/dir-right", ExpectAccount: "right@corp.com"},
	}, {
		name: "pin differs only by case",
		acct: config.ClaudeAccount{Name: "a", ConfigDir: "/h/dir-right", ExpectAccount: "RIGHT@corp.com"},
	}, {
		// The only refusal: launching here would quietly spend actual@corp.com.
		name:    "pinned and the dir holds a different login",
		acct:    config.ClaudeAccount{Name: "a", ConfigDir: "/h/dir-wrong", ExpectAccount: "right@corp.com"},
		wantErr: true,
	}, {
		name: "unpinned is never refused, whatever the dir holds",
		acct: config.ClaudeAccount{Name: "a", ConfigDir: "/h/dir-wrong"},
	}, {
		// claude will prompt for login in the pane; there is no silent mis-bill to
		// prevent, and refusing would strand anyone mid-onboarding.
		name: "pinned but no login recorded",
		acct: config.ClaudeAccount{Name: "a", ConfigDir: "/h/dir-absent", ExpectAccount: "right@corp.com"},
	}, {
		name: "inherit-env account has no dir to verify",
		acct: config.ClaudeAccount{Name: "a", ExpectAccount: "right@corp.com"},
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := accountIdentityError(tc.acct, read)
			if !tc.wantErr {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			// The message must be actionable on its own: which account, what it
			// expected, and who would actually be billed.
			for _, want := range []string{`"a"`, "right@corp.com", "actual@corp.com"} {
				assert.Contains(t, err.Error(), want)
			}
		})
	}
}

// pinnedHome routes everything to a single catch-all account pinned to want, whose
// directory actually holds have.
func pinnedHome(t *testing.T, want, have string) *home {
	t.Helper()
	h := newCreateFormHome(t)
	h.appConfig.ClaudeAccounts = []config.ClaudeAccount{
		{Name: "personal", ConfigDir: "~/.claude-personal", ExpectAccount: want},
	}
	h.readIdentity = idByBase(map[string]config.AccountIdentity{".claude-personal": email(have)})
	return h
}

func TestStartNewSession_RefusesMismatchedIdentity(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := pinnedHome(t, "me@personal.com", "me@work.com")

	before := h.list.NumInstances()
	_, err := h.startNewSession("s", t.TempDir(), true, "echo", "", "", nil, false)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "me@personal.com")
	assert.Contains(t, err.Error(), "me@work.com")
	assert.Equal(t, before, h.list.NumInstances(),
		"a refused launch must leave no session behind")
}

func TestStartNewSession_AllowsMatchingIdentity(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := pinnedHome(t, "me@personal.com", "me@personal.com")

	inst := startDirect(t, h, nil)
	assert.Equal(t, "personal", inst.ClaudeAccountName())
}

func TestStartNewSession_AllowsUnpinnedAndUnreadable(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	t.Run("unpinned account with a surprising login", func(t *testing.T) {
		h := pinnedHome(t, "", "someone@else.com") // no expect_account
		startDirect(t, h, nil)
	})

	t.Run("pinned account whose dir has no login", func(t *testing.T) {
		h := pinnedHome(t, "me@personal.com", "")
		h.readIdentity = idByBase(nil) // nothing readable anywhere
		startDirect(t, h, nil)
	})

	t.Run("dormant: no claude_accounts at all", func(t *testing.T) {
		h := newCreateFormHome(t)
		h.readIdentity = idByBase(nil)
		startDirect(t, h, nil)
	})
}

// The gate must run on the member rotation actually picked, not on the pool's first
// account — otherwise a pool with one rotted member launches onto it every time the
// cursor comes round. work-1 is clean and work-2 is not, so the first create succeeds
// and the second, which rotates to work-2, must be refused.
func TestStartNewSession_GatesTheRotatedMember(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := poolHome(t)
	h.appConfig.ClaudeAccounts[0].ExpectAccount = "one@corp.com"
	h.appConfig.ClaudeAccounts[1].ExpectAccount = "two@corp.com"
	h.readIdentity = idByBase(map[string]config.AccountIdentity{
		".claude-work":  email("one@corp.com"),
		".claude-work2": email("wrong@corp.com"),
	})

	first := startDirect(t, h, nil)
	require.Equal(t, "work-1", first.ClaudeAccountName())

	before := h.list.NumInstances()
	_, err := h.startNewSession("s2", t.TempDir(), true, "echo", "", "", nil, false)
	require.Error(t, err, "rotation landed on work-2, whose dir holds the wrong login")
	assert.Contains(t, err.Error(), "work-2")
	assert.Equal(t, before, h.list.NumInstances())
}

// A deliberate picker pin bypasses availability, but not identity: pinning cannot be
// an escape hatch onto the wrong login, because the pin expresses which ACCOUNT to
// use and the whole failure is the dir no longer being that account.
func TestStartNewSession_GatesAPickerPin(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := poolHome(t)
	h.appConfig.ClaudeAccounts[1].ExpectAccount = "two@corp.com"
	h.readIdentity = idByBase(map[string]config.AccountIdentity{
		".claude-work":  email("one@corp.com"),
		".claude-work2": email("wrong@corp.com"),
	})

	pin := &overlay.AccountSelection{Pool: "work", Member: &h.appConfig.ClaudeAccounts[1]}
	_, err := h.startNewSession("s", t.TempDir(), true, "echo", "", "", pin, false)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "work-2")
}

// pausedLike builds an instance stamped with an account, standing in for a paused
// session about to re-inject the config dir it was born with.
func pausedLike(t *testing.T, title, account, dir string) *session.Instance {
	t.Helper()
	inst, err := session.NewInstance(session.InstanceOptions{
		Title: title, Path: t.TempDir(), Program: "echo", Direct: true,
	})
	require.NoError(t, err)
	inst.SetClaudeAccount(account, dir, false)
	return inst
}

func TestVerifyResumeIdentity(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := newCreateFormHome(t)
	h.appConfig.ClaudeAccounts = []config.ClaudeAccount{
		{Name: "personal", ConfigDir: "/h/dir-personal", ExpectAccount: "me@personal.com"},
	}
	h.readIdentity = idByBase(map[string]config.AccountIdentity{
		"dir-personal": email("me@work.com"),
		"dir-other":    email("me@work.com"),
	})

	t.Run("mismatch refuses", func(t *testing.T) {
		err := h.verifyResumeIdentity(pausedLike(t, "s", "personal", "/h/dir-personal"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "me@personal.com")
	})

	// #470's anchor: the dir is what the session actually injected, so a renamed
	// account still resolves to the expectation its sessions run under.
	t.Run("renamed account still resolves by config dir", func(t *testing.T) {
		err := h.verifyResumeIdentity(pausedLike(t, "s", "old-name", "/h/dir-personal"))
		require.Error(t, err, "the stamped name is stale but the dir still anchors")
	})

	t.Run("a dir matching no configured account proceeds", func(t *testing.T) {
		assert.NoError(t, h.verifyResumeIdentity(pausedLike(t, "s", "personal", "/h/dir-other")))
	})

	t.Run("an unstamped session proceeds", func(t *testing.T) {
		assert.NoError(t, h.verifyResumeIdentity(pausedLike(t, "s", "", "")))
	})
}

// Wiring proof for the single-resume path: the gate must fire before the busy row is
// armed, so a session that must not run on this login never reaches Resume.
func TestResumeSelected_RefusesMismatchedIdentity(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := newCreateFormHome(t)
	h.appConfig.ClaudeAccounts = []config.ClaudeAccount{
		{Name: "personal", ConfigDir: "/h/dir-personal", ExpectAccount: "me@personal.com"},
	}
	consulted := false
	h.readIdentity = func(dir string) (config.AccountIdentity, bool) {
		consulted = true
		return email("me@work.com"), true
	}

	h.resumeSelected(pausedLike(t, "s", "personal", "/h/dir-personal"))

	assert.True(t, consulted, "resumeSelected never consulted the identity gate")
	assert.False(t, h.actionInFlight,
		"a refused resume armed the busy row, so Resume was reached anyway")
	assert.Nil(t, h.confirmationOverlay, "a refused resume must not ask a question first")
}

func TestResumeSelected_AllowsMatchingIdentity(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := newCreateFormHome(t)
	h.appConfig.ClaudeAccounts = []config.ClaudeAccount{
		{Name: "personal", ConfigDir: "/h/dir-personal", ExpectAccount: "me@personal.com"},
	}
	h.readIdentity = func(string) (config.AccountIdentity, bool) {
		return email("me@personal.com"), true
	}

	h.resumeSelected(pausedLike(t, "s", "personal", "/h/dir-personal"))

	assert.True(t, h.actionInFlight, "a verified resume must proceed to the busy row")
}

// One wrong login in a batch must not cancel the resume of every session beside it.
// Both instances here fail — one on identity, one because a never-started instance
// cannot resume — and that is the point: the loop reached the second at all.
func TestResumeInstances_GatesPerInstance(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := newCreateFormHome(t)
	h.appConfig.ClaudeAccounts = []config.ClaudeAccount{
		{Name: "personal", ConfigDir: "/h/dir-personal", ExpectAccount: "me@personal.com"},
	}
	h.readIdentity = idByBase(map[string]config.AccountIdentity{
		"dir-personal": email("me@work.com"),
	})

	h.resumeInstances([]*session.Instance{
		pausedLike(t, "rotted", "personal", "/h/dir-personal"),
		pausedLike(t, "unstamped", "", ""),
	}, "Resume 2 sessions?")

	require.NotNil(t, h.pendingConfirmAction, "resumeInstances staged no action")
	msg, ok := h.pendingConfirmAction().(batchResumeDoneMsg)
	require.True(t, ok, "unexpected message type %T", msg)

	require.Len(t, msg.failures, 2)
	assert.Equal(t, "rotted", msg.failures[0].title)
	assert.Contains(t, msg.failures[0].err.Error(), "me@personal.com",
		"the first failure should be the identity refusal")
	assert.Equal(t, "unstamped", msg.failures[1].title)
	assert.NotContains(t, msg.failures[1].err.Error(), "expects",
		"the ungated session failed on its own merits, not the gate")
}

// The gate defaults on. A home built without the seam must still verify, or a future
// construction path would silently opt out of the check.
func TestIdentityReadDefaultsToTheRealReader(t *testing.T) {
	h := &home{}
	require.NotNil(t, h.identityRead())

	dir := t.TempDir()
	_, ok := h.identityRead()(dir)
	assert.False(t, ok, "an empty dir has no login")

	// And the seam wins when set.
	h.readIdentity = func(string) (config.AccountIdentity, bool) { return email("x@y.com"), true }
	got, ok := h.identityRead()(dir)
	require.True(t, ok)
	assert.Equal(t, "x@y.com", got.Email)
}

// The no-dir and unreadable branches both proceed, so a return-value test cannot
// tell them apart — yet they must not behave the same. A pinned dir that holds no
// login is worth a line in the log, because someone asserted something about it that
// could not be checked. An inherit-env account has no directory at all: there is
// nothing to warn about, and warning would fire on every launch forever.
func TestAccountIdentityWarnsOnlyWhenThereIsADirToWarnAbout(t *testing.T) {
	var buf bytes.Buffer
	prev := log.WarningLog.Writer()
	log.WarningLog.SetOutput(&buf)
	t.Cleanup(func() { log.WarningLog.SetOutput(prev) })

	read := idByBase(nil) // nothing is readable

	require.NoError(t, accountIdentityError(
		config.ClaudeAccount{Name: "ambient", ExpectAccount: "me@personal.com"}, read))
	assert.Empty(t, buf.String(), "an account with no config dir warned about one")

	// Nor does an unpinned account with an unreadable dir: nobody asserted anything
	// about it, so there is no unanswered question to report. Without this case the
	// pin guard could be deleted and every test here would still pass.
	require.NoError(t, accountIdentityError(
		config.ClaudeAccount{Name: "unpinned", ConfigDir: "/h/dir-quiet"}, read))
	assert.Empty(t, buf.String(), "an unpinned account warned about an unverified dir")

	require.NoError(t, accountIdentityError(
		config.ClaudeAccount{Name: "pinned", ConfigDir: "/h/dir-x",
			ExpectAccount: "me@personal.com"}, read))
	assert.Contains(t, buf.String(), "/h/dir-x",
		"a pinned dir with no login recorded should say so")
}

// doctor's report and this gate now decide on one classifier, but they still consume
// it separately — the report maps states to text, the gate maps them to a refusal.
// This pins the correspondence: doctor calls an account wrong exactly when the gate
// refuses to launch it. A section printing "ok" beside a session that will not start
// would send the user hunting the wrong problem.
func TestGateRefusesExactlyWhatDoctorCallsAMismatch(t *testing.T) {
	cases := []struct {
		name       string
		pin        string
		actual     string // "" means nothing readable
		wantRefuse bool
		wantState  config.IdentityCheck
	}{
		{"verified", "me@a.com", "me@a.com", false, config.IdentityVerified},
		{"wrong login", "me@a.com", "me@b.com", true, config.IdentityWrongAccount},
		{"unpinned", "", "me@b.com", false, config.IdentityUnpinned},
		{"no login recorded", "me@a.com", "", false, config.IdentityUnreadable},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ids := map[string]config.AccountIdentity{}
			if tc.actual != "" {
				ids["dir"] = email(tc.actual)
			}
			acct := config.ClaudeAccount{Name: "a", ConfigDir: "/h/dir", ExpectAccount: tc.pin}

			refused := accountIdentityError(acct, idByBase(ids)) != nil
			assert.Equal(t, tc.wantRefuse, refused, "gate verdict")

			rep := doctor.CheckAccountIdentity(
				&config.Config{ClaudeAccounts: []config.ClaudeAccount{acct}}, idByBase(ids))
			require.Len(t, rep.Rows, 1)
			assert.Equal(t, tc.wantState, rep.Rows[0].State, "doctor state")

			assert.Equal(t, refused, rep.Rows[0].State == config.IdentityWrongAccount,
				"doctor and the gate disagree about whether this is a mismatch")
		})
	}
}

// A message that names neither the account nor the two logins is not actionable —
// the user has to guess which of several config dirs to re-login.
func TestAccountIdentityErrorNamesTheDirectory(t *testing.T) {
	err := accountIdentityError(
		config.ClaudeAccount{Name: "personal", ConfigDir: "/h/.claude-personal",
			ExpectAccount: "me@personal.com"},
		idByBase(map[string]config.AccountIdentity{".claude-personal": email("me@work.com")}))

	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "/h/.claude-personal"),
		"the error must name the directory to fix: %s", err)
}

// `atrium doctor` advertises this gate: its hint offers to "refuse to start a session
// on the wrong one" to anyone who has not pinned an account. That is a promise made
// in one package about behaviour implemented in another, and the test above only
// checks that the two agree about STATE — a report and a gate can classify an account
// identically while the sentence describing the consequence is fiction. #496 shipped
// exactly that: the hint offered a refusal, and no launch path performed one.
//
// So assert the advertisement against the enforcer, in both directions. The refusal
// must be real for the pinned mismatch the hint is selling, and must NOT already
// apply to the unpinned account it is addressed to — otherwise the hint is offering a
// change that has already happened.
func TestDoctorHintMatchesGate(t *testing.T) {
	unpinned := config.ClaudeAccount{Name: "a", ConfigDir: "/h/dir"}
	surprising := idByBase(map[string]config.AccountIdentity{"dir": email("someone@else.com")})

	hint := doctor.RenderAccountIdentity(doctor.CheckAccountIdentity(
		&config.Config{ClaudeAccounts: []config.ClaudeAccount{unpinned}}, surprising))
	require.Contains(t, hint, "unpinned: a", "no hint rendered, so there is nothing to check")
	assert.Contains(t, hint, "refuse",
		"the hint stopped advertising the refusal this gate performs; if the gate was "+
			"removed, remove this test with it")

	pinned := unpinned
	pinned.ExpectAccount = "me@corp.com"
	assert.Error(t, accountIdentityError(pinned, surprising),
		"the hint promises a refusal the gate does not perform")
	assert.NoError(t, accountIdentityError(unpinned, surprising),
		"an unpinned account is already refused, so the hint offers nothing new")
}
