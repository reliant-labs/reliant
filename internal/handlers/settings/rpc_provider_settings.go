package settings

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/activity"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/controlplane"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/llm/drivers/antigravity"
	"github.com/reliant-labs/reliant/internal/llm/drivers/copilot"
	"github.com/reliant-labs/reliant/internal/llm/models"

	"github.com/reliant-labs/reliant/internal/logging"
)

// ============================================================================
// Sub-phase 6d: Provider Settings (API Keys)
// ============================================================================

// providerOutputModalities returns the distinct output modalities the given
// provider's models can generate, derived from the model registry.
//
// Derived rather than hardcoded per provider: adding an image model to
// models.yaml makes its provider image-capable with no change here. A static
// per-provider map would drift the moment a provider ships a new endpoint, and
// the drift would be silent — the UI would keep claiming a capability the
// registry no longer backs, or hide one it does.
//
// Order is stable (text first, then the generative extras alphabetically) so
// the UI and any test see a deterministic list rather than map order.
func providerOutputModalities(provider string) []string {
	registry, err := models.GetRegistry()
	if err != nil {
		logging.Warn("Failed to load model registry for provider modalities", "error", err)
		return nil
	}

	seen := make(map[models.Modality]bool)
	for _, def := range registry.ListModelsByProvider(provider) {
		for _, m := range def.Capabilities.EffectiveOutputModalities() {
			seen[m] = true
		}
	}

	result := make([]string, 0, len(seen))
	if seen[models.ModalityText] {
		result = append(result, string(models.ModalityText))
	}
	extras := make([]string, 0, len(seen))
	for m := range seen {
		if m != models.ModalityText {
			extras = append(extras, string(m))
		}
	}
	sort.Strings(extras)
	return append(result, extras...)
}

// providerDisplayNames name the providers GetProviderStatuses reports, in display order.
var statusProviders = []struct{ id, name string }{
	{"claude", "Claude subscription"},
	{"codex", "Codex (ChatGPT)"},
	{"copilot", "GitHub Copilot"},
	{antigravity.DriverID, "Antigravity"},
	{"openrouter", "OpenRouter"},
	{"anthropic", "Anthropic API"},
	{"openai", "OpenAI"},
	{"gemini", "Google Gemini"},
	{"reliant", "Reliant"},
}

// GetProviderStatuses reports which providers can serve, derived from the user's accounts: a
// provider is configured when at least one of its accounts is not waiting for sign-in. Masked
// keys and per-account detail live on the accounts (ProviderAccountService).
func (s *Service) GetProviderStatuses(ctx context.Context, req *connect.Request[reliantv1.GetProviderStatusesRequest]) (*connect.Response[reliantv1.GetProviderStatusesResponse], error) {
	userID := auth.MustGetUserID(ctx)

	accounts, err := s.deps.Repo.ListProviderAccounts(ctx, userID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get provider statuses"))
	}
	total := map[string]int32{}
	usable := map[string]bool{}
	for _, a := range accounts {
		total[a.Provider]++
		if a.NeedsReauthAt == nil {
			usable[a.Provider] = true
		}
	}

	statuses := make([]*reliantv1.ProviderStatus, 0, len(statusProviders))
	for _, p := range statusProviders {
		statuses = append(statuses, &reliantv1.ProviderStatus{
			Provider:         p.id,
			DisplayName:      p.name,
			Configured:       usable[p.id],
			AccountCount:     total[p.id],
			OutputModalities: providerOutputModalities(p.id),
		})
	}
	return connect.NewResponse(&reliantv1.GetProviderStatusesResponse{Providers: statuses}), nil
}

// ValidateProviderAPIKey validates a pasted API key for a provider. Subscription providers have
// no key to validate: they answer whether the user has a working account.
func (s *Service) ValidateProviderAPIKey(ctx context.Context, req *connect.Request[reliantv1.ValidateProviderAPIKeyRequest]) (*connect.Response[reliantv1.ValidateProviderAPIKeyResponse], error) {
	provider := models.Family(req.Msg.Provider)
	userID := auth.MustGetUserID(ctx)

	switch provider {
	case "claude", "codex", "copilot", antigravity.DriverID:
		name := providerName(string(provider))
		accounts, err := s.deps.Repo.ListProviderAccounts(ctx, userID)
		if err != nil {
			return connect.NewResponse(&reliantv1.ValidateProviderAPIKeyResponse{Valid: false, Message: "Failed to load " + name + " credentials"}), nil
		}
		have, usable := false, false
		for _, a := range accounts {
			if a.Provider == string(provider) {
				have = true
				usable = usable || a.NeedsReauthAt == nil
			}
		}
		switch {
		case !have:
			return connect.NewResponse(&reliantv1.ValidateProviderAPIKeyResponse{Valid: false, Message: name + " is not connected"}), nil
		case !usable:
			return connect.NewResponse(&reliantv1.ValidateProviderAPIKeyResponse{Valid: false, Message: name + " session expired. Please reconnect " + name + "."}), nil
		}
		return connect.NewResponse(&reliantv1.ValidateProviderAPIKeyResponse{Valid: true, Message: "Connected to " + name}), nil
	}

	valid, message := s.deps.ValidateKey(ctx, provider, req.Msg.ApiKey)
	return connect.NewResponse(&reliantv1.ValidateProviderAPIKeyResponse{Valid: valid, Message: message}), nil
}

func requireOAuthParams(code, codeVerifier, redirectURI string) error {
	switch {
	case code == "":
		return svcerr.InvalidArgument("authorization code is required")
	case codeVerifier == "":
		return svcerr.InvalidArgument("code_verifier is required")
	case redirectURI == "":
		return svcerr.InvalidArgument("redirect_uri is required")
	}
	return nil
}

// connectedMessage is the success line for a sign-in, naming what happened to the account.
func connectedMessage(provider string, res signInResult) string {
	name := providerName(provider)
	if res.created {
		return "Connected to " + name
	}
	return name + " account already connected; signed in again"
}

// CompleteCodexOAuth exchanges an OAuth authorization code + PKCE verifier and stores the result
// as a Codex account: labelled by the ChatGPT email, planned from the id_token, deduped by the
// ChatGPT account and user.
func (s *Service) CompleteCodexOAuth(ctx context.Context, req *connect.Request[reliantv1.CompleteCodexOAuthRequest]) (*connect.Response[reliantv1.CompleteCodexOAuthResponse], error) {
	userID := auth.MustGetUserID(ctx)
	code := strings.TrimSpace(req.Msg.Code)
	codeVerifier := strings.TrimSpace(req.Msg.CodeVerifier)
	redirectURI := strings.TrimSpace(req.Msg.RedirectUri)
	if err := requireOAuthParams(code, codeVerifier, redirectURI); err != nil {
		return nil, err
	}

	tokens, err := s.deps.ExchangeCodexCode(code, codeVerifier, redirectURI)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.InvalidArgument("failed to complete Codex OAuth"))
	}

	res, err := s.connectAccount(ctx, signIn{
		userID: userID, provider: "codex", targetID: req.Msg.GetAccountId(),
		upsert: codexIdentity(tokens.AccessToken, tokens.RefreshToken, tokens.IDToken, tokens.AccountID),
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&reliantv1.CompleteCodexOAuthResponse{
		Success: true, Message: connectedMessage("codex", res),
		Account: res.account, Created: res.created, IdentityMismatch: res.mismatch,
	}), nil
}

// CompleteAntigravityOAuth exchanges a Google OAuth authorization code + PKCE verifier for
// Antigravity tokens and stores them as an account keyed by the Google subject.
//
// Unlike CompleteClaudeOAuth there is no state parameter: Google's token
// endpoint does not require it echoed back. The frontend still generates and
// verifies state — it just never reaches the backend.
func (s *Service) CompleteAntigravityOAuth(ctx context.Context, req *connect.Request[reliantv1.CompleteAntigravityOAuthRequest]) (*connect.Response[reliantv1.CompleteAntigravityOAuthResponse], error) {
	userID := auth.MustGetUserID(ctx)
	code := strings.TrimSpace(req.Msg.Code)
	codeVerifier := strings.TrimSpace(req.Msg.CodeVerifier)
	redirectURI := strings.TrimSpace(req.Msg.RedirectUri)
	if err := requireOAuthParams(code, codeVerifier, redirectURI); err != nil {
		return nil, err
	}

	tokens, err := s.deps.ExchangeAntigravityCode(code, codeVerifier, redirectURI)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.InvalidArgument("failed to complete Antigravity OAuth"))
	}

	u := core.AccountUpsert{
		AuthKind: core.AuthKindOAuth, Scope: tokens.Scope, ExpiresAt: tokens.ExpiresAt,
		Secrets: core.AccountSecrets{Secret: tokens.AccessToken, Refresh: tokens.RefreshToken, IDToken: tokens.IDToken},
	}
	if id, ok := core.GoogleIdentityFromIDToken(tokens.IDToken); ok {
		u.IdentityKey, u.Email, u.ExternalAccountID = id.IdentityKey, id.Email, id.ExternalAccountID
	}
	res, err := s.connectAccount(ctx, signIn{userID: userID, provider: antigravity.DriverID, targetID: req.Msg.GetAccountId(), upsert: u})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&reliantv1.CompleteAntigravityOAuthResponse{
		Success: true, Message: connectedMessage(antigravity.DriverID, res),
		Account: res.account, Created: res.created, IdentityMismatch: res.mismatch,
	}), nil
}

// StartCopilotDeviceAuth begins the GitHub Copilot device-authorization flow and
// returns the user code + verification URI for the client to present.
func (s *Service) StartCopilotDeviceAuth(ctx context.Context, req *connect.Request[reliantv1.StartCopilotDeviceAuthRequest]) (*connect.Response[reliantv1.StartCopilotDeviceAuthResponse], error) {
	// userID is not required to start the flow, but require an authenticated
	// caller for parity with the other provider handlers.
	_ = auth.MustGetUserID(ctx)

	deviceAuth, err := copilot.StartDeviceAuth(ctx)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to start GitHub Copilot login"))
	}

	return connect.NewResponse(&reliantv1.StartCopilotDeviceAuthResponse{
		DeviceCode:       deviceAuth.DeviceCode,
		UserCode:         deviceAuth.UserCode,
		VerificationUri:  deviceAuth.VerificationURI,
		IntervalSeconds:  int32(deviceAuth.Interval),
		ExpiresInSeconds: int32(deviceAuth.ExpiresIn),
	}), nil
}

// PollCopilotDeviceAuth performs a single poll of the GitHub device flow. On success it stores
// the GitHub token as a Copilot account keyed by the GitHub user id.
func (s *Service) PollCopilotDeviceAuth(ctx context.Context, req *connect.Request[reliantv1.PollCopilotDeviceAuthRequest]) (*connect.Response[reliantv1.PollCopilotDeviceAuthResponse], error) {
	userID := auth.MustGetUserID(ctx)

	deviceCode := strings.TrimSpace(req.Msg.DeviceCode)
	if deviceCode == "" {
		return nil, svcerr.InvalidArgument("device_code is required")
	}

	tokens, err := s.deps.PollCopilotDevice(ctx, deviceCode)
	if err != nil {
		// The flow has not authorized: no account was stored, so no
		// "connect" event, whatever the poll answers.
		activity.Skip(ctx)
		switch {
		case errors.Is(err, copilot.ErrAuthorizationPending), errors.Is(err, copilot.ErrSlowDown):
			return connect.NewResponse(&reliantv1.PollCopilotDeviceAuthResponse{
				Status: reliantv1.PollCopilotDeviceAuthResponse_STATUS_PENDING,
			}), nil
		case errors.Is(err, copilot.ErrDeviceCodeExpired):
			return connect.NewResponse(&reliantv1.PollCopilotDeviceAuthResponse{
				Status: reliantv1.PollCopilotDeviceAuthResponse_STATUS_EXPIRED,
			}), nil
		case errors.Is(err, copilot.ErrAccessDenied):
			return connect.NewResponse(&reliantv1.PollCopilotDeviceAuthResponse{
				Status: reliantv1.PollCopilotDeviceAuthResponse_STATUS_DENIED,
			}), nil
		default:
			logging.ErrorContext(ctx, "Copilot device authorization poll failed", "error", err)
			return connect.NewResponse(&reliantv1.PollCopilotDeviceAuthResponse{
				Status:       reliantv1.PollCopilotDeviceAuthResponse_STATUS_ERROR,
				ErrorMessage: err.Error(),
			}), nil
		}
	}

	// Individual is the only supported GitHub Copilot tier. Ignore whatever tier the client sent
	// (including a legacy "enterprise") so the driver routes to api.individual.githubcopilot.com.
	u := core.AccountUpsert{
		AuthKind: core.AuthKindDevice, PlanRaw: "individual", Scope: tokens.Scope,
		Secrets: core.AccountSecrets{Secret: tokens.AccessToken},
	}
	var label string
	if s.deps.GitHubIdentity != nil {
		gctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		gh, ghErr := s.deps.GitHubIdentity(gctx, tokens.AccessToken)
		cancel()
		if ghErr != nil {
			logging.WarnContext(ctx, "GitHub identity read failed; the Copilot account stays unlabelled", "error", ghErr)
		} else if gh.ID != 0 {
			u.IdentityKey = core.GitHubIdentityKey(strconv.FormatInt(gh.ID, 10))
			u.ExternalAccountID, u.Email, label = strconv.FormatInt(gh.ID, 10), gh.Email, gh.Login
		}
	}
	res, err := s.connectAccount(ctx, signIn{userID: userID, provider: "copilot", targetID: req.Msg.GetAccountId(), upsert: u, label: label})
	if err != nil {
		return nil, err
	}

	// Drop any cached per-account model availability so the picker re-queries GET
	// /models for the freshly-connected account on its next load.
	copilot.EvictAvailabilityCache(tokens.AccessToken)

	return connect.NewResponse(&reliantv1.PollCopilotDeviceAuthResponse{
		Status:  reliantv1.PollCopilotDeviceAuthResponse_STATUS_AUTHORIZED,
		Account: res.account, Created: res.created, IdentityMismatch: res.mismatch,
	}), nil
}

// CompleteClaudeOAuth exchanges an OAuth authorization code + PKCE verifier and stores the result
// as a Claude subscription account: labelled by the account email, planned from the profile
// endpoint, deduped by account and organization.
func (s *Service) CompleteClaudeOAuth(ctx context.Context, req *connect.Request[reliantv1.CompleteClaudeOAuthRequest]) (*connect.Response[reliantv1.CompleteClaudeOAuthResponse], error) {
	userID := auth.MustGetUserID(ctx)
	code := strings.TrimSpace(req.Msg.Code)
	codeVerifier := strings.TrimSpace(req.Msg.CodeVerifier)
	redirectURI := strings.TrimSpace(req.Msg.RedirectUri)
	state := strings.TrimSpace(req.Msg.State)
	if err := requireOAuthParams(code, codeVerifier, redirectURI); err != nil {
		return nil, err
	}

	tokens, err := s.deps.ExchangeClaudeCode(code, codeVerifier, redirectURI, state)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.InvalidArgument("failed to complete Claude OAuth"))
	}

	res, err := s.connectAccount(ctx, signIn{
		userID: userID, provider: "claude", targetID: req.Msg.GetAccountId(),
		upsert: s.claudeIdentity(ctx, tokens.AccessToken, tokens.AccountUUID, tokens.OrganizationUUID,
			tokens.AccountEmail, tokens.OrganizationName, tokens.Scope, tokens.ExpiresAt, tokens.RefreshToken),
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&reliantv1.CompleteClaudeOAuthResponse{
		Success: true, Message: connectedMessage("claude", res),
		Account: res.account, Created: res.created, IdentityMismatch: res.mismatch,
	}), nil
}

// SyncReliantProvider mints the user's LLM gateway key via control-plane and
// persists it locally as the "reliant" provider credential.
//
// The key is an `rlat_` access token (llm:invoke, acting as the user) for the
// device controlplane.ReliantProviderKeyName. Control-plane returns a NEW
// plaintext on every call and revokes this device's previous key in the same
// transaction — it never re-serves a stored plaintext — so created/rotated
// come from whether a previous key was replaced, not from comparing strings.
func (s *Service) SyncReliantProvider(ctx context.Context, req *connect.Request[reliantv1.SyncReliantProviderRequest]) (*connect.Response[reliantv1.SyncReliantProviderResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if s.deps.ControlPlane == nil {
		return nil, svcerr.Internal("control-plane client not configured")
	}

	jwt, ok := auth.GetUserJWT(userID)
	if !ok || strings.TrimSpace(jwt) == "" {
		return nil, svcerr.Unauthenticated("missing user JWT for control-plane call")
	}

	key, err := s.deps.ControlPlane.MintLLMKey(ctx, jwt, controlplane.ReliantProviderKeyName)
	if err != nil {
		// A remote Connect client's error describes control-plane's failure,
		// not this rpc's: it is the logged cause, never the verdict.
		return nil, svcerr.WithCause(svcerr.Internal("failed to mint Reliant LLM key"), err)
	}
	plaintext := strings.TrimSpace(key.Plaintext)
	if plaintext == "" {
		return nil, svcerr.Internal("control-plane returned an empty Reliant LLM key")
	}

	_, _, err = s.deps.Repo.UpsertProviderAccount(ctx, core.AccountUpsert{
		UserID: userID, Provider: "reliant", AuthKind: core.AuthKindManaged, IdentityKey: core.ManagedIdentityKey,
		KeyHint: core.KeyHint(plaintext), Secrets: core.AccountSecrets{Secret: plaintext},
	})
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to persist Reliant API key"))
	}

	if err := s.deps.Repo.EmitUserRefetch(ctx, userID, db.RefetchConfigHealth, db.RefetchOpts{}); err != nil {
		logging.WarnContext(ctx, "Failed to emit config_health refetch after SyncReliantProvider", "error", err)
	}
	s.wakeChats(ctx, userID, "the Reliant provider key was synced")

	created := !key.Rotated
	rotated := key.Rotated

	return connect.NewResponse(&reliantv1.SyncReliantProviderResponse{
		Success:    true,
		Message:    "Reliant provider synced",
		Synced:     true,
		CreatedKey: created,
		RotatedKey: rotated,
		Provider: &reliantv1.ProviderStatus{
			Provider:     "reliant",
			DisplayName:  "Reliant",
			Configured:   true,
			AccountCount: 1,
		},
	}), nil
}
