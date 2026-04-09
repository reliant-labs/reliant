package services

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/reliant/internal/analytics"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	controlplanev1 "github.com/reliant-labs/reliant/internal/gen/controlplane/v1"
	"github.com/reliant-labs/reliant/internal/gen/controlplane/v1/controlplanev1connect"
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/logging"
)

const (
	reliantProviderID                = "reliant"
	reliantSyncInitializedSettingKey = "providers.reliant.sync_initialized"
	defaultControlPlaneBaseURL       = "http://localhost:8090"
	reliantKeyRotationGracePeriod    = "24h"
)

type controlPlaneClient interface {
	GetCurrentUserReliantState(ctx context.Context, authHeader string) (*controlplanev1.GetCurrentUserReliantStateResponse, error)
	RepairCurrentUserReliantAccess(ctx context.Context, authHeader string) (*controlplanev1.RepairCurrentUserReliantAccessResponse, error)
	RotateCurrentUserReliantAccess(ctx context.Context, authHeader, gracePeriod string) (*controlplanev1.RotateCurrentUserReliantAccessResponse, error)
}

type connectControlPlaneClient struct {
	httpClient *http.Client
	baseURL    string
}

func newControlPlaneClient(baseURL string) controlPlaneClient {
	trimmed := strings.TrimSpace(baseURL)
	if trimmed == "" {
		trimmed = getControlPlaneBaseURL()
	}
	return &connectControlPlaneClient{
		httpClient: &http.Client{Timeout: 30 * time.Second},
		baseURL:    strings.TrimRight(trimmed, "/"),
	}
}

func getControlPlaneBaseURL() string {
	for _, key := range []string{"RELIANT_CONTROL_PLANE_URL", "CONTROL_PLANE_API_URL", "CONTROL_PLANE_BASE_URL"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return defaultControlPlaneBaseURL
}

func (c *connectControlPlaneClient) GetCurrentUserReliantState(ctx context.Context, authHeader string) (*controlplanev1.GetCurrentUserReliantStateResponse, error) {
	client := controlplanev1connect.NewBillingServiceClient(c.httpClient, c.baseURL)
	req := connect.NewRequest(&controlplanev1.GetCurrentUserReliantStateRequest{})
	attachAuthorization(req, authHeader)
	resp, err := client.GetCurrentUserReliantState(ctx, req)
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

func (c *connectControlPlaneClient) RepairCurrentUserReliantAccess(ctx context.Context, authHeader string) (*controlplanev1.RepairCurrentUserReliantAccessResponse, error) {
	client := controlplanev1connect.NewBillingServiceClient(c.httpClient, c.baseURL)
	req := connect.NewRequest(&controlplanev1.RepairCurrentUserReliantAccessRequest{})
	attachAuthorization(req, authHeader)
	resp, err := client.RepairCurrentUserReliantAccess(ctx, req)
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

func (c *connectControlPlaneClient) RotateCurrentUserReliantAccess(ctx context.Context, authHeader, gracePeriod string) (*controlplanev1.RotateCurrentUserReliantAccessResponse, error) {
	client := controlplanev1connect.NewBillingServiceClient(c.httpClient, c.baseURL)
	msg := &controlplanev1.RotateCurrentUserReliantAccessRequest{}
	if trimmed := strings.TrimSpace(gracePeriod); trimmed != "" {
		msg.GracePeriod = &trimmed
	}
	req := connect.NewRequest(msg)
	attachAuthorization(req, authHeader)
	resp, err := client.RotateCurrentUserReliantAccess(ctx, req)
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

func attachAuthorization[T any](req *connect.Request[T], authHeader string) {
	if trimmed := strings.TrimSpace(authHeader); trimmed != "" {
		req.Header().Set("Authorization", trimmed)
	}
}

func makeReliantProviderStatus(hasKey bool, maskedKey string) *reliantv1.ProviderStatus {
	status := &reliantv1.ProviderStatus{
		Provider:    reliantProviderID,
		DisplayName: "Reliant",
		Configured:  hasKey,
		HasApiKey:   hasKey,
	}
	if hasKey && strings.TrimSpace(maskedKey) != "" {
		status.MaskedKey = &maskedKey
	}
	return status
}

func maskProviderKey(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if len(trimmed) > 8 {
		return trimmed[:4] + "..." + trimmed[len(trimmed)-4:]
	}
	if trimmed != "" {
		return "***"
	}
	return ""
}

func (s *SettingsService) isReliantSyncInitialized(ctx context.Context, userID string) (bool, error) {
	setting, err := s.database.GetSetting(ctx, userID, nil, reliantSyncInitializedSettingKey)
	if err != nil {
		if isSettingNotFoundError(err) {
			return false, nil
		}
		return false, err
	}
	return strings.EqualFold(strings.TrimSpace(setting.Value), "true"), nil
}

func (s *SettingsService) markReliantSyncInitialized(ctx context.Context, userID string) error {
	return s.upsertSetting(ctx, userID, nil, reliantSyncInitializedSettingKey, "true")
}

func (s *SettingsService) SyncReliantProvider(ctx context.Context, req *connect.Request[reliantv1.SyncReliantProviderRequest]) (*connect.Response[reliantv1.SyncReliantProviderResponse], error) {
	userID := auth.MustGetUserID(ctx)
	authHeader := strings.TrimSpace(req.Header().Get("Authorization"))
	if authHeader == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("missing authorization header"))
	}

	client := s.controlPlaneClient
	if client == nil {
		client = newControlPlaneClient("")
	}

	syncInitialized, err := s.isReliantSyncInitialized(ctx, userID)
	if err != nil {
		logging.Error("Failed to load Reliant sync migration state", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to load Reliant sync state"))
	}

	stateResp, err := client.GetCurrentUserReliantState(ctx, authHeader)
	if err != nil {
		logging.Error("Failed to fetch current Reliant managed state", "error", err)
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("failed to load Reliant account state"))
	}

	managedAccess := stateResp.GetManagedAccess()
	needsRepair := managedAccess == nil || strings.TrimSpace(managedAccess.GetInternalOrgId()) == ""
	if !needsRepair && strings.TrimSpace(managedAccess.GetActiveLlmKeyId()) == "" {
		needsRepair = true
	}
	if req.Msg.GetForceRotate() {
		needsRepair = false
	}
	if needsRepair {
		repairResp, err := client.RepairCurrentUserReliantAccess(ctx, authHeader)
		if err != nil {
			logging.Error("Failed to repair managed Reliant access before sync", "error", err)
			return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("failed to repair Reliant access"))
		}
		managedAccess = repairResp.GetManagedAccess()
	}

	rotateResp, err := client.RotateCurrentUserReliantAccess(ctx, authHeader, reliantKeyRotationGracePeriod)
	if err != nil {
		logging.Error("Failed to rotate managed Reliant access", "error", err)
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("failed to rotate Reliant key"))
	}

	plaintextKey := strings.TrimSpace(rotateResp.GetPlaintextKey())
	if plaintextKey == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("no Reliant plaintext key available to sync"))
	}

	if err := s.database.SetProviderAPIKey(ctx, userID, reliantProviderID, plaintextKey); err != nil {
		logging.Error("Failed to persist Reliant provider key", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to persist Reliant key"))
	}
	if !syncInitialized {
		if err := s.markReliantSyncInitialized(ctx, userID); err != nil {
			logging.Error("Failed to persist Reliant sync migration marker", "error", err)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to persist Reliant sync state"))
		}
	}

	analyticsClient := analytics.GetClientForUser(ctx, userID)
	action := "updated"
	if rotateResp.GetReplaced() {
		action = "connected"
	}
	analyticsClient.TrackProviderSettingsUpdated(analytics.ProviderSettingsUpdatedMetrics{
		Provider:   reliantProviderID,
		Action:     action,
		AuthMethod: "control_plane_sync",
	})

	existingKeys, err := s.database.GetProviderAPIKeys(ctx, userID)
	totalProviders := 0
	if err == nil {
		for _, value := range existingKeys {
			if strings.TrimSpace(value) != "" {
				totalProviders++
			}
		}
	}
	analyticsClient.TrackAPIKeyConfigured(analytics.APIKeyConfiguredMetrics{
		Provider:       reliantProviderID,
		AuthMethod:     "control_plane_sync",
		IsFirstKey:     totalProviders <= 1,
		TotalProviders: totalProviders,
	})

	if err := s.database.EmitUserRefetch(ctx, userID, db.RefetchConfigHealth, db.RefetchOpts{}); err != nil {
		logging.Warn("Failed to emit config_health refetch after Reliant sync", "error", err)
	}

	masked := maskProviderKey(plaintextKey)
	return connect.NewResponse(&reliantv1.SyncReliantProviderResponse{
		Success:    true,
		Message:    "Reliant provider synced",
		Synced:     true,
		CreatedOrg: false,
		CreatedKey: rotateResp.GetReplaced(),
		RotatedKey: rotateResp.GetRotated(),
		Provider:   makeReliantProviderStatus(true, masked),
	}), nil
}
