// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The spawn tool tells each agent what its own sub-agents may do
// (reliant#2757): a root agent's sub-agents may fan out once more, a
// sub-agent's may not. Both name the concurrency caps and the inputs that set
// them, so a refused spawn is never a surprise.
func TestSpawnNestingNote_MatchesTheSpawnersDepth(t *testing.T) {
	t.Parallel()

	root := spawnNestingNote(0)
	assert.Contains(t, root, "may spawn sub-agents of its own")
	assert.Contains(t, root, "THEIRS cannot")
	assert.NotContains(t, root, "CANNOT spawn sub-agents of its own")

	sub := spawnNestingNote(1)
	assert.Contains(t, sub, "CANNOT spawn sub-agents of its own")
	assert.Contains(t, sub, `never tell one to "use sub-agents"`)

	for _, note := range []string{root, sub} {
		assert.Contains(t, note, "at most 8 of your sub-agents run at once")
		assert.Contains(t, note, "at most 16 across the whole run")
		assert.Contains(t, note, "max_concurrent_sub_agents")
		assert.Contains(t, note, "max_concurrent_sub_agents_total")
	}
}
