package transcript

// A session's estimated spend, summed offline from its Claude Code transcripts
// (#392).
//
// This is the cumulative sibling of LatestUsage. They read the same JSONL and
// almost nothing else about them is shared, because the two questions differ in
// every way that shapes a reader:
//
//   - Occupancy is a POINT reading — the newest qualifying entry wins — so a
//     128KB tail window is not merely sufficient, it is the correct scope. Cost
//     is a SUM over every entry ever written, so a window is simply wrong.
//   - Occupancy reads ONE file, the newest under the project dir. Cost reads the
//     whole directory, subagent trees included; see costFiles.
//   - Occupancy must SKIP sidechain entries, because a sub-agent's context is not
//     the main thread's. Cost must COUNT them: a sub-agent's tokens are billed.
//   - Occupancy needs no deduplication, because "last one wins" is idempotent
//     over duplicates. Cost is destroyed by them; see costFileState.LastKey.
//
// What the number means, and does not. It is the same arithmetic Claude Code's
// own /usage Session block performs — local token counts at published list rates
// — and carries the same caveats Claude Code states for its own figure: on a
// Pro/Max subscription it "isn't relevant for billing purposes", and on the API
// it may differ from the actual invoice. It is rendered with a "~" for that
// reason. See session/agent/pricing.go for the rates and their provenance.
//
// Two scoping decisions worth knowing, both deliberate:
//
// It sums the WHOLE project directory rather than the newest conversation, so it
// survives /clear and answers "what has this session cost" rather than "what has
// this conversation cost". That is sound because Atrium gives every git-backed
// session a worktree path ending in a nanosecond timestamp
// (git.resolveWorktreePaths), and restores that exact path across pause/resume
// (git.NewWorktreeFromStorage) — so the project directory belongs to exactly one
// Atrium session for its whole life. Two edges it does not cover: renaming a
// session MOVES the worktree, stranding everything spent under the old path; and
// a direct (non-git) session's working dir is shared, which is the case app's
// usagePolicy refuses outright.
//
// It is a LOWER bound whenever anything in the transcript cannot be priced —
// an unrecognized model id, or fast mode on a model with no published fast rate.
// Cost.Unpriced counts those and the chip switches its "~" for a ">". That is the
// same visible-degradation property the model tables are built around: a stale
// price table shows as a bound the user can see, never as a confident wrong
// number.
//
// How far this has actually been checked, since "same arithmetic as /usage" is a
// claim about method and not a measurement. An independent reimplementation of
// these rules, written in another language against the same files, agrees to the
// cent on a real 32-file session: $263.3350 over 2,139 requests, both figures
// identical. Every rate, multiplier and boundary is pinned against Anthropic's
// published catalog in session/agent/pricing_test.go. What has NOT been done is
// a comparison against Claude Code's own /usage Session block, which renders only
// inside its TUI and covers only the current conversation where this covers the
// whole session — so the two would not be expected to match anyway. Treat the
// method claim as sourced from Claude Code's documentation, not as verified
// against its output.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/ZviBaratz/atrium/session/agent"
)

// subagentsDir is the directory name Claude Code nests a conversation's
// sub-agent transcripts under: <project>/<session-uuid>/subagents/, with a
// further workflows/wf_<id>/ level below it for workflow-spawned agents.
//
// Reading it is not an optional refinement. isSidechain never appears in a main
// transcript on a current Claude Code build — all 233,416 occurrences in the
// development corpus live in these files — and on a measured session they
// carried 54% of the requests, 52.5% of the output tokens and 60% of the cache
// writes. A cost that skipped them would be roughly half the truth, which is
// worse than no cost at all.
const subagentsDir = "subagents"

// usageKey is the byte prefilter every line passes before any JSON decoding.
// Most bytes in a transcript are tool results, which carry no usage object, so
// this rejects the bulk of a multi-megabyte file for the price of a memchr. The
// same trick LoadCheckpoints uses for its three record types.
var usageKey = []byte(`"usage"`)

// Cost is a session's estimated spend and the honesty that has to travel with it.
type Cost struct {
	// USD is the estimate at published list rates. Zero means nothing priceable
	// was found, which the renderer shows as no chip rather than as $0.00.
	USD float64
	// Requests is how many distinct API calls the estimate covers, after
	// deduplication. Exposed because it is the number that makes a suspicious
	// estimate diagnosable.
	Requests int
	// Unpriced is how many requests carried real tokens that no rate could be
	// found for. Non-zero makes USD a lower bound; see Cost.Partial.
	Unpriced int
}

// Partial reports whether the estimate is a lower bound because some requests
// could not be priced. The renderer marks a partial estimate differently, so a
// price table that has gone stale is visible on the row rather than silently
// folded into a smaller number.
func (c Cost) Partial() bool { return c.Unpriced > 0 }

// costFileState is one transcript file's resume point and running subtotal.
type costFileState struct {
	// Offset is the first byte not yet consumed — always just past a newline, so
	// a half-written tail is re-read rather than skipped. See scanFrom.
	Offset int64
	// Size and ModTime are the change gate. Matching both means the file has not
	// been touched since the last pass, so it is never opened.
	Size    int64
	ModTime int64
	// LastKey is the (message.id, requestId) pair of the most recently counted
	// request, and it is the entire deduplication mechanism.
	//
	// Claude Code writes one JSONL line per content block of an API response,
	// each carrying a full copy of the same usage object, so summing lines
	// overstates badly — measured at 1.78x on cache reads, 2.26x on output and
	// 2.46x on cache writes for one real transcript (1837 lines, 983 requests).
	// A set of every key ever seen would fix that and would also grow without
	// bound and have to be persisted across the incremental boundary.
	//
	// It does not need to be a set, because the duplicates are always ADJACENT:
	// across 31,550 usage-bearing entries in 120 sampled transcripts, not one
	// repeated key was separated by an intervening request. So remembering the
	// previous key is sufficient, is O(1), and survives being carried across a
	// resume — which is what makes the incremental read possible at all.
	LastKey string
	// The subtotal for this file, accumulated across however many passes it took.
	USD      float64
	Requests int
	Unpriced int
}

// CostCursor is where a session's cost reader resumes. It is opaque on purpose:
// the caller stores it beside the instance and hands it back, exactly as it does
// with a Stamp, and has no business reaching inside.
//
// The zero value is a cold start and re-reads everything.
type CostCursor struct {
	files map[string]costFileState
}

// LatestCost sums the estimated cost of every transcript Claude Code has written
// for workingDir, resuming from prev.
//
// It returns ErrUnsupported for a non-claude program, so a codex/gemini/aider
// session degrades to no chip rather than to a wrong one. A missing or empty
// project directory is NOT an error: a session that has not talked to the model
// yet has legitimately spent nothing, and reporting that as a failure would make
// the caller hold a stale total.
//
// Cost, per call: one ReadDir of the project directory plus one Stat per
// transcript, and a file is opened only when its size or mtime moved.
//
// Measured over every Atrium session in the development corpus — 761 project
// directories, 157,359 requests — the warm pass is 2.6ms at its worst and a few
// hundred microseconds typically, which is why this rides the ordinary 500ms
// poll tick rather than needing a cadence of its own. The cold pass reads every
// byte once: 315ms for the largest directory there (64.2MB across the main and
// sub-agent trees). That happens once per session per Atrium start, because the
// cursor is not persisted — see Instance.costCursor for why.
func LatestCost(ctx context.Context, program, workingDir string, prev CostCursor, opts Options) (Cost, CostCursor, error) {
	if !(claudeAdapter{}).supports(program) {
		return Cost{}, prev, ErrUnsupported
	}
	if err := ctx.Err(); err != nil {
		return Cost{}, prev, err
	}
	if workingDir == "" {
		return Cost{}, prev, ErrUnsupported
	}

	opts = applyDefaults(opts)
	paths, err := costFiles(claudeProjectDir(opts.Root, workingDir))
	if err != nil {
		return Cost{}, prev, err
	}

	// A fresh map rather than a mutation of prev's. The previous cursor belongs to
	// the main thread while this runs on a poll goroutine, and dropping files that
	// have vanished is a deletion the caller must not observe half-done.
	next := CostCursor{files: make(map[string]costFileState, len(paths))}
	var total Cost

	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			// Vanished between the walk and the Stat, or unreadable. Drop it from the
			// cursor: keeping a subtotal for a file that is no longer there would
			// report spend the directory no longer accounts for.
			continue
		}

		state := prev.files[path]
		switch {
		case state.Size == info.Size() && state.ModTime == info.ModTime().UnixNano():
			// Untouched since the last pass. Carry the subtotal without opening it.
		case info.Size() < state.Offset:
			// Shorter than what was already consumed, so it was rewritten rather than
			// appended to and the subtotal describes bytes that no longer exist.
			// Start it over.
			state = costFileState{}
			fallthrough
		default:
			state, err = accumulate(ctx, path, state)
			if errors.Is(err, fs.ErrNotExist) {
				// The same race the Stat above catches, one line later: the file went
				// away between being listed and being opened. Skipping it there and
				// aborting here would mean a transcript deleted mid-pass threw away
				// every subtotal already gathered, so the next tick redid the whole
				// cold read for a file that was simply gone.
				//
				// Uncovered by design rather than by omission: reaching this needs a
				// deletion in the window between two syscalls, which no fixture can
				// schedule. Its two neighbours ARE tested — a file absent at Stat time
				// (skipped) and a file present but unreadable (fails the pass) — so
				// what is untested here is the branch, not the policy it implements.
				continue
			}
			if err != nil {
				// Anything else — EACCES, EIO, a stale mount — is a failure to read
				// something that may well be there. Skipping it would silently
				// under-report the session's spend, which is worse than losing a pass:
				// the caller keeps its previous total and the next tick retries.
				return Cost{}, prev, err
			}
			state.Size, state.ModTime = info.Size(), info.ModTime().UnixNano()
		}

		next.files[path] = state
		total.USD += state.USD
		total.Requests += state.Requests
		total.Unpriced += state.Unpriced
	}

	return total, next, nil
}

// accumulate reads path from state.Offset to its last complete line, folding
// every priceable request into state.
func accumulate(ctx context.Context, path string, state costFileState) (costFileState, error) {
	consumed, err := scanFrom(ctx, path, state.Offset, func(line []byte) {
		if !bytes.Contains(line, usageKey) {
			return
		}
		key, req, ok := decodeCost(line)
		if !ok {
			return
		}
		// The dedup gate. An empty key cannot be matched against, so an entry
		// carrying neither id is counted rather than silently merged into its
		// neighbour — over-counting one malformed entry beats dropping a real one.
		if key != "" && key == state.LastKey {
			return
		}
		state.LastKey = key

		usd, priced := agent.ClaudeCost(req)
		if !priced {
			state.Unpriced++
			return
		}
		state.USD += usd
		state.Requests++
	})
	state.Offset = consumed
	if err != nil {
		return state, err
	}
	return state, nil
}

// costFiles lists every transcript under a Claude project directory: the
// conversations directly inside it, and the sub-agent transcripts nested under
// each conversation's <session-uuid>/subagents/ tree (which has a further
// workflows/wf_<id>/ level a non-recursive glob would miss).
//
// A missing project directory returns no files and no error — see LatestCost.
// Anything else that fails is reported, because "unreadable" and "empty" must
// not look the same to a total that is meant to only ever grow.
func costFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	var paths []string
	for _, e := range entries {
		if !e.IsDir() {
			if strings.HasSuffix(e.Name(), ".jsonl") {
				paths = append(paths, filepath.Join(dir, e.Name()))
			}
			continue
		}
		// Every subdirectory is a candidate conversation directory. Walking only
		// the ones whose name matches a .jsonl beside them would be tidier and would
		// also silently drop a sub-agent tree whose parent transcript was deleted —
		// spend that was real.
		sub := filepath.Join(dir, e.Name(), subagentsDir)
		werr := filepath.WalkDir(sub, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				// An unreadable branch is skipped rather than failing the whole walk:
				// the rest of the tree still describes real spend, and the alternative
				// is a session with no chip because one directory lost its permissions.
				if d != nil && d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if !d.IsDir() && strings.HasSuffix(d.Name(), ".jsonl") {
				paths = append(paths, p)
			}
			return nil
		})
		if werr != nil {
			return nil, werr
		}
	}
	return paths, nil
}

// decodeCost turns one JSONL line into a dedup key and a priceable request.
//
// ok is false for everything that must not contribute: malformed JSON, a
// non-assistant entry, an entry with no usage object, the <synthetic> model
// Claude Code writes on an API error, and an entry whose token counts are all
// zero. That last rule is what keeps a failed turn from counting as a request:
// a synthetic entry carries a full usage object with every field at zero, and it
// is also the one entry type with no requestId, so it would defeat the dedup key
// as well.
//
// Sidechain entries are NOT skipped — unlike decodeUsage, which must skip them.
// A sub-agent's tokens are billed to the same account, and they are most of the
// bill.
func decodeCost(line []byte) (key string, req agent.Request, ok bool) {
	var raw rawEntry
	if err := json.Unmarshal(line, &raw); err != nil {
		return "", agent.Request{}, false
	}
	if raw.Type != "assistant" || len(raw.Message) == 0 {
		return "", agent.Request{}, false
	}

	var msg rawMessage
	if err := json.Unmarshal(raw.Message, &msg); err != nil {
		return "", agent.Request{}, false
	}
	if msg.Usage == nil || msg.Model == "" || msg.Model == syntheticModel {
		return "", agent.Request{}, false
	}

	tokens := costTokens(msg.Usage)
	if tokens == (agent.Tokens{}) {
		return "", agent.Request{}, false
	}

	// An entry with neither identifier gets an EMPTY key, not a key made of two
	// empty halves. The difference is not cosmetic: "\x00" is a perfectly good
	// map-of-one key, so two consecutive id-less entries would compare equal and
	// the second would be silently dropped as a duplicate. Over-counting one
	// malformed entry beats losing a real request, and the empty key is what
	// tells accumulate not to compare at all.
	key = msg.ID + "\x00" + raw.RequestID
	if msg.ID == "" && raw.RequestID == "" {
		key = ""
	}

	return key, agent.Request{
		Model:        msg.Model,
		At:           parseStamp(raw.Timestamp),
		Speed:        msg.Usage.Speed,
		InferenceGeo: msg.Usage.InferenceGeo,
		Tokens:       tokens,
	}, true
}

// costTokens splits a raw usage object into the categories that price
// differently.
//
// The cache-write tiers are the subtle part. When cache_creation is present its
// two fields sum to cache_creation_input_tokens exactly (257,560,969 of
// 257,560,969 across the sampled corpus), so they are used as written. When it
// is absent — a Claude Code build older than roughly 2.1.19x — the aggregate is
// charged at the 5-minute rate, which UNDER-states rather than over-states: the
// 1h tier is the dearer one, so the fallback errs toward the lower bound the
// rest of this file is careful to preserve.
func costTokens(u *rawUsage) agent.Tokens {
	t := agent.Tokens{
		Input:     int64(u.InputTokens),
		Output:    int64(u.OutputTokens),
		CacheRead: int64(u.CacheReadInputTokens),
	}
	if u.CacheCreation != nil {
		t.CacheWrite5m = int64(u.CacheCreation.Ephemeral5mInputTokens)
		t.CacheWrite1h = int64(u.CacheCreation.Ephemeral1hInputTokens)
	} else {
		t.CacheWrite5m = int64(u.CacheCreationInputTokens)
	}
	if u.ServerToolUse != nil {
		t.WebSearches = int64(u.ServerToolUse.WebSearchRequests)
	}
	return t
}
