package settings

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/reliant/internal/auth"

	"github.com/reliant-labs/reliant/internal/db"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"

	"github.com/reliant-labs/reliant/internal/logging"
)

// BatchUpsertSettings writes many settings in one round trip.
//
// Each entry goes through CreateSetting, which is an upsert server-side (see
// the ON CONFLICT note in queries/settings.sql), so a batch does not have to
// know which keys already exist. One refetch is emitted for the whole batch
// rather than one per key — the fan-out this RPC exists to remove would
// otherwise just reappear on the event side.
func (s *Service) BatchUpsertSettings(ctx context.Context, req *reliantv1.BatchUpsertSettingsRequest) (*reliantv1.BatchUpsertSettingsResponse, error) {
	userID := auth.MustGetUserID(ctx)

	var projectID *string
	if req.ProjectId != nil {
		projectID = req.ProjectId
	}

	for _, write := range req.Settings {
		if write.GetKey() == "" {
			return nil, svcerr.InvalidArgument("key is required for every setting in a batch")
		}
		if err := guardSettingWrite(ctx, write.GetKey()); err != nil {
			return nil, err
		}
		if err := validateSettingValue(write.GetKey(), write.GetValue()); err != nil {
			return nil, err
		}
	}

	now := time.Now().UTC()
	written := make([]*reliantv1.Setting, 0, len(req.Settings))

	for _, write := range req.Settings {
		valueType := "string"
		if write.ValueType != nil && *write.ValueType != "" {
			valueType = *write.ValueType
		}

		setting := &db.Setting{
			ID:        uuid.New().String(),
			UserID:    userID,
			ProjectID: projectID,
			Key:       write.GetKey(),
			Value:     write.GetValue(),
			ValueType: valueType,
			CreatedAt: now,
			UpdatedAt: now,
		}

		if err := s.deps.Repo.CreateSetting(ctx, setting); err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal(fmt.Sprintf("failed to upsert setting %q", write.GetKey())))
		}

		written = append(written, settingToProto(setting))
	}

	if len(written) > 0 {
		if err := s.deps.Repo.EmitUserRefetch(ctx, userID, db.RefetchConfigHealth, db.RefetchOpts{ProjectID: projectID}); err != nil {
			logging.WarnContext(ctx, "Failed to emit config_health refetch after BatchUpsertSettings", "error", err)
		}
	}

	return &reliantv1.BatchUpsertSettingsResponse{
		Settings: written,
	}, nil
}
