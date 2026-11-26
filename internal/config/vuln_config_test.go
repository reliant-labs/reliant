package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestVuln_AttachSBOMDefaultsOn(t *testing.T) {
	for _, tc := range []struct {
		yaml string
		want bool
	}{
		{"name: shop\n", true},
		{"name: shop\nvuln: {}\n", true},
		{"name: shop\nvuln:\n  attach_sbom: true\n", true},
		{"name: shop\nvuln:\n  attach_sbom: false\n", false},
	} {
		cfg, err := loadIn(t, tc.yaml)
		if err != nil {
			t.Fatalf("%q: %v", tc.yaml, err)
		}
		if got := cfg.Vuln.AttachSBOMEnabled(); got != tc.want {
			t.Errorf("%q: AttachSBOMEnabled = %v, want %v", tc.yaml, got, tc.want)
		}
	}
}

func TestVuln_RefusesUnknownKeysAndKeepsAnExplicitFalse(t *testing.T) {
	if _, err := loadIn(t, "name: shop\nvuln:\n  attach_sboms: false\n"); err == nil {
		t.Error("a misspelt vuln key loaded")
	}
	cfg, err := loadIn(t, "name: shop\nvuln:\n  attach_sbom: false\n")
	if err != nil {
		t.Fatal(err)
	}
	out, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "attach_sbom: false") {
		t.Errorf("a rewrite lost `attach_sbom: false`:\n%s", out)
	}
	cfg, err = loadIn(t, "name: shop\n")
	if err != nil {
		t.Fatal(err)
	}
	if out, _ := yaml.Marshal(cfg); strings.Contains(string(out), "\nvuln:") || strings.Contains(string(out), "attach_sbom") {
		t.Errorf("an absent vuln block was written back:\n%s", out)
	}
}

func TestReadVulnConfig(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if !ReadVulnConfig(dir).AttachSBOMEnabled() {
		t.Error("no forge.yaml must read as every default on")
	}
	if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte("name: shop\nvuln:\n  attach_sbom: false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if ReadVulnConfig(dir).AttachSBOMEnabled() {
		t.Error("attach_sbom: false read as on")
	}
}
