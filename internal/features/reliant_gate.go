package features

import (
	"context"
	"os"
	"strconv"
	"strings"

	"github.com/reliant-labs/reliant/internal/db"
)

const (
	ReliantManagedAccessEnabledKey     = "reliant_managed_access_enabled"
	ReliantManagedAccessEnabledSetting = "features.reliant_managed_access_enabled"
	ReliantManagedAccessEnabledEnvVar  = "RELIANT_FEATURE_RELIANT_MANAGED_ACCESS_ENABLED"
)

// IsReliantManagedAccessEnabledForContext evaluates whether Reliant managed access is enabled for a user context.
//
// Precedence:
// 1) RELIANT_FEATURE_RELIANT_MANAGED_ACCESS_ENABLED env override
// 2) user setting features.reliant_managed_access_enabled
// 3) feature registry/static default
func IsReliantManagedAccessEnabledForContext(ctx context.Context, repo db.Repository, userID string) bool {
	if envValue := strings.TrimSpace(os.Getenv(ReliantManagedAccessEnabledEnvVar)); envValue != "" {
		if parsed, err := strconv.ParseBool(envValue); err == nil {
			return parsed
		}
	}

	if userID != "" && repo != nil {
		enabled, err := repo.GetBoolOrDefault(ctx, userID, nil, ReliantManagedAccessEnabledSetting, false)
		if err == nil {
			return enabled
		}
	}

	return GetGlobalRegistry().EvaluateBool(ctx, ReliantManagedAccessEnabledKey, false)
}
