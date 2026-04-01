// Copyright (c) 2025 Reliant Labs
package analytics

import (
	"context"
	"sync"

	"github.com/reliant-labs/reliant/internal/logging"
)

// PrivacyChecker is an interface for checking user privacy settings
type PrivacyChecker interface {
	GetAnalyticsEnabled(ctx context.Context, userID string) (bool, error)
}

var (
	privacyChecker   PrivacyChecker
	privacyCheckerMu sync.RWMutex
)

// SetPrivacyChecker sets the privacy checker used to determine if analytics is enabled per user
func SetPrivacyChecker(checker PrivacyChecker) {
	privacyCheckerMu.Lock()
	defer privacyCheckerMu.Unlock()
	privacyChecker = checker
}

// GetClientForUser returns the appropriate analytics client based on user's privacy settings
// If user has disabled analytics, returns NoopClient
// If privacy checker is not set or user not found, returns the global client (respects default behavior)
func GetClientForUser(ctx context.Context, userID string) AnalyticsClient {
	// If no user ID provided, return global client
	if userID == "" {
		return GetClient()
	}

	// Check if we have a privacy checker configured
	privacyCheckerMu.RLock()
	checker := privacyChecker
	privacyCheckerMu.RUnlock()

	if checker == nil {
		// No privacy checker configured, fall back to global client
		return GetClient()
	}

	// Check user's privacy settings
	enabled, err := checker.GetAnalyticsEnabled(ctx, userID)
	if err != nil {
		// Error checking settings, log warning and fall back to global client
		logging.Warn("[Analytics] Failed to check privacy settings for user", "userID", userID, "error", err)
		return GetClient()
	}

	if !enabled {
		// User has disabled analytics, return no-op client
		return NewNoopClient()
	}

	// Analytics enabled, return global client
	return GetClient()
}
