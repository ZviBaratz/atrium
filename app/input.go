package app

import "os"

// ss3HomeEndReader normalizes the SS3 Home/End sequences a terminal emits while in
// application-cursor-keys mode (DECCKM) — ESC O H (Home) and ESC O F (End) — into the
// CSI forms ESC [ H / ESC [ F that bubbletea v1 recognizes.
//
// bubbletea's key table covers CSI Home/End and even the SS3 *arrow* variants, but has
// no entry for SS3 Home/End. Unmatched, ESC is decoded as an Alt modifier, so the
// sequence surfaces as alt+O followed by a literal 'H'/'F' rune — which text fields
// insert as the stray "OH"/"OF". Atrium attaches and detaches inner tmux sessions
// constantly, and a full-screen inner app leaves the outer terminal in application-cursor
// mode, so the new-session form routinely sees these SS3 sequences.
//
// The rewrite is a single in-place byte flip (O -> [) on each fully-present 3-byte
// window; both forms are the same length, so no re-buffering is needed. Arrows already
// work in application-cursor mode (bubbletea maps the SS3 arrows) and PageUp/PageDown use
// DECCKM-independent sequences, so Home/End are the only keys that need translation.
//
// It embeds *os.File and overrides only Read so it still satisfies the term.File (raw
// mode) and cancelreader.File (epoll) interfaces bubbletea type-asserts on; the
// cancelreader's epoll path waits on the real fd and then calls this Read, so the
// translation runs on the live input.
//
// The translation is deliberately stateless. Stitching a sequence split across two Reads
// would mean holding back a trailing ESC, which would delay a lone Escape keypress
// (cancel) until the next key — the classic ESC-disambiguation latency that bubbletea
// already solves one layer up. In raw mode a keystroke's escape sequence arrives in a
// single Read, so a split is rare and merely degrades to the pre-fix behavior rather than
// introducing a new failure.
type ss3HomeEndReader struct {
	*os.File
}

// newSS3HomeEndReader wraps an input file (normally os.Stdin) with SS3 Home/End
// normalization.
func newSS3HomeEndReader(f *os.File) *ss3HomeEndReader {
	return &ss3HomeEndReader{File: f}
}

func (r *ss3HomeEndReader) Read(p []byte) (int, error) {
	n, err := r.File.Read(p)
	normalizeSS3HomeEnd(p[:n])
	return n, err
}

// normalizeSS3HomeEnd rewrites every fully-present SS3 Home/End window (ESC O H / ESC O F)
// to its CSI form (ESC [ H / ESC [ F) in place. Only the middle 'O' byte changes, so the
// buffer length is unchanged.
func normalizeSS3HomeEnd(b []byte) {
	for i := 0; i+2 < len(b); i++ {
		if b[i] == 0x1b && b[i+1] == 'O' && (b[i+2] == 'H' || b[i+2] == 'F') {
			b[i+1] = '['
		}
	}
}
