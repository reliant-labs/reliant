package settings

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/activity"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/reliant/internal/auth"

	"github.com/reliant-labs/reliant/internal/db/core"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/llm/drivers/copilot"

	"github.com/reliant-labs/reliant/internal/logging"
)

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
