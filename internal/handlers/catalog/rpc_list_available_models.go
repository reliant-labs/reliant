package catalog

import (
	"context"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/logging"
)

// ListAvailableModels returns the models available to the caller across every
// configured provider, with per-account availability (enabled). Static providers
// report every model enabled; a dynamic provider (Copilot) reports its account's
// per-model policy. Delegates to the drivers aggregator, which fails open per
// provider so one flaky provider never blanks the list.
func (s *Service) ListAvailableModels(
	ctx context.Context,
	req *connect.Request[reliantv1.ListAvailableModelsRequest],
) (*connect.Response[reliantv1.ListAvailableModelsResponse], error) {
	userID := auth.MustGetUserID(ctx)
	scope, err := s.pickerScope(ctx, userID, req.Msg.GetOrgId())
	if err != nil {
		return nil, err
	}

	infos, err := drivers.ListAvailableModels(ctx, scope)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list available models"))
	}

	out := make([]*reliantv1.AvailableModelInfo, 0, len(infos))
	for _, mi := range infos {
		out = append(out, &reliantv1.AvailableModelInfo{
			Id:                  mi.ID,
			DisplayName:         mi.DisplayName,
			Provider:            mi.DriverID,
			Family:              mi.Family,
			ApiModel:            mi.APIModel,
			ContextWindow:       int64(mi.Capabilities.MaxContextWindow),
			DefaultMaxTokens:    int64(mi.Capabilities.MaxOutputTokens),
			CanReason:           mi.Capabilities.CanReason,
			SupportsAttachments: mi.Capabilities.SupportsAttachments,
			SupportsTools:       mi.Capabilities.SupportsTools,
			SupportsCaching:     mi.Capabilities.SupportsCaching,
			Tags:                mi.Tags,
			Enabled:             mi.Enabled,
		})
	}

	logging.DebugContext(ctx, "[ListAvailableModels] Returning available models", "userID", userID, "count", len(out))
	return connect.NewResponse(&reliantv1.ListAvailableModelsResponse{Models: out}), nil
}
