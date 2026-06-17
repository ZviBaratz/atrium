#!/usr/bin/env bash
# Atrium end-to-end smoke spike (issue #148) — drives the real binary through vhs.
#
# This is NOT part of `just test`/`go test`: it depends on non-Go binaries (vhs,
# ttyd, ffmpeg, tmux) and drives a real tmux server. It is an opt-in target
# (`just smoke`) that proves the live interactive layer — create → attach →
# detach → re-attach — renders deterministically.
#
# Each run is fully isolated: a fresh HOME (data dir + worktrees + state.json), a
# fresh throwaway git repo, and a private TMUX_TMPDIR so the inner `tmux -L atrium`
# socket never collides with the user's real Atrium socket.
#
# Env knobs:
#   RUNS=N     determinism sample size (default 3)
#   UPDATE=1   refresh the committed golden instead of comparing
#   KEEP=1     keep the scratch dir (prints its path) for debugging
set -euo pipefail

SMOKE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SMOKE_DIR/../.." && pwd)"
TESTDATA="$SMOKE_DIR/testdata"
GOLDEN="$TESTDATA/basic-flow.golden.txt"
FAKE="$SMOKE_DIR/fake-agent.sh"
RUNS="${RUNS:-3}"

for bin in vhs ttyd ffmpeg tmux git jq; do
	command -v "$bin" >/dev/null 2>&1 || { echo "smoke: missing dependency: $bin" >&2; exit 127; }
done
GO="${GO:-go}"
command -v "$GO" >/dev/null 2>&1 || { echo "smoke: missing dependency: go (set GO=...)" >&2; exit 127; }

WORK="$(mktemp -d "${TMPDIR:-/tmp}/atrium-smoke.XXXXXX")"
cleanup() {
	if [[ "${KEEP:-0}" == "1" ]]; then
		echo "smoke: kept scratch dir: $WORK" >&2
	else
		rm -rf "$WORK"
	fi
}
trap cleanup EXIT

chmod +x "$FAKE"
ATR_BIN="$WORK/atrium"
( cd "$REPO_ROOT" && "$GO" build -o "$ATR_BIN" . )

# setup_env <dir> — build an isolated HOME + repo + seeded config under <dir>, and
# write a sourceable env file ($dir/env) exporting HOME, TMUX_TMPDIR, PATH.
setup_env() {
	local home="$1/home" repo="$1/repo" tmux_dir="$1/tmux" bin="$1/bin"
	mkdir -p "$home" "$repo" "$tmux_dir" "$bin"

	git -C "$repo" init -q -b main
	git -C "$repo" config user.email smoke@example.com
	git -C "$repo" config user.name "Atrium Smoke"
	echo "# smoke" > "$repo/README.md"
	git -C "$repo" add -A
	git -C "$repo" -c commit.gpgsign=false commit -qm "init"

	# A headless run writes a full default config.json. seededDefaultConfig probes
	# PATH and would add a real "claude" profile that the new-session form's picker
	# selects over any -p flag, so rewrite default_program AND profiles to the fake
	# agent, and disable auto-attach so the tape drives attach/detach explicitly.
	HOME="$home" TMUX_TMPDIR="$tmux_dir" "$ATR_BIN" </dev/null >/dev/null 2>&1 || true
	local cfg="$home/.atrium/config.json"
	[[ -f "$cfg" ]] || { echo "smoke: expected seeded config at $cfg" >&2; return 1; }
	# Write through a sibling temp under $cfg (inside $WORK, so the EXIT trap
	# reaps it even if jq fails) rather than mktemp's system-temp file.
	jq --arg p "$FAKE" \
		'.default_program=$p | .profiles=[{name:$p,program:$p}] | .auto_attach=false' \
		"$cfg" > "$cfg.tmp" && mv "$cfg.tmp" "$cfg"

	# Launcher the tape invokes by name (keeps basic-flow.tape static/committable).
	cat > "$bin/atrium-smoke-launch" <<EOF
#!/usr/bin/env bash
cd "$repo"
exec "$ATR_BIN"
EOF
	chmod +x "$bin/atrium-smoke-launch"

	cat > "$1/env" <<EOF
export HOME="$home"
export TMUX_TMPDIR="$tmux_dir"
export PATH="$bin:$PATH"
unset CLAUDE_CONFIG_DIR
EOF
}

# vhs's .txt Output holds the terminal's FULL scrollback (every repaint), which is
# ~800 lines and brittle. Reduce it to the final visible screen — the attached
# fake-agent pane — anchored on the last context-bar header ("repo · smoke"):
# strip trailing whitespace, collapse blank runs, and trim trailing blanks so
# minor padding differences don't churn the golden.
extract_screen() {
	awk '
		{ l[NR] = $0 }
		index($0, "repo · smoke") { hdr = NR }
		END {
			if (hdr == 0) { print "ERROR: no attached agent header found" > "/dev/stderr"; exit 1 }
			start = (hdr > 1) ? hdr - 1 : 1
			n = 0
			for (i = start; i <= NR; i++) { x = l[i]; sub(/[ \t]+$/, "", x); a[++n] = x }
			m = 0
			for (i = 1; i <= n; i++) {
				if (a[i] == "") { if (blank) continue; blank = 1 } else blank = 0
				r[++m] = a[i]
			}
			while (m > 0 && r[m] == "") m--
			for (i = 1; i <= m; i++) print r[i]
		}
	' "$1"
}

run_once() {
	local idx="$1" out="$2"
	local dir="$WORK/run-$idx"
	setup_env "$dir"
	(
		# shellcheck disable=SC1091
		source "$dir/env"
		cd "$dir"
		# Reap the isolated tmux server however this subshell exits — success,
		# vhs failure, or signal. A subshell doesn't inherit the outer EXIT
		# trap, so without this a failed vhs run would orphan the server (and
		# the day-sleeping fake agent). TMUX_TMPDIR is set, so this only ever
		# touches the per-run socket, never the user's real Atrium server.
		trap 'tmux -L atrium kill-server >/dev/null 2>&1 || true' EXIT
		vhs "$SMOKE_DIR/basic-flow.tape" >/dev/null 2>"$dir/vhs.log" || {
			echo "smoke: vhs failed (run $idx); last log lines:" >&2; tail -20 "$dir/vhs.log" >&2; exit 1;
		}
	)
	[[ -f "$dir/capture.txt" ]] || { echo "smoke: no capture.txt produced (run $idx)" >&2; return 1; }
	extract_screen "$dir/capture.txt" > "$out"
}

first=""
for i in $(seq 1 "$RUNS"); do
	out="$WORK/screen-$i.txt"
	run_once "$i" "$out"
	if [[ -z "$first" ]]; then
		first="$out"
	elif ! diff -u "$first" "$out" >/dev/null; then
		echo "smoke: NONDETERMINISTIC — run $i differs from run 1:" >&2
		diff -u "$first" "$out" >&2 || true
		exit 1
	fi
done
echo "smoke: $RUNS runs produced identical final screens ✓"

if [[ "${UPDATE:-0}" == "1" ]]; then
	mkdir -p "$TESTDATA"
	cp "$first" "$GOLDEN"
	echo "smoke: golden updated → $GOLDEN"
	exit 0
fi
if [[ ! -f "$GOLDEN" ]]; then
	echo "smoke: no golden yet; run with UPDATE=1 to create $GOLDEN" >&2
	exit 1
fi
if diff -u "$GOLDEN" "$first"; then
	echo "smoke: matches golden ✓"
else
	echo "smoke: DIFFERS from golden (run with UPDATE=1 if intended)" >&2
	exit 1
fi
