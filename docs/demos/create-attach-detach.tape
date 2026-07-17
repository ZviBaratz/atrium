# create-attach-detach.tape — the core loop: make a session, attach, detach.
#
# Rendered by docs/demos/render.sh, which builds the binary and exports an
# isolated HOME + TMUX_TMPDIR + PATH (so `atrium-demo-launch` resolves) before
# invoking vhs. Deliberately slow-paced: multi-second Sleeps per step so the GIF
# is followable (the standing complaint about TUI READMEs is animations too fast
# to read).

Output create-attach-detach.gif

Set Shell "bash"
Set FontSize 18
Set Width 1200
Set Height 700
Set WaitTimeout 30s
Set TypingSpeed 60ms
Set PlaybackSpeed 1.0

Type "atrium-demo-launch"
Enter
Wait+Screen /get started/
Sleep 2s

# Prime input past the first-frame WindowSize round-trip (first keystroke can be
# swallowed), then open the new-session form.
Escape
Sleep 1s
Type "n"
Wait+Screen /New session/
Sleep 2s
Type "refactor-parser"
Sleep 2s
Ctrl+S

# Row appears; let the session spin up.
Wait+Screen /refactor-parser/
Sleep 3s

# Attach and watch the agent pane come alive.
Enter
Wait+Screen /DEMO_AGENT_READY/
Sleep 3s

# Detach back to the list.
Ctrl+Q
Wait+Screen /refactor-parser/
Sleep 3s
