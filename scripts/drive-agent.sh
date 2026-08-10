#!/usr/bin/env bash
#
# Drive a real agent CLI at a width ladder and capture its panes (issue #647).
#
# WHY THIS EXISTS
#
# Re-driving a CLI is the only thing that detects heuristic rot in session/agent/.
# registry.go's own header says VerifiedVersion is "a RECORD of what was last driven
# against a live pane, not a tripwire", and GranularityMinor truncates agy 1.1.5 and
# 1.1.11 to the same 1.1 — so the pin stayed silent across six patch releases that
# #512 had to drive by hand. The cost of re-driving IS the cost of drift detection,
# and #512 paid it in throwaway bash that did not survive the session. This is that
# bash, committed.
#
# It removes the MECHANICAL cost of getting panes on disk. It does not remove the
# judgement — which literal to key on is a human call, and #512 got it wrong twice
# from wide captures alone. See "THE JUDGEMENT" in `help`.
#
# ─────────────────────────────────────────────────────────────────────────────────
# THE SAFETY RULE THIS FILE ENCODES ONCE
#
# tmux resolves `-L <name>` against TMUX_TMPDIR, and reads an EMPTY or MISSING
# TMUX_TMPDIR as /tmp. Atrium's live socket is /tmp/tmux-<uid>/atrium. So
# `tmux -L atrium kill-server` in a teardown whose environment has gone stale kills
# every running agent on the machine. That is not hypothetical: it has already cost
# this project real sessions (#547, #581, #584).
#
# Therefore every tmux call here goes through t()/tmux_boot(), which address the
# server by ABSOLUTE SOCKET PATH (`tmux -S`). A path cannot resolve anywhere but
# itself. There is no `-L` in this file and there must never be one. TMUX_TMPDIR is
# exported as well, as defence in depth — not as the isolation.
#
# Every destructive operation (kill-server, rm -rf) calls assert_under_run_root
# first, mirroring tmuxSocketsUnder's prefix refusal in internal/testutil/tmux.go.
# Read that file before touching teardown here; it is the reference implementation.
#
# fleet_sessions() is the ONLY thing that names the live socket, and `list-sessions`
# is the only command it may ever send there. It addresses it BY PATH too, because
# TMUX_TMPDIR is exported by then and a `-L` lookup would resolve into this run's
# private root — making the before/after comparison a tautology that can never fire.
# It records session NAMES, and `down` diffs the sets: a count would report a fleet
# that lost one session and gained another as "unchanged".
# ─────────────────────────────────────────────────────────────────────────────────
#
# WHAT IT ISOLATES, AND WHAT IT DELIBERATELY DOES NOT
#
# Isolated: tmux (socket by path + private TMUX_TMPDIR) and the workspace (a scratch
# git repo under /tmp, never an Atrium worktree).
#
# NOT isolated: the agent's own config dir. agy authenticates through
# ~/.gemini/antigravity-cli and codex through its own; a sandboxed HOME would leave
# both signed out, and driving a LIVE, AUTHENTICATED CLI is the entire point. Three
# consequences you accept by running this:
#
#   - a capture run appends to your real conversation history;
#   - the CLI's splash renders your signed-in email, which is why `emit` redacts
#     email addresses by default;
#   - a confirmation dialog's options can include "(Persist to settings.json)", so a
#     misaimed Enter edits your real agent config. `keys` refuses to send Enter at
#     such a pane unless FORCE=1.
#
# USAGE — see `help`. The short version:
#
#   scripts/drive-agent.sh up agy
#   scripts/drive-agent.sh keys Enter                 # dismiss agy's trust screen
#   scripts/drive-agent.sh send 'run: rm -f hello.txt'
#   scripts/drive-agent.sh wait 'Do you want to proceed'
#   scripts/drive-agent.sh ladder confirm             # 120 60 40 34 28 26 24 20
#   scripts/drive-agent.sh emit agy > /tmp/fixtures.go.txt
#   scripts/drive-agent.sh down

set -euo pipefail

# The run root is short on purpose. tmux binds the socket at the literal path given
# to -S, and that path must fit sockaddr_un's sun_path (108 bytes on linux, 104 on
# darwin) or the server dies with "File name too long". $TMPDIR or a path under the
# repo would not fit reliably; /tmp/atrium-capture/<name>/tmux/sock is ~35 bytes.
#
# The name is spelled out rather than abbreviated because internal/testutil/tmux.go
# picked its own long prefix on the grounds that "this machine had /tmp/atr*" — a
# short atr* name lands in a namespace this repo has already flagged as colliding.
RUN_ROOT="${ATR_CAP_ROOT:-/tmp/atrium-capture}"

# The TMUX_TMPDIR this script INHERITED, snapshotted at the top of the file — before
# any code path can export the run's private one over it. live_socket needs it, and
# by the time live_socket is called the value in the environment is this run's.
HOST_TMUX_TMPDIR="${TMUX_TMPDIR:-}"

# The tmux session name inside the capture server. Note that nothing targets tmux
# objects BY THIS NAME: see resolve_ids.
SESSION=cap

# The ladder #512 needed. 24 is where agy's shipped trust literal died and 20 is the
# confirmation matcher's floor, so the narrow rungs are the load-bearing ones — a
# 120-only capture ships literals that are dead at widths Atrium actually renders.
DEFAULT_WIDTHS=(120 60 40 34 28 26 24 20)

DEFAULT_WIDTH=120
DEFAULT_HEIGHT=40

# The tmux floor. This is a SECOND COPY of session/tmux/version.go's MinVersion — a
# shell script cannot read a Go const — so drive_agent_drift_test.go asserts the two
# agree and fails when either moves. Without that test this line would silently keep
# the old value forever, which is the drift class CLAUDE.md warns nothing else here
# can see.
#
# 3.2 is also exactly what t()'s global -N needs: "Add -N flag to never start server
# even if command would normally do so" is in tmux's CHANGES FROM 3.1c TO 3.2 block.
# So the floor Atrium requires and the floor this script requires coincide today. If
# MinVersion ever drops below 3.2, -N is what breaks first.
MIN_TMUX=3.2

# How long to let a CLI repaint after a resize or a keystroke. A full-screen TUI
# redraws on SIGWINCH asynchronously, so capturing immediately catches a half-drawn
# frame. Raise it if a capture looks torn.
SETTLE="${SETTLE:-1.5}"

die() { printf 'drive-agent: %s\n' "$*" >&2; exit 1; }
note() { printf 'drive-agent: %s\n' "$*" >&2; }

# ── the live fleet ───────────────────────────────────────────────────────────────

# live_socket prints the path of the socket a running Atrium would be on, with the
# name derived the way config.RuntimeName() does it: prefer the new name, fall back
# to the legacy one, never move.
#
# The DIRECTORY is the half that is easy to get wrong, in both directions.
#
# It cannot come from $TMUX_TMPDIR, because by the time anything calls this the run's
# private TMUX_TMPDIR is exported over it, and a live socket looked up in an empty
# private root is a fleet of zero — fleet-before and fleet-after would then agree no
# matter what happened to the real fleet, which is the tautology this check exists to
# refuse to be.
#
# But it cannot be a hardcoded /tmp either. Atrium addresses its server as
# `-L atrium` (session/tmux/command.go), which tmux resolves against whatever
# TMUX_TMPDIR the TUI was started with — and this repo's own guidance tells people to
# export one when eyeballing the TUI. So take the value this SCRIPT inherited,
# snapshotted before any export, and apply tmux's own rule for it: empty, or naming
# something that is not a directory, means /tmp. That mirrors tmux.SocketDir
# (session/tmux/orphan.go), which is where Atrium answers the same question.
#
# Caveat worth knowing: tmux names the directory from getuid(), while `id -u` is the
# effective uid. They differ under sudo, and nothing here should ever be run under
# sudo.
live_socket() {
	local name=atrium
	if [[ ! -d "$HOME/.atrium" && -d "$HOME/.claude-squad" ]]; then
		name=claudesquad
	fi
	local root="$HOST_TMUX_TMPDIR"
	[[ -n "$root" && -d "$root" ]] || root=/tmp
	printf '%s/tmux-%s/%s' "$root" "$(id -u)" "$name"
}

# fleet_sessions prints the NAMES of the live Atrium server's sessions, sorted. This
# is the ONE function that names the live socket, and `list-sessions` is the ONLY
# command it may ever send there. Do not add a second live-socket caller; do not let
# this one grow a command that mutates.
#
# Names rather than a count, because `down` compares what was live at `up` against
# what is live now and a count cannot do that job: one session destroyed while
# another is created nets to zero and reports "unchanged ✓" — precisely the
# reassurance this check exists to refuse to give.
# LC_ALL=C is not decoration. `down` compares this output with `comm`, and a bare
# `sort` orders by LC_COLLATE — under en_US.UTF-8 that ignores punctuation at the
# primary level, so "atrium_auto-conf_x" and "atrium_atrium_x" come out in an order
# GNU comm, which validates its input byte-wise, rejects as unsorted. comm then exits
# non-zero and, under `set -e`, takes the rest of `down` with it: the run directory
# and the `current` pointer survive a teardown that reported failure. Pinning the
# byte collation on both sides is what makes the two agree.
fleet_sessions() {
	tmux -S "$(live_socket)" list-sessions -F '#{session_name}' 2>/dev/null | LC_ALL=C sort || true
}

fleet_count() { fleet_sessions | grep -c . || true; }

# ── run state ────────────────────────────────────────────────────────────────────

# Each verb is a separate process, so the run lives on disk. $RUN_ROOT/current points
# at the active run; ATR_CAP_RUN overrides it.
RUN=""
SOCK=""
REPO=""

# The fields of meta.env, which cmd_up writes and load_run sources back in. Declared
# here so the run's whole persisted state is discoverable in one place — and so a
# reader (and shellcheck) can tell these apart from a typo'd local.
PROGRAM=""  # the command line passed to `up`, verbatim
BIN=""      # its first token — the binary whose --version is the provenance
VERSION=""  # `$BIN --version`, first line
CAPTURED="" # UTC date of `up`
WIDTH=""    # the width `up` created the session at; ladder returns to it
HEIGHT=""   # the height every rung re-asserts
PANE=""     # %N — every capture and keystroke targets this, never the session name
WINDOW=""   # @N — every resize targets this
#
# The fleet snapshot is deliberately NOT a meta.env field: it is a sorted list of
# session names in $RUN/fleet-before.txt, which `down` diffs against the live set.

# assert_under_run_root refuses any path that is not UNDER $RUN_ROOT — at any depth,
# not merely a direct child of it. The depth matters: cmd_reap_all passes
# $RUN_ROOT/<run>/tmux/sock through here, so a direct-child test would refuse the one
# caller that has to sweep. What it buys is that a path cannot resolve outside the
# root, which is the property every destructive operation in this file needs.
#
# Every such operation runs this first — including on $RUN itself, which is settable
# from the environment (ATR_CAP_RUN=$HOME down would otherwise be `rm -rf "$HOME"`).
assert_under_run_root() {
	local path="$1"
	[[ -n "$path" ]] || die "refusing to operate on an empty path"
	[[ "$path" != "$RUN_ROOT" ]] || die "refusing to operate on the run root itself"
	# The run-root test above is a string comparison, so a trailing slash — which tab
	# completion hands you for free — slips past it and then satisfies the prefix test
	# below as well. "$RUN_ROOT/" naming the root itself is exactly what the previous
	# line refuses; refuse the spelling too rather than rely on a later step failing.
	case "$path" in
	*/) die "refusing to operate on a path with a trailing slash: $path" ;;
	esac
	case "$path" in
	"$RUN_ROOT"/*) ;;
	*) die "refusing to operate on a path outside $RUN_ROOT: $path" ;;
	esac
	# A path with a .. component satisfies the prefix test and still escapes.
	case "$path" in
	*/../* | */..) die "refusing to operate on a path containing '..': $path" ;;
	esac
}

# run_dir prints the run directory the verbs should act on, having proved it is one
# this script may own. It does NOT require the directory to exist — `down` has to be
# able to act on a pointer whose run is already gone.
# clear_pointer_if_ours removes $RUN_ROOT/current only when it names the run being
# torn down. `down` used to remove it unconditionally, which is indistinguishable in
# the single-run case and wrong in the only case ATR_CAP_RUN exists for: tearing down
# a pinned second run would clear the pointer the FIRST shell is steering by, and
# every verb there would then report "no active run" for a run that is still live.
clear_pointer_if_ours() {
	local dir="$1" cur
	[[ -f "$RUN_ROOT/current" ]] || return 0
	cur="$(cat "$RUN_ROOT/current")"
	if [[ "$cur" == "$dir" ]]; then
		rm -f "$RUN_ROOT/current"
	else
		note "$RUN_ROOT/current names another run ($cur) — leaving it alone"
	fi
}

# assert_sweepable refuses a run directory nested deeper than one level under the run
# root. `reap-all` globs $RUN_ROOT/*/tmux/sock, so a run one level further down is a
# server this script can create and can no longer sweep — the orphan class the whole
# file is organised around. assert_under_run_root cannot express this: it must admit
# any depth, because reap-all hands it the socket paths themselves.
assert_sweepable() {
	local dir="$1"
	case "${dir#"$RUN_ROOT"/}" in
	*/*) die "a run directory must sit directly under $RUN_ROOT, or \`reap-all\` cannot find its server: $dir" ;;
	esac
}

run_dir() {
	local dir="${ATR_CAP_RUN:-}"
	if [[ -z "$dir" && -f "$RUN_ROOT/current" ]]; then
		dir="$(cat "$RUN_ROOT/current")"
	fi
	[[ -n "$dir" ]] || die "no active run (run \`up <program>\` first, or set ATR_CAP_RUN)"
	assert_under_run_root "$dir"
	printf '%s' "$dir"
}

load_run() {
	local dir
	dir="$(run_dir)"
	# The advice here has to be advice that WORKS: `down` clears a pointer whose run
	# directory has gone rather than dying on it, which is why this can send you there.
	[[ -d "$dir" ]] || die "run directory is gone: $dir (run \`down\` to clear the pointer)"
	RUN="$dir"
	SOCK="$RUN/tmux/sock"
	REPO="$RUN/repo"
	# Belt, not the isolation: every tmux call below passes -S, which makes
	# TMUX_TMPDIR irrelevant. It is exported anyway so a command added later without
	# -S lands here rather than on /tmp. See live_socket for what this then breaks.
	export TMUX_TMPDIR="$RUN/tmux"
	# shellcheck disable=SC1091  # generated by cmd_up
	source "$RUN/meta.env"
}

# ── tmux ─────────────────────────────────────────────────────────────────────────

# tmux_boot is the only call allowed to START the capture server. It passes -f with
# THIS RUN'S generated config (write_capture_conf), never the host's ~/.tmux.conf,
# which could otherwise alter rendering — and, as #547 found, a `set -g exit-empty
# off` there is inherited and keeps a dead server alive. It is the tmux command's own
# flag and must sit BEFORE the subcommand; new-session's own -f is client flags, an
# unrelated thing.
tmux_boot() {
	tmux -f "$RUN/tmux/capture.conf" -S "$SOCK" "$@"
}

# t runs every other tmux command against the capture socket. -N means "do not start
# the server even if the command would normally do so", so a verb aimed at a run
# whose server has died fails loudly instead of silently standing up a fresh, empty
# server on the same socket and capturing its shell.
t() {
	[[ -n "$SOCK" ]] || die "internal error: t() called with no socket"
	tmux -N -f /dev/null -S "$SOCK" "$@"
}

# write_capture_conf generates the config tmux_boot hands to -f, so the capture server
# renders the way Atrium's own sessions do. Hermeticity alone (a bare -f /dev/null)
# costs FIDELITY, and fidelity is the point: a pane captured under a terminal
# production never uses pins the wrong glyphs, which is the exact class of bug #512
# was (its whole content was one byte, ">" vs "❯"). Every value below is the one
# session/tmux/atrium.conf.tmpl sets for real sessions.
#
# It is a FILE READ AT SERVER START rather than a run of `set-option` afterwards, and
# that is the mechanism production uses too (session/tmux renders atrium.conf.tmpl and
# passes it). The difference is measurable, not stylistic: some options are consumed
# when a pane is created and a later global set never reaches the pane that already
# exists. On tmux 3.6, `set-option -g history-limit 10000` after new-session leaves
# the running pane reporting 2000; the same line in the -f config gives it 10000.
# `default-terminal` is read at pane creation the same way — it happens to be a no-op
# on 3.6, whose built-in default is already tmux-256color, but that is luck and not
# something to rely on down at the 3.2 floor, where TERM decides which glyphs the CLI
# draws at all.
#
# `status off` is not cosmetic, but note which way the row goes. Atrium's DEFAULT is
# the context bar ON (config.GetSessionContextBar defaults true, accessors.go:251),
# and atrium.conf.tmpl then sets `status on` — so a default production session's pane
# is its window height MINUS ONE. Off here, the pane is the full window height. The
# rig gives up that row of fidelity on purpose: the bar's content is theme- and
# session-dependent, so leaving it on would put a varying line in every capture and
# make two runs of the same screen differ. The consequence to hold onto is the
# off-by-one — a rig `-y 40` is a default production `-y 41`. It is small because the
# window-based matchers count NON-EMPTY LINES from the bottom of the captured text
# (chrome.go's liveChromeLines, WindowPrompt), not window rows, and the status line
# is not in capture-pane output either way.
#
# `automatic-rename off` matters for a second reason — see resolve_ids.
write_capture_conf() {
	cat >"$RUN/tmux/capture.conf" <<-'EOF'
		set-option -g  default-terminal "tmux-256color"
		set-option -ga terminal-overrides ",*:Tc"
		set-option -g  status off
		set-option -g  pane-border-status off
		set-option -g  allow-rename off
		set-option -g  automatic-rename off
		set-window-option -g automatic-rename off
		set-option -g  history-limit 10000
		set-option -sg escape-time 10
		set-option -g  base-index 0
		set-window-option -g pane-base-index 0
		set-option -g  destroy-unattached off
		set-option -g  remain-on-exit off
	EOF
}

# assert_render_conf_applied proves the config was actually read. A -f pointing at an
# unreadable or mistyped file does not fail the boot — tmux carries on with its own
# defaults — so the whole fidelity argument above would be silently untrue. Check one
# value that differs from tmux's built-in default and would change what gets captured.
assert_render_conf_applied() {
	local limit status_
	limit="$(t display-message -p -t "$SESSION" '#{history_limit}')"
	status_="$(t show-option -gv status)"
	[[ "$limit" == 10000 && "$status_" == off ]] ||
		die "the capture config did not take effect (history-limit=$limit status=$status_) — captures would not match production"
}

# resolve_ids records the pane and window IDS of the capture session, and everything
# afterwards targets those rather than the session name.
#
# session/tmux/pane.go exists solely to argue this point for production: tmux
# resolves a session-name target to the ACTIVE pane of the session's current window,
# so any extra pane or window silently redirects every capture and keystroke —
# "misdirect either side and the wrong pane gets read, or worse, typed into." A rig
# that drives an agent into `rm -f` confirmation dialogs is not the place to relax
# it. Window targets are worse still: `-t cap` for a window-scoped command is parsed
# as a target-WINDOW (form `session:window`), whose lookup tries name prefixes and
# globs before anything else, so it is not specified to mean "the session cap" at
# all. An immutable @N/%N cannot be misread.
resolve_ids() {
	PANE="$(t display-message -p -t "$SESSION" '#{pane_id}')"
	WINDOW="$(t display-message -p -t "$SESSION" '#{window_id}')"
	[[ "$PANE" == %* && "$WINDOW" == @* ]] || die "could not resolve pane/window ids (got pane=$PANE window=$WINDOW)"
}

require_live() {
	t has-session -t "$SESSION" 2>/dev/null ||
		die "no live capture session on $SOCK (did the CLI exit? try \`status\`, then \`down\`)"
}

# reap kills a capture server and confirms it is gone. It addresses the server by
# absolute socket path and refuses any path outside $RUN_ROOT. The `|| true` is
# load-bearing under set -e: kill-server exits non-zero for a socket whose server has
# already gone, which is the normal case here.
reap() {
	local sock="$1"
	assert_under_run_root "$sock"
	[[ -S "$sock" ]] || return 0
	tmux -N -S "$sock" kill-server >/dev/null 2>&1 || true
	# kill-server returns on acknowledgement, not on socket close, and its exit
	# status cannot tell "already gone" from "the kill did not land". Ask the socket.
	local _i
	for _i in $(seq 1 50); do
		if ! tmux -N -S "$sock" has-session >/dev/null 2>&1; then
			# tmux never unlinks a socket when its server dies.
			rm -f "$sock"
			return 0
		fi
		sleep 0.1
	done
	die "a tmux server is still listening on $sock after kill-server. Leaving it and its directory in place — unlinking a live server's socket produces a server nothing can address, which is #547 rather than a fix for it."
}

# ── capture plumbing ─────────────────────────────────────────────────────────────

# pane prints the current pane in FIXTURE form: plain `capture-pane -p`, which strips
# trailing spaces (-N would preserve them). That is what the committed panes in
# registry_test.go are, and it is why they fit Go raw string literals at all.
#
# It is NOT what production captures. session/tmux/tmux.go uses `-p -e -J`, then
# poll.go strips CSI sequences with a regex that does not match OSC — so the string a
# matcher really sees can carry OSC 8 hyperlink residue a plain capture never shows,
# and -J joins wrapped lines at capture time. Every rung therefore also writes a
# production-form copy under captures/prod/. If a matcher passes against the fixture
# and misses in the wild, that pair is where the difference will be.
#
# Note the flag collision with t()'s global -N ("don't start a server"): that one
# goes before the subcommand, capture-pane's -N goes after, and they are unrelated.
pane() {
	t capture-pane -p -t "$PANE"
}

pane_prod() {
	t capture-pane -p -e -J -t "$PANE"
}

current_width() {
	t display-message -p -t "$PANE" '#{window_width}'
}

settle() { sleep "$SETTLE"; }

# write_capture writes the three artifacts for one screen: the fixture-form pane, a
# byte dump, and the production-form capture. The byte dump is not optional — agy's
# pointer looks like "❯" and is plain ASCII ">", and that one byte was the whole of
# #512. Never trust a glyph you have only seen rendered.
#
# `cat -vet` rather than `cat -A`: they are the same thing on GNU (-A is documented as
# equivalent to -vET) but -A does not exist in BSD cat, and under `set -e` its absence
# would abort the first rung of every ladder on macOS. The directory keeps the name
# cat-A/ because that is what #512 and the judgement notes below call these dumps.
write_capture() {
	local stem="$1"
	pane >"$RUN/captures/$stem.txt"
	cat -vet "$RUN/captures/$stem.txt" >"$RUN/captures/cat-A/$stem.cat-A.txt"
	pane_prod >"$RUN/captures/prod/$stem.esc.txt"
}

# ── verbs ────────────────────────────────────────────────────────────────────────

preflight() {
	local bin
	for bin in tmux git; do
		command -v "$bin" >/dev/null 2>&1 || die "missing dependency: $bin"
	done
	local have
	have="$(tmux -V | awk '{print $2}')"
	# A pure-sort version compare: if the lower of {have, floor} is not the floor,
	# have is older than the floor.
	if [[ "$(printf '%s\n%s\n' "$have" "$MIN_TMUX" | sort -V | head -1)" != "$MIN_TMUX" ]]; then
		die "tmux $have is older than Atrium's floor ($MIN_TMUX; session/tmux/version.go)"
	fi
	# The pane inherits LANG/LC_ALL from this shell. On a non-UTF-8 locale the agent
	# draws ASCII fallbacks and you pin the wrong glyphs — which is the class of bug
	# #512 was, so this is a warning worth reading rather than boilerplate.
	case "${LC_ALL:-${LC_CTYPE:-${LANG:-}}}" in
	*UTF-8* | *utf8* | *UTF8*) ;;
	*) note "WARNING: locale is '${LC_ALL:-${LC_CTYPE:-${LANG:-unset}}}', not UTF-8 — the CLI may draw ASCII fallbacks and you would pin the wrong glyphs" ;;
	esac
}

# new_workspace creates a scratch git repo at $1. A real repo with a commit, because
# several CLIs behave differently outside one — and never an Atrium worktree, whose
# contents are somebody's real uncommitted work.
new_workspace() {
	local dir="$1"
	mkdir -p "$dir"
	git -C "$dir" init -q -b main
	git -C "$dir" config user.email capture@example.invalid
	git -C "$dir" config user.name "Atrium Capture"
	echo "# capture workspace" >"$dir/README.md"
	git -C "$dir" add -A
	# gpgsign is disabled explicitly (as test/smoke/run.sh does): a developer with
	# commit.gpgsign=true globally would otherwise get a hang or a signing failure.
	git -C "$dir" -c commit.gpgsign=false commit -qm "init"
}

# probe_version prints the first line of `<bin> --version`, or "unknown", and is
# BOUNDED. An unbounded probe is not a theoretical worry: this harness exists to be
# pointed at a CLI nobody has characterised yet, and a binary that does not recognise
# --version does not necessarily print usage and exit — it can ignore the flag and
# start its normal event loop, at which point `up` hangs forever with no output, after
# the capture server is already running. That is the orphan-server class this file is
# about, reached through the one line that runs the target program outside tmux.
#
# `timeout(1)` would be the obvious tool and is not portable — it is GNU coreutils,
# absent from a stock macOS. Poll instead.
probe_version() {
	local bin="$1" out="$RUN/version.probe" pid _i first
	# The binary is the background job itself, not a subshell wrapping it, so $! is
	# the thing that needs killing rather than its parent.
	"$bin" --version </dev/null >"$out" 2>/dev/null &
	pid=$!
	for _i in $(seq 1 20); do
		kill -0 "$pid" 2>/dev/null || break
		sleep 0.5
	done
	if kill -0 "$pid" 2>/dev/null; then
		note "\`$bin --version\` did not exit within 10s — recording the version as unknown."
		note "a fixture's whole provenance is the version, so fill it in by hand before committing one."
		# SIGTERM, then SIGKILL if it is ignored. A TUI CLI installing a SIGTERM
		# handler is ordinary, and the `wait` below is unbounded — so stopping at
		# SIGTERM would hand the hang back to the very line this function exists to
		# bound, with the capture server already running.
		kill "$pid" 2>/dev/null || true
		for _i in $(seq 1 10); do
			kill -0 "$pid" 2>/dev/null || break
			sleep 0.2
		done
		kill -0 "$pid" 2>/dev/null && kill -9 "$pid" 2>/dev/null || true
	fi
	wait "$pid" 2>/dev/null || true
	first="$(head -1 "$out" 2>/dev/null || true)"
	rm -f "$out"
	printf '%s\n' "${first:-unknown}"
}

# start_session starts the CLI detached at a given size in a given workspace and
# records the resulting ids. Shared by `up` and `fresh`.
start_session() {
	local workdir="$1" width="$2" height="$3" program="$4"
	# -x/-y size a DETACHED session: "With -d, the initial size comes from the global
	# default-size option; -x and -y can be used to specify a different size." Without
	# them every capture would silently be 80x24 — and a capture at the wrong width is
	# worse than a failed one, because it becomes a committed fixture.
	tmux_boot new-session -d -s "$SESSION" -x "$width" -y "$height" \
		-c "$workdir" -n capture "$program"
	assert_render_conf_applied
	# resize-window sets window-size to manual by itself once it runs, so this is not
	# what makes the ladder stick. It is here for the attach hazard: until the first
	# resize the window is window-size `latest`, so someone attaching to eyeball the
	# session would resize it out from under the recorded width.
	t set-option -gw window-size manual
	resolve_ids
	# Wait for the CLI to paint before handing control back, so the first capture is
	# not of an empty pane.
	local _i
	for _i in $(seq 1 60); do
		[[ -n "$(pane | tr -d '[:space:]')" ]] && break
		sleep 0.5
	done
}

# up_failed is cmd_up's EXIT trap, armed only while a half-created run exists. It
# reads the globals rather than taking arguments, so nothing about the run's name can
# change how this line parses. Both paths still go through assert_under_run_root.
up_failed() {
	reap "$SOCK" 2>/dev/null || true
	assert_under_run_root "$RUN"
	rm -rf "$RUN"
}

cmd_up() {
	local program="${1:-}" width="${2:-$DEFAULT_WIDTH}" height="${3:-$DEFAULT_HEIGHT}"
	[[ -n "$program" ]] || die "usage: up <program> [width] [height]"
	# Validated here for the reason cmd_fresh validates its own width: these two values
	# are interpolated into meta.env, which every later verb SOURCES. Today tmux
	# rejecting a non-numeric -x/-y is the only thing standing in front of that, and it
	# only holds while start_session keeps running before the meta.env write — a
	# reordering away from `WIDTH=1; rm -rf …` being shell input on the next verb.
	# Do not depend on a neighbour's argument checking for a file you generate.
	local dim
	for dim in "$width" "$height"; do
		[[ "$dim" =~ ^[0-9]+$ ]] || die "width and height must be numbers, got: $width $height"
		((dim > 0)) || die "width and height must be greater than zero, got: $width $height"
	done
	preflight
	# The program may carry flags ("codex --foo"); only the first token is a binary.
	local bin0="${program%% *}"
	command -v "$bin0" >/dev/null 2>&1 || die "not on PATH: $bin0"

	# ATR_CAP_RUN names the run directory outright, and it is what makes a deliberate
	# second run possible — it is the only spelling that leaves $RUN_ROOT/current out
	# of the loop at BOTH ends: this verb neither refuses because of it nor publishes
	# to it, so the shell steering the first run goes on steering the first run.
	#
	# The refusal below used to name it as the remedy while `up` read only ATR_CAP_NAME
	# and consulted the pointer unconditionally, so the advice could not work and there
	# was in fact no way to drive two at once. An error message that names an escape
	# hatch has to have one.
	local pinned=0
	if [[ -n "${ATR_CAP_RUN:-}" ]]; then
		[[ -z "${ATR_CAP_NAME:-}" ]] ||
			die "set ATR_CAP_RUN or ATR_CAP_NAME, not both — they name the same directory two ways"
		pinned=1
	fi

	if ((!pinned)) && [[ -f "$RUN_ROOT/current" ]]; then
		local prev
		prev="$(cat "$RUN_ROOT/current")"
		if [[ -S "$prev/tmux/sock" ]] && tmux -N -S "$prev/tmux/sock" has-session >/dev/null 2>&1; then
			die "a capture run is already live at $prev — \`down\` it first (or export ATR_CAP_RUN=$RUN_ROOT/<name> in this shell to drive a second one in parallel, and read \`help\` on why the default is one)"
		fi
	fi

	# A single directory NAME from a conservative character set. Two distinct hazards,
	# both cheap to close here and awkward to close anywhere else:
	#
	# A slash passes assert_under_run_root — no ".." component, still under the root —
	# and lands the run one level deeper than `reap-all`'s $RUN_ROOT/*/tmux/sock glob
	# can see, leaving an orphan server this script can no longer sweep.
	#
	# And the name reaches $RUN, which reaches a `trap … EXIT` whose body is a string.
	# Restricting the charset means no quoting question about that body can arise —
	# the trap below reads globals rather than expanding them for the same reason, so
	# this is the belt to that pair of braces.
	if [[ -n "${ATR_CAP_NAME:-}" && ! "$ATR_CAP_NAME" =~ ^[A-Za-z0-9._-]+$ ]]; then
		die "ATR_CAP_NAME must be a single directory name of [A-Za-z0-9._-], got: $ATR_CAP_NAME"
	fi

	local fleet_before
	fleet_before="$(fleet_sessions)"

	mkdir -p "$RUN_ROOT"
	# A predictable path under /tmp can be pre-created by another uid on a shared
	# box, and `mkdir -p` succeeds on a directory you do not own. Refuse rather than
	# write a scratch repo and a socket into someone else's directory.
	[[ -O "$RUN_ROOT" ]] || die "$RUN_ROOT exists and is not owned by you — refusing to use it"
	chmod 700 "$RUN_ROOT"

	# The run directory name is DETERMINISTIC, not random, because the workspace's
	# absolute path ends up inside the fixtures themselves (registry_test.go's
	# agyConfirmPane contains "rm -f /tmp/agy512cap/repo/hello.txt"). A random id per
	# run would make every re-capture a large spurious diff and make fixtures from
	# different widths disagree with each other about where they were taken.
	if ((pinned)); then
		RUN="$ATR_CAP_RUN"
	else
		RUN="$RUN_ROOT/${ATR_CAP_NAME:-$(basename "$bin0")}"
	fi
	# `up` is the path that CREATES $RUN, so it is the one that must prove $RUN is a
	# place this script may own before writing a socket and a scratch repo into it —
	# every later verb reaches $RUN through load_run, which already asserts. Miss it
	# here and the trap two lines below is an `rm -rf` on an unchecked path. The
	# sweepability check is what ATR_CAP_RUN needs beyond that: it arrives as a whole
	# path rather than a name, so it can name a depth `reap-all` cannot glob.
	assert_under_run_root "$RUN"
	assert_sweepable "$RUN"
	[[ ! -e "$RUN" ]] || die "$RUN already exists — \`down\` the previous run, or set ATR_CAP_NAME"
	SOCK="$RUN/tmux/sock"
	REPO="$RUN/repo"
	mkdir -p "$RUN/tmux" "$RUN/captures/cat-A" "$RUN/captures/prod"
	# Written before anything can boot a server: tmux_boot passes this to -f, and
	# `fresh` boots again later from the same file.
	write_capture_conf
	# The fleet snapshot `down` will diff against. Guarded because $(…) strips trailing
	# newlines: an unguarded printf would write one blank line for an empty fleet, and
	# `comm` would then report a phantom "" session as missing.
	if [[ -n "$fleet_before" ]]; then printf '%s\n' "$fleet_before"; fi >"$RUN/fleet-before.txt"
	# WHICH fleet that snapshot is of, recorded so `down` can tell whether it is
	# comparing like with like. live_socket derives the path from the inherited
	# TMUX_TMPDIR, and each verb is a separate process — so a shell that exports or
	# unsets that variable between `up` and `down` watches one fleet and then reports
	# on another, and the report reads exactly like a clean one.
	live_socket >"$RUN/fleet-socket.txt"
	export TMUX_TMPDIR="$RUN/tmux"

	# Until the run is recorded, a failure must not leave a server behind. This trap
	# is installed ONLY in `up`, and disarmed at its end: in a verb-per-process model
	# `up` exiting IS the success path, so a trap left armed would destroy the session
	# it just created. ladder/send/wait/sample install no teardown trap at all,
	# because they must leave the session alive — see `reap-all` for what that costs.
	#
	# The trap runs a FUNCTION reading globals. The obvious spelling expands $SOCK and
	# $RUN into the trap string at arm time, which makes the paths part of a command
	# line that is re-parsed at exit — an apostrophe in the run name is then unbalanced
	# quoting wrapped around an `rm -rf`. A function name has no such surface.
	trap up_failed EXIT

	new_workspace "$REPO"
	start_session "$REPO" "$width" "$height" "$program"

	cat >"$RUN/meta.env" <<EOF
PROGRAM=$(printf '%q' "$program")
BIN=$(printf '%q' "$bin0")
VERSION=$(printf '%q' "$(probe_version "$bin0")")
CAPTURED=$(date -u +%Y-%m-%d)
WIDTH=$width
HEIGHT=$height
PANE=$PANE
WINDOW=$WINDOW
EOF

	# Disarm before publishing the pointer, so no failure path can leave `current`
	# naming a directory the trap has just deleted.
	trap - EXIT
	if ((pinned)); then
		note "ATR_CAP_RUN was set: $RUN_ROOT/current is left untouched. Keep ATR_CAP_RUN"
		note "        exported in this shell — without it the later verbs aim at the other run."
	else
		printf '%s\n' "$RUN" >"$RUN_ROOT/current"
	fi

	note "run     $RUN"
	note "socket  $SOCK"
	note "repo    $REPO"
	note "size    ${width}x${height}  pane $PANE  window $WINDOW"
	note "fleet   $(grep -c . "$RUN/fleet-before.txt" || true) live Atrium sessions recorded by name"
	note "attach  tmux -S $SOCK attach -t $SESSION"
}

# cmd_fresh replaces the session with a new one, in a NEW workspace, at a new width.
#
# This exists because a startup gate is once-per-path: agy's folder-trust screen does
# not come back for a directory it has already been answered for, so `ladder` — whose
# whole trick is resizing one LIVE dialog — structurally cannot capture the trust
# gate at a second width. #512 hit this and worked around it by hand; its fixtures
# still carry the evidence, agyTrustGatePane taken in ".../repo" and
# agyTrustGateNarrowPane in ".../fresh28".
cmd_fresh() {
	local width="${1:-$DEFAULT_WIDTH}"
	# Validated BEFORE anything is destroyed, and for two separate reasons.
	#
	# The width is interpolated into a path that is then `rm -rf`'d, so a value
	# carrying ".." walks out of the run root: `fresh /../../../x` would build
	# $RUN/fresh/../../../x and delete somebody else's directory. Digits cannot.
	#
	# And this verb reaps the live session before it hands the width to tmux, so
	# without a check here a typo is answered by tmux's bare "width invalid" AFTER
	# the session is gone — throwing away a dialog that cost an API turn to reach.
	[[ "$width" =~ ^[0-9]+$ ]] || die "width must be a number, got: $width"
	((width > 0)) || die "width must be greater than zero"
	load_run
	local workdir="$RUN/fresh$width"
	# Belt as well as braces: the regex above already makes an escape impossible, so
	# this asserts the property rather than trusting the derivation to preserve it.
	assert_under_run_root "$workdir"
	reap "$SOCK"
	[[ -e "$workdir" ]] && rm -rf "$workdir"
	new_workspace "$workdir"
	start_session "$workdir" "$width" "$HEIGHT" "$PROGRAM"
	# The ids change with the session; meta.env must follow or every later verb
	# targets a pane that no longer exists.
	#
	# Rewritten through a sibling file rather than with `sed -i`: GNU takes a bare -i,
	# BSD requires a backup-suffix argument after it, and the spelling that satisfies
	# both does not exist. The sibling lives in $RUN, so `down` reaps it either way.
	sed -e "s|^PANE=.*|PANE=$PANE|" -e "s|^WINDOW=.*|WINDOW=$WINDOW|" \
		"$RUN/meta.env" >"$RUN/meta.env.new"
	mv "$RUN/meta.env.new" "$RUN/meta.env"
	note "fresh session at width $width in $workdir (pane $PANE)"
}

# sends_enter reports whether a list of tmux key names contains one that submits.
#
# Keying the guard on the literal string "Enter" was too narrow: tmux sends the same
# byte for `C-m`, which is a spelling somebody reaching for a control key will
# reasonably use, and it walked straight past a guard whose whole job is to stand
# between a keystroke and the user's real config file. "Enter" as a substring already
# covers KPEnter/S-Enter/M-Enter; C-m and C-j are the ones that need naming.
#
# C-j (0x0A) is here even though it is NOT reliably a submit: in a readline-style
# composer it is accept-line, while several Ink-based ones treat it as the insert-a-
# newline key (#396 made exactly that binding in Atrium's own composers). Listing it
# resolves that ambiguity in the direction the guard's cost argument demands — a
# false positive costs one FORCE=1, a miss costs a permanent edit to the user's
# config.
sends_enter() {
	local k
	for k in "$@"; do
		case "$k" in
		*[Ee]nter* | C-m | C-M | c-m | c-M | C-j | C-J | c-j | c-J) return 0 ;;
		esac
	done
	return 1
}

# guard_enter refuses to submit at a pane offering to persist a choice to the agent's
# real config. It is a shared helper because `keys` is not the only verb that presses
# Enter — `send` and `paste` both end in one, and a guard on one of three doors is
# not a guard. This drives a REAL, authenticated CLI against your REAL config dir, so
# an Enter on the wrong row edits it for good. FORCE=1 when you mean it.
#
# The probe is ONE SHORT WORD against the pane with all whitespace removed, and both
# halves of that are this script's own #512 lesson turned on itself. A guard reading
# `grep -i 'persist to settings'` line by line is dead at exactly the widths this tool
# exists to drive: at 24 columns agy renders the option as "  2. Yes, allow (Persist"
# / " to settings.json)" and a multi-word literal spanning two physical lines matches
# nothing. Stripping whitespace repairs a wrap even one that fell mid-word, and a
# single token cannot be split by one. It will fire on panes that merely mention the
# word; that costs a FORCE=1, while a miss costs a permanent edit to the user's
# config — refuse in the cheap direction.
#
# Which is also why the capture is its own statement rather than the head of a
# `pane | tr | grep -q … || return 0` pipeline. That spelling makes the guard's
# verdict the status of a PIPELINE, so any way the capture can fail — a pane that
# died in the window between require_live and here, a tmux error — reads as "no
# persist option" and lets the Enter through. Failing to capture is not evidence of
# safety, so it refuses.
guard_enter() {
	local flat
	[[ "${FORCE:-0}" != "1" ]] || return 0
	flat="$(pane | tr -d '[:space:]')" ||
		die "could not capture the pane to check it for a persist-to-settings option"
	grep -qi 'persist' <<<"$flat" || return 0
	note "the pane offers a persist-to-settings option, and this CLI is running against your REAL config dir."
	note "check which row is highlighted, then re-run with FORCE=1 if that is what you want."
	die "refusing to submit at this pane"
}

cmd_keys() {
	[[ $# -gt 0 ]] || die "usage: keys <tmux-key>... (e.g. \`keys Enter\`, \`keys Down Enter\`)"
	load_run
	require_live
	# Only when the keys actually submit: `keys Down` moves a selection and needs no
	# ceremony, while `keys Enter` at a "(Persist to settings.json)" row edits the
	# user's real agent config. See guard_enter for why the check is shaped as it is.
	sends_enter "$@" && guard_enter
	t send-keys -t "$PANE" "$@"
	settle
}

cmd_send() {
	local text="${1:-}"
	[[ -n "$text" ]] || die "usage: send <text>"
	load_run
	require_live
	# -l sends the text literally, so words like "Enter" or "C-c" inside a prompt are
	# typed rather than interpreted as key names; `--` guards a leading dash. This
	# mirrors session/tmux/tmux.go's SendKeys. Enter goes separately, after a settle:
	# several CLIs debounce a fast burst into a collapsed paste chip, and submitting
	# inside that window loses it — which is also why the guard runs BEFORE the text
	# is typed: by the time Enter is due, the composer has redrawn over the dialog.
	guard_enter
	t send-keys -t "$PANE" -l -- "$text"
	settle
	t send-keys -t "$PANE" Enter
	settle
}

cmd_paste() {
	local text="${1:-}"
	[[ -n "$text" ]] || die "usage: paste <text>"
	load_run
	require_live
	# What Atrium actually does for a queued prompt (SendPasted, session/tmux/tmux.go):
	# stage in a buffer, deliver as ONE bracketed paste, then submit. It matters for a
	# capture because the composer state differs — claude renders a paste as a
	# "[Pasted text #1]" chip rather than the literal characters (#319), so a fixture
	# driven by `send` shows a composer production would never produce for that prompt.
	# Use `paste` when the fixture is about the composer; `send` when it is not.
	guard_enter
	t set-buffer -b atrium-capture -- "$text"
	t paste-buffer -d -p -b atrium-capture -t "$PANE"
	settle
	t send-keys -t "$PANE" Enter
	settle
}

cmd_wait() {
	local pattern="${1:-}" timeout="${2:-120}"
	[[ -n "$pattern" ]] || die "usage: wait <extended-regex> [timeout-seconds]"
	load_run
	require_live
	# Poll for the screen; never sleep-and-hope. A timeout prints the pane it gave up
	# on, because "the marker never appeared" and "the marker is spelled differently"
	# are indistinguishable from a bare failure.
	# `screen="$(pane)" && grep …` rather than `pane | grep -qE`, for the reason
	# guard_enter spells out: under `set -o pipefail` a match that lets grep exit while
	# the capture is still writing makes the pipeline's status the writer's SIGPIPE,
	# and a match then reads as no-match. A pane fits the pipe buffer, so the writer
	# has always finished first and this has never been reachable here — but the
	# failure it would produce is a timeout on a marker that WAS on screen, which is
	# indistinguishable from a wrong regex and would be debugged as one.
	local deadline=$((SECONDS + timeout)) screen
	while ((SECONDS < deadline)); do
		if screen="$(pane)" && grep -qE "$pattern" <<<"$screen"; then
			note "matched /$pattern/ after ${SECONDS}s"
			return 0
		fi
		sleep 1
	done
	note "TIMEOUT after ${timeout}s waiting for /$pattern/. Last pane:"
	pane >&2
	die "gave up waiting for /$pattern/"
}

cmd_ladder() {
	local label="${1:-}"
	[[ -n "$label" ]] || die "usage: ladder <label> [width...]"
	shift
	load_run
	require_live

	local widths=("$@")
	[[ ${#widths[@]} -gt 0 ]] || widths=("${DEFAULT_WIDTHS[@]}")

	local w got
	for w in "${widths[@]}"; do
		# Both dimensions, every rung: resize-window -x sets ONLY the width, so a
		# height changed behind our back (an attach, a stray client) would otherwise
		# persist silently through the whole ladder.
		t resize-window -t "$WINDOW" -x "$w" -y "$HEIGHT"
		settle
		# Verify the resize LANDED before capturing. A pane captured at a width other
		# than the one in its filename is a fixture that lies — the single worst thing
		# this script could produce.
		got="$(current_width)"
		[[ "$got" == "$w" ]] || die "asked for width $w but the window is $got — refusing to write a mislabelled capture"
		write_capture "$label-w$w"
		printf '  w%-4s %s non-empty lines\n' "$w" "$(grep -c . "$RUN/captures/$label-w$w.txt" || true)" >&2
	done

	# Leave the session at the width `up` recorded, so a following ladder starts from
	# a known state rather than from whatever the last rung happened to be.
	t resize-window -t "$WINDOW" -x "$WIDTH" -y "$HEIGHT"
	settle
	note "wrote ${#widths[@]} captures to $RUN/captures/ (label: $label)"
}

cmd_sample() {
	local label="${1:-}" seconds="${2:-30}" interval="${3:-1}"
	[[ -n "$label" ]] || die "usage: sample <label> [seconds] [interval]"
	load_run
	require_live

	local w n=0 deadline=$((SECONDS + seconds))
	w="$(current_width)"
	# Frames across a whole turn are how #512 established that agy's busy marker
	# survives streaming — claude's footer hint does NOT, which is the only reason
	# claude has a spinner matcher at all. One frame proves nothing about a turn.
	while ((SECONDS < deadline)); do
		write_capture "$(printf '%s-w%s-t%02d' "$label" "$w" "$n")"
		n=$((n + 1))
		sleep "$interval"
	done
	note "wrote $n frames at width $w (label: $label)"
}

# capture_name derives a Go identifier from a capture filename. The name is a
# PLACEHOLDER and the emitted comment says so: registry_test.go's convention puts no
# width in the identifier (agyConfirmNarrowestPane, agyConfirmFloorPane, and
# claudeFetchNarrowPane), and choosing between "Narrow", "Narrowest" and "Floor" is a
# judgement about which width matters — exactly the call this script must not make.
capture_name() {
	local prefix="$1" label="$2" width="$3" frame="$4" part out=""
	local IFS='-'
	for part in $label; do
		# ${part^} would be shorter and is bash 4 only — stock /bin/bash on macOS is
		# 3.2, where it is a "bad substitution" that kills `emit` outright.
		out+="$(printf '%s' "${part:0:1}" | tr '[:lower:]' '[:upper:]')${part:1}"
	done
	printf '%s%sW%s%sPane' "$prefix" "$out" "$width" "${frame:+T$frame}"
}

cmd_emit() {
	local prefix=agent join=0 arg
	for arg in "$@"; do
		case "$arg" in
		--join) join=1 ;;
		*) prefix="$arg" ;;
		esac
	done
	load_run

	# captures/ holds only fixture-form panes; the cat -A dumps and the
	# production-form captures live in subdirectories, so this glob cannot pick them
	# up and emit a const full of "M-bM-^T^@" byte escapes.
	local files=("$RUN"/captures/*.txt)
	[[ -e "${files[0]}" ]] || die "no captures in $RUN/captures — run \`ladder\` or \`sample\` first"

	local f base label width frame name body redacted
	for f in "${files[@]}"; do
		base="$(basename "$f" .txt)"
		# Anchor the width suffix on -w<digits> at the END, optionally followed by a
		# -t<digits> frame. Cutting at the first or last "-w" in the whole stem
		# instead reads a LABEL containing one — `ladder demo-wide` — as label "demo"
		# at width "ide", so every rung of that ladder emits the same identifier at a
		# wrong width: a duplicate declaration that does not compile, above a doc
		# comment that does not describe the pane.
		if [[ "$base" =~ ^(.+)-w([0-9]+)(-t([0-9]+))?$ ]]; then
			label="${BASH_REMATCH[1]}"
			width="${BASH_REMATCH[2]}"
			frame="${BASH_REMATCH[4]}"
		else
			die "cannot parse a label and width out of capture '$base' — expected <label>-w<width>[-t<frame>].txt"
		fi
		name="$(capture_name "$prefix" "$label" "$width" "$frame")"

		# agy's splash carries the signed-in email; it must never reach a committed
		# fixture. Redaction is on by default and disclosed in the comment, the way
		# #512's fixtures disclose their elisions. It covers only the narrow case: if
		# a fixture needs a whole REGION cut (#512 elided the startup splash), that is
		# still a hand edit, and it still needs justifying in the comment.
		body="$(sed -E 's/[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}/user@example.invalid/g' "$f")"
		redacted=""
		[[ "$body" != "$(cat "$f")" ]] && redacted=" Email addresses redacted."
		# `capture-pane` returns the whole pane, so a short screen in a 40-row window
		# arrives with ~25 empty lines after it. Every committed fixture is trimmed;
		# the trailing blanks carry no information either way, since the window-based
		# matchers count NON-empty lines (chrome.go's liveChromeLines). $(…) already
		# eats the trailing newlines, which is what does the trimming here — it is
		# recorded rather than incidental so nobody "fixes" it back.

		printf '// %s is the %s screen of %s %s at width %s, captured %s by\n' \
			"$name" "$label" "$BIN" "$VERSION" "$width" "$CAPTURED"
		printf '// scripts/drive-agent.sh (#647). RENAME ME: this file names fixtures\n'
		printf '// semantically, not by width. FILE ME: the width above is prose, and prose is\n'
		printf '// what #648 found rotting. If this pane is one its matcher must FIRE on, add it\n'
		printf '// to paneCoverage in pane_width_test.go under "<agent>/gate", "<agent>/busy" or\n'
		printf '// "<agent>/prompt/<matcher>" so the width becomes a datum. That table is\n'
		printf '// positive-only: a composer, an idle screen or any other pane the matcher must\n'
		printf '// NOT match belongs in a per-adapter test instead.%s\n' "$redacted"

		# Two things break a Go raw string literal, per the spec. A backtick
		# terminates it, and there is no escape for one. A CARRIAGE RETURN is silently
		# DISCARDED from the value — no compile error, no visual cue, and the const
		# stops matching the bytes on screen. Backslashes and tabs are fine; that is
		# what raw strings are for.
		if ((join)) || [[ "$body" == *'`'* || "$body" == *$'\r'* ]]; then
			printf 'var %s = strings.Join([]string{\n' "$name"
			# Both the CR expression and the indent carry a LITERAL control
			# character via $'…' rather than the escape "\r"/"\t". POSIX leaves
			# backslash-<char> undefined for anything but a few sequences, and BSD
			# sed takes it literally at both ends: on the left \r matches the letter
			# r, so every word containing one gets mangled, and on the right \t
			# inserts the letter t — which would open each fixture line with `t"`
			# instead of a tab, inside a Go composite literal, so the emitted file
			# does not compile. The join form is not opt-in (a backtick in the pane
			# selects it, and agent CLIs render markdown constantly), so that lands
			# on macOS by default. Order matters — the backslash doubling runs first,
			# so the backslash the CR rule introduces is not doubled again.
			printf '%s\n' "$body" |
				sed -e 's/\\/\\\\/g' -e 's/"/\\"/g' -e $'s/\r/\\\\r/g' -e $'s/^/\t"/' -e 's/$/",/'
			printf '}, "\\n")\n\n'
		else
			# shellcheck disable=SC2016  # the backticks are Go raw-string delimiters, not a subshell
			printf 'const %s = `%s`\n\n' "$name" "$body"
		fi
	done

	note "if these panes justify a heuristic change, the pin moves too:"
	note "  session/agent/registry.go  VerifiedVersion -> $VERSION"
	note "  session/agent/drift_fields_test.go  the hardcoded per-adapter table"
}

cmd_status() {
	load_run
	printf 'run      %s\n' "$RUN"
	printf 'program  %s (%s)\n' "$PROGRAM" "$VERSION"
	printf 'socket   %s\n' "$SOCK"
	if t has-session -t "$SESSION" 2>/dev/null; then
		printf 'session  live, %sx%s  pane %s\n' "$(current_width)" "$HEIGHT" "$PANE"
	else
		# shellcheck disable=SC2016  # backticks here are prose quoting a verb name
		printf 'session  DEAD (the CLI exited; `down` to clean up)\n'
	fi
	printf 'captures %s\n' "$(find "$RUN/captures" -maxdepth 1 -name '*.txt' | grep -c . || true)"
	# shellcheck disable=SC2016  # backticks here are prose quoting a verb name
	printf 'fleet    %s live Atrium sessions now, %s at `up`\n' \
		"$(fleet_count)" "$(grep -c . "$RUN/fleet-before.txt" || true)"
	printf 'attach   tmux -S %s attach -t %s\n' "$SOCK" "$SESSION"
}

cmd_down() {
	# A stale pointer is the one state `down` must handle before load_run does: when
	# the run directory has gone (a manual rm, a KEEP=1 run cleaned up by hand), every
	# other verb dies telling you to run `down`, so `down` dying too would leave no
	# way out but editing $RUN_ROOT/current by hand.
	local dir
	dir="$(run_dir)"
	if [[ ! -d "$dir" ]]; then
		note "run directory is already gone: $dir"
		clear_pointer_if_ours "$dir"
		# Say what clearing the pointer does NOT do. Whoever removed that directory
		# removed the socket file inside it, and a tmux server outlives its socket:
		# the process is still there and is now addressable by NEITHER -S (the path
		# is unlinked) NOR this script's reap-all (its glob looks under the run
		# root). Verified by doing it. `atrium reap` finds a server through /proc
		# wherever its socket sat (#602); otherwise it is `ps` and a PID.
		note "NOTE: if a capture server was running, deleting its directory unlinked its socket."
		note "      it cannot be reached by path any more — find it with \`atrium reap\` or ps."
		return 0
	fi

	load_run
	reap "$SOCK"

	# Compare the SET of session names, not the count. New sessions appearing between
	# `up` and `down` are normal on a fleet somebody is using, so only a name that was
	# there and is not now is worth saying anything about — and a count would let one
	# such loss hide behind one such gain.
	local before_n after_n missing
	# `up` writes this before meta.env, so load_run succeeding implies it exists. The
	# fallback is so a hand-edited run cannot make `comm` abort the cleanup below.
	[[ -f "$RUN/fleet-before.txt" ]] || {
		note "no fleet snapshot in $RUN — the before/after comparison is being skipped"
		: >"$RUN/fleet-before.txt"
	}
	before_n="$(grep -c . "$RUN/fleet-before.txt" || true)"
	after_n="$(fleet_count)"
	# Same-fleet check. Written as a warning rather than a refusal: the comparison
	# below is still worth running and still worth reading, it just is not the
	# reassurance it looks like when the two snapshots came from different servers.
	local snap_sock now_sock
	snap_sock="$(cat "$RUN/fleet-socket.txt" 2>/dev/null || true)"
	now_sock="$(live_socket)"
	if [[ -n "$snap_sock" && "$snap_sock" != "$now_sock" ]]; then
		note "*** the fleet snapshot is of a DIFFERENT server than the one being read now: ***"
		note "        at \`up\`: $snap_sock"
		note "        now:     $now_sock"
		note "*** TMUX_TMPDIR changed between the two. Treat the comparison below as unverified. ***"
	fi
	# comm's status is captured rather than allowed to propagate. `|| true` would be
	# the reflex and it is the wrong one here: it converts "the comparison did not
	# run" into an empty missing-list, which reads as an all-clear. Letting it
	# propagate is no better — under `set -e` it aborts `down` before the cleanup
	# below, leaving the run directory and the `current` pointer behind. Capture, then
	# report the third outcome honestly.
	local cmp_status=0
	missing="$(LC_ALL=C comm -23 "$RUN/fleet-before.txt" <(fleet_sessions) 2>"$RUN/fleet-compare.err")" || cmp_status=$?

	if ((cmp_status != 0)); then
		note "*** COULD NOT COMPARE THE FLEET (comm exited $cmp_status): ***"
		sed 's/^/        /' "$RUN/fleet-compare.err" >&2 || true
		note "*** That is UNVERIFIED, not clean — check the live fleet by hand. ***"
	elif [[ -n "$missing" ]]; then
		note "*** LIVE ATRIUM SESSIONS PRESENT AT \`up\` ARE GONE: ***"
		printf '%s\n' "$missing" | sed 's/^/        /' >&2
		# Name the innocent explanation too. This script only ever sends
		# list-sessions to the live socket, and an agent CLI exiting ends its tmux
		# session by itself, so a finished or hand-killed session lands here as
		# well. What must never happen is this going UNREPORTED — read the names,
		# and if you recognise them as yours, it is the ordinary case.
		note "*** A session whose agent exited, or one you killed, looks identical to this. ***"
		note "*** Read the names above before running this again. ***"
	elif ((after_n > before_n)); then
		note "fleet   $after_n live Atrium sessions ($before_n at \`up\`; every one of those $before_n is still live ✓)"
	else
		note "fleet   $after_n live Atrium sessions, every one recorded at \`up\` still live ✓"
	fi

	if [[ "${KEEP:-0}" == "1" ]]; then
		note "kept    $RUN"
	else
		assert_under_run_root "$RUN"
		rm -rf "$RUN"
		note "removed $RUN"
	fi
	clear_pointer_if_ours "$RUN"

	# Cleanup happens either way — a fleet that lost a session is a reason to look, not
	# a reason to leave a capture server and a scratch repo behind. The status is the
	# signal, and it is non-zero for "sessions went missing" AND for "could not tell".
	[[ -z "$missing" ]] && ((cmp_status == 0)) || exit 1
}

# cmd_reap_all sweeps every capture server under $RUN_ROOT. It exists because
# ladder/sample install no teardown trap — they must not, since their job is to leave
# the session alive — so a Ctrl-C during the long verb strands a server whose socket
# nothing else looks at. internal/testutil/tmux.go describes this same orphan class
# and why an automatic sweep was tried and reverted: the glob that finds these roots
# is one wrong prefix away from the live socket. Here it stays an explicit verb, and
# every path still goes through assert_under_run_root.
cmd_reap_all() {
	local sock n=0
	shopt -s nullglob
	for sock in "$RUN_ROOT"/*/tmux/sock; do
		note "reaping $sock"
		reap "$sock"
		n=$((n + 1))
	done
	shopt -u nullglob
	note "reaped $n capture server(s); live fleet: $(fleet_count) sessions"
}

cmd_help() {
	cat <<'EOF'
drive-agent.sh — drive a real agent CLI at a width ladder and capture its panes (#647)

VERBS
  up <program> [w] [h]     start <program> detached in a scratch git repo on an
                           isolated socket (default 120x40); prints the attach command
  keys <key>...            send-keys verbatim — how a per-CLI trust screen is dismissed
  send <text>              type <text> literally, then Enter (mirrors SendKeys)
  paste <text>             deliver <text> as one bracketed paste, then Enter
                           (mirrors SendPasted — what Atrium really does for a prompt)
  wait <regex> [secs]      poll until the pane matches; dumps the pane on timeout
  ladder <label> [w...]    resize + capture at each width (default 120 60 40 34 28 26 24 20)
  fresh [width]            restart in a NEW workspace at <width> — for a once-per-path
                           screen (a trust gate) that a resize cannot bring back
  sample <label> [s] [i]   capture a frame every i seconds for s seconds
  emit [prefix] [--join]   print Go fixtures for every capture, to stdout
  status                   run, size, capture count, live-fleet count
  down                     kill the capture server BY PATH, check the fleet, clean up
  reap-all                 sweep servers stranded by an interrupted ladder/sample
  help                     this

EACH RUNG WRITES THREE FILES
  captures/<label>-w<W>.txt          plain `capture-pane -p` — the fixture form, and
                                     what `emit` reads
  captures/cat-A/…                   byte dump (`cat -vet`, GNU's `cat -A` spelled so
                                     BSD cat understands it) — READ IT before
                                     trusting a glyph
  captures/prod/…esc.txt             `capture-pane -p -e -J` — what production captures
                                     (session/tmux/tmux.go). If a matcher passes the
                                     fixture and misses in the wild, diff this pair.

ENV
  ATR_CAP_ROOT   run root (default /tmp/atrium-capture; keep it short — sun_path is 108)
  ATR_CAP_NAME   run directory name (default the binary's basename). The workspace path
                 ends up INSIDE the fixtures, so it is deterministic on purpose.
  ATR_CAP_RUN    target a specific run directory instead of $ATR_CAP_ROOT/current. On
                 `up` it also PINS the run: the pointer is neither consulted nor
                 written, which is what makes a second parallel run possible.
  SETTLE         seconds to let the CLI repaint after a resize/keystroke (default 1.5)
  KEEP=1         keep the run directory on `down`
  FORCE=1        allow `keys Enter` at a persist-to-settings dialog

ONE RUN AT A TIME
  $ATR_CAP_ROOT/current is a single pointer, so a second `up` would silently re-aim
  every later verb at the new run — capturing the wrong program at the wrong width,
  which looks like output rather than an error. `up` refuses while a run is live.

  To drive two on purpose, export ATR_CAP_RUN in the SECOND shell before `up`:

      export ATR_CAP_RUN=$ATR_CAP_ROOT/codex   # then: up codex, ladder …, down

  That run is pinned to the path you gave and never touches `current`, so the first
  shell keeps steering the first run — including when the pinned one is torn down.
  Keep the variable exported for every verb of that run: unset it and the shell
  silently goes back to following `current`, which is the other run.

THE JUDGEMENT — what this script does NOT do for you
  Which literal a matcher keys on is a human call. #512 got it wrong twice from wide
  captures alone. What that cost taught, in the order it bites:

  * cat -A EVERY pane before trusting a glyph. agy's pointer looks like "❯" and is
    plain ASCII ">". That one byte was the whole of #512.
  * A CLI may TRUNCATE one region and WRAP another — agy truncates headline questions
    and wraps option rows, and only the second is repaired by flattening. Key on
    whatever sits nearest the bottom and is short enough not to truncate. "Key on the
    question, never the option text" is BACKWARDS for a gate, where the question is
    the long thing.
  * Pick the literal by the NARROWEST REACHABLE pane, not by your capture width. An
    agent's pane is Atrium's PREVIEW pane (app/app_layout.go GetPreviewSize →
    ui/list.go SetSessionPreviewSize → session/instance.go SetPreviewSize), the list
    may take maxListRatio = 0.60 of the terminal, and the remainder is UNCLAMPED. A
    70-column terminal leaves about 24. There is no floor. ui/terminal.go's "a
    reachable 28" is the SHELL pane and says reachable, not floor — it has been
    mis-cited as one at least twice.
  * For a fixed-position hint the floor is where the distinguishing token ENDS, not
    the literal's length. Shortening "↑/↓ Navigate · tab" to "Navigate · tab" needs
    the same 20 columns and dies at the same 19.
  * Pin a fixture AT the narrow width, or the length is an untested claim. Reverting
    to the too-long literal survived every test until a 24-column fixture existed.
  * Broadening a matcher to cover "any future dialog" is a regression waiting to
    happen: agy's slash-command menu renders "↑/↓ Navigate · enter Select · tab
    Complete" over a LIVE composer, so the generic prefix fires while the user is
    merely browsing /commands.
  * A miss is not always graceful. agy's dialogs carry "esc to cancel" in their own
    footer, so an unmatched dialog still satisfies HasBusyMarker and the row latches
    Working forever.

COST-SAVER
  Resize a LIVE dialog instead of re-running turns. A dialog waits for input, so one
  API turn covers the whole ladder — that is what `ladder` is for. `sample` needs a
  turn in flight, and `fresh` needs a new workspace, so budget those separately.
EOF
}

main() {
	local verb="${1:-help}"
	[[ $# -gt 0 ]] && shift
	case "$verb" in
	up) cmd_up "$@" ;;
	keys) cmd_keys "$@" ;;
	send) cmd_send "$@" ;;
	paste) cmd_paste "$@" ;;
	wait) cmd_wait "$@" ;;
	ladder) cmd_ladder "$@" ;;
	fresh) cmd_fresh "$@" ;;
	sample) cmd_sample "$@" ;;
	emit) cmd_emit "$@" ;;
	status) cmd_status "$@" ;;
	down) cmd_down "$@" ;;
	reap-all) cmd_reap_all "$@" ;;
	help | -h | --help) cmd_help ;;
	*) die "unknown verb: $verb (try \`help\`)" ;;
	esac
}

main "$@"
