package transcript

import (
	"bytes"
	"context"
	"path/filepath"
)

// ForkPath is where a session forked under sessionID would be written for
// (program, workingDir): the same project directory every reader in this package
// resolves, named after the session uuid. Claude files a transcript under its own
// session id, which is what makes the path derivable rather than something to go
// hunting for with newest-mtime. "" when program has no adapter or workingDir is
// unknown, matching ProjectDir.
func ForkPath(program, workingDir, sessionID string, opts Options) string {
	dir := ProjectDir(program, workingDir, opts)
	if dir == "" || sessionID == "" {
		return ""
	}
	return filepath.Join(dir, sessionID+".jsonl")
}

// ContainsEntries reports, for each of ids, whether it appears in the transcript
// at path. An id counts as present when it is some row's own uuid, and also when
// it merely occurs in the file's raw bytes — as another row's parentUuid, or
// quoted inside a payload.
//
// That union is deliberate, because the caller's two questions are asymmetric.
// Asking "is the entry we kept still here?" wants the structural answer, and
// asking "is the turn we dropped really gone?" wants the strict one: a fork that
// silently kept the dropped turn has that uuid both on the row and on the next
// row's parentUuid, and answering "absent" because neither decoded would be the
// exact false clean bill of health the fork verification exists to prevent.
//
// The raw half also covers this package's oversized-line handling. A line past
// scannerBufMax is delivered as buffer-sized pieces that do not decode as JSON,
// so a structural-only test would silently miss a uuid living on one; the pieces
// are still byte-scanned here, with a carry-over so an id straddling a piece
// boundary is not cut in half.
//
// The whole file is read (maxBytes 0): a fork's kept prefix begins at the top.
func ContainsEntries(ctx context.Context, path string, ids ...string) (map[string]bool, error) {
	found := make(map[string]bool, len(ids))
	// wanted mirrors needles, so a skipped empty id cannot slide the two out of
	// step the way indexing back into the caller's slice would.
	wanted := make([]string, 0, len(ids))
	needles := make([][]byte, 0, len(ids))
	longest := 0
	for _, id := range ids {
		if _, seen := found[id]; id == "" || seen {
			continue
		}
		found[id] = false
		wanted = append(wanted, id)
		needles = append(needles, []byte(id))
		if len(id) > longest {
			longest = len(id)
		}
	}
	if len(needles) == 0 {
		return found, nil
	}

	var carry []byte
	if _, err := scanTail(ctx, path, 0, func(line []byte) {
		// Join the previous piece's tail so a needle split across the boundary of an
		// oversized line's pieces is still matched. Bounded by the longest needle, so
		// this holds bytes, not lines.
		hay := line
		if len(carry) > 0 {
			hay = append(append(make([]byte, 0, len(carry)+len(line)), carry...), line...)
		}
		for i, needle := range needles {
			if !found[wanted[i]] && bytes.Contains(hay, needle) {
				found[wanted[i]] = true
			}
		}
		if n := longest - 1; n > 0 && len(line) > n {
			carry = append(carry[:0], line[len(line)-n:]...)
		} else {
			carry = append(carry[:0], line...)
		}
	}); err != nil {
		return nil, err
	}
	return found, nil
}
