package transcript

import (
	"bufio"
	"encoding/json"
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
	Type        string          `json:"type"`
	IsSidechain bool            `json:"isSidechain"`
	Message     json.RawMessage `json:"message"`
}

type rawMessage struct {
	Content json.RawMessage `json:"content"`
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
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return nil, false, err
	}
	if maxBytes > 0 && info.Size() > maxBytes {
		if _, err := f.Seek(info.Size()-maxBytes, io.SeekStart); err != nil {
			return nil, false, err
		}
		truncated = true
	}

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), scannerBufMax)
	skipFirst := truncated
	for sc.Scan() {
		if skipFirst {
			skipFirst = false
			continue
		}
		if e, ok := decodeLine(sc.Bytes()); ok {
			entries = append(entries, e)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, false, err
	}
	return entries, truncated, nil
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
