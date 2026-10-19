// Copyright (c) 2025 Reliant Labs
package commands

import (
	"strings"

	configv1 "github.com/reliant-labs/reliant/gen/config/v1"
	"github.com/reliant-labs/reliant/internal/analytics"
)

// analyticsSettings is a server role's AnalyticsConfig in the shape
// internal/analytics takes.
func analyticsSettings(cfg *configv1.AnalyticsConfig) analytics.Settings {
	return analytics.Settings{
		Disabled:        cfg.GetDisabled(),
		StatsigKey:      cfg.GetStatsigKey(),
		SupabaseURL:     cfg.GetSupabaseUrl(),
		SupabaseAnonKey: cfg.GetSupabaseAnonKey(),
		AppDataDir:      strings.TrimSpace(cfg.GetAppDataDir()),
	}
}
