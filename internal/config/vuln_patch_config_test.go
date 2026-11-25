package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestVulnPatchVerify(t *testing.T) {
	cfg, err := loadIn(t, "name: shop\nvuln:\n  patch:\n    verify: task test:short && task lint\n")
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Vuln.Patch.Verify; got != "task test:short && task lint" {
		t.Errorf("vuln.patch.verify = %q", got)
	}
	if _, err := loadIn(t, "name: shop\nvuln:\n  patch:\n    verfy: x\n"); err == nil {
		t.Error("a misspelt vuln.patch key loaded")
	}

	dir := t.TempDir()
	if got := ReadVulnConfig(dir); got != (VulnConfig{}) {
		t.Errorf("no forge.yaml: %+v", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte("name: shop\nvuln:\n  patch:\n    verify: make check\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ReadVulnConfig(dir).Patch.Verify; got != "make check" {
		t.Errorf("ReadVulnConfig = %q", got)
	}
}
