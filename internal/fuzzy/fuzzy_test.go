package fuzzy

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMatch_Subsequence(t *testing.T) {
	ok, _ := Match("atr", "atrium")
	assert.True(t, ok, "atr is a subsequence of atrium")

	ok, _ = Match("ar", "atrium")
	assert.True(t, ok, "ar is a (gapped) subsequence of atrium")

	ok, _ = Match("zq", "atrium")
	assert.False(t, ok, "zq is not a subsequence of atrium")

	ok, _ = Match("atrx", "atrium")
	assert.False(t, ok, "trailing unmatched rune fails the match")
}

func TestMatch_EmptyQueryMatchesAll(t *testing.T) {
	ok, score := Match("", "anything")
	assert.True(t, ok)
	assert.Equal(t, 0, score)
}

func TestMatch_CaseInsensitive(t *testing.T) {
	ok, _ := Match("ATR", "atrium")
	assert.True(t, ok)
}

func TestMatch_ContiguousBeatsGapped(t *testing.T) {
	_, contig := Match("ar", "archive")
	_, gapped := Match("ar", "atrium")
	assert.Greater(t, contig, gapped, "contiguous 'ar' should outscore a gapped 'ar'")
}

func TestMatch_BoundaryStartBeatsMid(t *testing.T) {
	_, start := Match("bar", "bar-baz")
	_, mid := Match("bar", "foobar")
	assert.Greater(t, start, mid, "a match at a word boundary should outscore a mid-word match")
}

func TestMatch_FindsLaterContiguousRun(t *testing.T) {
	// Greedy leftmost alignment would consume 's' from "projects" and score the
	// scattered embedding; the minimal-window pass must find the contiguous
	// boundary run in "sessions" and score it identically to the clean target.
	_, windowed := Match("ses", "projects/sessions")
	_, clean := Match("ses", "sessions")
	assert.Equal(t, clean, windowed, "the contiguous 'ses' run in the basename should be found and scored like a clean match")

	// The PR #120 screenshot bug: 'h' stolen by "/home" left "hub" half the
	// score of paths with a boundary 'b'.
	_, hub := Match("hub", "/home/zvi/quantivly/hub")
	_, box := Match("hub", "/home/zvi/quantivly/platform/src/box")
	assert.Greater(t, hub, box, "a contiguous basename 'hub' must outscore a scattered h…u…b embedding")
}

func TestRank_OrdersByScore(t *testing.T) {
	got := Rank([]string{"atrium", "archive"}, "ar")
	assert.Equal(t, []string{"archive", "atrium"}, got)
}

func TestRank_DropsNonMatches(t *testing.T) {
	got := Rank([]string{"atrium", "zebra", "archive"}, "ar")
	assert.Equal(t, []string{"archive", "atrium"}, got, "zebra has no 'ar' subsequence and is dropped")
}

func TestRank_StableOnTies(t *testing.T) {
	// Both score identically (boundary 'a' + gapped 'b'), so input order is preserved.
	got := Rank([]string{"aXb", "aYb"}, "ab")
	assert.Equal(t, []string{"aXb", "aYb"}, got)
}

func TestRank_EmptyQueryReturnsAllInOrder(t *testing.T) {
	got := Rank([]string{"c", "a", "b"}, "")
	assert.Equal(t, []string{"c", "a", "b"}, got)
}
