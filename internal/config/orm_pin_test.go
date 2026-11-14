package config

import (
	"strings"
	"testing"
)

// features.orm: false is the forge.yaml spelling of //forge:no-orm: the ORM
// goes off, the database and migrations stay on.
func TestFeatures_ORMPinnedOff(t *testing.T) {
	root := serviceTree(t)
	cfg, err := loadTreeWith(t, root, "features:\n  orm: false\n")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Features.ORMEnabled() {
		t.Fatal("features.orm: false must turn the ORM off")
	}
	if !cfg.Features.MigrationsEnabled() || !cfg.Features.CodegenEnabled() {
		t.Error("pinning the ORM off must leave migrations and codegen on")
	}
	if !cfg.Features.Pinned(FeatureORM) {
		t.Error("orm: false must read as pinned")
	}
}

func TestFeatures_ORMPinBesideMarker(t *testing.T) {
	root := serviceTree(t)
	write(t, root, "internal/db/doc.go", "//forge:no-orm: hand-written\npackage db\n")
	if cfg, err := loadTreeWith(t, root, "features:\n  orm: false\n"); err != nil || cfg.Features.ORMEnabled() {
		t.Fatalf("orm: false beside the marker says the same thing twice: err=%v", err)
	}
	_, err := loadTreeWith(t, root, "features:\n  orm: true\n")
	if err == nil || !strings.Contains(err.Error(), "//forge:no-orm") {
		t.Fatalf("orm: true beside //forge:no-orm must be a load error naming the marker, got %v", err)
	}
}
