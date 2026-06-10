// hints/assign_test.go
package hints

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Up to 26 matches every label is a single character, in alphabet order, so
// the most common screens stay one-keystroke.
func TestAssignLabels_SingleCharsUpToAlphabetSize(t *testing.T) {
	labels := assignLabels(26)
	require.Len(t, labels, 26)
	for i, l := range labels {
		assert.Len(t, l, 1, "label %d", i)
		assert.Equal(t, string(Alphabet[i]), l)
	}
}

// Past the alphabet size, tail characters are expanded into two-char combos.
// The result must stay prefix-free: no label may be a prefix of another, or
// typing the shorter one would shadow the longer.
func TestAssignLabels_PrefixFreeWhenExpanded(t *testing.T) {
	labels := assignLabels(40)
	require.Len(t, labels, 40)
	for i, a := range labels {
		for j, b := range labels {
			if i == j {
				continue
			}
			assert.False(t, strings.HasPrefix(b, a),
				"label %q is a prefix of label %q", a, b)
		}
	}
}

// The exact count requested comes back, for any n the screen can produce.
func TestAssignLabels_ExactCount(t *testing.T) {
	for _, n := range []int{0, 1, 25, 26, 27, 51, 52, 100} {
		assert.Len(t, assignLabels(n), n, "n=%d", n)
	}
}
