// Copyright (c) 2025 Reliant Labs
package launch

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestRunTitle(t *testing.T) {
	at := time.Date(2026, 10, 29, 7, 11, 13, 0, time.UTC)
	long := strings.Repeat("word ", 30)
	tests := []struct {
		name, workflow, subject, want string
	}{
		{"project workflow and a subject", "cicd-probe", "inventory the machine", "cicd-probe · inventory the machine"},
		{"scheme is dropped", "builtin://agent", "Fix the flaky test", "agent · Fix the flaky test"},
		{"project scheme is dropped", "project://pr-review", "PR 1481", "pr-review · PR 1481"},
		{"first non-blank line, whitespace collapsed", "agent", "\n\n  Review   PR 12\nthen merge it", "agent · Review PR 12"},
		{"no subject is the start time", "cicd-probe", "  \n ", "cicd-probe · 2026-10-29 07:11 UTC"},
		{"no workflow reads as the default agent", "", "hello", "agent · hello"},
		{"a long subject is cut to a row", "agent", long, "agent · " + strings.TrimSpace(long[:runTitleSubjectMax-1]) + "…"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, RunTitle(tt.workflow, tt.subject, at))
		})
	}
}
