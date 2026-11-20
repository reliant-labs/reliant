package settings

import (
	"context"
	"time"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/reliant/internal/auth"

	"github.com/reliant-labs/reliant/internal/db"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"

	"github.com/reliant-labs/reliant/internal/logging"
)

// UpdateSetting updates an existing setting
func (s *Service) UpdateSetting(ctx context.Context, req *reliantv1.UpdateSettingRequest) (*reliantv1.UpdateSettingResponse, error) {
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

	if req.Value != nil {
		if err := validateSettingValue(req.Key, *req.Value); err != nil {
			return nil, err
		}
		setting.Value = *req.Value
	}
	if req.ValueType != nil {
		setting.ValueType = *req.ValueType
	}
	setting.UpdatedAt = time.Now().UTC()

	if err := s.deps.Repo.UpdateSetting(ctx, setting); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to update setting"))
	}

	if err := s.deps.Repo.EmitUserRefetch(ctx, userID, db.RefetchConfigHealth, db.RefetchOpts{ProjectID: projectID}); err != nil {
		logging.WarnContext(ctx, "Failed to emit config_health refetch after UpdateSetting", "error", err)
	}

	return &reliantv1.UpdateSettingResponse{
		Setting: settingToProto(setting),
	}, nil
}
