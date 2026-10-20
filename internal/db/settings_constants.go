// Copyright (c) 2025 Reliant Labs
package db

// Privacy settings keys
const (
	// PrivacyAnalyticsEnabled controls whether product analytics events are recorded for the user
	PrivacyAnalyticsEnabled = "privacy.analytics_enabled"

	// PrivacyCrashReportingEnabled controls whether crash reporting (Sentry) is enabled
	PrivacyCrashReportingEnabled = "privacy.crash_reporting_enabled"
)

// Default privacy settings (opt-out model - enabled by default)
const (
	DefaultAnalyticsEnabled      = true
	DefaultCrashReportingEnabled = true
)
