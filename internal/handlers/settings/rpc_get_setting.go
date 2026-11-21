package settings

import (
	"context"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/reliant/internal/auth"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// GetSetting retrieves a setting by key
func (s *Service) GetSetting(ctx context.Context, req *connect.Request[reliantv1.GetSettingRequest]) (*connect.Response[reliantv1.GetSettingResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.Key == "" {
		return nil, svcerr.InvalidArgument("key is required")
	}

	var projectID *string
	if req.Msg.ProjectId != nil {
		projectID = req.Msg.ProjectId
	}

	setting, err := s.deps.Repo.GetSetting(ctx, userID, projectID, req.Msg.Key)
	if err != nil {
		// Return empty response instead of error for missing settings.
		// This prevents frontend callers from throwing when a setting
		// simply hasn't been created yet.
		return connect.NewResponse(&reliantv1.GetSettingResponse{
			Setting: nil,
		}), nil
	}

	return connect.NewResponse(&reliantv1.GetSettingResponse{
		Setting: settingToProto(setting),
	}), nil
}
