// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
)

// A user's attached image reaches only the agent of the thread it was sent in.
// A sub-agent starts from its prompt alone, and a compacted thread from its
// handoff alone, so in both cases the attachment_id has to be carried across
// in text or save_attachment has nothing to name. These pin the two places
// that tell the model so.

func TestSpawnToolSaysSubAgentsCannotSeeAttachments(t *testing.T) {
	t.Parallel()

	repo := db.NewTestRepo(t)
	spawn := (&CallLLMActivity{repo: repo}).getSpawnTool(context.Background(), spawnPresetSource{}, &SpawnConfig{Presets: []string{"general"}})
	require.NotNil(t, spawn)
	description := spawn.Description()
	assert.Contains(t, description, "not the images or files the user attached")
	assert.Contains(t, description, "attachment_id")
	assert.Contains(t, description, "save_attachment")
}

func TestCompactionHandoffKeepsAttachmentIDs(t *testing.T) {
	t.Parallel()
	assert.Contains(t, compactionMsg, "attachment_id")
}
