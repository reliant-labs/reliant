package features

import (
	"context"
	"os"
	"strconv"
	"strings"

	"github.com/reliant-labs/reliant/internal/db"
)

const (
	SkillsEnabledKey     = "skills_enabled"
	SkillsEnabledSetting = "features.skills_enabled"
	SkillsEnabledEnvVar  = "RELIANT_FEATURE_SKILLS_ENABLED"
)

// IsSkillsEnabledForContext evaluates whether the skills feature is enabled for a user context.
//
// Precedence:
// 1) RELIANT_FEATURE_SKILLS_ENABLED env override
// 2) user setting features.skills_enabled
// 3) feature registry/static default
func IsSkillsEnabledForContext(ctx context.Context, repo db.Repository, userID string) bool {
	if envValue := strings.TrimSpace(os.Getenv(SkillsEnabledEnvVar)); envValue != "" {
		if parsed, err := strconv.ParseBool(envValue); err == nil {
			return parsed
		}
	}

	if userID != "" && repo != nil {
		enabled, err := repo.GetBoolOrDefault(ctx, userID, nil, SkillsEnabledSetting, false)
		if err == nil {
			return enabled
		}
	}

	return GetGlobalRegistry().EvaluateBool(ctx, SkillsEnabledKey, false)
}
