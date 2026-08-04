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
// SetModelMeta). A zero-token reading advances the stamp — so the
// parsed-but-empty window isn't re-read next tick — without clearing the last
// known value.
//
// That last clause is what keeps the chip steady across an API error: Claude
// Code fabricates a "<synthetic>" assistant entry for one, decodeUsage refuses
// it, and the resulting empty reading leaves the previous number standing
// instead of dropping the row to 0.
func (i *Instance) SetUsageMeta(u transcript.Usage, stamp transcript.Stamp) {
	if u.ContextTokens > 0 {
		i.contextUsage = u
	}
	i.usageStamp = stamp
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
