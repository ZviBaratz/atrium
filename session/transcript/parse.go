package transcript

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
)

// entry is one user or assistant turn, normalized for rendering. A plain-string
// user prompt becomes a single text block so the renderer sees one shape.
type entry struct {
	Role   string // "user" or "assistant"
	Blocks []block
}

// block is one normalized content block of an entry.
type block struct {
	Kind      string // "text", "thinking", "tool_use", "tool_result", "image"
	Text      string // text/thinking body, or flattened tool_result content
	ToolName  string // tool_use only
	ToolInput string // tool_use only: raw JSON of the input object
	IsError   bool   // tool_result only
}

// Raw JSONL shapes. Claude Code writes one JSON object per line; message
// content is either a plain string or an array of typed blocks, so both
// levels decode through json.RawMessage and are sniffed.
type rawEntry struct {
	Type             string          `json:"type"`
	IsSidechain      bool            `json:"isSidechain"`
	IsCompactSummary bool            `json:"isCompactSummary"`
	Message          json.RawMessage `json:"message"`
	// RequestID and Timestamp are read only by the cost reader (#392).
	// RequestID is half the deduplication key — Claude Code writes one line per
	// content block of a single API response, each carrying a full copy of the
	// same usage object, so a sum over lines is a sum over content blocks.
	// Timestamp picks the list rate in effect when the request was made, which
	// matters because a model's price can change under a fixed id.
	RequestID string `json:"requestId"`
	Timestamp string `json:"timestamp"`
}

type rawMessage struct {
	Content json.RawMessage `json:"content"`
	Model   string          `json:"model"` // assistant entries only; see LatestModel
	// ID is the other half of the cost reader's dedup key (see rawEntry.RequestID).
	// The two agree exactly on every transcript measured — 983 distinct values of
	// each across 1837 assistant lines, with no id spanning two requests — so
	// either alone would do; both are used because the pair is the key #298's
	// implementation notes specify and the cost of carrying it is one string.
	ID string `json:"id"`
	// Usage rides the same assistant entry as Model — extracting it costs no
	// extra I/O beyond the tail scan LatestUsage already performs. A pointer, so
	// "the entry carried no usage object" stays distinguishable from "it carried
	// an all-zero one"; the latter is what a <synthetic> API-error entry looks
	// like, and conflating them would report 0 tokens right after an error.
	Usage *rawUsage `json:"usage"`
}

// rawUsage is the token accounting Claude Code writes on every assistant entry.
//
// The three input-side fields are what LatestUsage sums for context occupancy;
// output_tokens is excluded from THAT sum deliberately — it is this turn's
// generation, not context the next turn carries — but it is decoded here because
// the cost reader does need it, and at $25/MTok against $0.50/MTok for a cache
// hit it is not a term anything can afford to drop.
//
// Everything below OutputTokens exists only for pricing (#392), and every one of
// them may be absent: Claude Code builds older than ~2.1.19x omit CacheCreation,
// ServerToolUse and Speed entirely, and the live fields are written as JSON null
// on an API-error entry. All of them therefore zero-default into "no modifier",
// which is the same path a value we do not recognize takes.
//
// service_tier is deliberately NOT decoded. It would select the Batch API's 50%
// discount, and Claude Code never batches — it was "standard" on 24,711 of
// 24,720 sampled entries and null on the rest, all of them API errors.
type rawUsage struct {
	InputTokens              int `json:"input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	OutputTokens             int `json:"output_tokens"`

	// CacheCreation splits CacheCreationInputTokens into its two priced tiers.
	// The split is the whole reason to decode it: a 1-hour write costs 2x base
	// input and a 5-minute write 1.25x, and Claude Code's subscription cache
	// lifetime is the expensive one — 90.2% of cache-write tokens in the
	// development corpus were 1h. Absent (an older build) means the reader falls
	// back to the aggregate, which it prices at the 5m rate; see costTokens.
	CacheCreation *rawCacheCreation `json:"cache_creation"`
	// ServerToolUse carries the server-side tool charges that are billed per
	// request rather than per token. Only web search costs money ($10 per 1,000);
	// web fetch is free and is not decoded.
	ServerToolUse *rawServerToolUse `json:"server_tool_use"`
	// InferenceGeo is "us" on a request pinned to US-only inference, which costs
	// 1.1x across every token category. Every entry in the development corpus
	// recorded "not_available".
	InferenceGeo string `json:"inference_geo"`
	// Speed is "fast" on a fast-mode request, which is priced at double rates on
	// the models that support it. Every entry in the development corpus recorded
	// "standard", so this path is honored rather than observed.
	Speed string `json:"speed"`
}

// rawCacheCreation is usage.cache_creation, the per-tier breakdown of
// cache_creation_input_tokens. The two fields sum to the aggregate exactly —
// 257,560,969 of 257,560,969 tokens across the sampled corpus.
type rawCacheCreation struct {
	Ephemeral5mInputTokens int `json:"ephemeral_5m_input_tokens"`
	Ephemeral1hInputTokens int `json:"ephemeral_1h_input_tokens"`
}

// rawServerToolUse is usage.server_tool_use. web_fetch_requests is omitted
// because the web fetch tool carries no charge beyond the tokens it produces.
type rawServerToolUse struct {
	WebSearchRequests int `json:"web_search_requests"`
}

type rawBlock struct {
	Type     string          `json:"type"`
	Text     string          `json:"text"`
	Thinking string          `json:"thinking"`
	Name     string          `json:"name"`
	Input    json.RawMessage `json:"input"`
	Content  json.RawMessage `json:"content"`
	IsError  bool            `json:"is_error"`
}

// scannerBufMax bounds a single transcript line. Tool results routinely exceed
// bufio.Scanner's 64KB default; 4MB covers anything observed with margin.
const scannerBufMax = 4 << 20

// parseTail parses the last maxBytes of the JSONL file at path. When the file
// is larger than maxBytes it seeks to size−maxBytes and discards everything up
// to the first newline so a half object is never fed to the decoder, reporting
// truncated=true. Malformed lines, housekeeping entry types, and sidechain
// entries are skipped; only user/assistant message entries are returned.
func parseTail(path string, maxBytes int64) (entries []entry, truncated bool, err error) {
	// The render path is a synchronous UI-thread call, not a leaked background
	// goroutine, so it is not cancellation-sensitive — pass a never-cancelled
	// context. Only the poll path (LatestModel) threads a real one.
	truncated, err = scanTail(context.Background(), path, maxBytes, func(line []byte) {
		if e, ok := decodeLine(line); ok {
			entries = append(entries, e)
		}
	})
	if err != nil {
		return nil, false, err
	}
	return entries, truncated, nil
}

// scanTail streams the lines in the last maxBytes of path to fn, discarding a
// leading partial line when the window starts mid-file (truncated=true). It is
// the shared tail-reading core under parseTail (render) and LatestModel. It
// honors ctx: an already-cancelled context is reported before any I/O, and a
// cancellation mid-scan aborts with ctx.Err() rather than reading to the end.
//
// A line past scannerBufMax is chopped into buffer-sized pieces rather than
// aborting the scan (see scanLinesSkipOverlong): none of the pieces decodes, so
// the oversized line is dropped, and every line after it is still delivered.
func scanTail(ctx context.Context, path string, maxBytes int64, fn func(line []byte)) (truncated bool, err error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}

	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return false, err
	}
	if maxBytes > 0 && info.Size() > maxBytes {
		if _, err := f.Seek(info.Size()-maxBytes, io.SeekStart); err != nil {
			return false, err
		}
		truncated = true
	}

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), scannerBufMax)
	sc.Split(scanLinesSkipOverlong)
	skipFirst := truncated
	for sc.Scan() {
		if err := ctx.Err(); err != nil {
			return truncated, err
		}
		if skipFirst {
			skipFirst = false
			continue
		}
		fn(sc.Bytes())
	}
	if err := sc.Err(); err != nil {
		return false, err
	}
	return truncated, nil
}

// scanReadBuf is the read buffer scanFrom hands bufio.Reader. It matches the
// starting buffer scanTail gives its Scanner; a longer line is assembled from
// however many buffer-fulls it takes.
const scanReadBuf = 64 * 1024

// scanFrom streams the COMPLETE lines between offset and EOF to fn, and returns
// the offset of the first byte it did not consume. It is the incremental sibling
// of scanTail: where scanTail re-reads a fixed window from the end on every call,
// scanFrom resumes where the last call stopped, which is what lets a cumulative
// sum over an append-only file cost O(what was appended) rather than O(the file).
//
// The returned offset is the load-bearing part, and it is why this cannot be
// written with bufio.Scanner. A transcript is appended to WHILE it is read, so
// the last thing in the file is routinely half a JSON object. Scanner hands that
// half back as a token at EOF, indistinguishable from a complete line; consuming
// it would advance the offset past a line the reader never decoded, and the
// remainder would never be seen again — a request silently missing from the
// total, permanently. So the offset advances only across bytes that ended in a
// newline, and a partial tail is simply left for the next call.
//
// A line past scannerBufMax is dropped rather than assembled — the same rule
// scanLinesSkipOverlong applies for the same reason — but its bytes are still
// counted, so the scan resumes cleanly after it.
func scanFrom(ctx context.Context, path string, offset int64, fn func(line []byte)) (consumed int64, err error) {
	if err := ctx.Err(); err != nil {
		return offset, err
	}

	f, err := os.Open(path)
	if err != nil {
		return offset, err
	}
	defer func() { _ = f.Close() }()

	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return offset, err
		}
	}

	r := bufio.NewReaderSize(f, scanReadBuf)
	consumed = offset

	var (
		line    []byte // fragments of the current line, when it spans buffers
		pending int64  // bytes of the current line, complete or not
		dropped bool   // the current line is past scannerBufMax
	)
	for {
		if err := ctx.Err(); err != nil {
			return consumed, err
		}

		frag, readErr := r.ReadSlice('\n')
		pending += int64(len(frag))

		if errors.Is(readErr, bufio.ErrBufferFull) {
			// More of this line is coming. frag points into the reader's buffer and
			// dies on the next read, so it has to be copied to survive.
			if !dropped && int64(len(line)+len(frag)) > scannerBufMax {
				dropped, line = true, nil
			}
			if !dropped {
				line = append(line, frag...)
			}
			continue
		}
		if readErr != nil {
			// EOF, or a real read failure. Either way the bytes accumulated since
			// the last newline are an incomplete line: leave them unconsumed so the
			// next call re-reads them once the writer has finished the line.
			if errors.Is(readErr, io.EOF) {
				readErr = nil
			}
			return consumed, readErr
		}

		// frag ends in '\n', so the line is complete and safe to consume.
		consumed += pending
		pending = 0
		if !dropped {
			body := frag[:len(frag)-1]
			if len(line) == 0 {
				// The common case: the whole line fit in one buffer, so fn can read it
				// in place. fn must not retain it past the call, which every decoder
				// here honors by unmarshalling into its own structs.
				fn(body)
			} else {
				fn(append(line, body...))
			}
		}
		line, dropped = line[:0], false
	}
}

// scanLinesSkipOverlong is bufio.ScanLines with one difference: a line that fills
// the scanner's whole buffer without a newline is emitted as a token of its own
// instead of being left to grow past scannerBufMax, where bufio.Scanner would set
// ErrTooLong and stop permanently.
//
// Stopping is the wrong answer for a transcript. The tail readers never met this
// ceiling — their window is smaller than it — but LoadCheckpoints reads the whole
// file, so one pasted blob or one oversized tool_result would otherwise discard
// every checkpoint in the transcript, including the hundreds before it. Chopping
// the line into buffer-sized pieces costs nothing: no piece is valid JSON, so
// every decoder here drops them, and the scan continues into the lines that follow.
func scanLinesSkipOverlong(data []byte, atEOF bool) (advance int, token []byte, err error) {
	advance, token, err = bufio.ScanLines(data, atEOF)
	if err != nil || token != nil || atEOF {
		return advance, token, err
	}
	// Asking for more data with the buffer already at its ceiling is exactly the
	// state bufio.Scanner turns into ErrTooLong on the next pass.
	if len(data) >= scannerBufMax {
		return len(data), data, nil
	}
	return advance, token, err
}

// decodeLine decodes one JSONL line into a normalized entry. ok is false for
// anything that should not render: malformed JSON, non-message entry types,
// sidechain entries, and messages with no recognizable blocks.
func decodeLine(line []byte) (entry, bool) {
	var raw rawEntry
	if err := json.Unmarshal(line, &raw); err != nil {
		return entry{}, false
	}
	if raw.IsSidechain || (raw.Type != "user" && raw.Type != "assistant") {
		return entry{}, false
	}
	// Compact-summary user entries are the machine-generated "continued from a
	// previous conversation" wall that bridges a context compaction. Claude Code
	// never paints it as conversation, so it would only diverge the scrollback
	// from the live view — skip it.
	if raw.IsCompactSummary {
		return entry{}, false
	}
	var msg rawMessage
	if err := json.Unmarshal(raw.Message, &msg); err != nil {
		return entry{}, false
	}
	blocks := decodeContent(msg.Content)
	if len(blocks) == 0 {
		return entry{}, false
	}
	return entry{Role: raw.Type, Blocks: blocks}, true
}

// decodeContent normalizes message content: a plain string (a real user
// prompt) becomes one text block; an array maps block-per-block. Unknown block
// types are dropped.
func decodeContent(content json.RawMessage) []block {
	var s string
	if err := json.Unmarshal(content, &s); err == nil {
		return []block{{Kind: "text", Text: s}}
	}
	var rbs []rawBlock
	if err := json.Unmarshal(content, &rbs); err != nil {
		return nil
	}
	var blocks []block
	for _, rb := range rbs {
		switch rb.Type {
		case "text":
			blocks = append(blocks, block{Kind: "text", Text: rb.Text})
		case "thinking":
			blocks = append(blocks, block{Kind: "thinking", Text: rb.Thinking})
		case "tool_use":
			blocks = append(blocks, block{Kind: "tool_use", ToolName: rb.Name, ToolInput: string(rb.Input)})
		case "tool_result":
			blocks = append(blocks, block{Kind: "tool_result", Text: flattenResult(rb.Content), IsError: rb.IsError})
		case "image":
			blocks = append(blocks, block{Kind: "image"})
		}
	}
	return blocks
}

// flattenResult extracts the text of a tool_result content payload, which is
// either a plain string or an array of blocks (text / tool_reference).
func flattenResult(content json.RawMessage) string {
	var s string
	if err := json.Unmarshal(content, &s); err == nil {
		return s
	}
	var rbs []rawBlock
	if err := json.Unmarshal(content, &rbs); err != nil {
		return ""
	}
	var parts []string
	for _, rb := range rbs {
		if rb.Type == "text" && rb.Text != "" {
			parts = append(parts, rb.Text)
		}
	}
	return strings.Join(parts, "\n")
}
