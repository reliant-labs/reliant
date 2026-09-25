package services

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/controlplane"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeControlPlaneClient struct {
	issueKey       string
	issueRotated   bool
	issueErr       error
	issueCallCount int
	lastIssueJWT   string
	lastDevice     string
}

func (f *fakeControlPlaneClient) MintLLMKey(_ context.Context, jwt, deviceName string) (controlplane.LLMKey, error) {
	f.issueCallCount++
	f.lastIssueJWT, f.lastDevice = jwt, deviceName
	if f.issueErr != nil {
		return controlplane.LLMKey{}, f.issueErr
	}
	return controlplane.LLMKey{Plaintext: f.issueKey, Rotated: f.issueRotated}, nil
}

// DeleteCurrentUserAccount satisfies controlplane.Client. The settings tests
// never exercise account deletion; account.go's own tests cover that path.
func (f *fakeControlPlaneClient) DeleteCurrentUserAccount(context.Context, string) ([]controlplane.AccountDeletionBlocker, error) {
	return nil, nil
}

func TestSettingsService_SyncReliantProvider_PersistsKeyAndEmitsRefetch(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	userID := "test-user"
	jwt := "test-jwt-token"
	auth.SetUserJWT(userID, jwt)

	fake := &fakeControlPlaneClient{issueKey: "rlat_abcdef0123456789abcdef0123456789"}
	svc := NewSettingsService(repo, nil).WithControlPlaneClient(fake)

	ctx := newSettingsServiceTestContext()

	resp, err := svc.SyncReliantProvider(ctx, connect.NewRequest(&reliantv1.SyncReliantProviderRequest{}))
	require.NoError(t, err)
	require.True(t, resp.Msg.Success)
	assert.True(t, resp.Msg.Synced)
	assert.True(t, resp.Msg.CreatedKey)
	assert.False(t, resp.Msg.RotatedKey)
	require.NotNil(t, resp.Msg.Provider)
	assert.Equal(t, "reliant", resp.Msg.Provider.Provider)
	assert.True(t, resp.Msg.Provider.Configured)

	stored, err := repo.GetProviderAPIKey(ctx, userID, "reliant")
	require.NoError(t, err)
	assert.Equal(t, "rlat_abcdef0123456789abcdef0123456789", stored)

	assert.Equal(t, 1, fake.issueCallCount)
	assert.Equal(t, jwt, fake.lastIssueJWT)
	assert.Equal(t, controlplane.ReliantProviderKeyName, fake.lastDevice)
}

// TestSettingsService_SyncReliantProvider_RotationComesFromControlPlane: each
// sync mints a NEW plaintext, so "rotated" is what control-plane reports (a
// previous key for this device was replaced) — never a plaintext comparison.
// The previous-plaintext heuristic would call every re-sync "rotated", even the
// first sync after a local DB reset.
func TestSettingsService_SyncReliantProvider_RotationComesFromControlPlane(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	userID := "test-user"
	auth.SetUserJWT(userID, "test-jwt-token")
	require.NoError(t, repo.SetProviderAPIKey(context.Background(), userID, "reliant", "rlat_previous0000000000000000000000000"))

	fake := &fakeControlPlaneClient{issueKey: "rlat_next000000000000000000000000000000", issueRotated: true}
	svc := NewSettingsService(repo, nil).WithControlPlaneClient(fake)
	resp, err := svc.SyncReliantProvider(newSettingsServiceTestContext(), connect.NewRequest(&reliantv1.SyncReliantProviderRequest{}))
	require.NoError(t, err)
	assert.True(t, resp.Msg.RotatedKey)
	assert.False(t, resp.Msg.CreatedKey)

	stored, err := repo.GetProviderAPIKey(context.Background(), userID, "reliant")
	require.NoError(t, err)
	assert.Equal(t, "rlat_next000000000000000000000000000000", stored)

	// A local store holding a key does NOT make a fresh control-plane mint a
	// rotation: control-plane is the authority on whether one was replaced.
	fake.issueRotated = false
	resp, err = svc.SyncReliantProvider(newSettingsServiceTestContext(), connect.NewRequest(&reliantv1.SyncReliantProviderRequest{}))
	require.NoError(t, err)
	assert.True(t, resp.Msg.CreatedKey)
	assert.False(t, resp.Msg.RotatedKey)
}

func TestSettingsService_SyncReliantProvider_MissingJWTUnauthenticated(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	// Different user, no JWT registered.
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "user-without-jwt")

	fake := &fakeControlPlaneClient{issueKey: "rlat_should_not_be_used"}
	svc := NewSettingsService(repo, nil).WithControlPlaneClient(fake)

	_, err := svc.SyncReliantProvider(ctx, connect.NewRequest(&reliantv1.SyncReliantProviderRequest{}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	assert.Equal(t, 0, fake.issueCallCount)
}

func TestSettingsService_SyncReliantProvider_ControlPlaneErrorPropagates(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	userID := "test-user"
	auth.SetUserJWT(userID, "test-jwt")

	fake := &fakeControlPlaneClient{issueErr: errors.New("boom")}
	svc := NewSettingsService(repo, nil).WithControlPlaneClient(fake)

	_, err := svc.SyncReliantProvider(newSettingsServiceTestContext(), connect.NewRequest(&reliantv1.SyncReliantProviderRequest{}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
}
