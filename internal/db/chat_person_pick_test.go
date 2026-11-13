// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/chatselection"
)

// A person's root pick is consent for its provider only in its org and only
// while the chat's selection still holds it (org-provider-accounts §4.4, §10).
func TestChatPersonPickDriver(t *testing.T) {
	t.Parallel()
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	chatID := seedChatForRun(t, repo, ctx, "alice")
	setModel := func(model any, provider string) {
		t.Helper()
		_, err := repo.MutateChatSelection(ctx, chatID, func(cur *chatselection.Selection) (*chatselection.Selection, error) {
			return &chatselection.Selection{ChatID: chatID, UserID: "alice", WorkflowName: "builtin://agent",
				Params: map[string]any{"model": model}, ChatProvider: provider}, nil
		})
		require.NoError(t, err)
	}
	driver := func(org string) string {
		t.Helper()
		d, err := repo.ChatPersonPickDriver(ctx, chatID, org)
		require.NoError(t, err)
		return d
	}

	setModel(map[string]any{"id": "gpt-5.6-sol@codex"}, "")
	assert.Empty(t, driver("org-acme"), "nobody picked")

	key, d := RootModelPick(map[string]any{"model": map[string]any{"id": "gpt-5.6-sol@codex"}})
	require.Equal(t, "codex", d)
	require.NoError(t, repo.RecordChatPersonPick(ctx, chatID, "alice", "org-acme", key, ""))
	assert.Equal(t, "codex", driver("org-acme"), "the person's pick, as the selection holds it")
	assert.Empty(t, driver("org-other"), "a pick is consent only in the org it was made in")
	assert.Empty(t, driver(""))

	setModel(map[string]any{"id": "claude-5.5-opus@claude"}, "")
	assert.Empty(t, driver("org-acme"), "a selection changed since the pick voids it")

	setModel(map[string]any{"id": "gpt-5.6-sol", "providers": []any{"codex"}}, "")
	assert.Equal(t, "codex", driver("org-acme"), "the same pick, in the {id, providers} form")

	setModel(map[string]any{"id": "gpt-5.6-sol@codex"}, "codex")
	assert.Empty(t, driver("org-acme"), "a provider switch the person did not make voids it")
	require.NoError(t, repo.RecordChatPersonPick(ctx, chatID, "alice", "org-acme", key, "codex"))
	assert.Equal(t, "codex", driver("org-acme"), "the person's switch")

	// A branch copies the selection, never the pick.
	branch := seedChatForRun(t, repo, ctx, "alice")
	require.NoError(t, repo.CopyChatSelection(ctx, chatID, branch, "alice"))
	d, err := repo.ChatPersonPickDriver(ctx, branch, "org-acme")
	require.NoError(t, err)
	assert.Empty(t, d, "a branch starts with no pick")

	// The pick goes with its chat.
	require.NoError(t, repo.DeleteChat(ctx, chatID))
	n, err := repo.ORM().Bun().NewSelect().Table("chat_person_picks").Where("chat_id = ?", chatID).Count(ctx)
	require.NoError(t, err)
	assert.Zero(t, n)
}

func TestRootModelPick(t *testing.T) {
	for _, tc := range []struct {
		model       any
		key, driver string
	}{
		{map[string]any{"id": "gpt-5.6-sol@codex"}, "gpt-5.6-sol@codex", "codex"},
		{map[string]any{"id": "gpt-5.6-sol", "providers": []any{"codex"}}, "gpt-5.6-sol@codex", "codex"},
		{map[string]any{"id": "gpt-5.6-sol", "providers": []any{"codex", "openai"}}, "gpt-5.6-sol", ""},
		{map[string]any{"id": "gpt-5.6-sol"}, "gpt-5.6-sol", ""},
		{"gpt-5.6-sol@codex", "gpt-5.6-sol@codex", "codex"},
		{nil, "", ""},
	} {
		key, driver := RootModelPick(map[string]any{"model": tc.model})
		assert.Equal(t, tc.key, key, "%v", tc.model)
		assert.Equal(t, tc.driver, driver, "%v", tc.model)
	}
}
