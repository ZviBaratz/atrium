#!/usr/bin/env bash
#
# Measure what Atrium's per-session pty attach fanout actually costs (issue #800).
#
# WHY THIS EXISTS
#
# Atrium holds one `tmux attach-session` pty client per started session, for that
# session's whole life (session/tmux/tmux.go, Session.Restore, called from Start). At
# 30 sessions that is 30 client processes, 30 pty masters and 30 reaper goroutines
# standing permanently. #548 filed that shape as "bounded today, unbounded in N" and
# was explicit that it must be MEASURED before anyone optimises it — that the honest
# outcome might be wontfix, and that the data is the deliverable either way.
#
# WHY THE MEASUREMENT IS A DIFFERENCE, NOT A READING
#
# A fleet always has its clients, so pricing one tells you what N sessions cost — not
# what their clients cost. The harness therefore builds the fleet, prices it, drops
# every attach client, and prices the same fleet again. The with-minus-without
# difference is the answer. It also asserts that the control actually dropped them:
# a control that quietly failed would report the fanout as free, which is the
# conclusion this whole exercise is most at risk of reaching by accident.
#
# WHAT IT COSTS TO RUN
#
# Real tmux sessions and real wall-clock sitting still: roughly
# (2 modes x 2 arms x window) per fleet size, plus setup. With the defaults — sizes
# 1,5,15 and a 10s window — expect about three minutes. That is why the harness is
# opt-in and `just ci` skips it.
#
# USAGE
#
#   scripts/measure-fanout.sh                 # sizes 1,5,15, 10s window
#   scripts/measure-fanout.sh 1,10,30 20      # explicit sizes and window seconds
#
# Isolation: the test package's TestMain sandboxes HOME and TMUX_TMPDIR, so the fleet
# is built on a private tmux socket and cannot touch a running Atrium. Do not
# hand-set TMUX_TMPDIR around this — that opts out of the teardown that reaps what
# the run leaves behind.

set -euo pipefail

SIZES=${1:-}
WINDOW=${2:-}

GO=${GO:-go}
if ! command -v "$GO" >/dev/null 2>&1; then
  echo "no '$GO' on PATH; set GO=/path/to/go" >&2
  exit 1
fi
if ! command -v tmux >/dev/null 2>&1; then
  echo "no tmux on PATH; the measurement builds real sessions and cannot run without it" >&2
  exit 1
fi
if [[ "$(uname -s)" != "Linux" ]]; then
  echo "the measurement reads /proc, which only Linux has; see proccost_other.go" >&2
  exit 1
fi

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$ROOT"

echo "############ ENVIRONMENT"
echo "  tmux:   $(tmux -V)"
echo "  go:     $($GO version)"
echo "  kernel: $(uname -sr)"
echo "  cpus:   $(nproc)"
echo "  commit: $(git rev-parse --short HEAD 2>/dev/null || echo unknown)"
echo "  sizes:  ${SIZES:-1,5,15 (default)}"
echo "  window: ${WINDOW:-10 (default)}s"
echo

# The gate is exported here rather than baked into the test so the harness stays
# skipped for everyone who did not ask for it. ATRIUM_CI_REQUIRE_TMUX turns a missing
# tmux into a failure instead of a skip: past this point a skip is an answer of
# nothing to someone who explicitly asked.
export ATRIUM_MEASURE_FANOUT=1
export ATRIUM_CI_REQUIRE_TMUX=1
# `if` rather than `[[ ... ]] && export ...`: under `set -e` a bare AND-list whose
# test fails exits the script, so the no-argument default run would die here.
if [[ -n "$SIZES" ]]; then
  export ATRIUM_MEASURE_FANOUT_SIZES="$SIZES"
fi
if [[ -n "$WINDOW" ]]; then
  export ATRIUM_MEASURE_FANOUT_WINDOW="$WINDOW"
fi

# -count=1 defeats the test cache: this reads the machine, not the code, so a cached
# PASS would report a fleet that is not running.
"$GO" test ./session/tmux/ -run TestAttachFanoutCost -count=1 -v -timeout 30m
