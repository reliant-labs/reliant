package config

import (
	"fmt"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// MigrationsConfig is the `database.migrations:` block: where a project's
// migrations live and which tool applies them.
//
// A team that already runs goose, atlas, flyway, dbmate, golang-migrate or a
// command of its own keeps it. forge does not convert the migrations; it runs
// the team's tool — the migrations HOOK, in the spirit of `generate.hooks` —
// wherever it would have applied its own: to the throwaway shadow database the
// ORM, seeding and the other schema readers introspect, and from `forge db
// migrate up` / `status`. Everything that reads the APPLIED schema keeps
// working unchanged, because it never cared who applied it.
//
//	database:
//	  migrations:
//	    dir: migrations
//	    tool: goose
//
// The hook contract: forge runs Apply (and Status, Check) as an argv — no shell —
// in the project root, with DATABASE_URL set to the database to migrate, the
// tool-specific variables the built-in tool reads (GOOSE_DBSTRING,
// FLYWAY_URL, …), FORGE_MIGRATIONS_DIR (absolute), FORGE_PROJECT_DIR and
// FORGE_PHASE (shadow, cli). `${NAME}` in an argument is replaced by one of
// those variables; nothing else is expanded. Exit 0 means applied. Status
// prints whatever the tool prints; forge shows it and does not parse it.
type MigrationsConfig struct {
	// Dir is the project-relative directory that holds the migrations.
	// Empty means `database.migrations_dir`, else DefaultMigrationsDir.
	Dir string `yaml:"dir,omitempty"`
	// Tool names the migration tool: forge (the default), goose, atlas,
	// flyway, dbmate, golang-migrate or custom. Every tool but forge and
	// custom has a built-in Apply and Status.
	Tool string `yaml:"tool,omitempty"`
	// Apply is the argv that applies every pending migration to
	// $DATABASE_URL. It overrides a built-in tool's; `tool: custom` requires
	// it.
	Apply []string `yaml:"apply,omitempty"`
	// Status is the argv that reports what is applied. It overrides a
	// built-in tool's; optional for `tool: custom`.
	Status []string `yaml:"status,omitempty"`
	// Check is the argv that answers "is $DATABASE_URL fully migrated?":
	// exit 0 only when every migration is applied and none failed part-way.
	// forge runs it before seeding writes to a database. A built-in tool
	// needs none (forge reads its ledger); `tool: custom` needs it to seed,
	// since forge cannot read a ledger it does not know.
	Check []string `yaml:"check,omitempty"`
}

// The migration tools `database.migrations.tool` accepts.
const (
	MigrationToolForge         = "forge"
	MigrationToolGoose         = "goose"
	MigrationToolAtlas         = "atlas"
	MigrationToolFlyway        = "flyway"
	MigrationToolDbmate        = "dbmate"
	MigrationToolGolangMigrate = "golang-migrate"
	MigrationToolCustom        = "custom"
)

// MigrationTools lists every accepted `database.migrations.tool`, forge first.
func MigrationTools() []string {
	return []string{MigrationToolForge, MigrationToolGoose, MigrationToolAtlas, MigrationToolFlyway,
		MigrationToolDbmate, MigrationToolGolangMigrate, MigrationToolCustom}
}

// MigrationTool is the tool that applies the project's migrations: the
// declared `database.migrations.tool`, lower-cased, or forge.
func (c DatabaseConfig) MigrationTool() string {
	if t := strings.ToLower(strings.TrimSpace(c.Migrations.Tool)); t != "" {
		return t
	}
	return MigrationToolForge
}

// UsesMigrationHook reports whether a tool other than forge's own migrator
// applies the migrations, so forge runs the hook wherever it would have
// applied them itself.
func (c DatabaseConfig) UsesMigrationHook() bool {
	return c.MigrationTool() != MigrationToolForge
}

// MigrationToolIn returns the migration tool of the project rooted at
// projectDir (DatabaseConfig.MigrationTool), read the way MigrationsDirIn
// reads the directory. A missing or unreadable forge.yaml is forge.
func MigrationToolIn(projectDir string) string {
	db, ok := readDatabaseBlock(projectDir)
	if !ok {
		return MigrationToolForge
	}
	return db.MigrationTool()
}

// DatabaseIn returns forge.yaml's `database:` block of the project rooted at
// projectDir, decoded alone (see MigrationsDirIn for why not the full load).
// A missing or unreadable forge.yaml is the zero block: forge's migrator over
// db/migrations.
func DatabaseIn(projectDir string) DatabaseConfig {
	db, _ := readDatabaseBlock(projectDir)
	return db
}

// validateMigrations checks `database.migrations:` and how it sits beside the
// `database.migrations_dir` shorthand.
func validateMigrations(cfg *ProjectConfig, root *yaml.Node) []validationIssue {
	m := cfg.Database.Migrations
	var out []validationIssue
	at := func(segs ...string) (int, int) {
		line, col := findNodePos(root, append([]string{"database", "migrations"}, segs...))
		if line == 0 {
			line, col = findNodePos(root, []string{"database", "migrations"})
		}
		return line, col
	}

	if m.Dir != "" && !isProjectRelative(m.Dir, false) {
		line, col := at("dir")
		out = append(out, validationIssue{line: line, column: col,
			msg: fmt.Sprintf("database.migrations.dir %q is not a directory inside the project", m.Dir),
			fix: "use a project-relative directory such as `migrations`, or delete the key for the default " + DefaultMigrationsDir + "."})
	}
	if m.Dir != "" && cfg.Database.MigrationsDir != "" &&
		cleanMigrationsDir(m.Dir) != cleanMigrationsDir(cfg.Database.MigrationsDir) {
		line, col := at("dir")
		out = append(out, validationIssue{line: line, column: col,
			msg: fmt.Sprintf("database.migrations.dir %q and database.migrations_dir %q name different directories", m.Dir, cfg.Database.MigrationsDir),
			fix: "keep one: database.migrations.dir is the canonical key, database.migrations_dir its shorthand."})
	}

	tool := cfg.Database.MigrationTool()
	if !slices.Contains(MigrationTools(), tool) {
		line, col := at("tool")
		out = append(out, validationIssue{line: line, column: col,
			msg: fmt.Sprintf("database.migrations.tool %q is not a known migration tool", m.Tool),
			fix: "use one of " + strings.Join(MigrationTools(), ", ") + " — `custom` runs your own `apply:` argv."})
		return out
	}
	for _, f := range []struct {
		key  string
		argv []string
	}{{"apply", m.Apply}, {"status", m.Status}, {"check", m.Check}} {
		if f.argv == nil {
			continue
		}
		if tool == MigrationToolForge {
			line, col := at(f.key)
			out = append(out, validationIssue{line: line, column: col,
				msg: "database.migrations." + f.key + " is set, but tool is forge",
				fix: "forge's own migrator takes no command. To run one of yours, set `tool:` to your tool (or `custom`)."})
			continue
		}
		if len(f.argv) == 0 || strings.TrimSpace(f.argv[0]) == "" {
			line, col := at(f.key)
			out = append(out, validationIssue{line: line, column: col,
				msg: "database.migrations." + f.key + " has no command",
				fix: "set it to the argv to run, e.g. [./scripts/migrate.sh, up], which reads $DATABASE_URL and $FORGE_MIGRATIONS_DIR from its environment (keep the credentialed URL off the argv); a shell line is [sh, -c, \"...\"]."})
		}
	}
	if tool == MigrationToolCustom && len(m.Apply) == 0 {
		line, col := at("tool")
		out = append(out, validationIssue{line: line, column: col,
			msg: "database.migrations.tool is custom but there is no apply command",
			fix: "set `apply:` to the argv that applies every pending migration to $DATABASE_URL."})
	}
	return out
}
