package services

import (
	"context"
	"fmt"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	controlplanev1 "github.com/reliant-labs/reliant/internal/gen/controlplane/v1"
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeControlPlaneClient struct {
	getCurrentUserReliantStateResp     *controlplanev1.GetCurrentUserReliantStateResponse
	repairCurrentUserReliantAccessResp *controlplanev1.RepairCurrentUserReliantAccessResponse
	rotateCurrentUserReliantAccessResp *controlplanev1.RotateCurrentUserReliantAccessResponse

	getCurrentUserReliantStateErr     error
	repairCurrentUserReliantAccessErr error
	rotateCurrentUserReliantAccessErr error

	repairCalls     int
	rotateCalls     int
	lastAuthHeader  string
	lastGracePeriod string
}

func (f *fakeControlPlaneClient) GetCurrentUserReliantState(_ context.Context, authHeader string) (*controlplanev1.GetCurrentUserReliantStateResponse, error) {
	f.lastAuthHeader = authHeader
	if f.getCurrentUserReliantStateResp == nil {
		f.getCurrentUserReliantStateResp = &controlplanev1.GetCurrentUserReliantStateResponse{}
	}
	return f.getCurrentUserReliantStateResp, f.getCurrentUserReliantStateErr
}

func (f *fakeControlPlaneClient) RepairCurrentUserReliantAccess(_ context.Context, authHeader string) (*controlplanev1.RepairCurrentUserReliantAccessResponse, error) {
	f.lastAuthHeader = authHeader
	f.repairCalls++
	if f.repairCurrentUserReliantAccessResp == nil {
		f.repairCurrentUserReliantAccessResp = &controlplanev1.RepairCurrentUserReliantAccessResponse{
			ManagedAccess: &controlplanev1.ManagedReliantAccess{
				InternalOrgId:  "org-1",
				ActiveLlmKeyId: "key-1",
			},
			Repaired: true,
		}
	}
	return f.repairCurrentUserReliantAccessResp, f.repairCurrentUserReliantAccessErr
}

func (f *fakeControlPlaneClient) RotateCurrentUserReliantAccess(_ context.Context, authHeader, gracePeriod string) (*controlplanev1.RotateCurrentUserReliantAccessResponse, error) {
	f.lastAuthHeader = authHeader
	f.lastGracePeriod = gracePeriod
	f.rotateCalls++
	if f.rotateCurrentUserReliantAccessResp == nil {
		f.rotateCurrentUserReliantAccessResp = &controlplanev1.RotateCurrentUserReliantAccessResponse{
			Rotated:      true,
			PlaintextKey: fmt.Sprintf("rotated-%s", gracePeriod),
		}
	}
	return f.rotateCurrentUserReliantAccessResp, f.rotateCurrentUserReliantAccessErr
}

func newReliantSyncTestContext() context.Context {
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	ctx = context.WithValue(ctx, auth.UserEmailContextKey, "test.user@example.com")
	return ctx
}

func TestSettingsService_SyncReliantProviderRepairsMissingManagedAccessThenRotates(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	cp := &fakeControlPlaneClient{
		getCurrentUserReliantStateResp: &controlplanev1.GetCurrentUserReliantStateResponse{},
		repairCurrentUserReliantAccessResp: &controlplanev1.RepairCurrentUserReliantAccessResponse{
			ManagedAccess: &controlplanev1.ManagedReliantAccess{
				InternalOrgId:  "org-1",
				ActiveLlmKeyId: "key-1",
			},
			Repaired: true,
		},
		rotateCurrentUserReliantAccessResp: &controlplanev1.RotateCurrentUserReliantAccessResponse{
			Rotated:      true,
			Replaced:     true,
			PlaintextKey: "rlnt_repaired_key",
		},
	}
	svc := NewSettingsService(repo, nil)
	svc.controlPlaneClient = cp

	req := connect.NewRequest(&reliantv1.SyncReliantProviderRequest{})
	req.Header().Set("Authorization", "Bearer sync-token")
	resp, err := svc.SyncReliantProvider(newReliantSyncTestContext(), req)
	require.NoError(t, err)
	require.True(t, resp.Msg.Success)
	assert.Equal(t, "Bearer sync-token", cp.lastAuthHeader)
	assert.Equal(t, 1, cp.repairCalls)
	assert.Equal(t, 1, cp.rotateCalls)
	assert.Equal(t, reliantKeyRotationGracePeriod, cp.lastGracePeriod)
	assert.True(t, resp.Msg.CreatedKey)
	assert.True(t, resp.Msg.RotatedKey)
	assert.False(t, resp.Msg.CreatedOrg)

	stored, err := repo.GetProviderAPIKey(newReliantSyncTestContext(), "test-user", "reliant")
	require.NoError(t, err)
	assert.Equal(t, "rlnt_repaired_key", stored)
	require.NotNil(t, resp.Msg.Provider)
	assert.True(t, resp.Msg.Provider.Configured)
	assert.True(t, resp.Msg.Provider.HasApiKey)

	marker, err := repo.GetSetting(newReliantSyncTestContext(), "test-user", nil, reliantSyncInitializedSettingKey)
	require.NoError(t, err)
	assert.Equal(t, "true", marker.Value)
}

func TestSettingsService_SyncReliantProviderRotatesExistingManagedAccess(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := newReliantSyncTestContext()
	require.NoError(t, repo.SetProviderAPIKey(ctx, "test-user", "reliant", "existing-local-key"))
	svc := NewSettingsService(repo, nil)
	require.NoError(t, svc.upsertSetting(ctx, "test-user", nil, reliantSyncInitializedSettingKey, "true"))

	cp := &fakeControlPlaneClient{
		getCurrentUserReliantStateResp: &controlplanev1.GetCurrentUserReliantStateResponse{
			ManagedAccess: &controlplanev1.ManagedReliantAccess{
				InternalOrgId:  "org-1",
				ActiveLlmKeyId: "key-1",
			},
		},
		rotateCurrentUserReliantAccessResp: &controlplanev1.RotateCurrentUserReliantAccessResponse{
			Rotated:      true,
			Replaced:     false,
			PlaintextKey: "rlnt_rotated_key",
		},
	}
	svc.controlPlaneClient = cp

	req := connect.NewRequest(&reliantv1.SyncReliantProviderRequest{})
	req.Header().Set("Authorization", "Bearer sync-token")
	resp, err := svc.SyncReliantProvider(ctx, req)
	require.NoError(t, err)
	assert.True(t, resp.Msg.Success)
	assert.Equal(t, 0, cp.repairCalls)
	assert.Equal(t, 1, cp.rotateCalls)
	assert.False(t, resp.Msg.CreatedKey)
	assert.True(t, resp.Msg.RotatedKey)

	stored, err := repo.GetProviderAPIKey(ctx, "test-user", "reliant")
	require.NoError(t, err)
	assert.Equal(t, "rlnt_rotated_key", stored)
}

func TestSettingsService_SyncReliantProviderForceRotateSkipsRepair(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := newReliantSyncTestContext()
	require.NoError(t, repo.SetProviderAPIKey(ctx, "test-user", "reliant", "existing-local-key"))
	svc := NewSettingsService(repo, nil)
	require.NoError(t, svc.upsertSetting(ctx, "test-user", nil, reliantSyncInitializedSettingKey, "true"))

	cp := &fakeControlPlaneClient{
		getCurrentUserReliantStateResp: &controlplanev1.GetCurrentUserReliantStateResponse{},
		rotateCurrentUserReliantAccessResp: &controlplanev1.RotateCurrentUserReliantAccessResponse{
			Rotated:      true,
			PlaintextKey: "rlnt_force_rotated_key",
		},
	}
	svc.controlPlaneClient = cp

	req := connect.NewRequest(&reliantv1.SyncReliantProviderRequest{ForceRotate: true})
	req.Header().Set("Authorization", "Bearer sync-token")
	resp, err := svc.SyncReliantProvider(ctx, req)
	require.NoError(t, err)
	assert.True(t, resp.Msg.Success)
	assert.Equal(t, 0, cp.repairCalls)
	assert.Equal(t, 1, cp.rotateCalls)
	assert.True(t, resp.Msg.RotatedKey)

	stored, err := repo.GetProviderAPIKey(ctx, "test-user", "reliant")
	require.NoError(t, err)
	assert.Equal(t, "rlnt_force_rotated_key", stored)
}

func TestSettingsService_SyncReliantProviderFailsWhenRotateReturnsNoPlaintext(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	cp := &fakeControlPlaneClient{
		getCurrentUserReliantStateResp: &controlplanev1.GetCurrentUserReliantStateResponse{
			ManagedAccess: &controlplanev1.ManagedReliantAccess{
				InternalOrgId:  "org-1",
				ActiveLlmKeyId: "key-1",
			},
		},
		rotateCurrentUserReliantAccessResp: &controlplanev1.RotateCurrentUserReliantAccessResponse{
			Rotated:      true,
			PlaintextKey: "",
		},
	}
	svc := NewSettingsService(repo, nil)
	svc.controlPlaneClient = cp

	req := connect.NewRequest(&reliantv1.SyncReliantProviderRequest{})
	req.Header().Set("Authorization", "Bearer sync-token")
	_, err := svc.SyncReliantProvider(newReliantSyncTestContext(), req)
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}

func TestSettingsService_SyncReliantProviderRequiresAuthorizationHeader(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	svc := NewSettingsService(repo, nil)
	svc.controlPlaneClient = &fakeControlPlaneClient{}

	_, err := svc.SyncReliantProvider(newReliantSyncTestContext(), connect.NewRequest(&reliantv1.SyncReliantProviderRequest{}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}
