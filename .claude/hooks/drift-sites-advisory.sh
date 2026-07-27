#!/usr/bin/env bash
# PostToolUse/Edit|Write hook: after editing a file whose facts live in several
# places, name the sibling sites — because most are guarded by a drift test and a
# few are not, and a green suite does not distinguish them.
#
# Advisory only: it reports after the edit already happened. Exit 2 surfaces stderr
# to the agent as feedback; exit 0 stays silent.
#
# Full map: .claude/skills/tui-drift-sites/SKILL.md
set -uo pipefail

payload="$(cat)"

if command -v jq >/dev/null 2>&1; then
	path="$(printf '%s' "$payload" | jq -r '.tool_input.file_path // ""' 2>/dev/null)"
else
	path="$(printf '%s' "$payload" | python3 -c \
		'import json,sys;print(json.load(sys.stdin).get("tool_input",{}).get("file_path",""))' 2>/dev/null)"
fi

[[ -z "$path" ]] && exit 0

# Patterns use a bare leading `*` so they match both the repo-relative path and an
# absolute one — `*` matches the empty string, so `*keys/registry.go` covers both
# "keys/registry.go" and "/abs/path/keys/registry.go". Listing only the `*/` form
# silently misses relative paths, which is how two of these branches were dead.
case "$path" in
*keys/registry.go)
	cat >&2 <<-'EOF'
		keys/registry.go touched — a keybinding has 7 sites. 6 are guarded; site 4 is not.

		  1 keys/keys.go            KeyName const + doc comment
		  2 keys/registry.go        the Entry                      <- you are here
		  3 keys/help_layout.go     a HelpRow (or a Mentions)
		  4 app/app_update.go       case keys.KeyX: in handleKeyPress   <- NO GUARD
		  5 keys/registry_test.go   the golden inventory pair
		  6 README.md               `#### Keybindings`, backticked
		  7 app/app_update.go       keyAllowedWhileBusy, if it must work mid-action

		Nothing asserts a registered key has a dispatch case, so a key can ship
		registered, documented, README'd — and dead. Press it, or drive it through
		handleKeyPress in a test. See .claude/skills/tui-drift-sites/SKILL.md.
	EOF
	exit 2
	;;
*config/types.go)
	cat >&2 <<-'EOF'
		config/types.go touched — a Config field has 4 sites. 3 are guarded.

		  1 config/types.go                   the field + json tag   <- you are here
		  2 config/config.go DefaultConfig()  the default            <- manual
		  3 README.md                         "Configuration reference" row
		  4 ui/overlay/settings_schema.go     a settingRow, if user-editable

		Sites 3 and 4 are guarded bidirectionally (TestReadmeDocumentsEveryConfigField;
		TestEveryScalarConfigFieldHasARow + TestEveryRowKeyIsAConfigFieldOrReadOnly).
		Site 2 is not. See .claude/skills/tui-drift-sites/SKILL.md.
	EOF
	exit 2
	;;
*ui/theme/theme.go | *ui/theme/registry.go | *ui/theme/agent.go)
	cat >&2 <<-'EOF'
		A glyph table was touched. Two invariants, both guarded:

		  - every glyph must measure width 1, across each palette x glyph-set
		    (TestGlyphWidths, TestAgentGlyphWidths, TestNoteGlyphIsSingleCellEverywhere)
		  - a new glyph must reach the ? legend or be excluded with a reason
		    (TestLegendCoversRowVocabulary reflects over the live Glyphs table)

		A 2-cell glyph desyncs the alt-screen renderer into accumulating ghost rows —
		it is not a cosmetic bug. See ui/theme/panel.go's SanitizeWidth docstring.
	EOF
	exit 2
	;;
esac

exit 0
