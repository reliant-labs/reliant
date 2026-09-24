// Copyright (c) 2025 Reliant Labs
package scenario

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func boolp(b bool) *bool { return &b }
func intp(i int) *int    { return &i }

func TestCheckMessageExpectations(t *testing.T) {
	t.Parallel()
	saved := []SavedMessage{
		{Node: "loop.feedback", Role: "user", Content: "AUDIT FEEDBACK: do not delete tests"},
		{Node: "loop.feedback", Role: "user", Content: "AUDIT FEEDBACK: add a test"},
		{Node: "report", Role: "assistant", Content: "done"},
	}

	cases := []struct {
		name   string
		expect map[string]MessageExpectation
		want   int // number of mismatches
	}{
		{"saved true holds", map[string]MessageExpectation{"report": {Saved: boolp(true)}}, 0},
		{"saved true fails when none", map[string]MessageExpectation{"other": {Saved: boolp(true)}}, 1},
		{"saved false holds", map[string]MessageExpectation{"other": {Saved: boolp(false)}}, 0},
		{"saved false fails when saved", map[string]MessageExpectation{"report": {Saved: boolp(false)}}, 1},
		{"count exact", map[string]MessageExpectation{"loop.feedback": {Count: intp(2)}}, 0},
		{"count mismatch", map[string]MessageExpectation{"loop.feedback": {Count: intp(1)}}, 1},
		{"role holds", map[string]MessageExpectation{"loop.feedback": {Role: "user"}}, 0},
		{"role mismatch", map[string]MessageExpectation{"report": {Role: "user"}}, 1},
		{"content contains any message", map[string]MessageExpectation{"loop.feedback": {ContentContains: []string{"add a test", "delete tests"}}}, 0},
		{"content missing", map[string]MessageExpectation{"loop.feedback": {ContentContains: []string{"nope"}}}, 1},
		{"content not contains", map[string]MessageExpectation{"report": {ContentNotContains: []string{"FEEDBACK"}}}, 0},
		{"content not contains violated", map[string]MessageExpectation{"loop.feedback": {ContentNotContains: []string{"FEEDBACK"}}}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Len(t, checkMessageExpectations(tc.expect, saved), tc.want)
		})
	}
}
