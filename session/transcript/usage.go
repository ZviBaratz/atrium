package transcript

// How much of its context window has a session burned? (#596)
//
// Claude Code writes the answer locally on every turn, on the same JSONL entry
// LatestModel already reads: message.usage. Context occupancy at that turn is
//
//	input_tokens + cache_read_input_tokens + cache_creation_input_tokens
//
// The formula was validated against Claude Code's own rotating idle hint
// ("new task? /clear to save <N>k tokens"), which is a free oracle for it:
// measured on three live sessions the transcript sum and the hint agreed to
// within ~1%, with both-signed deltas — consistent with the hint being computed
// a beat later than the last assistant entry, not with a systematic offset.
//
// The hint is only an oracle, never a data source. It was measured and rejected
// as one: it is a transient entry in a rotating carousel (present in 6 of 18
// live panes, and observed being replaced minutes later), and it sits above the
// input box, outside footerBelowBox's region.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
)

// usageMaxBytes caps the tail parsed for the context reading. Usage rides every
// assistant entry, so this needs to span one entry rather than a conversation;
// it matches modelMaxBytes so the poll-path readers have the same worst case.
const usageMaxBytes = 128 * 1024

// Usage is a point-in-time context reading: how many tokens the conversation
// occupied at its newest real assistant turn, plus the model of that same
// entry.
//
// The two travel together on purpose. Turning the count into a percentage needs
// a denominator, and taking the model from anywhere else — Instance.ModelInfo,
// a later poll — risks pairing one turn's count with another turn's ceiling.
// Reading both off one entry makes that impossible by construction.
//
// A zero ContextTokens means "no reading", which is how every non-Claude
// profile, unparsable transcript and unstarted session reports.
type Usage struct {
	ContextTokens int
	Model         string
}

// LatestUsage returns the context occupancy of the newest non-sidechain,
// non-synthetic assistant entry in the newest transcript for (program,
// workingDir).
//
// It mirrors LatestModel's contract exactly, including the stamp gate: when the
// newest transcript's (path, mtime, size) equal prev it returns (Usage{}, prev,
// nil) without reading the file, so an unchanged session costs one ReadDir +
// Stat. A parsed tail containing no qualifying entry likewise returns a zero
// Usage under an advanced stamp: the caller keeps its last value and won't
// re-parse the same bytes. Non-claude programs return ErrUnsupported.
//
// It honors ctx the same way: an already-cancelled context returns before any
// filesystem I/O, and a cancellation mid-scan aborts the tail read.
//
// This is a second reader over the file LatestModel already scans, in the shape
// EndedAsking established — its own stamp, its own scan. On an unchanged
// transcript that is one extra ReadDir + Stat per tick and no file open; on a
// changed one, a second <=128KB tail parse. Folding the usage field into
// LatestModel's return would save that second scan at the cost of collapsing
// two independent poll-path readers into one, which is a bigger change than the
// scan is worth.
func LatestUsage(ctx context.Context, program, workingDir string, prev Stamp, opts Options) (Usage, Stamp, error) {
	if !(claudeAdapter{}).supports(program) {
		return Usage{}, prev, ErrUnsupported
	}
	if err := ctx.Err(); err != nil {
		return Usage{}, prev, err
	}
	// Apply the usage-sized tail cap before applyDefaults, which would fill a
	// zero MaxBytes with the render path's larger default.
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = usageMaxBytes
	}
	opts = applyDefaults(opts)

	path, err := newestTranscript(filepath.Join(opts.Root, "projects", sanitizeCWD(workingDir)))
	if err != nil {
		return Usage{}, prev, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return Usage{}, prev, err
	}
	stamp := Stamp{Path: path, ModTime: info.ModTime(), Size: info.Size()}
	if stamp.Equal(prev) {
		return Usage{}, prev, nil
	}

	var usage Usage
	if _, err := scanTail(ctx, path, opts.MaxBytes, func(line []byte) {
		if u, ok := decodeUsage(line); ok {
			usage = u // last qualifying entry wins
		}
	}); err != nil {
		return Usage{}, prev, err
	}
	return usage, stamp, nil
}

// decodeUsage returns the context reading of one JSONL line when it is a
// non-sidechain, non-synthetic assistant entry carrying a usage object; ok is
// false for everything else (malformed lines included — the render path
// tolerates them the same way).
//
// The guards are decodeModel's, for the reasons that file states plus one this
// reader makes sharper. A sidechain entry would show a sub-agent's context in
// place of the main thread's. A "<synthetic>" entry — the placeholder Claude
// Code fabricates for an API error — carries a usage object whose every field
// is zero (verified against a real one), so admitting it would not merely show
// a wrong number: it would drop the chip to 0 for the whole turn after any API
// error, which reads as a bug rather than as missing data.
func decodeUsage(line []byte) (Usage, bool) {
	var raw rawEntry
	if err := json.Unmarshal(line, &raw); err != nil {
		return Usage{}, false
	}
	if raw.IsSidechain || raw.Type != "assistant" {
		return Usage{}, false
	}
	var msg rawMessage
	if err := json.Unmarshal(raw.Message, &msg); err != nil {
		return Usage{}, false
	}
	if msg.Model == "" || msg.Model == syntheticModel || msg.Usage == nil {
		return Usage{}, false
	}
	tokens := msg.Usage.InputTokens + msg.Usage.CacheReadInputTokens + msg.Usage.CacheCreationInputTokens
	if tokens <= 0 {
		// An all-zero usage object carries no reading. Reporting it as one would
		// clobber the caller's last known value with a 0.
		return Usage{}, false
	}
	return Usage{ContextTokens: tokens, Model: msg.Model}, true
}
