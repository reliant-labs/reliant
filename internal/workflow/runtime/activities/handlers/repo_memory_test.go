// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"fmt"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func injectedChars(msgs []message.Message) int {
	total := 0
	for _, m := range msgs {
		total += len(m.Content().Text)
	}
	return total
}

// Prod incident 2026-10-09: ~30 hand-made worktree checkouts inside one
// project each carried the 18KB forge framework guide plus their repo's
// reliant.md, and every request — every sub-agent's FIRST turn included —
// carried all of it: a 246.8k-token base against a 231.2k compaction
// threshold. Distinct clones (not linked worktrees, which #648 collapses) still
// reach the same shape, so the injection itself must be bounded.
func TestFormatRepoMemoryMessages_BoundedByTheModelWindow(t *testing.T) {
	memories := map[string]string{}
	for i := 0; i < 30; i++ {
		repo := fmt.Sprintf("reliant-feature-%02d", i)
		memories[repo] = fmt.Sprintf("# %s\n%s", repo, strings.Repeat("forge framework guide. ", 800)) // ~18KB, distinct per repo
	}

	const window = 272_000 // gpt-5.6-terra on codex
	budget := repoMemoryBudgetChars(window)
	msgs, omitted := formatRepoMemoryMessages(memories, budget)

	require.NotEmpty(t, omitted, "30 x 18KB of memory cannot all ride on a 272k-window request")
	notice := msgs[len(msgs)-1].Content().Text
	require.True(t, strings.HasPrefix(notice, "<system-memory-omitted>"))
	assert.LessOrEqual(t, injectedChars(msgs)-len(notice), budget,
		"injected repo memory must stay within %.0f%% of the window", repoMemoryBudgetFraction*100)
	assert.Less(t, injectedChars(msgs), budget+len(notice)+1)

	// Nothing vanishes silently: every omitted repo is named, with where its
	// memory lives.
	for _, repo := range omitted {
		assert.Contains(t, notice, repo+"/reliant.md")
	}
	assert.Equal(t, 30, len(msgs)-1+len(omitted), "every repo is either injected or named in the notice")
}

// The bound must not bite a real multi-repo project: reliant-labs' three repos
// carry ~64KB of memory after #648.
func TestFormatRepoMemoryMessages_RealMultiRepoProjectFits(t *testing.T) {
	msgs, omitted := formatRepoMemoryMessages(map[string]string{
		"control-plane": strings.Repeat("c", 30_000),
		"forge":         strings.Repeat("f", 20_000),
		"reliant":       strings.Repeat("r", 14_000),
	}, repoMemoryBudgetChars(272_000))
	assert.Empty(t, omitted)
	assert.Len(t, msgs, 3)
}

// A memory too large for what is left is skipped, and smaller ones after it in
// sort order are still admitted.
func TestFormatRepoMemoryMessages_SkipsWhatDoesNotFit(t *testing.T) {
	msgs, omitted := formatRepoMemoryMessages(map[string]string{
		"a-huge": strings.Repeat("h", 5_000),
		"b":      "b memory",
		"c":      "c memory",
	}, 1_000)
	assert.Equal(t, []string{"a-huge"}, omitted)
	require.Len(t, msgs, 3)
	assert.Contains(t, msgs[0].Content().Text, "<system-memory repo=b>")
	assert.Contains(t, msgs[1].Content().Text, "<system-memory repo=c>")
	assert.Contains(t, msgs[2].Content().Text, "- a-huge (5 KB): read a-huge/reliant.md")
}

func TestRepoMemoryBudgetChars(t *testing.T) {
	assert.Equal(t, 108_800, repoMemoryBudgetChars(272_000))
	assert.Equal(t, 80_000, repoMemoryBudgetChars(0), "unknown window assumes 200k")
}
