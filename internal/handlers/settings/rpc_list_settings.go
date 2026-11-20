package settings

import (
	"context"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/reliant/internal/auth"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// ListSettings lists all settings for the current user
func (s *Service) ListSettings(ctx context.Context, req *reliantv1.ListSettingsRequest) (*reliantv1.ListSettingsResponse, error) {
	userID := auth.MustGetUserID(ctx)

	var projectID *string
	if req.ProjectId != nil {
		projectID = req.ProjectId
	}

	settings, err := s.deps.Repo.ListSettings(ctx, userID, projectID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list settings"))
	}

	protoSettings := make([]*reliantv1.Setting, len(settings))
	for i, setting := range settings {
		protoSettings[i] = settingToProto(setting)
	}

	return &reliantv1.ListSettingsResponse{
		Settings: protoSettings,
		Total:    int32(len(protoSettings)),
	}, nil
}
