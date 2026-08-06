package config

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The three shapes a user may write, and the assertion that each survives a save.
//
// SaveConfig marshals the whole struct, and it runs on every keystroke in the
// settings panel — so a spec that does not round-trip is not a cosmetic
// annoyance: the first time the user opens settings, Atrium silently rewrites a
// section of a file they hand-authored.
func TestKeySpec_RoundTripsInTheShapeItWasWritten(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		want      KeySpec
	}{
		{"single key", `"ctrl+g"`, KeySpec{Keys: []string{"ctrl+g"}}},
		{"list of keys", `["up","w"]`, KeySpec{Keys: []string{"up", "w"}, wasArray: true}},
		{"disabled", `"disabled"`, KeySpec{Disabled: true}},
		{"empty list", `[]`, KeySpec{Keys: []string{}, wasArray: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got KeySpec
			require.NoError(t, json.Unmarshal([]byte(tc.raw), &got))
			assert.Equal(t, tc.want, got)

			back, err := json.Marshal(got)
			require.NoError(t, err)
			assert.JSONEq(t, tc.raw, string(back),
				"a spec must marshal back in the shape it was written in")
		})
	}
}

// A value Atrium cannot read at all must still not be an unmarshalling error:
// LoadConfig is called from a dozen-odd non-TUI sites, including the daemon and
// the worktree hooks, so a mistyped keybinding must not stop a session starting.
func TestKeySpec_MalformedValueIsDataNotAnError(t *testing.T) {
	var got KeySpec
	require.NoError(t, json.Unmarshal([]byte(`{"key":"x"}`), &got))
	assert.NotEmpty(t, got.Malformed, "an unreadable value must carry its reason forward")
	assert.Empty(t, got.Keys)
	assert.False(t, got.Disabled)
}

// The whole section through a config save and load, including an action that
// does not exist.
//
// The unknown action is the point. Validation drops it from the *bindings*, but
// it must stay in the map: if load stripped it, the first settings keystroke
// would delete the user's line, and the doctor message telling them it was wrong
// would vanish along with the evidence.
func TestConfig_KeybindingsSurviveASaveIncludingAnUnknownAction(t *testing.T) {
	raw := []byte(`{
	  "keybindings": {
	    "attach_toggle": "ctrl+g",
	    "up": ["up", "w"],
	    "pause_all": "disabled",
	    "pauze_all": "ctrl+j"
	  }
	}`)
	var cfg Config
	require.NoError(t, json.Unmarshal(raw, &cfg))
	require.Len(t, cfg.Keybindings, 4)

	back, err := json.Marshal(cfg)
	require.NoError(t, err)

	var again Config
	require.NoError(t, json.Unmarshal(back, &again))
	assert.Equal(t, cfg.Keybindings, again.Keybindings,
		"every entry the user wrote must survive a save, including one Atrium refuses")
	assert.Contains(t, again.Keybindings, "pauze_all",
		"a misspelled action must not be silently deleted from the file that names it")
}

// A config with no keybindings section must not grow one, or every user who has
// never remapped anything gets an empty object written into their file.
func TestConfig_NoKeybindingsSectionStaysAbsent(t *testing.T) {
	back, err := json.Marshal(DefaultConfig())
	require.NoError(t, err)
	assert.NotContains(t, string(back), "keybindings")
}

// The bridge to the validating package, including the malformed reason.
func TestKeybindingOverrides_CarriesEveryShapeThrough(t *testing.T) {
	cfg := &Config{Keybindings: map[string]KeySpec{
		"new":       {Keys: []string{"ctrl+n"}},
		"pause_all": {Disabled: true},
		"kill":      {Malformed: "value 7 is neither a key, a list of keys, nor \"disabled\""},
	}}
	got := cfg.KeybindingOverrides()
	require.Len(t, got, 3)
	assert.Equal(t, []string{"ctrl+n"}, got["new"].Keys)
	assert.True(t, got["pause_all"].Disabled)
	assert.NotEmpty(t, got["kill"].Malformed)

	assert.Nil(t, (&Config{}).KeybindingOverrides(), "no section means no overrides")
	assert.Nil(t, (*Config)(nil).KeybindingOverrides(), "a nil config must not panic")
}
