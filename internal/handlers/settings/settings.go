// Copyright (c) 2025 Reliant Labs
package settings

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/reliant/internal/automationpolicy"

	"github.com/reliant-labs/reliant/internal/db"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"

	"github.com/reliant-labs/reliant/internal/orgfunding"
	"github.com/reliant-labs/reliant/internal/orgs"
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

// guardSettingWrite refuses a write of a setting only a person may change. The
// person's org funding choice (orgfunding.SettingKey) says whether a chat in an
// org may spend their own AI accounts: it is the same consent as answering the
// chat's question (ProviderAccountService.ResolvePersonalFundingChoice), so an
// agent's or a script's credential may not set it, nor clear it, on any path.
func guardSettingWrite(ctx context.Context, key string) error {
	if key != orgfunding.SettingKey {
		return nil
	}
	return orgs.RequirePerson(ctx, "change who pays for your AI in an organization")
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
