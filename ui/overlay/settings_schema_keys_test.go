package overlay

import (
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/keys"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The settings panel is prose about keys, and a key it spells is a copy of the
// keymap that stops being true the moment the user rebinds. double_tap_confirm is
// the sharp one: the dialog's second-press key follows the rebind (armDoubleTap
// takes keys.PrimaryKey), so a hardcoded "Ctrl+X" taught the one key the dialog
// stopped answering to — in front of the user who rebound kill exactly because
// ctrl+x is their shell's editing key.
//
// It carries TWO examples since #798 generalized the double-tap, so both are
// checked: one right key beside one stale literal is the shape that reads as
// covered and is not.
func TestSettingRows_FollowARebind(t *testing.T) {
	_, restore := keys.Apply(map[string]keys.Spec{
		"kill":          {Keys: []string{"ctrl+g"}},
		"push_branch":   {Keys: []string{"ctrl+u"}},
		"merge_pr":      {Keys: []string{"g"}},
		"move_up":       {Keys: []string{"alt+up"}},
		"move_group_up": {Keys: []string{"alt+left"}},
	})
	defer restore()

	text := map[string]string{}
	for _, r := range newSettingRows(config.DefaultConfig()) {
		text[r.key] = r.summary + " " + r.detail + " " + strings.Join(glossValues(r), " ")
	}

	for _, tc := range []struct{ row, want, stale string }{
		{"double_tap_confirm", "ctrl-g", "Ctrl+X"},
		{"double_tap_confirm", "ctrl-u", " P twice"},
		{"pr_create_draft", "g", "with m in-app"},
		{"session_sort", "alt-↑", "J/K"},
		{"session_sort", "alt-←", "`{`"},
	} {
		got, ok := text[tc.row]
		require.Truef(t, ok, "no %q row", tc.row)
		assert.Containsf(t, got, tc.want, "%s must name the rebound key", tc.row)
		assert.NotContainsf(t, got, tc.stale, "%s must not still teach the old key", tc.row)
	}
}

// glossValues is the row's enum glosses, which are prose too.
func glossValues(r settingRow) []string {
	out := make([]string, 0, len(r.gloss))
	for _, v := range r.gloss {
		out = append(out, v)
	}
	return out
}
