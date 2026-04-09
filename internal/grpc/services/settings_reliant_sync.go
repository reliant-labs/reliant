package services

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/reliant/internal/analytics"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	controlplanev1 "github.com/reliant-labs/reliant/internal/gen/controlplane/v1"
	"github.com/reliant-labs/reliant/internal/gen/controlplane/v1/controlplanev1connect"
	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	reliantdriver "github.com/reliant-labs/reliant/internal/llm/drivers/reliant"
	"github.com/reliant-labs/reliant/internal/logging"
)

const (
	reliantProviderID                   = "reliant"
	reliantSyncInitializedSettingKey    = "providers.reliant.sync_initialized"
	defaultControlPlaneBaseURL         = "http://localhost:8090"
	reliantKeyNamePrefix               = "Reliant App"
	reliantKeyRotationGracePeriod      = "24h"
)

var nonSlugChars = regexp.MustCompile(`[^a-z0-9-]+`)

type controlPlaneClient interface {
	GetCurrentUser(ctx context.Context, authHeader string) (*controlplanev1.GetCurrentUserResponse, error)
	ListOrgs(ctx context.Context, authHeader string) (*controlplanev1.ListOrgsResponse, error)
	CreateOrg(ctx context.Context, authHeader, name, slug string) (*controlplanev1.CreateOrgResponse, error)
	ListLLMKeys(ctx context.Context, authHeader, orgID string) (*controlplanev1.ListLLMKeysResponse, error)
	CreateLLMKey(ctx context.Context, authHeader, orgID, name string, models []string) (*controlplanev1.CreateLLMKeyResponse, error)
	RotateLLMKey(ctx context.Context, authHeader, keyID, gracePeriod string) (*controlplanev1.RotateLLMKeyResponse, error)
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

func (c *connectControlPlaneClient) GetCurrentUser(ctx context.Context, authHeader string) (*controlplanev1.GetCurrentUserResponse, error) {
	client := controlplanev1connect.NewUserServiceClient(c.httpClient, c.baseURL)
	req := connect.NewRequest(&controlplanev1.GetCurrentUserRequest{})
	attachAuthorization(req, authHeader)
	resp, err := client.GetCurrentUser(ctx, req)
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

func (c *connectControlPlaneClient) ListOrgs(ctx context.Context, authHeader string) (*controlplanev1.ListOrgsResponse, error) {
	client := controlplanev1connect.NewOrgServiceClient(c.httpClient, c.baseURL)
	req := connect.NewRequest(&controlplanev1.ListOrgsRequest{})
	attachAuthorization(req, authHeader)
	resp, err := client.ListOrgs(ctx, req)
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

func (c *connectControlPlaneClient) CreateOrg(ctx context.Context, authHeader, name, slug string) (*controlplanev1.CreateOrgResponse, error) {
	client := controlplanev1connect.NewOrgServiceClient(c.httpClient, c.baseURL)
	req := connect.NewRequest(&controlplanev1.CreateOrgRequest{Name: name, Slug: slug})
	attachAuthorization(req, authHeader)
	resp, err := client.CreateOrg(ctx, req)
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

func (c *connectControlPlaneClient) ListLLMKeys(ctx context.Context, authHeader, orgID string) (*controlplanev1.ListLLMKeysResponse, error) {
	client := controlplanev1connect.NewLLMGatewayServiceClient(c.httpClient, c.baseURL)
	req := connect.NewRequest(&controlplanev1.ListLLMKeysRequest{OrgId: orgID})
	attachAuthorization(req, authHeader)
	resp, err := client.ListLLMKeys(ctx, req)
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

func (c *connectControlPlaneClient) CreateLLMKey(ctx context.Context, authHeader, orgID, name string, models []string) (*controlplanev1.CreateLLMKeyResponse, error) {
	client := controlplanev1connect.NewLLMGatewayServiceClient(c.httpClient, c.baseURL)
	req := connect.NewRequest(&controlplanev1.CreateLLMKeyRequest{
		OrgId:  orgID,
		Name:   name,
		Models: models,
	})
	attachAuthorization(req, authHeader)
	resp, err := client.CreateLLMKey(ctx, req)
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

func (c *connectControlPlaneClient) RotateLLMKey(ctx context.Context, authHeader, keyID, gracePeriod string) (*controlplanev1.RotateLLMKeyResponse, error) {
	client := controlplanev1connect.NewLLMGatewayServiceClient(c.httpClient, c.baseURL)
	req := connect.NewRequest(&controlplanev1.RotateLLMKeyRequest{KeyId: keyID, GracePeriod: gracePeriod})
	attachAuthorization(req, authHeader)
	resp, err := client.RotateLLMKey(ctx, req)
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

func deriveReliantOrgName(email string) string {
	trimmed := strings.TrimSpace(email)
	if trimmed == "" {
		return "Reliant"
	}
	local := trimmed
	if at := strings.Index(local, "@"); at > 0 {
		local = local[:at]
	}
	local = strings.TrimSpace(strings.NewReplacer(".", " ", "_", " ", "-", " ").Replace(local))
	if local == "" {
		return "Reliant"
	}
	parts := strings.Fields(local)
	for i, p := range parts {
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, " ") + "'s Reliant"
}

func deriveReliantOrgSlug(email, userID string) string {
	base := strings.ToLower(strings.TrimSpace(email))
	if at := strings.Index(base, "@"); at > 0 {
		base = base[:at]
	}
	base = strings.ReplaceAll(base, "_", "-")
	base = nonSlugChars.ReplaceAllString(base, "-")
	base = strings.Trim(base, "-")
	if len(base) > 40 {
		base = strings.Trim(base[:40], "-")
	}
	if len(base) < 3 {
		base = "reliant"
	}

	suffix := strings.ToLower(strings.TrimSpace(userID))
	suffix = nonSlugChars.ReplaceAllString(suffix, "")
	if len(suffix) > 8 {
		suffix = suffix[:8]
	}

	candidate := strings.Trim(base+"-"+suffix, "-")
	if candidate == "" {
		candidate = "reliant-org"
	}
	if candidate[0] < 'a' || candidate[0] > 'z' {
		candidate = "r-" + candidate
	}
	if len(candidate) > 50 {
		candidate = strings.Trim(candidate[:50], "-")
	}
	if len(candidate) < 3 {
		candidate = "reliant-org"
	}
	return candidate
}

func selectOrgFromCurrentUser(resp *controlplanev1.GetCurrentUserResponse) *controlplanev1.Organization {
	if resp == nil || len(resp.Organizations) == 0 {
		return nil
	}
	return resp.Organizations[0]
}

func selectFirstOrg(resp *controlplanev1.ListOrgsResponse) *controlplanev1.Organization {
	if resp == nil || len(resp.Orgs) == 0 {
		return nil
	}
	return resp.Orgs[0]
}

func selectActiveReliantKey(resp *controlplanev1.ListLLMKeysResponse) *controlplanev1.LLMKey {
	if resp == nil {
		return nil
	}
	for _, key := range resp.Keys {
		if key != nil && key.Status == controlplanev1.LLMKeyStatus_LLM_KEY_STATUS_ACTIVE {
			return key
		}
	}
	return nil
}

func supportedReliantModelStrings() []string {
	out := make([]string, 0, len(reliantdriver.SupportedModels))
	for _, modelID := range reliantdriver.SupportedModels {
		out = append(out, string(modelID))
	}
	return out
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

	cpUser, err := client.GetCurrentUser(ctx, authHeader)
	if err != nil {
		logging.Error("Failed to fetch current control-plane user", "error", err)
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("failed to load Reliant account state"))
	}

	org := selectOrgFromCurrentUser(cpUser)
	createdOrg := false
	if org == nil {
		if orgsResp, err := client.ListOrgs(ctx, authHeader); err == nil {
			org = selectFirstOrg(orgsResp)
		} else {
			logging.Warn("Failed to list control-plane orgs during Reliant sync", "error", err)
		}
	}
	if org == nil {
		email, _ := ctx.Value(auth.UserEmailContextKey).(string)
		createResp, err := client.CreateOrg(ctx, authHeader, deriveReliantOrgName(email), deriveReliantOrgSlug(email, userID))
		if err != nil {
			logging.Error("Failed to create control-plane org during Reliant sync", "error", err)
			return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("failed to create Reliant organization"))
		}
		org = createResp.Org
		createdOrg = true
	}
	if org == nil || strings.TrimSpace(org.Id) == "" {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("control-plane org resolution failed"))
	}

	existingLocalKey, _ := s.database.GetProviderAPIKey(ctx, userID, reliantProviderID)
	syncInitialized, err := s.isReliantSyncInitialized(ctx, userID)
	if err != nil {
		logging.Error("Failed to load Reliant sync migration state", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to load Reliant sync state"))
	}

	keysResp, err := client.ListLLMKeys(ctx, authHeader, org.Id)
	if err != nil {
		logging.Error("Failed to list Reliant control-plane keys", "orgID", org.Id, "error", err)
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("failed to load Reliant key state"))
	}
	activeKey := selectActiveReliantKey(keysResp)
	shouldMigrateLegacyLocalKey := !syncInitialized && strings.TrimSpace(existingLocalKey) != "" && !req.Msg.ForceRotate

	var plaintextKey string
	createdKey := false
	rotatedKey := false

	switch {
	case activeKey == nil:
		createResp, err := client.CreateLLMKey(ctx, authHeader, org.Id, reliantKeyNamePrefix, supportedReliantModelStrings())
		if err != nil {
			logging.Error("Failed to create Reliant control-plane key", "orgID", org.Id, "error", err)
			return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("failed to create Reliant key"))
		}
		plaintextKey = strings.TrimSpace(createResp.PlaintextKey)
		createdKey = true
	case req.Msg.ForceRotate || strings.TrimSpace(existingLocalKey) == "" || shouldMigrateLegacyLocalKey:
		rotateResp, err := client.RotateLLMKey(ctx, authHeader, activeKey.Id, reliantKeyRotationGracePeriod)
		if err != nil {
			logging.Error("Failed to rotate Reliant control-plane key", "keyID", activeKey.Id, "error", err)
			return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("failed to rotate Reliant key"))
		}
		plaintextKey = strings.TrimSpace(rotateResp.PlaintextKey)
		rotatedKey = true
	default:
		plaintextKey = existingLocalKey
	}

	if strings.TrimSpace(plaintextKey) == "" {
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
	if createdKey {
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
		CreatedOrg: createdOrg,
		CreatedKey: createdKey,
		RotatedKey: rotatedKey,
		Provider:   makeReliantProviderStatus(true, masked),
	}), nil
}