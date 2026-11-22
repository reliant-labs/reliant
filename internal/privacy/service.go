// yours: scaffolded once, never touched again — forge will not overwrite this file
// Service implementation; canonical pattern in `forge skill load service-layer`.
package privacy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/pkg/config"
)

// The user-level settings keys the privacy choices live under.
const (
	// AnalyticsEnabledKey controls whether product analytics events are
	// recorded for the user.
	AnalyticsEnabledKey = "privacy.analytics_enabled"
	// CrashReportingEnabledKey controls whether crash reporting is enabled.
	CrashReportingEnabledKey = "privacy.crash_reporting_enabled"
)

// Both choices are on until the user turns them off (opt-out).
const (
	defaultAnalyticsEnabled      = true
	defaultCrashReportingEnabled = true
)

// Deps holds dependencies for the privacy package.
type Deps struct {
	Logger *slog.Logger
	Config *config.Config

	// Settings holds the user's setting rows.
	Settings Settings
}

//forge:contract
type service struct {
	deps Deps
}

// New creates a new privacy service instance.
//
// forge:constructor
func New(deps Deps) (Service, error) {
	if deps.Settings == nil {
		return nil, errors.New("privacy: Deps.Settings is required")
	}
	return &service{deps: deps}, nil
}

// GetAnalyticsEnabled reports whether product analytics may be recorded for
// the user. A user who never chose is opted in; an empty user id is an error,
// never an answer.
func (s *service) GetAnalyticsEnabled(ctx context.Context, userID string) (bool, error) {
	return s.getBool(ctx, userID, AnalyticsEnabledKey, defaultAnalyticsEnabled)
}

// GetPrivacySettings returns the user's analytics and crash-reporting choices.
func (s *service) GetPrivacySettings(ctx context.Context, userID string) (analyticsEnabled, crashReportingEnabled bool, err error) {
	if analyticsEnabled, err = s.getBool(ctx, userID, AnalyticsEnabledKey, defaultAnalyticsEnabled); err != nil {
		return false, false, err
	}
	if crashReportingEnabled, err = s.getBool(ctx, userID, CrashReportingEnabledKey, defaultCrashReportingEnabled); err != nil {
		return false, false, err
	}
	return analyticsEnabled, crashReportingEnabled, nil
}

// SetPrivacySettings stores the user's analytics and crash-reporting choices.
func (s *service) SetPrivacySettings(ctx context.Context, userID string, analyticsEnabled, crashReportingEnabled bool) error {
	if err := s.setBool(ctx, userID, AnalyticsEnabledKey, analyticsEnabled); err != nil {
		return err
	}
	return s.setBool(ctx, userID, CrashReportingEnabledKey, crashReportingEnabled)
}

func (s *service) getBool(ctx context.Context, userID, key string, fallback bool) (bool, error) {
	if userID == "" {
		return false, errors.New("user_id cannot be empty")
	}
	setting, err := s.deps.Settings.GetSetting(ctx, userID, nil, key)
	if err != nil {
		if svcerr.IsNotFound(err) {
			return fallback, nil
		}
		return false, err
	}
	val, err := strconv.ParseBool(setting.Value)
	if err != nil {
		return false, fmt.Errorf("failed to parse bool value: %w", err)
	}
	return val, nil
}

func (s *service) setBool(ctx context.Context, userID, key string, value bool) error {
	if userID == "" {
		return errors.New("user_id cannot be empty")
	}
	now := time.Now().UTC()
	return s.deps.Settings.UpsertSetting(ctx, &core.Setting{
		ID:        uuid.NewString(),
		UserID:    userID,
		Key:       key,
		Value:     strconv.FormatBool(value),
		ValueType: "bool",
		CreatedAt: now,
		UpdatedAt: now,
	})
}
