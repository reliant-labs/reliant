package settings

import (
	"context"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db/core"

	"github.com/reliant-labs/reliant/internal/db"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"

	"github.com/reliant-labs/reliant/internal/logging"
)

// ============================================================================
// Sub-phase 6a: Core Settings CRUD
// ============================================================================

// CreateSetting creates a new setting
func (s *Service) CreateSetting(ctx context.Context, req *connect.Request[reliantv1.CreateSettingRequest]) (*connect.Response[reliantv1.CreateSettingResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.Key == "" {
		return nil, svcerr.InvalidArgument("key is required")
	}
	if req.Msg.Value == "" {
		return nil, svcerr.InvalidArgument("value is required")
	}
	if err := guardSettingWrite(ctx, req.Msg.Key); err != nil {
		return nil, err
	}
	if err := validateSettingValue(req.Msg.Key, req.Msg.Value); err != nil {
		return nil, err
	}

	valueType := "string"
	if req.Msg.ValueType != nil {
		valueType = *req.Msg.ValueType
	}

	var projectID *string
	if req.Msg.ProjectId != nil {
		projectID = req.Msg.ProjectId
	}

	now := time.Now().UTC()
	setting := &db.Setting{
		ID:        uuid.New().String(),
		UserID:    userID,
		ProjectID: projectID,
		Key:       req.Msg.Key,
		Value:     req.Msg.Value,
		ValueType: valueType,
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := s.deps.Repo.CreateSetting(ctx, setting); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to create setting"))
	}

	if err := s.deps.Refetch.EmitUserRefetch(ctx, userID, core.RefetchConfigHealth, core.RefetchOpts{ProjectID: projectID}); err != nil {
		logging.WarnContext(ctx, "Failed to emit config_health refetch after CreateSetting", "error", err)
	}

	return connect.NewResponse(&reliantv1.CreateSettingResponse{
		Setting: settingToProto(setting),
	}), nil
}
