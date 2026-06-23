package services

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/reliant/internal/auth"
	controlplanev1 "github.com/reliant-labs/reliant/gen/controlplane/v1"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"

	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeControlPlaneClient struct {
	issueKey       string
	issueErr       error
	issueCallCount int
	lastIssueJWT   string
}

func (f *fakeControlPlaneClient) CheckManagedReliantAffordability(ctx context.Context, managedKey string, request controlplane.ManagedReliantAffordabilityRequest) (*controlplanev1.CheckManagedReliantAffordabilityResponse, error) {
	return nil, errors.New("not implemented in fake")
}

func (f *fakeControlPlaneClient) ReserveManagedReliantUsage(ctx context.Context, managedKey string, request controlplane.ManagedReliantReservationRequest) (*controlplanev1.ReserveManagedReliantUsageResponse, error) {
	return nil, errors.New("not implemented in fake")
}

func (f *fakeControlPlaneClient) FinalizeManagedReliantUsage(ctx context.Context, managedKey string, request controlplane.ManagedReliantFinalizeRequest) (*controlplanev1.FinalizeManagedReliantUsageResponse, error) {
	return nil, errors.New("not implemented in fake")
}

func (f *fakeControlPlaneClient) ReleaseManagedReliantUsageReservation(ctx context.Context, managedKey, reservationID string) (*controlplanev1.ReleaseManagedReliantUsageReservationResponse, error) {
	return nil, errors.New("not implemented in fake")
}

func (f *fakeControlPlaneClient) IssueMyReliantAPIKey(ctx context.Context, jwt string) (string, error) {
	f.issueCallCount++
	f.lastIssueJWT = jwt
	if f.issueErr != nil {
		return "", f.issueErr
	}
	return f.issueKey, nil
}

func TestSettingsService_SyncReliantProvider_PersistsKeyAndEmitsRefetch(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	userID := "test-user"
	jwt := "test-jwt-token"
	auth.SetUserJWT(userID, jwt)

	fake := &fakeControlPlaneClient{issueKey: "rlnt_abcdef0123456789"}
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
	assert.Equal(t, "rlnt_abcdef0123456789", stored)

	assert.Equal(t, 1, fake.issueCallCount)
	assert.Equal(t, jwt, fake.lastIssueJWT)
}

func TestSettingsService_SyncReliantProvider_IdempotentReturnsSameKey(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	userID := "test-user"
	jwt := "test-jwt-token"
	auth.SetUserJWT(userID, jwt)

	existing := "rlnt_existing0123456789"
	require.NoError(t, repo.SetProviderAPIKey(context.Background(), userID, "reliant", existing))

	fake := &fakeControlPlaneClient{issueKey: existing}
	svc := NewSettingsService(repo, nil).WithControlPlaneClient(fake)

	resp, err := svc.SyncReliantProvider(newSettingsServiceTestContext(), connect.NewRequest(&reliantv1.SyncReliantProviderRequest{}))
	require.NoError(t, err)

	assert.False(t, resp.Msg.CreatedKey)
	assert.False(t, resp.Msg.RotatedKey)
	assert.True(t, resp.Msg.Synced)
}

func TestSettingsService_SyncReliantProvider_MissingJWTUnauthenticated(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	// Different user, no JWT registered.
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "user-without-jwt")

	fake := &fakeControlPlaneClient{issueKey: "rlnt_should_not_be_used"}
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
