// Package transcript renders an agent program's native session transcript into
// plain text for the preview pane's scroll mode. Agent TUIs (Claude Code's
// ink-style renderer in particular) repaint the alternate screen in place, so
// tmux history is structurally empty — the program's own transcript file is the
// only surviving record of the conversation.
package transcript

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ZviBaratz/atrium/session/agent"
)

// claudeAdapter renders Claude Code's session JSONL, written to
// <root>/projects/<sanitized-cwd>/<session-uuid>.jsonl.
type claudeAdapter struct{}

// supports resolves the program through the session/agent registry, so
// "claude --continue", absolute paths, and launcher wrappers all match — one
// source of truth with the poller's per-agent heuristics.
func (claudeAdapter) supports(program string) bool {
	return agent.Resolve(program).Key == agent.KeyClaude
}

func (claudeAdapter) render(workingDir string, opts Options) (string, error) {
	dir := filepath.Join(opts.Root, "projects", sanitizeCWD(workingDir))
	path, err := newestTranscript(dir)
	if err != nil {
		return "", err
	}
	entries, truncated, err := parseTail(path, opts.MaxBytes)
	if err != nil {
		return "", err
	}
	if len(entries) == 0 {
		// A just-started session writes housekeeping lines before any
		// conversation. Erroring (→ tmux fallback) beats returning success
		// with nothing to show: the UI would frame an empty string as a
		// blank region labeled "transcript".
		return "", fmt.Errorf("no renderable entries in %s", path)
	}
	return renderEntries(entries, truncated, opts.Width), nil
}

// hasSession reports whether Claude Code has a resumable conversation for workingDir,
// applying the exact rule `claude --continue` uses: a newest non-empty *.jsonl directly
// under <root>/projects/<sanitized-cwd>. newestTranscript returns an error for a
// missing/empty dir or an empty newest file, so err == nil ⇔ there is something to resume.
func (claudeAdapter) hasSession(workingDir string, opts Options) bool {
	dir := filepath.Join(opts.Root, "projects", sanitizeCWD(workingDir))
	_, err := newestTranscript(dir)
	return err == nil
}

// sanitizeCWD maps an absolute working directory to Claude Code's project
// directory name: every rune outside [A-Za-z0-9] becomes '-'. Verified against
// real ~/.claude/projects entries — '.', '_', and '/' all map to '-'.
func sanitizeCWD(cwd string) string {
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return '-'
	}, cwd)
}

// newestTranscript returns the newest-mtime *.jsonl directly under dir — the
// live session, by the same rule `claude --continue` uses to pick what to
// resume. Session-uuid subdirectories (subagent transcripts) are not searched.
// A missing dir, no .jsonl files, or an empty newest file all return an error:
// the caller treats every error as "fall back to the tmux capture".
func newestTranscript(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	var newest string
	var newestMtime time.Time
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if newest == "" || info.ModTime().After(newestMtime) {
			newest = filepath.Join(dir, e.Name())
			newestMtime = info.ModTime()
		}
	}
	if newest == "" {
		return "", fmt.Errorf("no transcript files in %s", dir)
	}
	if info, err := os.Stat(newest); err != nil || info.Size() == 0 {
		return "", fmt.Errorf("transcript %s is empty", newest)
	}
	return newest, nil
}
