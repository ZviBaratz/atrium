package app

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeSS3HomeEnd(t *testing.T) {
	esc := byte(0x1b)
	cases := []struct {
		name string
		in   []byte
		want []byte
	}{
		{"home SS3 to CSI", []byte{esc, 'O', 'H'}, []byte{esc, '[', 'H'}},
		{"end SS3 to CSI", []byte{esc, 'O', 'F'}, []byte{esc, '[', 'F'}},
		{"plain text untouched", []byte("hello"), []byte("hello")},
		{"csi home untouched", []byte{esc, '[', 'H'}, []byte{esc, '[', 'H'}},
		{"mouse sgr untouched", []byte{esc, '[', '<', '0', ';', '1', ';', '1', 'M'}, []byte{esc, '[', '<', '0', ';', '1', ';', '1', 'M'}},
		{"bracketed paste marker untouched", []byte{esc, '[', '2', '0', '0', '~'}, []byte{esc, '[', '2', '0', '0', '~'}},
		{"bare ESC O at end untouched (could be alt+O)", []byte{esc, 'O'}, []byte{esc, 'O'}},
		{"SS3 other letter untouched", []byte{esc, 'O', 'A'}, []byte{esc, 'O', 'A'}},
		{"two sequences in one buffer", []byte{esc, 'O', 'H', esc, 'O', 'F'}, []byte{esc, '[', 'H', esc, '[', 'F'}},
		{"sequence embedded in text", append([]byte("ab"), esc, 'O', 'H', 'c'), append([]byte("ab"), esc, '[', 'H', 'c')},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := append([]byte(nil), tc.in...)
			normalizeSS3HomeEnd(b)
			assert.Equal(t, tc.want, b)
		})
	}
}

// The reader delegates to the embedded file's Read and normalizes the bytes it returns.
func TestSS3HomeEndReader_Read(t *testing.T) {
	pr, pw, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() { _ = pr.Close() })

	go func() {
		_, _ = pw.Write([]byte{0x1b, 'O', 'H', 'x'})
		_ = pw.Close()
	}()

	r := newSS3HomeEndReader(pr)
	buf := make([]byte, 8)
	n, _ := r.Read(buf)
	assert.Equal(t, []byte{0x1b, '[', 'H', 'x'}, buf[:n])
}

// Embedding *os.File means the wrapper still exposes Fd/Name/Write/Close, so bubbletea's
// term.File and the cancelreader's File type-assertions both succeed and raw mode is set.
func TestSS3HomeEndReader_PreservesFileIdentity(t *testing.T) {
	r := newSS3HomeEndReader(os.Stdin)
	assert.Equal(t, os.Stdin.Fd(), r.Fd())
	assert.Equal(t, os.Stdin.Name(), r.Name())
	// The wrapper must still satisfy the file interfaces bubbletea (term.File) and the
	// cancelreader (File) type-assert on, or raw mode and clean cancellation break.
	var _ interface {
		Read([]byte) (int, error)
		Write([]byte) (int, error)
		Close() error
		Fd() uintptr
		Name() string
	} = r
}
