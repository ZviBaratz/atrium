package session

// Did the last turn end by asking the user something? (#571)
//
// The transcript rule lives in session/transcript/asked.go; this is the Instance-side
// memo, shaped like session/model.go so the poll path treats both the same way.

import (
	"github.com/ZviBaratz/atrium/session/transcript"
)

// EndedAsking reports whether the session's last turn ended with a question the user has
// not answered. It is the gate that keeps a queued follow-up from being delivered as the
// answer (see app.questionHoldsPrompt, which both dispatchers apply) and what tells a
// question apart from a plain finish on the notification ladder, which grew its own rung
// for it in #571 (notify.EventAsked).
//
// Always false for a non-claude agent: only claude has a transcript adapter, so codex,
// gemini and aider keep their existing behaviour untouched.
func (i *Instance) EndedAsking() bool { return i.endedAsking }

// SetAskedMeta records a question-check result. Main thread only (like SetModelMeta).
//
// It stores asked UNCONDITIONALLY, which is the one place this deliberately departs from
// SetModelMeta beside it. That function treats an empty model as "no information" and
// keeps the last known truth, because a model that stops being reported has not changed.
// A question is the opposite: it is answered by the next turn, so a false MUST clear a
// previous true. Carrying the old value forward the way the model does would latch the
// flag on and hold every future queued prompt for the life of the session.
func (i *Instance) SetAskedMeta(asked bool, stamp transcript.Stamp) {
	i.endedAsking = asked
	i.askedStamp = stamp
}

// ComputeAsked re-derives the question flag off the main thread (the metadata-poll
// goroutine), gated by the memoized stamp so an unchanged transcript costs one ReadDir +
// Stat. ok=false means nothing to apply: unstarted/paused, non-claude program, transcript
// unavailable, or unchanged.
//
// The caller gates this on a pane that has actually gone idle, so it runs about once per
// turn-end rather than every tick — strictly cheaper than the unconditional ComputeModel
// beside it.
//
// Like ComputeModel it derives its lifecycle context from i.baseContext() rather than
// taking a ctx parameter, so app shutdown cancels an in-flight transcript read.
func (i *Instance) ComputeAsked() (asked bool, stamp transcript.Stamp, ok bool) {
	if !i.isStarted() || i.Paused() {
		return false, transcript.Stamp{}, false
	}
	a, s, err := transcript.EndedAsking(i.baseContext(), i.Program, i.WorkingDir(), i.askedStamp,
		transcript.Options{Root: i.claudeConfigDir})
	if err != nil || s.Equal(i.askedStamp) {
		return false, transcript.Stamp{}, false
	}
	return a, s, true
}
