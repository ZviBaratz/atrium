#!/usr/bin/env bash
# Dev reset: kill Atrium's tmux server(s) and remove its data dir + local worktrees.
# Cleans both the new (atrium) and legacy (claudesquad) layouts.
tmux -L atrium kill-server 2>/dev/null
tmux -L claudesquad kill-server 2>/dev/null
rm -rf worktree*
rm -rf ~/.atrium ~/.claude-squad
