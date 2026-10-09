// Copyright (c) 2025 Reliant Labs
package services

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// Existing servers decode repo_memories_json as map[repoPath]content. Keep the
// daemon snapshot map keyed by each individual path until a new protobuf field
// can carry groups: a display key such as "app, app-feature" is a valid string
// map entry but an old server interprets it as one literal repository path.
func TestFlattenRepoMemories_RemainsReadableByLegacyMapReader(t *testing.T) {
	stored := flattenRepoMemories(map[string][]byte{
		"app":         []byte("shared rules"),
		"app-feature": []byte("shared rules"),
	})
	require.NotNil(t, stored)

	var legacy map[string]string
	require.NoError(t, json.Unmarshal([]byte(*stored), &legacy))
	require.Equal(t, map[string]string{
		"app":         "shared rules",
		"app-feature": "shared rules",
	}, legacy)
}
