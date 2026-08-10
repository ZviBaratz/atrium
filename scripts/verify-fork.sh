#!/usr/bin/env bash
#
# Prove that fork-from-checkpoint still truncates, against live claude (issue #644).
#
# WHY THIS EXISTS
#
# `--resume-session-at` is the flag that cuts a resumed conversation at a checkpoint,
# and outside print mode claude ACCEPTS IT AND IGNORES IT. No error, no warning: the
# process starts, the agent answers, the row goes Ready, and the only thing wrong is
# that the session was seeded from the end of the conversation instead of the chosen
# checkpoint. Every signal except the transcript's own contents reports success.
#
# So no amount of hermetic testing can retire this check. session/fork_test.go stubs
# the transcript reader and proves verifyFork's LOGIC separates a truncated fork from
# an untruncated one; it cannot prove claude still behaves the way that logic assumes.
# Only a driven fork can, and only for one version at a time — which is why this
# prints the version it ran against and why the answer expires when claude updates.
#
# The flag is `.hideHelp()` in the bundle, so `claude --help` will not confirm it
# exists and a `--help` probe cannot be used as a cheaper substitute.
#
# WHAT IT DRIVES
#
# Atrium's own code, not a hand-written claude command line. The checkpoint comes
# from transcript.LoadCheckpoints, the argv from forkArgv, the environment from
# forkEnv, the destination from transcript.ForkPath, and the verdict from verifyFork.
# A re-implementation here would test this file instead of the feature.
#
# The one exception is the CONTROL, which has to build an argv Atrium will not
# produce: the same fork with `--resume-session-at` removed. That arm is the point of
# the whole exercise. Without it, "the fork worked" and "the verifier does nothing"
# are the same observation — a verifyFork mutated to `return nil` passes every other
# arm here.
#
# ─────────────────────────────────────────────────────────────────────────────────
# THE TWO SAFETY RULES THIS FILE ENCODES
#
# 1. SANDBOX THE CONFIG DIR, NOT $HOME. claude keeps credentials in
#    $CLAUDE_CONFIG_DIR/.credentials.json, so HOME=<sandbox> with CLAUDE_CONFIG_DIR
#    unset leaves every call returning "Not logged in" — and a probe of pure failures
#    scores as a conversation if it greps for a marker that also appears in the
#    prompt. This copies .claude.json and SYMLINKS .credentials.json, so a token
#    refresh writes through to the real file instead of stranding a stale copy.
#
# 2. NEVER `tmux -L`. tmux resolves `-L <name>` against TMUX_TMPDIR and reads an
#    EMPTY or MISSING TMUX_TMPDIR as /tmp, where Atrium's live socket lives. A
#    kill-server on that name destroys every running agent on the machine (#547,
#    #581, #584). This file sends no tmux commands at all; the one arm that needs a
#    server goes through internal/testutil, which addresses sockets by absolute path
#    and hard-fails when TMUX_TMPDIR is not its own sandbox root.
# ─────────────────────────────────────────────────────────────────────────────────
#
# WHAT IT COSTS
#
# Real API turns on the account whose credentials it borrows: three to build the
# source conversation, then one per fork arm. It appends nothing to your real
# conversation history — every transcript it writes lives in a temp config dir that
# is removed on exit — but the turns are billed.
#
# USAGE
#
#   scripts/verify-fork.sh                 # build a source, run every arm
#   scripts/verify-fork.sh -k              # keep the sandbox for inspection
#   scripts/verify-fork.sh -n 5            # a longer source conversation
#
# A green run is evidence for ONE claude version. Record it on the PR with the
# version string this prints.

set -uo pipefail

KEEP=0
TURNS=3
while getopts ":kn:h" opt; do
  case "$opt" in
    k) KEEP=1 ;;
    n) TURNS="$OPTARG" ;;
    h) awk 'NR>1 && !/^#/ {exit} NR>1' "$0"; exit 0 ;;
    *) echo "unknown option: -$OPTARG (try -h)" >&2; exit 2 ;;
  esac
done

# A non-numeric -n would only surface as an arithmetic error two screens later,
# after the sandbox had been built.
if ! [[ "$TURNS" =~ ^[0-9]+$ ]] || ((TURNS < 2)); then
  echo "FATAL: -n takes a number >= 2 (a one-turn conversation has no turn to drop); got '$TURNS'" >&2
  exit 2
fi

# Repo root, so the script works from anywhere and `go test` finds the module.
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT" || exit 1

GO="${GO:-go}"
command -v "$GO" >/dev/null 2>&1 || { echo "FATAL: no '$GO' on PATH (set GO=/path/to/go)"; exit 1; }

CLAUDE_BIN="$(command -v claude || true)"
[[ -x "$CLAUDE_BIN" ]] || { echo "FATAL: no claude on PATH"; exit 1; }
VERSION="$("$CLAUDE_BIN" --version 2>&1)"

REAL_CONFIG="${CLAUDE_CONFIG_DIR:-$HOME/.claude}"
[[ -r "$REAL_CONFIG/.credentials.json" ]] || {
  echo "FATAL: no credentials at $REAL_CONFIG/.credentials.json"
  echo "       claude keeps them in the CONFIG dir, not \$HOME — set CLAUDE_CONFIG_DIR if yours is elsewhere."
  exit 1
}

echo "=== verify-fork — $(date -Is) ==="
echo "claude:  $CLAUDE_BIN ($VERSION)"
echo "config:  $REAL_CONFIG (credentials symlinked, never copied)"
echo "repo:    $ROOT"
echo

# A short run root: the tmux arm binds a socket under it, and a socket path must fit
# sockaddr_un's sun_path (108 bytes on linux). A path under the repo worktree would
# not, and internal/testutil would refuse it.
RUN_ROOT="$(mktemp -d /tmp/atr-fork.XXXXXX)"
CFG="$RUN_ROOT/config"
SRC="$RUN_ROOT/src"
mkdir -p "$CFG" "$SRC"

# shellcheck disable=SC2329  # invoked by the EXIT/INT/TERM trap installed below
cleanup() {
  # Unlink the SYMLINK first and by name. rm -rf on the tree would follow nothing —
  # rm does not traverse symlinks — but removing it explicitly makes the ordering a
  # property of this file rather than of rm's semantics.
  rm -f "$CFG/.credentials.json"
  if [[ "$KEEP" == "1" ]]; then
    echo "--- kept: $RUN_ROOT (credentials symlink removed)"
    return
  fi
  # Belt and braces before an rm -rf, mirroring internal/testutil's prefix refusal:
  # a variable that lost its value must not turn this into `rm -rf /`.
  case "$RUN_ROOT" in
    /tmp/atr-fork.*) rm -rf "$RUN_ROOT" ;;
    *) echo "REFUSING to remove $RUN_ROOT — not under /tmp/atr-fork.*" >&2 ;;
  esac
}
trap cleanup EXIT INT TERM

cp "$REAL_CONFIG/.claude.json" "$CFG/.claude.json" 2>/dev/null || {
  echo "FATAL: could not copy $REAL_CONFIG/.claude.json"; exit 1; }
ln -s "$REAL_CONFIG/.credentials.json" "$CFG/.credentials.json" || {
  echo "FATAL: could not symlink credentials"; exit 1; }

########################################################################
echo "--- building a $TURNS-turn source conversation in $SRC"
########################################################################
# Each turn is its own checkpoint, which is what gives the fork something to cut at.
# The prompts are one-word replies so the source costs as little as it can while
# still being a real conversation with real file-history records.
SESSION_ID="$(cat /proc/sys/kernel/random/uuid 2>/dev/null || uuidgen)"
WORDS=(ALPHA BRAVO CHARLIE DELTA ECHO FOXTROT GOLF HOTEL INDIA JULIETT)
for ((i = 0; i < TURNS; i++)); do
  word="${WORDS[$((i % ${#WORDS[@]}))]}"
  if ((i == 0)); then
    args=(--session-id "$SESSION_ID")
  else
    args=(--resume "$SESSION_ID")
  fi
  if ! (cd "$SRC" && CLAUDE_CONFIG_DIR="$CFG" "$CLAUDE_BIN" -p "Reply with exactly one word: $word" "${args[@]}") >"$RUN_ROOT/turn$i.out" 2>"$RUN_ROOT/turn$i.err"; then
    echo "FATAL: turn $i failed:"; sed 's/^/    /' "$RUN_ROOT/turn$i.err"; exit 1
  fi
  printf '    turn %d: %s -> %s\n' "$((i + 1))" "$word" "$(tr -d '\n' <"$RUN_ROOT/turn$i.out")"
done

TRANSCRIPT="$(find "$CFG/projects" -name "$SESSION_ID.jsonl" 2>/dev/null | head -1)"
[[ -f "$TRANSCRIPT" ]] || { echo "FATAL: the source transcript never materialized"; exit 1; }

# A conversation of pure failures leaves a transcript too. Assert on assistant rows
# specifically — a marker also appears in the recorded USER message, which is how an
# earlier probe scored five failed runs as a pass.
ASSISTANTS=$(grep -c '"type":"assistant"' "$TRANSCRIPT" || true)
if [[ "$ASSISTANTS" -lt "$TURNS" ]]; then
  echo "FATAL: $ASSISTANTS assistant turns in the source, want $TURNS — the arms below would be vacuous"
  exit 1
fi
echo "    transcript: $TRANSCRIPT ($ASSISTANTS assistant turns)"
echo

########################################################################
echo "--- driving Atrium's fork path"
########################################################################
# -count=1 defeats the test cache: a cached PASS from a previous claude version is
# exactly the stale answer this harness exists to refuse.
set +e
ATRIUM_LIVE_FORK=1 \
ATRIUM_LIVE_FORK_CONFIG="$CFG" \
ATRIUM_LIVE_FORK_SRC="$SRC" \
  "$GO" test ./session/ -run TestLiveFork -count=1 -v -timeout 20m 2>&1 | tee "$RUN_ROOT/go.log"
STATUS=${PIPESTATUS[0]}
set -e

echo
echo "############ VERDICT"
if [[ "$STATUS" -eq 0 ]]; then
  if grep -q -- "--- SKIP" "$RUN_ROOT/go.log"; then
    echo "PARTIAL — some arms skipped (tmux missing?). Skipped arms prove nothing:"
    grep -- "--- SKIP" "$RUN_ROOT/go.log" | sed 's/^/    /'
  fi
  echo "PASS on $VERSION"
  echo
  echo "What that buys: a real fork truncated, AND the verifier refused a real"
  echo "untruncated one. The second is what makes the first evidence — without it,"
  echo "a verifier that checked nothing would look identical."
else
  echo "FAIL on $VERSION — see the arm names above."
  echo
  echo "If the failure is 'the fork kept the turn it was meant to drop', claude has"
  echo "stopped honouring --resume-session-at in print mode. That is the feature"
  echo "breaking, not the harness: re-read the bundle before changing any assertion."
fi
exit "$STATUS"
