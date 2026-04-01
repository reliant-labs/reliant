// Copyright (c) 2025 Reliant Labs
package configloader

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"gopkg.in/yaml.v3"
)

func TestMergeModelsConfig_OnlyUsesUserConfig(t *testing.T) {
	// Models config only comes from the user (global) config.
	// Project and local configs are ignored for models because:
	// - Reliant runs as a single server with multiple project windows
	// - Local model servers are machine-specific, not project-specific

	user := &models.UserModelsConfig{
		Providers: models.UserProvidersConfig{
			Local: &models.LocalProviderConfig{
				BaseURL: "http://localhost:11434/v1",
			},
		},
	}

	merged := mergeModelsConfig(user, nil, nil)

	if merged == nil {
		t.Fatal("mergeModelsConfig returned nil when user config was set")
	}

	if merged.Providers.Local == nil {
		t.Fatal("merged.Providers.Local is nil, expected it to be set")
	}

	if merged.Providers.Local.BaseURL != "http://localhost:11434/v1" {
		t.Errorf("BaseURL = %q, want %q", merged.Providers.Local.BaseURL, "http://localhost:11434/v1")
	}
}

func TestMergeModelsConfig_IgnoresProjectAndLocal(t *testing.T) {
	// Models config ONLY comes from user (global) config.
	// Project and local configs are completely ignored.
	user := &models.UserModelsConfig{
		Providers: models.UserProvidersConfig{
			Local: &models.LocalProviderConfig{
				BaseURL: "http://user:1111/v1",
			},
		},
	}

	project := &models.UserModelsConfig{
		Providers: models.UserProvidersConfig{
			Local: &models.LocalProviderConfig{
				BaseURL: "http://project:2222/v1",
			},
		},
	}

	local := &models.UserModelsConfig{
		Providers: models.UserProvidersConfig{
			Local: &models.LocalProviderConfig{
				BaseURL: "http://local:3333/v1",
			},
		},
	}

	// User config should be returned regardless of project/local
	merged := mergeModelsConfig(user, project, local)
	if merged.Providers.Local.BaseURL != "http://user:1111/v1" {
		t.Errorf("User config should be used, got %q", merged.Providers.Local.BaseURL)
	}

	// User config still used even with project set
	merged = mergeModelsConfig(user, project, nil)
	if merged.Providers.Local.BaseURL != "http://user:1111/v1" {
		t.Errorf("User config should be used, got %q", merged.Providers.Local.BaseURL)
	}

	// Nil user means nil result (no global models config)
	merged = mergeModelsConfig(nil, project, local)
	if merged != nil {
		t.Error("Should return nil when user config is nil")
	}
}

func TestMergeModelsConfig_UserConfigPassthrough(t *testing.T) {
	// The user config should be returned as-is
	cfg := &models.UserModelsConfig{
		Providers: models.UserProvidersConfig{
			Local: &models.LocalProviderConfig{
				BaseURL: "http://localhost:11434/v1",
			},
		},
		Custom: []models.ModelDefinition{
			{ID: "test-model"},
		},
		TagPreferences: map[string][]string{
			"fast": {"model-a", "model-b"},
		},
	}

	merged := mergeModelsConfig(cfg, nil, nil)

	if merged != cfg {
		t.Fatal("mergeModelsConfig should return the user config directly")
	}
}

func TestLoadProjectConfig_ModelsProviders(t *testing.T) {
	// Create a temp directory structure
	// UserConfigDir is the .reliant directory itself (e.g., ~/.reliant/)
	userConfigDir := t.TempDir()
	projectDir := t.TempDir()

	// Write user config (global models config)
	configContent := `
models:
  providers:
    local:
      base_url: http://localhost:11434/v1
`
	configPath := filepath.Join(userConfigDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Load the config
	loader, err := NewLoader(LoaderOptions{UserConfigDir: userConfigDir})
	if err != nil {
		t.Fatalf("NewLoader failed: %v", err)
	}
	cfg, err := loader.LoadForProject(projectDir)
	if err != nil {
		t.Fatalf("LoadForProject failed: %v", err)
	}

	if cfg.Models == nil {
		t.Fatal("cfg.Models is nil, expected models config to be loaded")
	}

	if cfg.Models.Providers.Local == nil {
		t.Fatal("cfg.Models.Providers.Local is nil")
	}

	if cfg.Models.Providers.Local.BaseURL != "http://localhost:11434/v1" {
		t.Errorf("BaseURL = %q, want %q", cfg.Models.Providers.Local.BaseURL, "http://localhost:11434/v1")
	}
}

func TestLoadProjectConfig_ModelsWithCustomAndProviders(t *testing.T) {
	// UserConfigDir is the .reliant directory itself (e.g., ~/.reliant/)
	userConfigDir := t.TempDir()
	projectDir := t.TempDir()

	configContent := `
models:
  providers:
    local:
      base_url: http://localhost:11434/v1
  custom:
    - id: local-test
      name: Test Model
      tags: [local, fast]
      providers:
        - driver: local
          api_model: test:latest
`
	configPath := filepath.Join(userConfigDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatal(err)
	}

	loader, err := NewLoader(LoaderOptions{UserConfigDir: userConfigDir})
	if err != nil {
		t.Fatalf("NewLoader failed: %v", err)
	}
	cfg, err := loader.LoadForProject(projectDir)
	if err != nil {
		t.Fatalf("LoadForProject failed: %v", err)
	}

	if cfg.Models == nil {
		t.Fatal("cfg.Models is nil")
	}

	// Check providers
	if cfg.Models.Providers.Local == nil {
		t.Fatal("cfg.Models.Providers.Local is nil")
	}
	if cfg.Models.Providers.Local.BaseURL != "http://localhost:11434/v1" {
		t.Errorf("BaseURL = %q, want %q", cfg.Models.Providers.Local.BaseURL, "http://localhost:11434/v1")
	}

	// Check custom models
	if len(cfg.Models.Custom) != 1 {
		t.Fatalf("Custom models count = %d, want 1", len(cfg.Models.Custom))
	}
	if cfg.Models.Custom[0].ID != "local-test" {
		t.Errorf("Custom model ID = %q, want %q", cfg.Models.Custom[0].ID, "local-test")
	}
}

// TestDirectYAMLParsing verifies that the model types can be parsed directly from YAML
// This isolates whether the issue is with the types or with viper's unmarshaling
func TestDirectYAMLParsing(t *testing.T) {
	configContent := `
models:
  providers:
    local:
      base_url: http://localhost:11434/v1
`
	var cfg config.Config
	if err := yaml.Unmarshal([]byte(configContent), &cfg); err != nil {
		t.Fatalf("yaml.Unmarshal failed: %v", err)
	}

	if cfg.Models == nil {
		t.Fatal("cfg.Models is nil after YAML unmarshal")
	}

	if cfg.Models.Providers.Local == nil {
		t.Fatal("cfg.Models.Providers.Local is nil after YAML unmarshal")
	}

	if cfg.Models.Providers.Local.BaseURL != "http://localhost:11434/v1" {
		t.Errorf("BaseURL = %q, want %q", cfg.Models.Providers.Local.BaseURL, "http://localhost:11434/v1")
	}
}

// TestCustomModelLoading tests loading custom model definitions from config
func TestCustomModelLoading(t *testing.T) {
	// UserConfigDir is the .reliant directory itself (e.g., ~/.reliant/)
	userConfigDir := t.TempDir()
	projectDir := t.TempDir()

	configContent := `
models:
  providers:
    local:
      base_url: http://localhost:11434/v1
  custom:
    - id: local-qwen3
      name: Qwen3 (Ollama)
      tags: [local, fast, moderate]
      visibility: user
      capabilities:
        can_reason: true
        supports_tools: true
        supports_attachments: false
        supports_streaming: true
        supports_caching: false
        max_context_window: 200000
        max_output_tokens: 32768
      cost:
        input_per_1m: 0
        output_per_1m: 0
      providers:
        - driver: local
          api_model: qwen3:latest
`
	configPath := filepath.Join(userConfigDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatal(err)
	}

	loader, err := NewLoader(LoaderOptions{UserConfigDir: userConfigDir})
	if err != nil {
		t.Fatalf("NewLoader failed: %v", err)
	}

	cfg, err := loader.LoadForProject(projectDir)
	if err != nil {
		t.Fatalf("LoadForProject failed: %v", err)
	}

	if cfg.Models == nil {
		t.Fatal("cfg.Models is nil")
	}

	// Check providers
	if cfg.Models.Providers.Local == nil {
		t.Fatal("Providers.Local is nil")
	}
	if cfg.Models.Providers.Local.BaseURL != "http://localhost:11434/v1" {
		t.Errorf("BaseURL = %q, want %q", cfg.Models.Providers.Local.BaseURL, "http://localhost:11434/v1")
	}

	// Check custom models
	if len(cfg.Models.Custom) != 1 {
		t.Fatalf("Custom models count = %d, want 1", len(cfg.Models.Custom))
	}

	model := cfg.Models.Custom[0]
	t.Logf("Loaded model: %+v", model)

	if model.ID != "local-qwen3" {
		t.Errorf("ID = %q, want 'local-qwen3'", model.ID)
	}
	if model.Name != "Qwen3 (Ollama)" {
		t.Errorf("Name = %q, want 'Qwen3 (Ollama)'", model.Name)
	}
	if len(model.Tags) != 3 {
		t.Errorf("Tags count = %d, want 3", len(model.Tags))
	}
	if model.Capabilities.MaxContextWindow != 200000 {
		t.Errorf("MaxContextWindow = %d, want 200000", model.Capabilities.MaxContextWindow)
	}
	if model.Capabilities.SupportsTools != true {
		t.Errorf("SupportsTools = %v, want true", model.Capabilities.SupportsTools)
	}
	if len(model.Providers) != 1 {
		t.Fatalf("Providers count = %d, want 1", len(model.Providers))
	}
	if model.Providers[0].Driver != "local" {
		t.Errorf("Provider.Driver = %q, want 'local'", model.Providers[0].Driver)
	}
	if model.Providers[0].APIModel != "qwen3:latest" {
		t.Errorf("Provider.APIModel = %q, want 'qwen3:latest'", model.Providers[0].APIModel)
	}
}

func TestViperUnmarshalIssue(t *testing.T) {
	// UserConfigDir is the .reliant directory itself (e.g., ~/.reliant/)
	userConfigDir := t.TempDir()
	projectDir := t.TempDir()

	configContent := `
models:
  providers:
    local:
      base_url: http://localhost:11434/v1
`
	configPath := filepath.Join(userConfigDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Read the file back and parse directly to verify it's valid YAML
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}

	var directCfg config.Config
	if err := yaml.Unmarshal(data, &directCfg); err != nil {
		t.Fatalf("Direct YAML unmarshal failed: %v", err)
	}

	t.Logf("Direct unmarshal result: Models=%+v", directCfg.Models)
	if directCfg.Models != nil {
		t.Logf("Providers.Local=%+v", directCfg.Models.Providers.Local)
	}

	// Now load via the loader
	loader, err := NewLoader(LoaderOptions{UserConfigDir: userConfigDir})
	if err != nil {
		t.Fatalf("NewLoader failed: %v", err)
	}

	cfg, err := loader.LoadForProject(projectDir)
	if err != nil {
		t.Fatalf("LoadForProject failed: %v", err)
	}

	t.Logf("Loader result: Models=%+v", cfg.Models)
	if cfg.Models != nil {
		t.Logf("Providers.Local=%+v", cfg.Models.Providers.Local)
	}

	// Compare results
	if directCfg.Models != nil && cfg.Models == nil {
		t.Error("Direct YAML works but viper loader returns nil Models")
	}

	if directCfg.Models != nil && directCfg.Models.Providers.Local != nil {
		if cfg.Models == nil || cfg.Models.Providers.Local == nil {
			t.Error("Direct YAML has Providers.Local but viper loader doesn't")
		} else if cfg.Models.Providers.Local.BaseURL != directCfg.Models.Providers.Local.BaseURL {
			t.Errorf("BaseURL mismatch: loader=%q, direct=%q",
				cfg.Models.Providers.Local.BaseURL,
				directCfg.Models.Providers.Local.BaseURL)
		}
	}
}
