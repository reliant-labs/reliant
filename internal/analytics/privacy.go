// Copyright (c) 2025 Reliant Labs
package analytics

import (
	"context"

	"github.com/reliant-labs/reliant/internal/logging"
)

// PrivacyChecker reads a user's analytics consent (the privacy settings
// toggle).
type PrivacyChecker interface {
	GetAnalyticsEnabled(ctx context.Context, userID string) (bool, error)
}

// ForUser returns the Tracker for events about userID, which happen in ctx.
//
// An event that names a user is recorded only when that user's consent was
// read and is on. A user who opted out, a consent read that failed, and a
// client with nowhere to read consent from all get the zero Tracker, which
// records nothing. An event with no user (userID "") names nobody and needs
// no consent.
func (c *Client) ForUser(ctx context.Context, userID string) Tracker {
	if c == nil {
		return Tracker{}
	}
	if userID != "" {
		if c.consent == nil {
			return Tracker{}
		}
		enabled, err := c.consent.GetAnalyticsEnabled(ctx, userID)
		if err != nil {
			logging.WarnContext(ctx, "[Analytics] Could not read the user's analytics consent; recording nothing",
				"userID", userID, "error", err)
			return Tracker{}
		}
		if !enabled {
			return Tracker{}
		}
	}
	return Tracker{c: c, ctx: ctx, userID: userID}
}
