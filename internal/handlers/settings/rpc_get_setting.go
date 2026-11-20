package settings

import (
	"context"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/reliant/internal/auth"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// GetSetting retrieves a setting by key
func (s *Service) GetSetting(ctx context.Context, req *reliantv1.GetSettingRequest) (*reliantv1.GetSettingResponse, error) {
	userID := auth.MustGetUserID(ctx)

	if req.Key == "" {
		return nil, svcerr.InvalidArgument("key is required")
	}

	var projectID *string
	if req.ProjectId != nil {
		projectID = req.ProjectId
	}

	setting, err := s.deps.Repo.GetSetting(ctx, userID, projectID, req.Key)
	if err != nil {
		// Return empty response instead of error for missing settings.
		// This prevents frontend callers from throwing when a setting
		// simply hasn't been created yet.
		return &reliantv1.GetSettingResponse{
			Setting: nil,
		}, nil
	}

	return &reliantv1.GetSettingResponse{
		Setting: settingToProto(setting),
	}, nil
}
