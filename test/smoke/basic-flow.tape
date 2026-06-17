# basic-flow.tape — Atrium end-to-end smoke spike (issue #148).
#
# Driven by test/smoke/run.sh, which builds the binary and exports an isolated
# HOME, TMUX_TMPDIR (socket isolation), and PATH (so `atrium-smoke-launch`
# resolves) before invoking vhs from a scratch dir. The capture is the final
# frame: the attached fake-agent pane, which is fully deterministic.

Output capture.txt

Set Shell "bash"
Set FontSize 16
Set Width 1200
Set Height 600
Set WaitTimeout 30s
Set TypingSpeed 20ms

# Launch Atrium against the throwaway repo + fake agent program. The title is
# ASCII-art, so match the empty-state hint instead of the literal "Atrium".
Type "atrium-smoke-launch"
Enter
Wait+Screen /get started/

# The very first keystroke after launch can be swallowed while the Bubble Tea
# model finishes its initial WindowSize round-trip, so prime input with a no-op
# Escape (inert in the default state) before the real keys.
Sleep 1s
Escape
Sleep 500ms

# Create a session: n opens the form (title focused). Wait for the form to be on
# screen before typing, then Ctrl+S submits and starts it in the background
# (worktree + tmux + fake agent).
Type "n"
Wait+Screen /New session/
Type "smoke"
Sleep 300ms
Ctrl+S

# The form closes, the new row appears, and the session starts; give it a moment
# to spin up before attaching.
Wait+Screen /smoke/
Sleep 1s

# Attach (Enter) and confirm the fake agent's pane is live.
Enter
Wait+Screen /ATRIUM_SMOKE_AGENT_READY/
Sleep 1s

# Detach (Ctrl+Q) back to the list, then re-attach — exercises the full
# attach/detach round trip while ending on the deterministic agent pane. Anchor
# the "back on the list" check on the populated-list footer (stable) rather than
# the row text (which flickers through the transition), and let the list settle
# before re-attaching so Enter isn't dropped mid-render.
Ctrl+Q
Wait+Screen /n new · s send/
Sleep 1s
Enter
Wait+Screen /ATRIUM_SMOKE_AGENT_READY/
Sleep 1s
