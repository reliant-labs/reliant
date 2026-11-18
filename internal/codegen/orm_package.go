package codegen

import (
	"github.com/reliant-labs/forge/internal/config"
)

// ProjectORMPackage is the ORM placement projectDir's forge.yaml declares
// (database.orm.package), the default internal/db when it declares none or
// cannot be read. Best-effort by design, like projectAPIRESTEnabled: an
// unreadable forge.yaml has already been reported by the load that matters.
func ProjectORMPackage(projectDir string) config.ORMConfig {
	cfg, err := config.LoadProjectDir(projectDir)
	if err != nil {
		return config.ORMConfig{}
	}
	return config.ORMPackage(cfg)
}

// ORMImportSpec is the generated ORM package as an import spec for code
// that qualifies it as `db.`: the import path, or "db <path>" when the
// package is not called db. The spec format ("[alias ]path") is the one
// crudShimImports and ensureImportLine already speak.
func ORMImportSpec(modulePath string, orm config.ORMConfig) string {
	path := modulePath + "/" + orm.PackageDir()
	if orm.PackageName() == "db" {
		return path
	}
	return "db " + path
}
