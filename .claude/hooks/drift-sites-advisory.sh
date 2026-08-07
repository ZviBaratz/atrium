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
		keys/registry.go touched — a keybinding has 9 sites, all but one guarded.

		  1  keys/keys.go            KeyName const + doc comment
		  2  keys/registry.go        the Entry                      <- you are here
		  2b keys/registry.go        the Entry's Action             <- and here
		  3  keys/help_layout.go     a HelpRow (or a Mentions)
		  4  app/app_update.go       case keys.KeyX: in dispatchAction
		  5  keys/registry_test.go   TestActionVocabulary_Golden
		  6  README.md               `#### Keybindings` AND `##### Action names`
		  7  app/palette_gates.go    a paletteGates entry, for any dispatchable action
		  8  app/app_update.go       keyAllowedWhileBusy, if it must work mid-action

		Site 4 used to be the hole — a key could ship registered, documented,
		README'd and dead. TestEveryRegistryActionHasADispatchCase closes it by
		reading dispatchAction's case labels out of the source.

		It proves the case exists, not that it does the right thing. Still press the
		key, or drive it through handleKeyPress in a test.

		Site 2b is the name a user's config.json binds to: add to the vocabulary,
		never rename it. Site 6 is two tables now — the keybindings one and the
		action-names one, which is a two-column split, so inserting a row reflows it.

		Site 7 fails in a file you were not editing: the palette reaches every action,
		so an un-gated one defaults into "always fine". Keep the gate no stricter than
		the handler, and check the preconditions in the handler's order.

		And do not spell a global key in prose — read it with keys.LabelOf, or
		TestNoProseNamesAKeyLiterally fails. An overlay's own keys are exempt.
		See .claude/skills/tui-drift-sites/SKILL.md.
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

		A 2-cell glyph breaks the column math the list and panel layout depend on —
		that is what TestGlyphWidths says it guards, and it is not cosmetic.

		Do not confuse it with the adjacent hazard: a *measured* width diverging from
		the *rendered* one wraps the line and desyncs bubbletea's alt-screen renderer
		into accumulating ghost rows. That is SanitizeWidth's problem
		(ui/theme/panel.go), and it is about untrusted captured pane content, not
		about this curated table.
	EOF
	exit 2
	;;
esac

exit 0
