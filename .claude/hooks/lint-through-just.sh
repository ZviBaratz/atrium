#!/usr/bin/env bash
# PreToolUse/Bash hook: refuse a bare `golangci-lint run`.
#
# Why (#486): `just lint` keys GOLANGCI_LINT_CACHE to *this* worktree. A bare
# `golangci-lint run` uses the global cache, which reports stale findings from a
# sibling worktree — so it fails on code that is fine, or passes code that is not.
# The recipe takes package args: `just lint ./ui/...`.
#
# Contract: exit 0 to allow, exit 2 to block with stderr shown to the agent.
# Any other exit is a non-blocking error, which is why this script must not use
# `set -e` — a stray failure would silently degrade to "allow".
#
# `-f` because this script only inspects command words: leaving globbing on would
# expand a stray `*` against the cwd while splitting, for no benefit.
#
# Every case below is pinned by TestLintThroughJustHook in claude_hooks_test.go.
set -fuo pipefail

payload="$(cat)"

# Prefer jq; fall back to python3 so the hook still works without jq installed.
# A hook that dies because a tool is missing fails open and silently.
if command -v jq >/dev/null 2>&1; then
	cmd="$(printf '%s' "$payload" | jq -r '.tool_input.command // ""' 2>/dev/null)"
else
	cmd="$(printf '%s' "$payload" | python3 -c \
		'import json,sys;print(json.load(sys.stdin).get("tool_input",{}).get("command",""))' 2>/dev/null)"
fi

[[ -z "$cmd" ]] && exit 0

# Subcommands that read no cache at all, so #486's worktree keying cannot apply.
safe_subcommand() {
	case "${1-}" in
	"" | version | help | linters | -h | --help | --version) return 0 ;;
	*) return 1 ;;
	esac
}

# Decide whether one pipeline/list segment *invokes* golangci-lint.
#
# Only a segment's command word can be an invocation. Matching the bare string
# anywhere cannot tell an invocation from a mention, so it also blocks `which
# golangci-lint`, `command -v golangci-lint`, `grep -r golangci-lint .`, `echo
# golangci-lint` and `# golangci-lint` — the first two being exactly the PATH
# diagnostics CLAUDE.md tells you to reach for. Comparing the command word's
# basename covers the path forms in one stroke, including the relative ones
# (`./golangci-lint`, `bin/golangci-lint`, `~/go/bin/golangci-lint`) that a
# leading-slash pattern misses even though they hit the same global cache.
segment_invokes_golangci() {
	local seg="$1" tok word="" args=()
	# Grouping and substitution punctuation precedes the command word without
	# being it: `(cd x && golangci-lint run)`, `$(golangci-lint run)`.
	seg="${seg//[\$\(\)\{\}]/ }"
	for tok in $seg; do
		if [[ -z "$word" ]]; then
			# Leading VAR=value assignments also precede the command word.
			[[ "$tok" == *=* ]] && continue
			word="$tok"
			continue
		fi
		args+=("$tok")
	done
	[[ "${word##*/}" == golangci-lint ]] || return 1
	safe_subcommand "${args[0]-}" && return 1
	return 0
}

# Split on every operator that begins a new command, so a bare run chained onto
# an allowed one is still judged on its own — `just lint && golangci-lint run`
# must block on the second segment. Longest operators first: once `&&` is a
# newline there is no `&&` left for the `&` pass to corrupt.
segments="${cmd//&&/$'\n'}"
segments="${segments//||/$'\n'}"
segments="${segments//|/$'\n'}"
segments="${segments//;/$'\n'}"
segments="${segments//&/$'\n'}"

# A here-string redirect keeps the loop in this shell, so `exit 2` really exits.
while IFS= read -r segment; do
	segment_invokes_golangci "$segment" || continue
	cat >&2 <<-'EOF'
		Blocked: run golangci-lint through `just lint`, never directly (#486).

		The recipe keys GOLANGCI_LINT_CACHE to this worktree. The global cache a bare
		run uses reports stale findings from a sibling worktree — failing correct code,
		or passing broken code.

		  just lint            # whole module
		  just lint ./ui/...   # scoped: pass packages to the recipe
	EOF
	exit 2
done <<<"$segments"

exit 0
