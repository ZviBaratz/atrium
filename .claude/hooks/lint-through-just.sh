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
set -uo pipefail

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

# Match an invocation of the golangci-lint binary, by bare name or absolute path,
# at the start of the command or after a pipe/&&/;/newline. Deliberately narrow:
# it must be a real invocation, not the string appearing in an echo or a comment.
if [[ "$cmd" =~ (^|[|;&[:space:]])(/[^[:space:]]*/)?golangci-lint([[:space:]]|$) ]]; then
	# `just lint` itself shells out to golangci-lint; never block the recipe.
	if [[ "$cmd" =~ (^|[|;&[:space:]])just([[:space:]]+[^|;&]*)?[[:space:]]lint([[:space:]]|$) ]]; then
		exit 0
	fi
	cat >&2 <<-'EOF'
		Blocked: run golangci-lint through `just lint`, never directly (#486).

		The recipe keys GOLANGCI_LINT_CACHE to this worktree. The global cache a bare
		run uses reports stale findings from a sibling worktree — failing correct code,
		or passing broken code.

		  just lint            # whole module
		  just lint ./ui/...   # scoped: pass packages to the recipe
	EOF
	exit 2
fi

exit 0
