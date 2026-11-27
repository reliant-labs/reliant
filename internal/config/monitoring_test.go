package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestMonitoring_ShorthandAndMapping(t *testing.T) {
	cfg, err := loadIn(t, "name: shop\nenvs: [staging, prod]\nmonitoring: true\n")
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.MonitoringOn() || cfg.Monitoring.Endpoint != "" || !cfg.DeclaresEnv("prod") || cfg.DeclaresEnv("dev") {
		t.Fatalf("shorthand = %+v envs=%v", cfg.Monitoring, cfg.Envs)
	}

	cfg, err = loadIn(t, "name: shop\nenvs: [prod]\nmonitoring:\n  endpoint: https://otel.example.com:4318\n")
	if err != nil {
		t.Fatal(err)
	}
	// A present block with no `enabled:` is on.
	if !cfg.MonitoringOn() || cfg.Monitoring.Endpoint != "https://otel.example.com:4318" {
		t.Fatalf("mapping = %+v", cfg.Monitoring)
	}

	cfg, err = loadIn(t, "name: shop\nmonitoring:\n  enabled: false\n")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MonitoringOn() {
		t.Fatal("enabled: false loaded as on")
	}

	cfg, err = loadIn(t, "name: shop\n")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Monitoring != nil || cfg.MonitoringOn() {
		t.Fatal("no block must be no monitoring")
	}
}

func TestMonitoring_ShorthandSurvivesARewrite(t *testing.T) {
	cfg, err := loadIn(t, "name: shop\nenvs: [prod]\nmonitoring: true\n")
	if err != nil {
		t.Fatal(err)
	}
	out, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "monitoring: true") || !strings.Contains(string(out), "- prod") {
		t.Fatalf("rewrite lost the author's shorthand:\n%s", out)
	}
}

func TestMonitoring_ValidationRefusals(t *testing.T) {
	for name, src := range map[string]string{
		"bad env name":       "name: shop\nenvs: [Prod]\n",
		"duplicate env":      "name: shop\nenvs: [prod, prod]\n",
		"relative endpoint":  "name: shop\nmonitoring:\n  endpoint: otel.example.com\n",
		"bad control plane":  "name: shop\nmonitoring:\n  control_plane: ftp://cp.example.com\n",
		"not a bool or map":  "name: shop\nmonitoring: sometimes\n",
		"unknown nested key": "name: shop\nmonitoring:\n  endpiont: https://otel.example.com\n",
	} {
		if _, err := loadIn(t, src); err == nil {
			t.Errorf("%s: loaded", name)
		}
	}
}

func TestValidEnvName(t *testing.T) {
	for _, ok := range []string{"prod", "staging", "eu-west-1", "p"} {
		if !ValidEnvName(ok) {
			t.Errorf("%q refused", ok)
		}
	}
	for _, bad := range []string{"", "Prod", "-prod", "prod-", "pr od", "../x", strings.Repeat("a", 64)} {
		if ValidEnvName(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}

// loadIn loads src as the forge.yaml of an empty directory (no go.mod, so no
// module_path is required).
func loadIn(t *testing.T, src string) (*ProjectConfig, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "forge.yaml")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	return LoadProject([]byte(src), path)
}
