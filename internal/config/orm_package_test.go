package config

import (
	"strings"
	"testing"
)

func TestORMConfig_Defaults(t *testing.T) {
	var zero ORMConfig
	if zero.PackageDir() != "internal/db" || zero.PackageName() != "db" {
		t.Errorf("zero ORMConfig = %q / %q, want internal/db / db", zero.PackageDir(), zero.PackageName())
	}
	if got := ORMPackage(nil).PackageDir(); got != "internal/db" {
		t.Errorf("ORMPackage(nil) = %q", got)
	}
	c := ORMConfig{Package: "internal/store/"}
	if c.PackageDir() != "internal/store" || c.PackageName() != "store" {
		t.Errorf("internal/store/ = %q / %q", c.PackageDir(), c.PackageName())
	}
}

func TestLoadProject_ORMPackage(t *testing.T) {
	cfg, err := LoadProject([]byte("name: app\nmodule_path: example.com/app\ndatabase:\n  orm:\n    package: internal/store\n"), "forge.yaml")
	if err != nil {
		t.Fatalf("database.orm.package must be a known key: %v", err)
	}
	if cfg.Database.ORM.PackageDir() != "internal/store" {
		t.Errorf("PackageDir = %q", cfg.Database.ORM.PackageDir())
	}

	for _, bad := range []string{"/abs/store", "internal/Store", "internal/my-store", "cmd/main", "../store"} {
		_, err := LoadProject([]byte("name: app\nmodule_path: example.com/app\ndatabase:\n  orm:\n    package: "+bad+"\n"), "forge.yaml")
		if err == nil || !strings.Contains(err.Error(), "database.orm.package") {
			t.Errorf("package %q: err = %v, want a database.orm.package refusal", bad, err)
		}
	}
}
