package projectstore

import (
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

func sampleConfig() *config.ProjectConfig {
	return &config.ProjectConfig{
		Name:         "demo",
		ModulePath:   "github.com/acme/demo",
		Kind:         "service",
		Binary:       "shared",
		ForgeVersion: "0.9.0",
		Database:     config.DatabaseConfig{Driver: "postgres"},
	}
}

func TestMetaMirrorsConfig(t *testing.T) {
	s := New(sampleConfig())
	m := s.Meta()
	if m.Name != "demo" || m.ModulePath != "github.com/acme/demo" {
		t.Fatalf("meta identity mismatch: %+v", m)
	}
	if !m.IsServiceKind() || m.IsCLIKind() || m.IsLibraryKind() {
		t.Fatalf("kind helpers wrong: %+v", m)
	}
	if !m.IsBinaryShared() {
		t.Fatalf("expected shared binary")
	}
	if m.EffectiveForgeVersion() != "0.9.0" {
		t.Fatalf("forge version: %q", m.EffectiveForgeVersion())
	}
	if New(&config.ProjectConfig{}).Meta().EffectiveForgeVersion() != "0.0.0" {
		t.Fatalf("empty forge version should default to 0.0.0")
	}
}

func TestFeaturesMirror(t *testing.T) {
	cfg := sampleConfig()
	cfg.Features = cfg.Features.With(config.FeatureDeploy, false)
	s := New(cfg)
	if s.Features().DeployEnabled() {
		t.Fatalf("a feature forced off should resolve disabled")
	}
	if !s.Features().CodegenEnabled() {
		t.Fatalf("forcing one feature off must leave the others at their defaults")
	}
}

func TestSectionAccessors(t *testing.T) {
	cfg := sampleConfig()
	s := New(cfg)
	if s.Database().Driver != "postgres" {
		t.Fatalf("database accessor wrong")
	}
	if s.Config() != cfg {
		t.Fatalf("Config() should return the underlying pointer")
	}
}
