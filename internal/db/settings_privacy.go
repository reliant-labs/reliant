// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
)

// GetAnalyticsEnabled returns whether analytics is enabled for the user
func (r *Repo) GetAnalyticsEnabled(ctx context.Context, userID string) (bool, error) {
	return r.getBoolOrDefault(ctx, userID, nil, PrivacyAnalyticsEnabled, DefaultAnalyticsEnabled)
}

// setAnalyticsEnabled sets whether analytics is enabled for the user
func (r *Repo) setAnalyticsEnabled(ctx context.Context, userID string, enabled bool) error {
	return r.setBool(ctx, userID, nil, PrivacyAnalyticsEnabled, enabled)
}

// getCrashReportingEnabled returns whether crash reporting is enabled for the user
func (r *Repo) getCrashReportingEnabled(ctx context.Context, userID string) (bool, error) {
	return r.getBoolOrDefault(ctx, userID, nil, PrivacyCrashReportingEnabled, DefaultCrashReportingEnabled)
}

// setCrashReportingEnabled sets whether crash reporting is enabled for the user
func (r *Repo) setCrashReportingEnabled(ctx context.Context, userID string, enabled bool) error {
	return r.setBool(ctx, userID, nil, PrivacyCrashReportingEnabled, enabled)
}

// GetPrivacySettings returns both analytics and crash reporting settings
func (r *Repo) GetPrivacySettings(ctx context.Context, userID string) (analyticsEnabled, crashReportingEnabled bool, err error) {
	analyticsEnabled, err = r.GetAnalyticsEnabled(ctx, userID)
	if err != nil {
		return false, false, err
	}

	crashReportingEnabled, err = r.getCrashReportingEnabled(ctx, userID)
	if err != nil {
		return false, false, err
	}

	return analyticsEnabled, crashReportingEnabled, nil
}

// SetPrivacySettings sets both analytics and crash reporting settings
func (r *Repo) SetPrivacySettings(ctx context.Context, userID string, analyticsEnabled, crashReportingEnabled bool) error {
	if err := r.setAnalyticsEnabled(ctx, userID, analyticsEnabled); err != nil {
		return err
	}

	return r.setCrashReportingEnabled(ctx, userID, crashReportingEnabled)
}
