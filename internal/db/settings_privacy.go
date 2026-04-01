// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
)

// GetAnalyticsEnabled returns whether analytics is enabled for the user
func (r *Repo) GetAnalyticsEnabled(ctx context.Context, userID string) (bool, error) {
	return r.GetBoolOrDefault(ctx, userID, nil, PrivacyAnalyticsEnabled, DefaultAnalyticsEnabled)
}

// SetAnalyticsEnabled sets whether analytics is enabled for the user
func (r *Repo) SetAnalyticsEnabled(ctx context.Context, userID string, enabled bool) error {
	return r.SetBool(ctx, userID, nil, PrivacyAnalyticsEnabled, enabled)
}

// GetCrashReportingEnabled returns whether crash reporting is enabled for the user
func (r *Repo) GetCrashReportingEnabled(ctx context.Context, userID string) (bool, error) {
	return r.GetBoolOrDefault(ctx, userID, nil, PrivacyCrashReportingEnabled, DefaultCrashReportingEnabled)
}

// SetCrashReportingEnabled sets whether crash reporting is enabled for the user
func (r *Repo) SetCrashReportingEnabled(ctx context.Context, userID string, enabled bool) error {
	return r.SetBool(ctx, userID, nil, PrivacyCrashReportingEnabled, enabled)
}

// GetPrivacySettings returns both analytics and crash reporting settings
func (r *Repo) GetPrivacySettings(ctx context.Context, userID string) (analyticsEnabled, crashReportingEnabled bool, err error) {
	analyticsEnabled, err = r.GetAnalyticsEnabled(ctx, userID)
	if err != nil {
		return false, false, err
	}

	crashReportingEnabled, err = r.GetCrashReportingEnabled(ctx, userID)
	if err != nil {
		return false, false, err
	}

	return analyticsEnabled, crashReportingEnabled, nil
}

// SetPrivacySettings sets both analytics and crash reporting settings
func (r *Repo) SetPrivacySettings(ctx context.Context, userID string, analyticsEnabled, crashReportingEnabled bool) error {
	if err := r.SetAnalyticsEnabled(ctx, userID, analyticsEnabled); err != nil {
		return err
	}

	return r.SetCrashReportingEnabled(ctx, userID, crashReportingEnabled)
}
