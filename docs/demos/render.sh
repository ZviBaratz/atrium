#!/usr/bin/env bash
# Render the committed demo tapes (docs/demos/*.tape) to GIFs with vhs.
#
# Mirrors test/smoke/run.sh's isolation: a fresh HOME (data dir + worktrees +
# state.json), a throwaway git repo, and a private TMUX_TMPDIR so the inner
# `tmux -L atrium` socket never collides with the user's real Atrium socket. Uses
# a deterministic fake agent so the GIFs don't depend on a real agent's auth or
# nondeterministic output.
#
# Deps (not in `just test`): vhs, ttyd, ffmpeg, tmux, git — installed directly,
# not via charmbracelet/vhs-action (whose linux ffmpeg installer is broken; see
# .github/workflows/smoke.yml). Run via `just gifs`.
#
#   GO=/path/to/go  override the Go toolchain
set -euo pipefail

DEMO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$DEMO_DIR/../.." && pwd)"
GO="${GO:-go}"

for bin in vhs ttyd ffmpeg tmux git; do
	command -v "$bin" >/dev/null 2>&1 || { echo "demos: missing dependency: $bin" >&2; exit 127; }
done
command -v "$GO" >/dev/null 2>&1 || { echo "demos: missing dependency: go (set GO=...)" >&2; exit 127; }

WORK="$(mktemp -d "${TMPDIR:-/tmp}/atrium-demos.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT

ATR_BIN="$WORK/atrium"
( cd "$REPO_ROOT" && "$GO" build -o "$ATR_BIN" . )

home="$WORK/home"; repo="$WORK/repo"; tmux_dir="$WORK/tmux"; bin="$WORK/bin"
mkdir -p "$home" "$repo" "$tmux_dir" "$bin"

# An editing fake agent: writes a tracked file on start so the Diff tab and
# pause (commit) have something real to show, then idles so the pane never dies.
cat > "$bin/demo-agent" <<'AGENT'
#!/usr/bin/env bash
set -u
printf 'DEMO_AGENT_READY\n'
if [ -n "${DEMO_EDIT:-}" ] && [ ! -e feature.txt ]; then
	printf 'add a small feature\n' > feature.txt
	printf 'wrote feature.txt\n'
fi
while IFS= read -r _l; do printf 'DEMO_AGENT_ECHO\n'; done
while :; do sleep 86400; done
AGENT
chmod +x "$bin/demo-agent"

git -C "$repo" init -q -b main
git -C "$repo" config user.email demo@example.com
git -C "$repo" config user.name "Atrium Demo"
printf '# demo repo\n' > "$repo/README.md"
git -C "$repo" add -A
git -C "$repo" -c commit.gpgsign=false commit -qm init

# Seed a full default config, then point it at the fake agent and disable
# auto-attach so the tapes drive attach/detach explicitly.
HOME="$home" TMUX_TMPDIR="$tmux_dir" "$ATR_BIN" </dev/null >/dev/null 2>&1 || true
cfg="$home/.atrium/config.json"
[ -f "$cfg" ] || { echo "demos: expected seeded config at $cfg" >&2; exit 1; }
python3 - "$cfg" "$bin/demo-agent" <<'PY'
import json, sys
cfg, agent = sys.argv[1], sys.argv[2]
d = json.load(open(cfg))
d["default_program"] = agent
d["profiles"] = [{"name": "demo", "program": agent}]
d["auto_attach"] = False
json.dump(d, open(cfg, "w"), indent=2)
PY

cat > "$bin/atrium-demo-launch" <<EOF
#!/usr/bin/env bash
cd "$repo"
exec env DEMO_EDIT=1 "$ATR_BIN"
EOF
chmod +x "$bin/atrium-demo-launch"

export HOME="$home" TMUX_TMPDIR="$tmux_dir" PATH="$bin:$PATH"
unset CLAUDE_CONFIG_DIR TMUX

for tape in "$DEMO_DIR"/*.tape; do
	name="$(basename "$tape")"
	echo "demos: rendering $name"
	( cd "$DEMO_DIR" && trap 'tmux -L atrium kill-server >/dev/null 2>&1 || true' EXIT && vhs "$tape" )
done
echo "demos: wrote GIFs to $DEMO_DIR"
