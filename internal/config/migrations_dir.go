package config

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"
)

// MigrationsPath is the project-relative, slash-separated migrations
// directory: `database.migrations.dir`, else its shorthand
// `database.migrations_dir`, else DefaultMigrationsDir. It is the one place
// the alias and the default are applied — every reader of the directory
// (generate, the ORM's shadow apply, migration-safety, the db commands, the
// embedded migration set, audit) goes through it or through MigrationsDirIn.
// The loader refuses the two keys naming different directories.
func (c DatabaseConfig) MigrationsPath() string {
	if d := cleanMigrationsDir(c.Migrations.Dir); d != "" {
		return d
	}
	if d := cleanMigrationsDir(c.MigrationsDir); d != "" {
		return d
	}
	return DefaultMigrationsDir
}

// cleanMigrationsDir normalises a declared directory to the slash-separated
// form every consumer compares against: "./sql/migrations/" and
// "sql/migrations" are one directory. Empty stays empty.
func cleanMigrationsDir(dir string) string {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return ""
	}
	return path.Clean(filepath.ToSlash(dir))
}

// MigrationsDirIn returns the project-relative migrations directory of the
// project rooted at projectDir (see DatabaseConfig.MigrationsPath), for the
// leaf code that has a project directory and no loaded config: codegen, the
// shadow-schema readers, the seeder.
//
// It decodes only forge.yaml's `database:` block — not the whole
// LoadProjectDir derivation, which reads the tree to derive the project kind
// and would be paid again by every one of those callers in a single generate.
// It still goes through the yaml decoder, so flow style, anchors and nesting
// read exactly as the full loader reads them. A missing or unreadable
// forge.yaml is the default: the command that loaded it first has already
// reported it.
func MigrationsDirIn(projectDir string) string {
	db, _ := readDatabaseBlock(projectDir)
	return db.MigrationsPath()
}

// readDatabaseBlock decodes forge.yaml's `database:` block alone. ok is false
// when there is no readable, parseable forge.yaml; the zero block is then
// every default.
func readDatabaseBlock(projectDir string) (DatabaseConfig, bool) {
	data, err := os.ReadFile(filepath.Join(projectDir, ProjectConfigFile)) //nolint:gosec // project dir + fixed filename
	if err != nil {
		return DatabaseConfig{}, false
	}
	var doc struct {
		Database DatabaseConfig `yaml:"database"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return DatabaseConfig{}, false
	}
	return doc.Database, true
}

// MigrationsPathIn joins MigrationsDirIn onto projectDir: the on-disk
// migrations directory of that project.
func MigrationsPathIn(projectDir string) string {
	return filepath.Join(projectDir, filepath.FromSlash(MigrationsDirIn(projectDir)))
}

// validateMigrationsDir rejects a `database.migrations_dir` that is not a
// directory inside the project. An absolute path would bake one machine's
// layout into a file every teammate and CI runner shares, and a path that
// climbs out of the project cannot be embedded into the binary (go:embed
// reads only below its package) or mounted into the build context.
func validateMigrationsDir(cfg *ProjectConfig, root *yaml.Node) []validationIssue {
	dir := strings.TrimSpace(cfg.Database.MigrationsDir)
	if dir == "" || isProjectRelative(dir, false) {
		return nil
	}
	line, col := findNodePos(root, []string{"database", "migrations_dir"})
	return []validationIssue{{
		line: line, column: col,
		msg: fmt.Sprintf("database.migrations_dir %q is not a directory inside the project", cfg.Database.MigrationsDir),
		fix: "use a project-relative directory such as `sql/migrations`, or delete the key for the default " + DefaultMigrationsDir + ".",
	}}
}
