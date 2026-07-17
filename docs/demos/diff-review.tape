# diff-review.tape — review a session's changes in the Diff tab, then push.
#
# Rendered by docs/demos/render.sh with an *editing* fake agent that writes a
# tracked file on start, so the Diff tab shows a real change. Slow-paced for
# readability.

Output diff-review.gif

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
Type "add-feature"
Sleep 1s
Ctrl+S
Wait+Screen /add-feature/
Sleep 3s

# Jump to the Diff tab (2) to review the agent's change without attaching.
Type "2"
Sleep 3s
# Scroll through the diff.
Down
Down
Down
Sleep 3s
# Back to the preview tab.
Type "1"
Sleep 2s
