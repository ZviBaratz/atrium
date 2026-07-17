# pause-resume.tape — park a session (commit + free its worktree), then restore it.
#
# Rendered by docs/demos/render.sh. Pause commits the session's changes and
# removes the worktree, keeping the branch; resume recreates it. Slow-paced.

Output pause-resume.gif

Set Shell "bash"
Set FontSize 18
Set Width 1200
Set Height 700
Set WaitTimeout 30s
Set TypingSpeed 60ms

Type "atrium-demo-launch"
Enter
Wait+Screen /get started/
Sleep 2s

Escape
Sleep 1s
Type "n"
Wait+Screen /New session/
Sleep 1s
Type "wip-task"
Sleep 1s
Ctrl+S
Wait+Screen /wip-task/
Sleep 3s

# Pause: commit changes + free the worktree. The row stays, marked paused.
Type "p"
Sleep 4s

# Resume: recreate the worktree and bring the session back.
Type "r"
Sleep 4s
