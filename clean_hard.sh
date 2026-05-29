#!/usr/bin/env bash
# Dev hard reset: like clean.sh, but also prune git's worktree registrations.
# Cleans both the new (atrium) and legacy (claudesquad) layouts.
tmux -L atrium kill-server 2>/dev/null
tmux -L claudesquad kill-server 2>/dev/null
rm -rf worktree*
rm -rf ~/.atrium ~/.claude-squad
git worktree prune
