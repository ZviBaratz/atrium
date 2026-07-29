package overlay

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseList_TrimsAndDropsBlanks(t *testing.T) {
	assert.Equal(t, []string{"a", "b"}, parseList("a ,, b,"))
	assert.Nil(t, parseList("   "), "whitespace-only → nil, never a blank token")
	assert.Nil(t, parseList(""), "empty → nil")
	assert.Equal(t, []string{"github.com/acme"}, parseList(" github.com/acme "))
}

func TestAccountForm_SeedAndParse(t *testing.T) {
	f := newAccountForm(false, true, "work", "~/.claude-work", "github.com/acme, gh.com/x", "~/work/", "", "team-a")
	assert.Equal(t, "work", f.Name())
	assert.Equal(t, "~/.claude-work", f.ConfigDir())
	assert.Equal(t, []string{"github.com/acme", "gh.com/x"}, f.RemoteMatches())
	assert.Equal(t, []string{"~/work/"}, f.PathMatches())
	assert.Nil(t, f.TokenEnv(), "Claude form has no token field")
	assert.Equal(t, "team-a", f.Pool(), "Claude form seeds the Pool field")
	assert.Len(t, f.inputs, 5, "Claude form: name/configDir/remote/path/pool")
}

func TestAccountForm_GHHasTokenField(t *testing.T) {
	f := newAccountForm(true, false, "gh", "~/.config/gh-work", "", "", "GH_TOKEN, GITHUB_TOKEN", "")
	assert.Len(t, f.inputs, 5, "GH form: name/configDir/remote/path/token, no pool")
	assert.Equal(t, []string{"GH_TOKEN", "GITHUB_TOKEN"}, f.TokenEnv())
	assert.Equal(t, "", f.Pool(), "GH form never exposes a Pool field")
}

// The Antigravity tab passes showToken=false AND showPool=false, so its form has
// neither optional field — just the four shared ones. This guards the regression
// where showPool was derived as !showToken and wrongly grew a Pool field on agy.
func TestAccountForm_AgyHasNeitherTokenNorPool(t *testing.T) {
	f := newAccountForm(false, false, "agy", "~/.antigravity", "", "", "", "")
	assert.Len(t, f.inputs, 4, "agy form: name/configDir/remote/path only")
	assert.False(t, f.showPool, "agy form must not show the Claude-only Pool field")
	assert.False(t, f.showToken, "agy form must not show the GH-only Token field")
	assert.Nil(t, f.TokenEnv(), "agy form has no token field")
	assert.Equal(t, "", f.Pool(), "agy form has no pool field")
}

func TestAccountForm_NavAndSubmitCancel(t *testing.T) {
	f := newAccountForm(false, true, "", "", "", "", "", "")
	assert.Equal(t, fldName, f.focus)
	f.HandleKeyPress(keyMsg("tab"))
	assert.Equal(t, fldConfigDir, f.focus, "tab advances focus")
	f.HandleKeyPress(keyMsg("shift+tab"))
	assert.Equal(t, fldName, f.focus, "shift+tab retreats focus")

	assert.True(t, f.HandleKeyPress(keyMsg("enter")))
	assert.True(t, f.Submitted())

	g := newAccountForm(false, true, "", "", "", "", "", "")
	assert.True(t, g.HandleKeyPress(keyMsg("esc")))
	assert.True(t, g.Canceled())
}

func TestAccountForm_CtrlOOpensPickerOnConfigDirOnly(t *testing.T) {
	f := newAccountForm(false, true, "", "", "", "", "", "")
	f.HandleKeyPress(keyMsg("ctrl+o")) // focus is Name
	assert.Nil(t, f.picker, "ctrl+o does nothing unless the config-dir field is focused")

	f.focus = fldConfigDir
	f.applyFocus()
	f.HandleKeyPress(keyMsg("ctrl+o"))
	assert.NotNil(t, f.picker, "ctrl+o on config dir opens the picker")

	// esc closes the picker (returns to the form), does NOT finish the form.
	done := f.HandleKeyPress(keyMsg("esc"))
	assert.False(t, done)
	assert.Nil(t, f.picker)
	assert.False(t, f.Canceled(), "esc in the picker must not cancel the whole form")
}

func TestAccountForm_PickerEnterWritesBack(t *testing.T) {
	dir := t.TempDir()
	f := newAccountForm(false, true, "", dir, "", "", "", "")
	f.focus = fldConfigDir
	f.applyFocus()
	f.HandleKeyPress(keyMsg("ctrl+o"))
	require.NotNil(t, f.picker)
	f.HandleKeyPress(keyMsg("enter")) // accept current selection
	assert.Nil(t, f.picker)
	assert.Equal(t, dir, f.ConfigDir(), "the picked path is written into the config-dir field")
}

func TestAccountForm_ConfigDirExistsHint(t *testing.T) {
	dir := t.TempDir()
	f := newAccountForm(false, true, "", dir, "", "", "", "")
	assert.Contains(t, f.configDirHint(), "exists")

	g := newAccountForm(false, true, "", "/no/such/path/xyzzy", "", "", "", "")
	assert.Contains(t, g.configDirHint(), "not found")

	h := newAccountForm(false, true, "", "", "", "", "", "")
	assert.Equal(t, "", h.configDirHint(), "empty config dir shows no hint")
}
