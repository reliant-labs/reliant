package services

import (
	"testing"

	"connectrpc.com/connect"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/ptr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A batch write must persist every key in one call, and must be an upsert so
// callers do not have to know which keys already exist — the whole point of
// replacing the per-key create/update fan-out.
func TestSettingsService_BatchUpsertSettings_WritesAllKeysInOneCall(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	svc := NewSettingsService(repo, nil)
	ctx := newSettingsServiceTestContext()

	resp, err := svc.BatchUpsertSettings(ctx, connect.NewRequest(&reliantv1.BatchUpsertSettingsRequest{
		Settings: []*reliantv1.SettingWrite{
			{Key: "tour.completed", Value: "true"},
			{Key: "tour.completedSteps", Value: `["a","b"]`},
			{Key: "tour.skippedSteps", Value: "[]"},
		},
	}))
	require.NoError(t, err)
	require.Len(t, resp.Msg.Settings, 3)

	for key, want := range map[string]string{
		"tour.completed":      "true",
		"tour.completedSteps": `["a","b"]`,
		"tour.skippedSteps":   "[]",
	} {
		got, err := repo.GetSetting(ctx, "test-user", nil, key)
		require.NoError(t, err, "key %q should have been persisted", key)
		assert.Equal(t, want, got.Value, "key %q", key)
	}
}

func TestSettingsService_BatchUpsertSettings_ReplacesExistingValues(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	svc := NewSettingsService(repo, nil)
	ctx := newSettingsServiceTestContext()

	write := func(value string) {
		_, err := svc.BatchUpsertSettings(ctx, connect.NewRequest(&reliantv1.BatchUpsertSettingsRequest{
			Settings: []*reliantv1.SettingWrite{{Key: "appearance.fontSize", Value: value}},
		}))
		require.NoError(t, err)
	}

	write("14")
	write("16")

	got, err := repo.GetSetting(ctx, "test-user", nil, "appearance.fontSize")
	require.NoError(t, err)
	// Upsert, not insert: a second write replaces rather than duplicating. A
	// duplicate row could win the next ListSettings and revert the value.
	assert.Equal(t, "16", got.Value)

	all, err := repo.ListSettingsByKey(ctx, "test-user", "appearance.fontSize")
	require.NoError(t, err)
	assert.Len(t, all, 1, "second write must not append a duplicate row")
}

func TestSettingsService_BatchUpsertSettings_DefaultsValueTypeToString(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	svc := NewSettingsService(repo, nil)
	ctx := newSettingsServiceTestContext()

	_, err := svc.BatchUpsertSettings(ctx, connect.NewRequest(&reliantv1.BatchUpsertSettingsRequest{
		Settings: []*reliantv1.SettingWrite{
			{Key: "a", Value: "1"},
			{Key: "b", Value: "2", ValueType: ptr.Of("json")},
		},
	}))
	require.NoError(t, err)

	a, err := repo.GetSetting(ctx, "test-user", nil, "a")
	require.NoError(t, err)
	assert.Equal(t, "string", a.ValueType)

	b, err := repo.GetSetting(ctx, "test-user", nil, "b")
	require.NoError(t, err)
	assert.Equal(t, "json", b.ValueType)
}

func TestSettingsService_BatchUpsertSettings_RejectsEmptyKey(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	svc := NewSettingsService(repo, nil)
	ctx := newSettingsServiceTestContext()

	_, err := svc.BatchUpsertSettings(ctx, connect.NewRequest(&reliantv1.BatchUpsertSettingsRequest{
		Settings: []*reliantv1.SettingWrite{
			{Key: "good", Value: "1"},
			{Key: "", Value: "2"},
		},
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	// Validation happens before any write, so the valid key in the same batch
	// must not have been persisted either.
	_, getErr := repo.GetSetting(ctx, "test-user", nil, "good")
	assert.Error(t, getErr, "no key should be written when the batch is invalid")
}

func TestSettingsService_BatchUpsertSettings_EmptyBatchIsANoOp(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	svc := NewSettingsService(repo, nil)
	ctx := newSettingsServiceTestContext()

	resp, err := svc.BatchUpsertSettings(ctx, connect.NewRequest(&reliantv1.BatchUpsertSettingsRequest{}))
	require.NoError(t, err)
	assert.Empty(t, resp.Msg.Settings)
}
