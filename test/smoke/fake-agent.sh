#!/usr/bin/env bash
# Deterministic fake agent for the vhs smoke spike (issue #148).
#
# A real coding agent (claude/codex/…) needs auth and produces nondeterministic,
# time-varying output — useless for a golden snapshot. This stub stands in for the
# agent program Atrium launches in a session pane (atrium -p <this script>). It:
#   - prints a fixed banner carrying a unique matcher token, then
#   - echoes a constant token for each line of input (proving keystrokes reach the
#     pane on attach without reflecting the variable input back), then
#   - idles forever so the tmux pane never dies — a dead pane would repaint the
#     screen and break the capture.
# It emits no clock, spinner, or counter, so the attached pane is byte-stable and
# the captured snapshot is reproducible across runs.
set -u

printf 'ATRIUM_SMOKE_AGENT_READY\n'
printf 'fake agent: input is echoed as a fixed token\n'

while IFS= read -r _line; do
	printf 'ATRIUM_SMOKE_AGENT_ECHO\n'
done

# stdin closed (e.g. after detach): keep the process alive so the pane stays put.
while :; do
	sleep 86400
done
