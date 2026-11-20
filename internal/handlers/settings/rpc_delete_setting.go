package settings

import (
	"context"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/reliant/internal/auth"

	"github.com/reliant-labs/reliant/internal/db"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"

	"github.com/reliant-labs/reliant/internal/logging"
)

// DeleteSetting deletes a setting by key
func (s *Service) DeleteSetting(ctx context.Context, req *reliantv1.DeleteSettingRequest) (*reliantv1.DeleteSettingResponse, error) {
	userID := auth.MustGetUserID(ctx)

	if req.Key == "" {
		return nil, svcerr.InvalidArgument("key is required")
	}
	if err := guardSettingWrite(ctx, req.Key); err != nil {
		return nil, err
	}

	var projectID *string
	if req.ProjectId != nil {
		projectID = req.ProjectId
	}

	setting, err := s.deps.Repo.GetSetting(ctx, userID, projectID, req.Key)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get setting"))
	}

	if err := s.deps.Repo.DeleteSetting(ctx, setting.ID); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to delete setting"))
	}

	if err := s.deps.Repo.EmitUserRefetch(ctx, userID, db.RefetchConfigHealth, db.RefetchOpts{ProjectID: projectID}); err != nil {
		logging.WarnContext(ctx, "Failed to emit config_health refetch after DeleteSetting", "error", err)
	}

	return &reliantv1.DeleteSettingResponse{
		Success: true,
		Message: "Setting deleted successfully",
	}, nil
}
