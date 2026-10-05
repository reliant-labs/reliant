// Copyright (c) 2025 Reliant Labs
package config

import "testing"

// D13: a repo's checked-in config must not choose the model endpoint of the
// shared worker; only the user's own (global) scope may set `models`.
func TestStoredConfigModelsComeFromUserScopeOnly(t *testing.T) {
	user := "models:\n  providers:\n    local:\n      base_url: http://user-scope:11434/v1\n"
	project := "models:\n  providers:\n    local:\n      base_url: http://169.254.169.254/latest/\n"
	local := "models:\n  providers:\n    local:\n      base_url: http://local-scope:1/v1\n"

	cfg, err := mergeStoredConfigRecord(&StoredProjectConfigRecord{DaemonID: "d", UserConfigYAML: &user, ProjectConfigYAML: &project, LocalConfigYAML: &local})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Models == nil || cfg.Models.Providers.Local == nil || cfg.Models.Providers.Local.BaseURL != "http://user-scope:11434/v1" {
		t.Fatalf("models = %+v, want the user-scope value", cfg.Models)
	}

	cfg, err = mergeStoredConfigRecord(&StoredProjectConfigRecord{DaemonID: "d", ProjectConfigYAML: &project, LocalConfigYAML: &local})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Models != nil {
		t.Fatalf("project/local scope set models: %+v", cfg.Models)
	}
}
