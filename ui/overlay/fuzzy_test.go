package overlay

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFuzzyMatch_Subsequence(t *testing.T) {
	ok, _ := fuzzyMatch("atr", "atrium")
	assert.True(t, ok, "atr is a subsequence of atrium")

	ok, _ = fuzzyMatch("ar", "atrium")
	assert.True(t, ok, "ar is a (gapped) subsequence of atrium")

	ok, _ = fuzzyMatch("zq", "atrium")
	assert.False(t, ok, "zq is not a subsequence of atrium")

	ok, _ = fuzzyMatch("atrx", "atrium")
	assert.False(t, ok, "trailing unmatched rune fails the match")
}

func TestFuzzyMatch_EmptyQueryMatchesAll(t *testing.T) {
	ok, score := fuzzyMatch("", "anything")
	assert.True(t, ok)
	assert.Equal(t, 0, score)
}

func TestFuzzyMatch_CaseInsensitive(t *testing.T) {
	ok, _ := fuzzyMatch("ATR", "atrium")
	assert.True(t, ok)
}

func TestFuzzyMatch_ContiguousBeatsGapped(t *testing.T) {
	_, contig := fuzzyMatch("ar", "archive")
	_, gapped := fuzzyMatch("ar", "atrium")
	assert.Greater(t, contig, gapped, "contiguous 'ar' should outscore a gapped 'ar'")
}

func TestFuzzyMatch_BoundaryStartBeatsMid(t *testing.T) {
	_, start := fuzzyMatch("bar", "bar-baz")
	_, mid := fuzzyMatch("bar", "foobar")
	assert.Greater(t, start, mid, "a match at a word boundary should outscore a mid-word match")
}

func TestFuzzyRank_OrdersByScore(t *testing.T) {
	got := fuzzyRank([]string{"atrium", "archive"}, "ar")
	assert.Equal(t, []string{"archive", "atrium"}, got)
}

func TestFuzzyRank_DropsNonMatches(t *testing.T) {
	got := fuzzyRank([]string{"atrium", "zebra", "archive"}, "ar")
	assert.Equal(t, []string{"archive", "atrium"}, got, "zebra has no 'ar' subsequence and is dropped")
}

func TestFuzzyRank_StableOnTies(t *testing.T) {
	// Both score identically (boundary 'a' + gapped 'b'), so input order is preserved.
	got := fuzzyRank([]string{"aXb", "aYb"}, "ab")
	assert.Equal(t, []string{"aXb", "aYb"}, got)
}

func TestFuzzyRank_EmptyQueryReturnsAllInOrder(t *testing.T) {
	got := fuzzyRank([]string{"c", "a", "b"}, "")
	assert.Equal(t, []string{"c", "a", "b"}, got)
}
