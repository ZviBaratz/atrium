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
# WHY IT BORROWS A REAL CONVERSATION INSTEAD OF BUILDING ONE
#
# A checkpoint IS a file-history snapshot, and `claude -p` does not write them. The
# first version of this harness built its own three-turn source with print mode and
# got a transcript with three assistant turns and ZERO checkpoints — nothing to fork
# from. (Which of two differences causes that is untested: the run was print mode AND
# in a plain non-git temp dir. Every snapshot-bearing transcript in this machine's
# corpus is an interactive session in a git repo. The harness does not need to know
# which factor it is.)
#
# So the source is one of your existing conversations, copied — never moved, never
# written to — into a throwaway config dir. That is also the better fixture: a real
# transcript brings real bookkeeping rows, real sidechains and real attachments,
# which is exactly the shape the ForkAtID walk-back has to survive.
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
# THE THREE SAFETY RULES THIS FILE ENCODES
#
# 1. SANDBOX THE CONFIG DIR, NOT $HOME. claude keeps credentials in
#    $CLAUDE_CONFIG_DIR/.credentials.json, so HOME=<sandbox> with CLAUDE_CONFIG_DIR
#    unset leaves every call returning "Not logged in" — and a run of pure failures
#    scores as a conversation if it greps for a marker that also appears in the
#    prompt. This copies .claude.json and SYMLINKS .credentials.json, so a token
#    refresh writes through to the real file instead of stranding a stale copy.
#
# 2. DENY THE MUTATING TOOLS. The fork replays a real conversation, which may have
#    been in the middle of editing a real repository, and the resumed context carries
#    absolute paths. The prompt asks for one word and the model will almost certainly
#    just answer — but "almost certainly" is not what you want standing between a
#    verification run and your working tree, so the sandbox writes a settings.json
#    that denies Write/Edit/Bash outright.
#
# 3. NEVER `tmux -L`. tmux resolves `-L <name>` against TMUX_TMPDIR and reads an
#    EMPTY or MISSING TMUX_TMPDIR as /tmp, where Atrium's live socket lives. A
#    kill-server on that name destroys every running agent on the machine (#547,
#    #581, #584). This file sends no tmux commands at all; the one arm that needs a
#    server goes through internal/testutil, which addresses sockets by absolute path
#    and hard-fails when TMUX_TMPDIR is not its own sandbox root.
# ─────────────────────────────────────────────────────────────────────────────────
#
# WHAT IT COSTS
#
# Three forks, each replaying the source conversation as context. That is why it
# picks the SMALLEST qualifying transcript rather than the newest, and prints the
# size before spending anything. Nothing is appended to your real history: every
# transcript written lives in a temp config dir removed on exit.
#
# USAGE
#
#   scripts/verify-fork.sh                 # smallest conversation with >= 2 checkpoints
#   scripts/verify-fork.sh -s <transcript> # fork a specific one
#   scripts/verify-fork.sh -m 300000       # raise the source size cap (bytes)
#   scripts/verify-fork.sh -k              # keep the sandbox for inspection
#
# A green run is evidence for ONE claude version. Record it on the PR with the
# version string this prints.

set -uo pipefail

KEEP=0
MAX_BYTES=150000
SOURCE=""
while getopts ":ks:m:h" opt; do
  case "$opt" in
    k) KEEP=1 ;;
    s) SOURCE="$OPTARG" ;;
    m) MAX_BYTES="$OPTARG" ;;
    h) awk 'NR>1 && !/^#/ {exit} NR>1' "$0"; exit 0 ;;
    *) echo "unknown option: -$OPTARG (try -h)" >&2; exit 2 ;;
  esac
done

if ! [[ "$MAX_BYTES" =~ ^[0-9]+$ ]]; then
  echo "FATAL: -m takes a byte count; got '$MAX_BYTES'" >&2
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

########################################################################
# Choose the source conversation.
########################################################################
# Smallest, not newest: every fork arm replays the whole thing as context, so size is
# the bill. Two checkpoints is the floor — one gives the newest checkpoint nothing to
# be forked *before*, which is the ForkAtID every arm needs.
if [[ -z "$SOURCE" ]]; then
  echo "--- picking the smallest conversation with >= 2 checkpoints (cap ${MAX_BYTES}B)"
  SOURCE="$(
    for f in "$REAL_CONFIG"/projects/*/*.jsonl; do
      [[ -f "$f" ]] || continue
      size=$(stat -c%s "$f" 2>/dev/null) || continue
      ((size <= MAX_BYTES)) || continue
      snaps=$(grep -c 'file-history-snapshot' "$f" 2>/dev/null)
      ((${snaps:-0} >= 2)) || continue
      printf '%d\t%s\n' "$size" "$f"
    done | sort -n | head -1 | cut -f2
  )"
fi
[[ -n "$SOURCE" && -f "$SOURCE" ]] || {
  echo "FATAL: no conversation with >= 2 checkpoints under ${MAX_BYTES}B in $REAL_CONFIG/projects"
  echo "       Raise the cap with -m, or name one with -s. A checkpoint is a"
  echo "       file-history snapshot, and only interactive sessions write them."
  exit 1
}

SRC_SNAPS=$(grep -c 'file-history-snapshot' "$SOURCE" 2>/dev/null)
SRC_SIZE=$(stat -c%s "$SOURCE")
echo "    source: $SOURCE"
echo "            ${SRC_SIZE}B, $SRC_SNAPS checkpoints — replayed once per fork arm (3 arms)"
echo

########################################################################
# Build the sandbox.
########################################################################
# A short run root: the tmux arm binds a socket under it, and a socket path must fit
# sockaddr_un's sun_path (108 bytes on linux). A path under the repo worktree would
# not, and internal/testutil would refuse it.
RUN_ROOT="$(mktemp -d /tmp/atr-fork.XXXXXX)"
CFG="$RUN_ROOT/config"
SRC="$RUN_ROOT/src"
mkdir -p "$CFG" "$SRC"

# shellcheck disable=SC2329  # invoked by the EXIT/INT/TERM trap installed below
cleanup() {
  # Unlink the SYMLINK first and by name. rm -rf does not traverse symlinks, so this
  # is not load-bearing — but it makes the ordering a property of this file rather
  # than of rm's semantics, which is the kind of thing that stops being true quietly.
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

# Safety rule 2. The fork resumes a conversation that may have been mid-edit in a
# real repository, and its context carries absolute paths — so the sandbox refuses
# the tools that could act on them. The forked conversation only has to answer.
cat >"$CFG/settings.json" <<'JSON'
{
  "permissions": {
    "deny": ["Write", "Edit", "MultiEdit", "NotebookEdit", "Bash", "WebFetch"]
  }
}
JSON

# The reader resolves a transcript as <root>/projects/<sanitized cwd>/<id>.jsonl, and
# sanitizeCWD maps every non-alphanumeric rune to '-'. Reproduced here rather than
# imported because this is the one place the harness has to speak the reader's filing
# convention; session/transcript/claude.go is the definition.
SRC_PROJECT="$CFG/projects/$(printf '%s' "$SRC" | sed 's/[^a-zA-Z0-9]/-/g')"
mkdir -p "$SRC_PROJECT"
cp "$SOURCE" "$SRC_PROJECT/$(basename "$SOURCE")" || {
  echo "FATAL: could not copy the source transcript into the sandbox"; exit 1; }

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
  echo "  source: $(basename "$SOURCE") ($SRC_SNAPS checkpoints)"
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
