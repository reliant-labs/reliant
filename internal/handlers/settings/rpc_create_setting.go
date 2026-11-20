package settings

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/reliant/internal/auth"

	"github.com/reliant-labs/reliant/internal/db"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"

	"github.com/reliant-labs/reliant/internal/logging"
)

// ============================================================================
// Sub-phase 6a: Core Settings CRUD
// ============================================================================

// CreateSetting creates a new setting
func (s *Service) CreateSetting(ctx context.Context, req *reliantv1.CreateSettingRequest) (*reliantv1.CreateSettingResponse, error) {
	userID := auth.MustGetUserID(ctx)

	if req.Key == "" {
		return nil, svcerr.InvalidArgument("key is required")
	}
	if req.Value == "" {
		return nil, svcerr.InvalidArgument("value is required")
	}
	if err := guardSettingWrite(ctx, req.Key); err != nil {
		return nil, err
	}
	if err := validateSettingValue(req.Key, req.Value); err != nil {
		return nil, err
	}

	valueType := "string"
	if req.ValueType != nil {
		valueType = *req.ValueType
	}

	var projectID *string
	if req.ProjectId != nil {
		projectID = req.ProjectId
	}

	now := time.Now().UTC()
	setting := &db.Setting{
		ID:        uuid.New().String(),
		UserID:    userID,
		ProjectID: projectID,
		Key:       req.Key,
		Value:     req.Value,
		ValueType: valueType,
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := s.deps.Repo.CreateSetting(ctx, setting); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to create setting"))
	}

	if err := s.deps.Repo.EmitUserRefetch(ctx, userID, db.RefetchConfigHealth, db.RefetchOpts{ProjectID: projectID}); err != nil {
		logging.WarnContext(ctx, "Failed to emit config_health refetch after CreateSetting", "error", err)
	}

	return &reliantv1.CreateSettingResponse{
		Setting: settingToProto(setting),
	}, nil
}
