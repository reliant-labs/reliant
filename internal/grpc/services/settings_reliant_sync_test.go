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
	getCurrentUserResp *controlplanev1.GetCurrentUserResponse
	listOrgsResp       *controlplanev1.ListOrgsResponse
	createOrgResp      *controlplanev1.CreateOrgResponse
	listKeysResp       *controlplanev1.ListLLMKeysResponse
	createKeyResp      *controlplanev1.CreateLLMKeyResponse
	rotateKeyResp      *controlplanev1.RotateLLMKeyResponse

	getCurrentUserErr error
	listOrgsErr       error
	createOrgErr      error
	listKeysErr       error
	createKeyErr      error
	rotateKeyErr      error

	createOrgCalls int
	createKeyCalls int
	rotateKeyCalls int
	lastAuthHeader string
	lastCreateSlug string
}

func (f *fakeControlPlaneClient) GetCurrentUser(_ context.Context, authHeader string) (*controlplanev1.GetCurrentUserResponse, error) {
	f.lastAuthHeader = authHeader
	return f.getCurrentUserResp, f.getCurrentUserErr
}

func (f *fakeControlPlaneClient) ListOrgs(_ context.Context, authHeader string) (*controlplanev1.ListOrgsResponse, error) {
	f.lastAuthHeader = authHeader
	return f.listOrgsResp, f.listOrgsErr
}

func (f *fakeControlPlaneClient) CreateOrg(_ context.Context, authHeader, name, slug string) (*controlplanev1.CreateOrgResponse, error) {
	f.lastAuthHeader = authHeader
	f.createOrgCalls++
	f.lastCreateSlug = slug
	if f.createOrgResp == nil {
		f.createOrgResp = &controlplanev1.CreateOrgResponse{Org: &controlplanev1.Organization{Id: "org-created", Name: name, Slug: slug}}
	}
	return f.createOrgResp, f.createOrgErr
}

func (f *fakeControlPlaneClient) ListLLMKeys(_ context.Context, authHeader, _ string) (*controlplanev1.ListLLMKeysResponse, error) {
	f.lastAuthHeader = authHeader
	return f.listKeysResp, f.listKeysErr
}

func (f *fakeControlPlaneClient) CreateLLMKey(_ context.Context, authHeader, orgID, name string, models []string) (*controlplanev1.CreateLLMKeyResponse, error) {
	f.lastAuthHeader = authHeader
	f.createKeyCalls++
	if f.createKeyResp == nil {
		f.createKeyResp = &controlplanev1.CreateLLMKeyResponse{
			Key:          &controlplanev1.LLMKey{Id: "key-created", OrgId: orgID, Name: name, Models: models, Status: controlplanev1.LLMKeyStatus_LLM_KEY_STATUS_ACTIVE},
			PlaintextKey: "rlnt_created_key",
		}
	}
	return f.createKeyResp, f.createKeyErr
}

func (f *fakeControlPlaneClient) RotateLLMKey(_ context.Context, authHeader, keyID, gracePeriod string) (*controlplanev1.RotateLLMKeyResponse, error) {
	f.lastAuthHeader = authHeader
	f.rotateKeyCalls++
	if f.rotateKeyResp == nil {
		f.rotateKeyResp = &controlplanev1.RotateLLMKeyResponse{
			Key:          &controlplanev1.LLMKey{Id: keyID, OrgId: "org-1", Name: "Reliant App", Status: controlplanev1.LLMKeyStatus_LLM_KEY_STATUS_ACTIVE},
			PlaintextKey: fmt.Sprintf("rotated-%s-%s", keyID, gracePeriod),
		}
	}
	return f.rotateKeyResp, f.rotateKeyErr
}

func newReliantSyncTestContext() context.Context {
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	ctx = context.WithValue(ctx, auth.UserEmailContextKey, "test.user@example.com")
	return ctx
}

func TestSettingsService_SyncReliantProviderCreatesOrgAndKey(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	cp := &fakeControlPlaneClient{
		getCurrentUserResp: &controlplanev1.GetCurrentUserResponse{User: &controlplanev1.User{Id: "cp-user", Email: "test.user@example.com"}},
		listOrgsResp:       &controlplanev1.ListOrgsResponse{},
	}
	svc := NewSettingsService(repo, nil)
	svc.controlPlaneClient = cp

	req := connect.NewRequest(&reliantv1.SyncReliantProviderRequest{})
	req.Header().Set("Authorization", "Bearer sync-token")
	resp, err := svc.SyncReliantProvider(newReliantSyncTestContext(), req)
	require.NoError(t, err)
	require.True(t, resp.Msg.Success)
	assert.True(t, resp.Msg.CreatedOrg)
	assert.True(t, resp.Msg.CreatedKey)
	assert.False(t, resp.Msg.RotatedKey)
	assert.Equal(t, "Bearer sync-token", cp.lastAuthHeader)
	assert.Equal(t, 1, cp.createOrgCalls)
	assert.Equal(t, 1, cp.createKeyCalls)
	assert.Equal(t, 0, cp.rotateKeyCalls)
	assert.Contains(t, cp.lastCreateSlug, "test")

	stored, err := repo.GetProviderAPIKey(newReliantSyncTestContext(), "test-user", "reliant")
	require.NoError(t, err)
	assert.Equal(t, "rlnt_created_key", stored)
	require.NotNil(t, resp.Msg.Provider)
	assert.True(t, resp.Msg.Provider.Configured)
	assert.True(t, resp.Msg.Provider.HasApiKey)
}

func TestSettingsService_SyncReliantProviderRotatesWhenLocalKeyMissing(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()

	cp := &fakeControlPlaneClient{
		getCurrentUserResp: &controlplanev1.GetCurrentUserResponse{
			User: &controlplanev1.User{Id: "cp-user", Email: "test.user@example.com"},
			Organizations: []*controlplanev1.Organization{{Id: "org-1", Name: "Test Org", Slug: "test-org"}},
		},
		listKeysResp: &controlplanev1.ListLLMKeysResponse{
			Keys: []*controlplanev1.LLMKey{{Id: "key-1", OrgId: "org-1", Name: "Reliant App", Status: controlplanev1.LLMKeyStatus_LLM_KEY_STATUS_ACTIVE}},
		},
		rotateKeyResp: &controlplanev1.RotateLLMKeyResponse{
			Key:          &controlplanev1.LLMKey{Id: "key-1", OrgId: "org-1", Name: "Reliant App", Status: controlplanev1.LLMKeyStatus_LLM_KEY_STATUS_ACTIVE},
			PlaintextKey: "rlnt_rotated_key",
		},
	}
	svc := NewSettingsService(repo, nil)
	svc.controlPlaneClient = cp

	req := connect.NewRequest(&reliantv1.SyncReliantProviderRequest{})
	req.Header().Set("Authorization", "Bearer sync-token")
	resp, err := svc.SyncReliantProvider(newReliantSyncTestContext(), req)
	require.NoError(t, err)
	assert.False(t, resp.Msg.CreatedOrg)
	assert.False(t, resp.Msg.CreatedKey)
	assert.True(t, resp.Msg.RotatedKey)
	assert.Equal(t, 1, cp.rotateKeyCalls)

	stored, err := repo.GetProviderAPIKey(newReliantSyncTestContext(), "test-user", "reliant")
	require.NoError(t, err)
	assert.Equal(t, "rlnt_rotated_key", stored)
}

func TestSettingsService_SyncReliantProviderMigratesLegacyExistingLocalKeyOnce(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := newReliantSyncTestContext()
	require.NoError(t, repo.SetProviderAPIKey(ctx, "test-user", "reliant", "existing-local-key"))

	cp := &fakeControlPlaneClient{
		getCurrentUserResp: &controlplanev1.GetCurrentUserResponse{
			User: &controlplanev1.User{Id: "cp-user", Email: "test.user@example.com"},
			Organizations: []*controlplanev1.Organization{{Id: "org-1", Name: "Test Org", Slug: "test-org"}},
		},
		listKeysResp: &controlplanev1.ListLLMKeysResponse{
			Keys: []*controlplanev1.LLMKey{{Id: "key-1", OrgId: "org-1", Name: "Reliant App", Status: controlplanev1.LLMKeyStatus_LLM_KEY_STATUS_ACTIVE}},
		},
		rotateKeyResp: &controlplanev1.RotateLLMKeyResponse{
			Key:          &controlplanev1.LLMKey{Id: "key-1", OrgId: "org-1", Name: "Reliant App", Status: controlplanev1.LLMKeyStatus_LLM_KEY_STATUS_ACTIVE},
			PlaintextKey: "rlnt_migrated_key",
		},
	}
	svc := NewSettingsService(repo, nil)
	svc.controlPlaneClient = cp

	req := connect.NewRequest(&reliantv1.SyncReliantProviderRequest{})
	req.Header().Set("Authorization", "Bearer sync-token")
	resp, err := svc.SyncReliantProvider(ctx, req)
	require.NoError(t, err)
	assert.True(t, resp.Msg.Success)
	assert.False(t, resp.Msg.CreatedKey)
	assert.True(t, resp.Msg.RotatedKey)
	assert.Equal(t, 0, cp.createKeyCalls)
	assert.Equal(t, 1, cp.rotateKeyCalls)

	stored, err := repo.GetProviderAPIKey(ctx, "test-user", "reliant")
	require.NoError(t, err)
	assert.Equal(t, "rlnt_migrated_key", stored)

	marker, err := repo.GetSetting(ctx, "test-user", nil, reliantSyncInitializedSettingKey)
	require.NoError(t, err)
	assert.Equal(t, "true", marker.Value)
}

func TestSettingsService_SyncReliantProviderReusesExistingLocalKeyAfterInitialization(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := newReliantSyncTestContext()
	require.NoError(t, repo.SetProviderAPIKey(ctx, "test-user", "reliant", "existing-local-key"))

	svc := NewSettingsService(repo, nil)
	require.NoError(t, svc.upsertSetting(ctx, "test-user", nil, reliantSyncInitializedSettingKey, "true"))

	cp := &fakeControlPlaneClient{
		getCurrentUserResp: &controlplanev1.GetCurrentUserResponse{
			User: &controlplanev1.User{Id: "cp-user", Email: "test.user@example.com"},
			Organizations: []*controlplanev1.Organization{{Id: "org-1", Name: "Test Org", Slug: "test-org"}},
		},
		listKeysResp: &controlplanev1.ListLLMKeysResponse{
			Keys: []*controlplanev1.LLMKey{{Id: "key-1", OrgId: "org-1", Name: "Reliant App", Status: controlplanev1.LLMKeyStatus_LLM_KEY_STATUS_ACTIVE}},
		},
	}
	svc.controlPlaneClient = cp

	req := connect.NewRequest(&reliantv1.SyncReliantProviderRequest{})
	req.Header().Set("Authorization", "Bearer sync-token")
	resp, err := svc.SyncReliantProvider(ctx, req)
	require.NoError(t, err)
	assert.True(t, resp.Msg.Success)
	assert.Equal(t, 0, cp.createKeyCalls)
	assert.Equal(t, 0, cp.rotateKeyCalls)

	stored, err := repo.GetProviderAPIKey(ctx, "test-user", "reliant")
	require.NoError(t, err)
	assert.Equal(t, "existing-local-key", stored)
}

func TestSettingsService_SyncReliantProviderForceRotateOverridesInitializedLocalKey(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := newReliantSyncTestContext()
	require.NoError(t, repo.SetProviderAPIKey(ctx, "test-user", "reliant", "existing-local-key"))

	svc := NewSettingsService(repo, nil)
	require.NoError(t, svc.upsertSetting(ctx, "test-user", nil, reliantSyncInitializedSettingKey, "true"))

	cp := &fakeControlPlaneClient{
		getCurrentUserResp: &controlplanev1.GetCurrentUserResponse{
			User: &controlplanev1.User{Id: "cp-user", Email: "test.user@example.com"},
			Organizations: []*controlplanev1.Organization{{Id: "org-1", Name: "Test Org", Slug: "test-org"}},
		},
		listKeysResp: &controlplanev1.ListLLMKeysResponse{
			Keys: []*controlplanev1.LLMKey{{Id: "key-1", OrgId: "org-1", Name: "Reliant App", Status: controlplanev1.LLMKeyStatus_LLM_KEY_STATUS_ACTIVE}},
		},
		rotateKeyResp: &controlplanev1.RotateLLMKeyResponse{
			Key:          &controlplanev1.LLMKey{Id: "key-1", OrgId: "org-1", Name: "Reliant App", Status: controlplanev1.LLMKeyStatus_LLM_KEY_STATUS_ACTIVE},
			PlaintextKey: "rlnt_force_rotated_key",
		},
	}
	svc.controlPlaneClient = cp

	req := connect.NewRequest(&reliantv1.SyncReliantProviderRequest{ForceRotate: true})
	req.Header().Set("Authorization", "Bearer sync-token")
	resp, err := svc.SyncReliantProvider(ctx, req)
	require.NoError(t, err)
	assert.True(t, resp.Msg.Success)
	assert.True(t, resp.Msg.RotatedKey)
	assert.Equal(t, 1, cp.rotateKeyCalls)

	stored, err := repo.GetProviderAPIKey(ctx, "test-user", "reliant")
	require.NoError(t, err)
	assert.Equal(t, "rlnt_force_rotated_key", stored)
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