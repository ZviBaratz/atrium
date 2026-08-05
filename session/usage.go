package session

// Per-session context occupancy: how much of its context window a session has
// burned (#596). One source only — the transcript (transcript.LatestUsage) —
// unlike the model, which arbitrates transcript truth against a --model flag.
// There is no launch-flag analogue of a token count, so a session with no
// reading has none, and the UI shows nothing rather than a zero.

import "github.com/ZviBaratz/atrium/session/transcript"

// UsageInfo returns the session's last known context reading. A zero
// ContextTokens means "no reading": a non-claude program, an unparsable or
// absent transcript, or a session that has not taken a turn yet. Callers render
// nothing for it — an absent chip, never a "0".
func (i *Instance) UsageInfo() transcript.Usage { return i.contextUsage }

// SetUsageMeta records a context-extraction result. Main thread only (like
// SetModelMeta). A zero-token reading always advances the stamp — so the
// parsed-but-empty window isn't re-read next tick — and what it does to the
// stored value depends on WHICH transcript produced it:
//
//   - Same file as last time: keep the previous number. This is what holds the
//     chip steady across an API error — Claude Code fabricates a "<synthetic>"
//     assistant entry for one, decodeUsage refuses it, and the row would
//     otherwise drop to no chip at all the moment a request failed.
//   - A different file: clear it. A new transcript path means a new
//     conversation (/clear starts one, and so does resuming after a pause), and
//     the old conversation's occupancy is not a stale reading of the new one —
//     it is a reading of something else entirely. Carrying it forward paints a
//     red 92% over an empty conversation until the first turn lands, which is
//     the one failure mode this feature was built to avoid.
//
// Distinguishing them needs nothing but the path already on the stamp: the two
// cases are "same conversation, nothing new to say" and "different
// conversation, nothing said yet".
func (i *Instance) SetUsageMeta(u transcript.Usage, stamp transcript.Stamp) {
	switch {
	case u.ContextTokens > 0:
		i.contextUsage = u
	case stamp.Path != i.usageStamp.Path:
		i.contextUsage = transcript.Usage{}
	}
	i.usageStamp = stamp
}

// ClearUsage drops the stored reading and its memo. Main thread only.
//
// The poll layer calls it for a session whose reading must not be taken at all
// — an ambiguous transcript source (see ContextSourceKey), or the chip switched
// off in config. Clearing rather than merely declining to refresh is the whole
// point: a value that is only hidden stays in the field, and resurfaces the
// moment the condition that hid it goes away — the neighbour is killed, or the
// setting is switched back on — as a number that was never trustworthy and is
// now also old. Zeroing the stamp with it is what lets the next legitimate tick
// re-read instead of short-circuiting on a memo of bytes nobody kept.
func (i *Instance) ClearUsage() {
	i.contextUsage = transcript.Usage{}
	i.usageStamp = transcript.Stamp{}
}

// ContextSourceKey identifies the transcript file set this session's context
// reading comes from, for the fleet-level ambiguity check the poll layer runs
// (transcript.ProjectDir). "" means the session reads nothing — unstarted, a
// non-claude program, or no resolvable working directory — so it can neither
// hold a reading nor spoil anyone else's.
//
// Paused sessions deliberately still report a key: a paused session's
// transcript stays on disk and is as likely as not to be the newest-mtime file
// in its directory, so it spoils a running neighbour's reading exactly as a
// running one would. WorkingDir() keeps returning the worktree path across a
// pause even though pause removes the tree, which is what makes that possible —
// the directory is gone, the transcripts it produced are not.
func (i *Instance) ContextSourceKey() string {
	if !i.Started() {
		return ""
	}
	return transcript.ProjectDir(i.Program, i.WorkingDir(),
		transcript.Options{Root: i.claudeConfigDir})
}

// ComputeUsage re-extracts the transcript context reading off the main thread
// (the metadata-poll goroutine), gated by the memoized stamp so an idle session
// costs one ReadDir + Stat per tick. ok=false means nothing to apply:
// unstarted/paused, non-claude program, transcript unavailable, or unchanged.
//
// Like ComputeModel it derives its lifecycle context from i.baseContext() (= the
// app ctx) rather than taking a ctx parameter, so app shutdown cancels an
// in-flight transcript read.
func (i *Instance) ComputeUsage() (usage transcript.Usage, stamp transcript.Stamp, ok bool) {
	if !i.isStarted() || i.Paused() {
		return transcript.Usage{}, transcript.Stamp{}, false
	}
	u, s, err := transcript.LatestUsage(i.baseContext(), i.Program, i.WorkingDir(), i.usageStamp,
		transcript.Options{Root: i.claudeConfigDir})
	if err != nil || s.Equal(i.usageStamp) {
		return transcript.Usage{}, transcript.Stamp{}, false
	}
	return u, s, true
}
