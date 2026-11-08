package codegen

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

func TestORMImportSpec(t *testing.T) {
	cases := []struct {
		orm  config.ORMConfig
		want string
	}{
		{config.ORMConfig{}, "example.com/app/internal/db"},
		{config.ORMConfig{Package: "internal/db"}, "example.com/app/internal/db"},
		// Generated CRUD qualifies the ORM as db., so a package with
		// another name is imported under that name.
		{config.ORMConfig{Package: "internal/store"}, "db example.com/app/internal/store"},
		{config.ORMConfig{Package: "internal/data/db"}, "example.com/app/internal/data/db"},
	}
	for _, c := range cases {
		if got := ORMImportSpec("example.com/app", c.orm); got != c.want {
			t.Errorf("ORMImportSpec(%+v) = %q, want %q", c.orm, got, c.want)
		}
	}
	data := CRUDTemplateData{DBImport: "db example.com/app/internal/store"}
	if got := data.DBImportLine(); got != `db "example.com/app/internal/store"` {
		t.Errorf("DBImportLine = %s", got)
	}
}

func TestProjectORMPackage_ReadsForgeYAML(t *testing.T) {
	dir := t.TempDir()
	if got := ProjectORMPackage(dir).PackageDir(); got != "internal/db" {
		t.Errorf("no forge.yaml: PackageDir = %q, want internal/db", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "forge.yaml"),
		[]byte("name: app\nmodule_path: example.com/app\ndatabase:\n  orm:\n    package: internal/store\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	orm := ProjectORMPackage(dir)
	if orm.PackageDir() != "internal/store" || orm.PackageName() != "store" {
		t.Errorf("ProjectORMPackage = %q / %q, want internal/store / store", orm.PackageDir(), orm.PackageName())
	}
}
