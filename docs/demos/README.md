# Demo GIFs

Per-flow screencasts for the README, generated from committed [vhs](https://github.com/charmbracelet/vhs) tapes so they are reproducible and reviewable as text.

| Tape | Flow |
|------|------|
| `create-attach-detach.tape` | make a session → attach → detach |
| `diff-review.tape` | review a session's change in the Diff tab |
| `pause-resume.tape` | pause (commit + free the worktree) → resume |

## Rendering

```bash
just gifs      # or: bash docs/demos/render.sh
```

`render.sh` builds the binary and drives it through vhs in a fully isolated
sandbox (fresh `HOME`, throwaway git repo, private `TMUX_TMPDIR`) with a
deterministic fake agent, so the GIFs never depend on a real agent's auth or
nondeterministic output. It writes `*.gif` next to the tapes.

The tapes are deliberately **slow-paced** — multi-second `Sleep`s per step — so
the animation is followable rather than a blur.

## Dependencies

vhs and its headless terminal `ttyd` are not in `just test`. Install them
directly (the `charmbracelet/vhs-action` GitHub action can't be used — its linux
ffmpeg installer is broken; see `.github/workflows/smoke.yml`):

```bash
sudo apt-get install -y tmux ffmpeg ttyd
go install github.com/charmbracelet/vhs@v0.11.0
```
