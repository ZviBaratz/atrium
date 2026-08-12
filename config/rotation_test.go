package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestSelectPoolMember(t *testing.T) {
	now := time.Date(2026, 7, 23, 16, 0, 0, 0, time.UTC)
	two := []ClaudeAccount{{Name: "work-1"}, {Name: "work-2"}}
	cases := []struct {
		name       string
		members    []ClaudeAccount
		avail      map[string]AccountAvailability
		cursor     int
		wantIdx    int
		wantAllLim bool
	}{
		{"cursor 0, all available", two, nil, 0, 0, false},
		{"cursor 1, all available", two, nil, 1, 1, false},
		{"cursor wraps past the end", two, nil, 2, 0, false},
		{"negative cursor normalizes", two, nil, -1, 1, false},
		{"oversized cursor normalizes", two, nil, 7, 1, false},
		{"cursor's member limited -> next", two,
			map[string]AccountAvailability{"work-1": {Limited: true}}, 0, 1, false},
		{"skip wraps to an earlier member", two,
			map[string]AccountAvailability{"work-2": {Limited: true}}, 1, 0, false},
		// The fallback startNewSession relies on: the CURSOR's member, not 0.
		{"all limited returns the cursor's own member", two,
			map[string]AccountAvailability{"work-1": {Limited: true}, "work-2": {Limited: true}},
			1, 1, true},
		{"elapsed Until counts available", two,
			map[string]AccountAvailability{"work-1": {Limited: true, Until: "2026-07-23T15:00:00Z"}}, 0, 0, false},
		{"future Until counts limited", two,
			map[string]AccountAvailability{"work-1": {Limited: true, Until: "2026-07-23T17:00:00Z"}}, 0, 1, false},
		{"malformed Until counts limited", two,
			map[string]AccountAvailability{"work-1": {Limited: true, Until: "nope"}}, 0, 1, false},
		{"empty members", nil, nil, 0, -1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotIdx, gotAllLim := SelectPoolMember(tc.members, tc.avail, tc.cursor, now)
			assert.Equal(t, tc.wantIdx, gotIdx, "index")
			assert.Equal(t, tc.wantAllLim, gotAllLim, "allLimited")
		})
	}
}

// Moved from app/rotation_test.go with the function it tests.
func TestSoonestResetMember(t *testing.T) {
	members := []ClaudeAccount{{Name: "a"}, {Name: "b"}, {Name: "c"}}
	avail := map[string]AccountAvailability{
		"a": {Limited: true, Until: "2026-07-23T18:00:00Z"},
		"b": {Limited: true, Until: "2026-07-23T17:00:00Z"},
		"c": {Limited: true}, // indefinite sorts last
	}
	assert.Equal(t, 1, SoonestResetMember(members, avail), "b resets soonest")

	allIndef := map[string]AccountAvailability{"a": {Limited: true}, "b": {Limited: true}, "c": {Limited: true}}
	assert.Equal(t, 0, SoonestResetMember(members, allIndef), "all indefinite -> fallback 0")
}
