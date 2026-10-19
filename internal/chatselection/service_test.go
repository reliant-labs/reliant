// Copyright (c) 2025 Reliant Labs
package chatselection

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// memStore is a Store over a map, versioning as the database does.
type memStore struct {
	rows   map[string]*Selection
	writes int
}

func newMemStore() *memStore { return &memStore{rows: map[string]*Selection{}} }

func (m *memStore) GetChatSelection(_ context.Context, chatID string) (*Selection, error) {
	if row, ok := m.rows[chatID]; ok {
		cp := *row
		return &cp, nil
	}
	return nil, nil
}

func (m *memStore) MutateChatSelection(_ context.Context, chatID string, mutate func(*Selection) (*Selection, error)) (*Selection, error) {
	cur, _ := m.GetChatSelection(context.Background(), chatID)
	next, err := mutate(cur)
	if err != nil {
		return nil, err
	}
	if next == nil || next == cur {
		return cur, nil
	}
	m.writes++
	stored := *next
	stored.Version = 1
	if cur != nil {
		stored.Version = cur.Version + 1
	}
	stored.UpdatedAt = time.Now().UTC()
	m.rows[chatID] = &stored
	return &stored, nil
}

func (m *memStore) CopyChatSelection(_ context.Context, from, to, userID string) error {
	if row, ok := m.rows[from]; ok {
		cp := *row
		cp.ChatID, cp.UserID, cp.Version = to, userID, 1
		m.rows[to] = &cp
	}
	return nil
}

func model(id string) map[string]any { return map[string]any{"id": id} }

func TestMerge_CreatesThenPatchesKeyByKey(t *testing.T) {
	store := newMemStore()
	svc := New(store)
	ctx := context.Background()

	first, err := svc.Merge(ctx, "c1", "u1", "builtin://agent", map[string]any{"model": model("gpt-5.6-sol@codex"), "mode": "auto"}, 0)
	require.NoError(t, err)
	assert.EqualValues(t, 1, first.Version)

	second, err := svc.Merge(ctx, "c1", "u1", "builtin://agent", map[string]any{"mode": "plan"}, 0)
	require.NoError(t, err)
	assert.EqualValues(t, 2, second.Version)
	assert.Equal(t, "plan", second.Params["mode"])
	assert.Equal(t, model("gpt-5.6-sol@codex"), second.Params["model"], "a mode-only patch must leave the model alone")
}

func TestMerge_NilValueDeletesAKey(t *testing.T) {
	svc := New(newMemStore())
	ctx := context.Background()
	_, err := svc.Merge(ctx, "c1", "u1", "w", map[string]any{"model": model("a"), "mode": "auto"}, 0)
	require.NoError(t, err)

	got, err := svc.Merge(ctx, "c1", "u1", "w", map[string]any{"mode": nil}, 0)
	require.NoError(t, err)
	assert.NotContains(t, got.Params, "mode")
	assert.Contains(t, got.Params, "model")
}

func TestMerge_ReplacesAModelSelectorWhole(t *testing.T) {
	svc := New(newMemStore())
	ctx := context.Background()
	_, err := svc.Merge(ctx, "c1", "u1", "w", map[string]any{"model": map[string]any{"id": "a", "thinking_level": "high"}}, 0)
	require.NoError(t, err)

	got, err := svc.Merge(ctx, "c1", "u1", "w", map[string]any{"model": model("b")}, 0)
	require.NoError(t, err)
	assert.Equal(t, model("b"), got.Params["model"], "a selector is a value: the old effort must not leak into the new model")
}

func TestMerge_AWorkflowChangeReplacesParams(t *testing.T) {
	svc := New(newMemStore())
	ctx := context.Background()
	_, err := svc.Merge(ctx, "c1", "u1", "builtin://agent", map[string]any{"model": model("a"), "mode": "auto"}, 0)
	require.NoError(t, err)

	got, err := svc.Merge(ctx, "c1", "u1", "builtin://other", map[string]any{"mode": "plan"}, 0)
	require.NoError(t, err)
	assert.Equal(t, "builtin://other", got.WorkflowName)
	assert.Equal(t, map[string]any{"mode": "plan"}, got.Params, "params belong to one workflow")
}

func TestMerge_ANoOpStoresNothingAndKeepsTheVersion(t *testing.T) {
	store := newMemStore()
	svc := New(store)
	ctx := context.Background()
	_, err := svc.Merge(ctx, "c1", "u1", "w", map[string]any{"mode": "auto"}, 0)
	require.NoError(t, err)
	writes := store.writes

	got, err := svc.Merge(ctx, "c1", "u1", "w", map[string]any{"mode": "auto"}, 0)
	require.NoError(t, err)
	assert.EqualValues(t, 1, got.Version)
	assert.Equal(t, writes, store.writes, "an unchanged patch must not churn the row")
}

func TestMerge_AnEmptyPatchOnAChatWithNothingCreatesNothing(t *testing.T) {
	store := newMemStore()
	got, err := New(store).Merge(context.Background(), "c1", "u1", "w", nil, 0)
	require.NoError(t, err)
	assert.Nil(t, got)
	assert.Empty(t, store.rows)
}

func TestMerge_StaleVersionIsRefused(t *testing.T) {
	svc := New(newMemStore())
	ctx := context.Background()
	_, err := svc.Merge(ctx, "c1", "u1", "w", map[string]any{"mode": "auto"}, 0)
	require.NoError(t, err)
	_, err = svc.Merge(ctx, "c1", "u1", "w", map[string]any{"mode": "plan"}, 0)
	require.NoError(t, err)

	_, err = svc.Merge(ctx, "c1", "u1", "w", map[string]any{"mode": "manual"}, 1)
	assert.ErrorIs(t, err, ErrVersionConflict)

	ok, err := svc.Merge(ctx, "c1", "u1", "w", map[string]any{"mode": "manual"}, 2)
	require.NoError(t, err)
	assert.EqualValues(t, 3, ok.Version)
}

func TestMerge_AClientThatReadNoneMayCreateButNotAfterSomeoneElseDid(t *testing.T) {
	svc := New(newMemStore())
	ctx := context.Background()
	// expectedVersion 0 means unconditional; a client that read version 5 of a
	// record that does not exist is stale.
	_, err := svc.Merge(ctx, "c1", "u1", "w", map[string]any{"mode": "auto"}, 5)
	assert.ErrorIs(t, err, ErrVersionConflict)
}

func TestSetPolicy_RoundTripsAndKeepsParams(t *testing.T) {
	svc := New(newMemStore())
	ctx := context.Background()
	_, err := svc.Merge(ctx, "c1", "u1", "w", map[string]any{"model": model("a")}, 0)
	require.NoError(t, err)

	policy := ProviderPolicy{OnLimit: OnLimitFallback, FallbackModel: "gpt-5.6-sol@codex"}
	got, err := svc.SetPolicy(ctx, "c1", "u1", "w", policy, 0)
	require.NoError(t, err)
	assert.Equal(t, policy, got.Policy)
	assert.Equal(t, model("a"), got.Params["model"])

	again, err := svc.Get(ctx, "c1")
	require.NoError(t, err)
	assert.Equal(t, policy, again.Policy)
}

func TestSetPolicy_CreatesTheRecordWhenThereIsNone(t *testing.T) {
	svc := New(newMemStore())
	got, err := svc.SetPolicy(context.Background(), "c1", "u1", "w", ProviderPolicy{OnLimit: OnLimitWait}, 0)
	require.NoError(t, err)
	assert.Equal(t, OnLimitWait, got.Policy.OnLimit)
	assert.Empty(t, got.Params)
}

func TestSetPolicy_ClearingAbsentPolicyCreatesNothing(t *testing.T) {
	store := newMemStore()
	got, err := New(store).SetPolicy(context.Background(), "c1", "u1", "w", ProviderPolicy{}, 0)
	require.NoError(t, err)
	assert.Nil(t, got)
	assert.Empty(t, store.rows)
}

func TestCopy_GivesTheBranchItsOwnRecord(t *testing.T) {
	store := newMemStore()
	svc := New(store)
	ctx := context.Background()
	_, err := svc.Merge(ctx, "src", "u1", "w", map[string]any{"model": model("a")}, 0)
	require.NoError(t, err)

	require.NoError(t, svc.Copy(ctx, "src", "branch", "u1"))
	got, err := svc.Get(ctx, "branch")
	require.NoError(t, err)
	assert.Equal(t, model("a"), got.Params["model"])

	_, err = svc.Merge(ctx, "branch", "u1", "w", map[string]any{"model": model("b")}, 0)
	require.NoError(t, err)
	src, _ := svc.Get(ctx, "src")
	assert.Equal(t, model("a"), src.Params["model"], "editing the branch must not move the source")
}
