// Copyright (c) 2025 Reliant Labs
package settings

import (
	"context"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/automationpolicy"

	"github.com/reliant-labs/reliant/internal/db"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"

	"github.com/reliant-labs/reliant/internal/logging"
)

// ============================================================================
// Helper Methods
// ============================================================================

// settingToProto converts a db.Setting to proto Setting
func settingToProto(s *db.Setting) *reliantv1.Setting {
	setting := &reliantv1.Setting{
		Id:        s.ID,
		Key:       s.Key,
		Value:     s.Value,
		ValueType: s.ValueType,
		CreatedAt: s.CreatedAt.Format(time.RFC3339),
		UpdatedAt: s.UpdatedAt.Format(time.RFC3339),
	}
	if s.ProjectID != nil {
		setting.ProjectId = s.ProjectID
	}
	return setting
}

// validateSettingValue refuses a value the server would misread for a key it
// interprets. Settings are otherwise free-form.
func validateSettingValue(key, value string) error {
	if key == automationpolicy.ApprovalSettingKey {
		switch automationpolicy.ApprovalMode(value) {
		case automationpolicy.ApprovalAlways, automationpolicy.ApprovalOnCapabilityChange, automationpolicy.ApprovalNever:
		default:
			return svcerr.InvalidArgument(fmt.Sprintf("%s must be always, on_capability_change or never", key))
		}
	}
	return nil
}

// upsertSetting creates or updates a setting
func (s *Service) upsertSetting(ctx context.Context, userID string, projectID *string, key, value string) error {
	setting, err := s.deps.Repo.GetSetting(ctx, userID, projectID, key)
	if err != nil {
		// The store says "unset" as its NotFound; anything else is a failure.
		if !svcerr.IsNotFound(err) {
			return err
		}

		// Setting doesn't exist, create it
		newSetting := &db.Setting{
			ID:        uuid.New().String(),
			UserID:    userID,
			ProjectID: projectID,
			Key:       key,
			Value:     value,
			ValueType: "string",
			CreatedAt: time.Now().UTC(),
			UpdatedAt: time.Now().UTC(),
		}
		return s.deps.Repo.CreateSetting(ctx, newSetting)
	}

	// Setting exists, update it
	setting.Value = value
	setting.UpdatedAt = time.Now().UTC()
	return s.deps.Repo.UpdateSetting(ctx, setting)
}

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

	if err := s.deps.Repo.EmitUserRefetch(ctx, userID, db.RefetchConfigHealth, db.RefetchOpts{ProjectID: projectID}); err != nil {
		logging.WarnContext(ctx, "Failed to emit config_health refetch after CreateSetting", "error", err)
	}

	return connect.NewResponse(&reliantv1.CreateSettingResponse{
		Setting: settingToProto(setting),
	}), nil
}

// ListSettings lists all settings for the current user
func (s *Service) ListSettings(ctx context.Context, req *connect.Request[reliantv1.ListSettingsRequest]) (*connect.Response[reliantv1.ListSettingsResponse], error) {
	userID := auth.MustGetUserID(ctx)

	var projectID *string
	if req.Msg.ProjectId != nil {
		projectID = req.Msg.ProjectId
	}

	settings, err := s.deps.Repo.ListSettings(ctx, userID, projectID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list settings"))
	}

	protoSettings := make([]*reliantv1.Setting, len(settings))
	for i, setting := range settings {
		protoSettings[i] = settingToProto(setting)
	}

	return connect.NewResponse(&reliantv1.ListSettingsResponse{
		Settings: protoSettings,
		Total:    int32(len(protoSettings)),
	}), nil
}

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

// UpdateSetting updates an existing setting
func (s *Service) UpdateSetting(ctx context.Context, req *connect.Request[reliantv1.UpdateSettingRequest]) (*connect.Response[reliantv1.UpdateSettingResponse], error) {
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
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get setting"))
	}

	if req.Msg.Value != nil {
		if err := validateSettingValue(req.Msg.Key, *req.Msg.Value); err != nil {
			return nil, err
		}
		setting.Value = *req.Msg.Value
	}
	if req.Msg.ValueType != nil {
		setting.ValueType = *req.Msg.ValueType
	}
	setting.UpdatedAt = time.Now().UTC()

	if err := s.deps.Repo.UpdateSetting(ctx, setting); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to update setting"))
	}

	if err := s.deps.Repo.EmitUserRefetch(ctx, userID, db.RefetchConfigHealth, db.RefetchOpts{ProjectID: projectID}); err != nil {
		logging.WarnContext(ctx, "Failed to emit config_health refetch after UpdateSetting", "error", err)
	}

	return connect.NewResponse(&reliantv1.UpdateSettingResponse{
		Setting: settingToProto(setting),
	}), nil
}

// BatchUpsertSettings writes many settings in one round trip.
//
// Each entry goes through CreateSetting, which is an upsert server-side (see
// the ON CONFLICT note in queries/settings.sql), so a batch does not have to
// know which keys already exist. One refetch is emitted for the whole batch
// rather than one per key — the fan-out this RPC exists to remove would
// otherwise just reappear on the event side.
func (s *Service) BatchUpsertSettings(ctx context.Context, req *connect.Request[reliantv1.BatchUpsertSettingsRequest]) (*connect.Response[reliantv1.BatchUpsertSettingsResponse], error) {
	userID := auth.MustGetUserID(ctx)

	var projectID *string
	if req.Msg.ProjectId != nil {
		projectID = req.Msg.ProjectId
	}

	for _, write := range req.Msg.Settings {
		if write.GetKey() == "" {
			return nil, svcerr.InvalidArgument("key is required for every setting in a batch")
		}
		if err := validateSettingValue(write.GetKey(), write.GetValue()); err != nil {
			return nil, err
		}
	}

	now := time.Now().UTC()
	written := make([]*reliantv1.Setting, 0, len(req.Msg.Settings))

	for _, write := range req.Msg.Settings {
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

	return connect.NewResponse(&reliantv1.BatchUpsertSettingsResponse{
		Settings: written,
	}), nil
}

// DeleteSetting deletes a setting by key
func (s *Service) DeleteSetting(ctx context.Context, req *connect.Request[reliantv1.DeleteSettingRequest]) (*connect.Response[reliantv1.DeleteSettingResponse], error) {
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
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get setting"))
	}

	if err := s.deps.Repo.DeleteSetting(ctx, setting.ID); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to delete setting"))
	}

	if err := s.deps.Repo.EmitUserRefetch(ctx, userID, db.RefetchConfigHealth, db.RefetchOpts{ProjectID: projectID}); err != nil {
		logging.WarnContext(ctx, "Failed to emit config_health refetch after DeleteSetting", "error", err)
	}

	return connect.NewResponse(&reliantv1.DeleteSettingResponse{
		Success: true,
		Message: "Setting deleted successfully",
	}), nil
}
