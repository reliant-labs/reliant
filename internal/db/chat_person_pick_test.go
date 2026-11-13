// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/chatselection"
	"github.com/reliant-labs/reliant/internal/store"
)

// A person's root pick is consent for its provider only in its org and only
// while the chat's selection is at the version the pick was written with, in
// the same locked write (org-provider-accounts §4.4, §10).
func TestChatPersonPickDriver(t *testing.T) {
	t.Parallel()
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	chatID := seedChatForRun(t, repo, ctx, "alice")
	person := WithPersonPick(ctx, PersonPick{UserID: "alice", OrgID: "org-acme"})
	write := func(ctx context.Context, model any, provider, mode string) *chatselection.Selection {
		t.Helper()
		sel, err := repo.MutateChatSelection(ctx, chatID, func(cur *chatselection.Selection) (*chatselection.Selection, error) {
			next := &chatselection.Selection{ChatID: chatID, UserID: "alice", WorkflowName: "builtin://agent",
				Params: map[string]any{"model": model, "mode": mode}, ChatProvider: provider}
			if cur != nil && cur.ChatProvider == provider && sameParams(cur.Params, next.Params) {
				return cur, nil
			}
			return next, nil
		})
		require.NoError(t, err)
		return sel
	}
	driver := func(org string) string {
		t.Helper()
		d, err := repo.ChatPersonPickDriver(ctx, chatID, org)
		require.NoError(t, err)
		return d
	}
	pickRows := func() int {
		t.Helper()
		n, err := repo.ORM().Bun().NewSelect().Table("chat_person_picks").Where("chat_id = ?", chatID).Count(ctx)
		require.NoError(t, err)
		return int(n)
	}

	write(ctx, map[string]any{"id": "gpt-5.6-sol@codex"}, "", "build")
	assert.Empty(t, driver("org-acme"), "a write that is no person's pick records none")
	assert.Zero(t, pickRows())

	picked := write(person, map[string]any{"id": "gpt-5.6-sol@codex"}, "", "plan")
	assert.Equal(t, "codex", driver("org-acme"), "the person's pick, at the version it was written with")
	assert.Empty(t, driver("org-other"), "a pick is consent only in the org it was made in")
	assert.Empty(t, driver(""))
	pick, err := store.GetChatPersonPick(ctx, repo.ORM(), chatID)
	require.NoError(t, err)
	assert.Equal(t, picked.Version, pick.SelectionVersion, "bound to the version its own write left")

	// A write that changes nothing advances nothing: the selection is still
	// the one the person picked.
	write(ctx, map[string]any{"id": "gpt-5.6-sol@codex"}, "", "plan")
	assert.Equal(t, "codex", driver("org-acme"))

	// Any superseding write, even one that keeps the model, voids the pick by
	// construction: nothing clears the row, its version is simply behind.
	write(ctx, map[string]any{"id": "gpt-5.6-sol@codex"}, "", "build")
	assert.Empty(t, driver("org-acme"), "a superseding write voids the pick")
	assert.Equal(t, 1, pickRows(), "no clear: the row is untouched and still void")

	// Going back to the picked selection does not revive it: versions only move on.
	write(ctx, map[string]any{"id": "gpt-5.6-sol@codex"}, "", "plan")
	assert.Empty(t, driver("org-acme"), "a superseded pick is never revived")

	// The person's switch, in the {id, providers} form.
	write(person, map[string]any{"id": "gpt-5.6-sol", "providers": []any{"codex"}}, "codex", "plan")
	assert.Equal(t, "codex", driver("org-acme"))

	// A branch copies the selection, never the pick.
	branch := seedChatForRun(t, repo, ctx, "alice")
	require.NoError(t, repo.CopyChatSelection(ctx, chatID, branch, "alice"))
	d, err := repo.ChatPersonPickDriver(ctx, branch, "org-acme")
	require.NoError(t, err)
	assert.Empty(t, d, "a branch starts with no pick")

	// The pick goes with its chat.
	require.NoError(t, repo.DeleteChat(ctx, chatID))
	assert.Zero(t, pickRows())
}

// A superseding write that commits after a reader's earlier look at the
// selection still voids the pick for that reader: the pick and the
// selection's version are read in one statement, so there is no gap between
// two reads for the write to land in.
func TestChatPersonPickDriver_SupersedingWriteBetweenReads(t *testing.T) {
	t.Parallel()
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	chatID := seedChatForRun(t, repo, ctx, "alice")
	person := WithPersonPick(ctx, PersonPick{UserID: "alice", OrgID: "org-acme"})
	set := func(ctx context.Context, model string) {
		t.Helper()
		_, err := repo.MutateChatSelection(ctx, chatID, func(*chatselection.Selection) (*chatselection.Selection, error) {
			return &chatselection.Selection{ChatID: chatID, UserID: "alice", WorkflowName: "builtin://agent",
				Params: map[string]any{"model": map[string]any{"id": model}}}, nil
		})
		require.NoError(t, err)
	}
	set(person, "gpt-5.6-sol@codex")

	// The reader saw the person's selection ...
	seen, err := repo.GetChatSelection(ctx, chatID)
	require.NoError(t, err)
	key, _ := RootModelPick(seen.Params)
	require.Equal(t, "gpt-5.6-sol@codex", key)
	// ... then a workflow's write commits ...
	set(ctx, "gpt-5.6-sol@codex")
	// ... and the reader's consent read finds the pick void.
	d, err := repo.ChatPersonPickDriver(ctx, chatID, "org-acme")
	require.NoError(t, err)
	assert.Empty(t, d, "no personal spend on a pick a later write superseded")
}

func sameParams(a, b map[string]any) bool {
	return fmt.Sprint(a) == fmt.Sprint(b)
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
