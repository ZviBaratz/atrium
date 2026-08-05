package transcript

import (
	"context"
	"encoding/json"
	"os"
	"strings"
)

// Did the last turn end by asking the user something? (#571)
//
// A turn that ends with a PROSE question is indistinguishable from a finished turn
// everywhere else in Atrium: the pane goes idle, ApplyPaneState maps it to Ready, and a
// queued follow-up is then delivered as the answer to a question the user never saw.
// Claude's own AskUserQuestion tool is already handled — it renders a selection footer
// that DetectPrompt matches, so the pane reads PanePromptManual and delivery is refused
// (session/agent/registry.go, the "selection" matcher). This file covers the one shape
// that surface does not: a question the agent typed instead of asking through the tool.
//
// The signal has to be built because none exists. Nothing on the pane, on the Instance,
// or in the hook record differs between "finished" and "stopped to ask" — verified
// before this file was written, not assumed.
//
// This is a heuristic, and the repo has been burned by heuristics (#332), so the
// difference is worth stating: those read the vendor's rendered chrome, which drifts
// with every claude release and arrives wrapped, truncated, ANSI-laden and quotable out
// of the scrollback. This reads the model's own prose out of a JSON field. It cannot
// drift with a claude version, and there is no width, wrapping or chrome to model. Its
// failure mode is judgement instead — which is why the rule below was MEASURED rather
// than reasoned about.

// Measured over 789 transcripts / 5,735 turns, across every live CLAUDE_CONFIG_DIR root
// (2026-08-02). Four candidate rules were scored and this one won on evidence:
//
//	last line ENDS WITH '?'            687 hits (11.98%)  <- the obvious rule
//	last line CONTAINS '?'             956 hits (16.67%)
//	the rule below (contains, masked)  941 hits (16.41%)  <- shipped
//	raw 300-char tail contains '?'    1007 hits (17.56%)
//
// The ends-with rule is a STRICT SUBSET of this one (|ends-with \ this| = 0 over the
// whole corpus), so widening costs nothing and recovers 254 more real asks. What it was
// missing is one structural shape, not a semantic class: the question is there, but a
// parenthetical or an explanatory sentence follows it — "…or will you do it? (I held off
// since it ends your currently-running agent process.)".
//
// Masking is what makes the widening safe. On a 54-case systematic sample of the cases
// only the wider rule catches, contains-'?' scored 51/54 and this rule scored 51/51: the
// mask removed exactly the three false positives — a quoted "which of my stated reasons
// is factually wrong?", a literal `??`, and a `?format=json` — and zero true positives.
// Sampled precision overall was 91/91.
//
// The tail rule was rejected: its 66 extra hits are almost all plain sign-offs that
// merely have a '?' somewhere in the last 300 bytes ("Otherwise, we're done.").
//
// The limit this rule structurally cannot reach: 415 turns (7.24%) end with an
// IMPERATIVE ask — "say the word", "let me know", "tell me whether" — and carry no
// question mark at all. Real recall is therefore ~69%, not ~100%. That is an acceptable
// trade only because of which way it fails: a miss is exactly today's behaviour, while a
// false positive merely holds a queued prompt until the user looks at the row.

// askedMaxBytes caps the tail parsed for the question check. Only the LAST assistant
// entry is consulted and it sits at the end of the file, so this needs to span one entry
// rather than a conversation; it matches modelMaxBytes so the two poll-path readers have
// the same worst case.
const askedMaxBytes = 128 * 1024

// EndedAsking reports whether the newest transcript for (program, workingDir) ends with
// an assistant turn that asked the user something.
//
// It mirrors LatestModel's contract exactly, including the stamp gate: when the newest
// transcript's (path, mtime, size) equal prev it returns (false, prev, nil) without
// reading the file, so an unchanged session costs one ReadDir + Stat. Non-claude
// programs return ErrUnsupported — codex, gemini and aider have no transcript adapter,
// so they keep their existing behaviour untouched.
//
// It honors ctx the same way: an already-cancelled context returns before any filesystem
// I/O, and a cancellation mid-scan aborts the tail read. The poll goroutine passes the
// session's lifecycle context, so app shutdown unwinds an in-flight read.
//
// The false it returns on an unchanged stamp is NOT a verdict — the caller distinguishes
// it by comparing the returned stamp with prev, exactly as ComputeModel does.
func EndedAsking(ctx context.Context, program, workingDir string, prev Stamp, opts Options) (bool, Stamp, error) {
	if !(claudeAdapter{}).supports(program) {
		return false, prev, ErrUnsupported
	}
	if err := ctx.Err(); err != nil {
		return false, prev, err
	}
	// Apply the question-sized tail cap before applyDefaults, which would fill a zero
	// MaxBytes with the render path's larger default.
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = askedMaxBytes
	}
	opts = applyDefaults(opts)

	path, err := newestTranscript(claudeProjectDir(opts.Root, workingDir))
	if err != nil {
		return false, prev, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return false, prev, err
	}
	stamp := Stamp{Path: path, ModTime: info.ModTime(), Size: info.Size()}
	if stamp.Equal(prev) {
		return false, prev, nil
	}

	// Last qualifying entry wins, like decodeModel. Non-message entry types (mode,
	// file-history-snapshot, ai-title, …) and sidechain entries are skipped by
	// decodeAssistantBlocks, so a sub-agent's question can never be mistaken for the main
	// turn's.
	var last []block
	if _, err := scanTail(ctx, path, opts.MaxBytes, func(line []byte) {
		if bs, ok := decodeAssistantBlocks(line); ok {
			last = bs
		}
	}); err != nil {
		return false, prev, err
	}
	return blocksEndAsking(last), stamp, nil
}

// decodeAssistantBlocks returns the normalized content blocks of one JSONL line when it
// is a non-sidechain assistant entry; ok is false for everything else (malformed lines
// included — the render path tolerates them the same way).
func decodeAssistantBlocks(line []byte) ([]block, bool) {
	var raw rawEntry
	if err := json.Unmarshal(line, &raw); err != nil {
		return nil, false
	}
	if raw.IsSidechain || raw.Type != "assistant" {
		return nil, false
	}
	var msg rawMessage
	if err := json.Unmarshal(raw.Message, &msg); err != nil {
		return nil, false
	}
	blocks := decodeContent(msg.Content)
	if len(blocks) == 0 {
		return nil, false
	}
	return blocks, true
}

// blocksEndAsking applies the measured rule to ONE assistant entry's blocks.
//
// Taking blocks from a single entry — the caller's last — is what stops a question from
// an EARLIER entry holding a prompt after the agent moved past it. An entry carrying no
// text at all (a bare tool call) therefore contributes nothing and reads as false rather
// than borrowing the previous entry's prose; empty text falls through the same path and
// yields false without a special case.
//
// The LAST text block wins WITHIN the entry, because claude interleaves prose and tool
// calls inside one turn and it is the closing prose the user is looking at. Note what
// that does and does not say: blocks after the last text block are ignored, so an entry
// shaped [text("…?"), tool_use] reads as true even though its final block is a call.
// That is deliberate — on an idle pane such an entry means the tool never produced a
// result, and holding is the safe direction — but it is not "the entry's final act was a
// tool call, so false". Pinned by TestEndedAsking_TrailingToolUseKeepsTheProse.
func blocksEndAsking(blocks []block) bool {
	text := ""
	for _, b := range blocks {
		if b.Kind == "text" && strings.TrimSpace(b.Text) != "" {
			text = b.Text
		}
	}
	return strings.Contains(maskSpans(lastProseLine(text)), "?")
}

// lastProseLine returns the last non-empty line of text with fenced code blocks removed.
// A fence toggles the skip, so an unterminated fence swallows the rest of the block —
// which is the safe direction: an unclosed fence means everything after it was meant to
// be code.
func lastProseLine(text string) string {
	var last string
	inFence := false
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			last = trimmed
		}
	}
	return last
}

// maskSpans blanks out `inline code` and "quoted" spans so a '?' inside one cannot read
// as a question. This is the whole reason the widened rule is safe to ship: the three
// false positives it removed from the measured margin were a quoted question, a literal
// `??` and a `?format=json`, and it removed no true positives.
//
// An unterminated opener is left alone rather than masking to end-of-line: prose uses
// apostrophes and stray backticks far more often than it quotes a question, so treating
// one as an unclosed span would silence real asks. Both quote characters claude actually
// emits are handled — ASCII " and the typographic pair “ ” its prose prefers.
func maskSpans(line string) string {
	closers := map[rune]rune{'`': '`', '"': '"', '“': '”'}
	runes := []rune(line)
	out := make([]rune, 0, len(runes))
	for i := 0; i < len(runes); i++ {
		closer, isOpener := closers[runes[i]]
		if !isOpener {
			out = append(out, runes[i])
			continue
		}
		end := -1
		for j := i + 1; j < len(runes); j++ {
			if runes[j] == closer {
				end = j
				break
			}
		}
		if end < 0 {
			out = append(out, runes[i])
			continue
		}
		for range runes[i : end+1] {
			out = append(out, ' ')
		}
		i = end
	}
	return string(out)
}
