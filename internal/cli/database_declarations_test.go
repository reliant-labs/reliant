package cli

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/migrationhook"
)

// The render contract carries each database's schema, and the decoder reads
// it onto the entity that declares it: a managed one (hosted), a schema-only
// one (mixed). A database that declares no migrations decodes with none.
func TestRenderContract_DatabaseSchemasDecode(t *testing.T) {
	hosted, _ := loadContract(t, "hosted")
	if len(hosted.Databases) != 1 {
		t.Fatalf("hosted: %d databases, want 1", len(hosted.Databases))
	}
	db := hosted.Databases[0]
	want := DatabaseSchemaEntity{
		Dialect:    "postgres",
		Migrations: &DatabaseMigrationsEntity{Dir: "db/migrations", Driver: "forge"},
		ORM:        &DatabaseORMEntity{Package: "internal/db"},
	}
	if !reflect.DeepEqual(db.DatabaseSchemaEntity, want) {
		t.Errorf("hosted database schema = %+v, want %+v", db.DatabaseSchemaEntity, want)
	}

	mixed, _ := loadContract(t, "mixed")
	if len(mixed.SchemaOnlyDatabases) != 1 || mixed.SchemaOnlyDatabases[0].Name != "analytics" {
		t.Fatalf("mixed: schema_only_databases = %+v, want analytics", mixed.SchemaOnlyDatabases)
	}
	if got := mixed.SchemaOnlyDatabases[0].Migrations; got == nil || got.Driver != "goose" || got.Dir != "analytics/migrations" {
		t.Errorf("mixed: analytics migrations = %+v", got)
	}

	cluster, _ := loadContract(t, "cluster")
	for _, d := range cluster.Databases {
		if d.Migrations != nil || d.Dialect != "" || d.ORM != nil {
			t.Errorf("cluster: database %s declares no migrations but decoded a schema %+v", d.Name, d.DatabaseSchemaEntity)
		}
	}
}

func schemaEnv(dbs ...DatabaseEntity) *KCLEntities { return &KCLEntities{Databases: dbs} }

func schemaMigrations(dir, driver string) DatabaseSchemaEntity {
	return DatabaseSchemaEntity{Dialect: "postgres", Migrations: &DatabaseMigrationsEntity{Dir: dir, Driver: driver}}
}

// The union reads every env: a database one env declares is the project's,
// envs that agree (in any spelling) merge, and a database that declares no
// migrations is not a schema declaration.
func TestUnionDeclaredDatabases_MergesAgreeingEnvs(t *testing.T) {
	got, err := unionDeclaredDatabases(map[string]*KCLEntities{
		"dev":  schemaEnv(DatabaseEntity{Name: "default", DatabaseSchemaEntity: schemaMigrations("./db/migrations/", "forge")}),
		"prod": schemaEnv(DatabaseEntity{Name: "default", DatabaseSchemaEntity: schemaMigrations("db/migrations", "")}, DatabaseEntity{Name: "cache"}),
		"e2e": {SchemaOnlyDatabases: []SchemaOnlyDatabaseEntity{{
			Name: "analytics",
			DatabaseSchemaEntity: DatabaseSchemaEntity{
				Migrations: &DatabaseMigrationsEntity{Dir: "analytics", Driver: "goose"},
				ORM:        &DatabaseORMEntity{Package: "internal/analyticsdb/"},
			},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d databases, want analytics and default: %+v", len(got), got)
	}
	a, d := got[0], got[1]
	if a.Name != "analytics" || a.Migrations.Tool != "goose" || a.ORM.Package != "internal/analyticsdb" || a.Dialect != "postgres" {
		t.Errorf("analytics = %+v", a)
	}
	if d.Name != "default" || d.MigrationsPath() != "db/migrations" || d.Migrations.Tool != "forge" {
		t.Errorf("default = %+v", d)
	}
	if !reflect.DeepEqual(d.Envs, []string{"dev", "prod"}) || d.Source != config.DatabaseSourceKCL {
		t.Errorf("default envs/source = %v/%s", d.Envs, d.Source)
	}
}

func TestUnionDeclaredDatabases_RefusesDisagreeingEnvs(t *testing.T) {
	_, err := unionDeclaredDatabases(map[string]*KCLEntities{
		"dev":  schemaEnv(DatabaseEntity{Name: "app", DatabaseSchemaEntity: schemaMigrations("db/migrations", "forge")}),
		"prod": schemaEnv(DatabaseEntity{Name: "app", DatabaseSchemaEntity: schemaMigrations("migrations", "goose")}),
	})
	if err == nil || !strings.Contains(err.Error(), `database "app" declares a different schema in env prod than in env dev`) {
		t.Fatalf("err = %v", err)
	}
}

func TestUnionDeclaredDatabases_RefusesSharedDirAcrossEnvs(t *testing.T) {
	_, err := unionDeclaredDatabases(map[string]*KCLEntities{
		"dev":  schemaEnv(DatabaseEntity{Name: "a", DatabaseSchemaEntity: schemaMigrations("db/migrations", "forge")}),
		"prod": schemaEnv(DatabaseEntity{Name: "b", DatabaseSchemaEntity: schemaMigrations("./db/migrations", "forge")}),
	})
	if err == nil || !strings.Contains(err.Error(), "both declare migrations dir db/migrations") {
		t.Fatalf("err = %v", err)
	}
}

// writeProject lays out a project with forge.yaml body yaml and, per env, a
// deploy/kcl/<env>/main.k holding kcl.
func writeDBProject(t *testing.T, yaml string, kcl map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	if yaml != "" {
		if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte(yaml), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for env, body := range kcl {
		p := filepath.Join(dir, "deploy", "kcl", env, "main.k")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// stubDeclarer installs a declarer that answers dbs for every project.
func stubDeclarer(t *testing.T, dbs []config.DeclaredDatabase, err error) {
	t.Helper()
	prev := config.RegisterDatabaseDeclarer(func(string) ([]config.DeclaredDatabase, error) { return dbs, err })
	t.Cleanup(func() { config.RegisterDatabaseDeclarer(prev) })
}

// Precedence: with no declaration the project is its forge.yaml; with one,
// the declaration wins and forge.yaml's orm fills the primary; with
// migrations in both, it is an error.
func TestProjectDatabases_Precedence(t *testing.T) {
	kclDefault := config.DeclaredDatabase{Name: "default", Dialect: "postgres", Source: config.DatabaseSourceKCL,
		Migrations: config.MigrationsConfig{Dir: "sql", Tool: "goose"}}

	t.Run("legacy only", func(t *testing.T) {
		stubDeclarer(t, nil, nil)
		dir := writeDBProject(t, "name: x\ndatabase:\n  migrations_dir: sql/migrations\n", nil)
		dbs, err := config.ProjectDatabases(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(dbs) != 1 || dbs[0].Name != "default" || dbs[0].MigrationsPath() != "sql/migrations" || dbs[0].Source != config.DatabaseSourceForgeYAML {
			t.Fatalf("dbs = %+v", dbs)
		}
		if got := config.MigrationsDirIn(dir); got != "sql/migrations" {
			t.Errorf("MigrationsDirIn = %s", got)
		}
	})

	t.Run("kcl wins, forge.yaml orm fills the primary", func(t *testing.T) {
		stubDeclarer(t, []config.DeclaredDatabase{kclDefault}, nil)
		dir := writeDBProject(t, "name: x\ndatabase:\n  orm:\n    package: internal/store\n", nil)
		dbs, err := config.ProjectDatabases(dir)
		if err != nil {
			t.Fatal(err)
		}
		if dbs[0].ORM.Package != "internal/store" {
			t.Errorf("forge.yaml orm did not fill the primary: %+v", dbs[0])
		}
		if got := config.MigrationsDirIn(dir); got != "sql" {
			t.Errorf("MigrationsDirIn = %s, want the declaration's sql", got)
		}
		if got := config.MigrationToolIn(dir); got != "goose" {
			t.Errorf("MigrationToolIn = %s, want goose", got)
		}
	})

	t.Run("both declare migrations", func(t *testing.T) {
		stubDeclarer(t, []config.DeclaredDatabase{kclDefault}, nil)
		dir := writeDBProject(t, "name: x\ndatabase:\n  migrations:\n    dir: db/migrations\n", nil)
		_, err := config.ProjectDatabases(dir)
		if !errors.Is(err, config.ErrDatabaseDeclaredTwice) {
			t.Fatalf("err = %v, want ErrDatabaseDeclaredTwice", err)
		}
		// A leaf reader cannot fail: it keeps forge.yaml's answer, and the
		// command that reads databases reports the error.
		if got := config.MigrationsDirIn(dir); got != "db/migrations" {
			t.Errorf("MigrationsDirIn = %s", got)
		}
	})
}

func TestSelectDatabase(t *testing.T) {
	a := config.DeclaredDatabase{Name: "analytics"}
	b := config.DeclaredDatabase{Name: "ledger"}
	def := config.DeclaredDatabase{Name: "default"}
	if d, err := config.SelectDatabase([]config.DeclaredDatabase{a}, ""); err != nil || d.Name != "analytics" {
		t.Errorf("the only database is the primary: %v %v", d, err)
	}
	if d, err := config.SelectDatabase([]config.DeclaredDatabase{a, def}, ""); err != nil || d.Name != "default" {
		t.Errorf("default is the primary: %v %v", d, err)
	}
	if _, err := config.SelectDatabase([]config.DeclaredDatabase{a, b}, ""); err == nil || !strings.Contains(err.Error(), "pass --db <name>") {
		t.Errorf("ambiguous: %v", err)
	}
	if d, err := config.SelectDatabase([]config.DeclaredDatabase{a, b}, "ledger"); err != nil || d.Name != "ledger" {
		t.Errorf("named: %v %v", d, err)
	}
	if _, err := config.SelectDatabase([]config.DeclaredDatabase{a, b}, "nope"); err == nil || !strings.Contains(err.Error(), "analytics, ledger") {
		t.Errorf("unknown: %v", err)
	}
}

// The pre-check finds a declaration wherever the project's KCL spells one,
// and finds none in a project that predates them — which therefore
// evaluates nothing.
func TestKCLMayDeclareDatabaseSchemas(t *testing.T) {
	none := writeDBProject(t, "", map[string]string{"dev": "import forge\n# migrations = later\noutput = forge.render(forge.Bundle {project = \"x\", databases = [forge.ManagedDatabase {name = \"db\"}]})\n"})
	if kclMayDeclareDatabaseSchemas(none) {
		t.Error("a project with no schema declaration (a commented one included) matched")
	}
	some := writeDBProject(t, "", map[string]string{"prod": "_db = forge.ManagedDatabase {\n    name = \"db\"\n    migrations = forge.DatabaseMigrations {}\n}\n"})
	if !kclMayDeclareDatabaseSchemas(some) {
		t.Error("a declaration was missed")
	}
	if kclMayDeclareDatabaseSchemas(t.TempDir()) {
		t.Error("a project with no deploy/kcl matched")
	}
	// With no deploy/kcl the declarer reads nothing: the project is its
	// forge.yaml.
	if dbs, err := declaredDatabasesOfEnvs(none); err != nil || dbs != nil {
		t.Errorf("declarer on a non-declaring project = %v, %v", dbs, err)
	}
}

// `forge db … --db <name>` acts on the named database; without it, on the
// primary; with several and no `default`, it refuses rather than guess.
func TestDBSelect_ByName(t *testing.T) {
	clearProjectDir(t)
	proj := writeDBProject(t, "name: x\nmodule_path: example.com/x\n", nil)
	elsewhere(t)
	if err := cmdutil.SetProjectDir(proj); err != nil {
		t.Fatal(err)
	}
	app := config.DeclaredDatabase{Name: "app", Source: config.DatabaseSourceKCL, Migrations: config.MigrationsConfig{Dir: "db/migrations", Tool: "forge"}}
	analytics := config.DeclaredDatabase{Name: "analytics", Source: config.DatabaseSourceKCL, Migrations: config.MigrationsConfig{Dir: "analytics/migrations", Tool: "goose"}}
	stubDeclarer(t, []config.DeclaredDatabase{analytics, app}, nil)
	prev := dbFlagName
	t.Cleanup(func() { dbFlagName = prev })

	dbFlagName = ""
	if _, err := selectMigrationsDir(""); err == nil || !strings.Contains(err.Error(), "none is named \"default\"") {
		t.Fatalf("ambiguous default: err = %v", err)
	}
	if _, err := requireMigrationsDir(""); err == nil || !strings.Contains(err.Error(), "pass --db <name>") {
		t.Fatalf("requireMigrationsDir must refuse the ambiguous default: %v", err)
	}
	// --dir needs no --db when a declared database names it; with several
	// declared, a --dir none of them names is refused rather than applied
	// with the primary's tool (review R4.B2 on #2211).
	if got, err := selectMigrationsDir("analytics/migrations"); err != nil || got != filepath.Join(proj, "analytics", "migrations") {
		t.Errorf("--dir analytics/migrations = %q, %v", got, err)
	}
	if _, err := selectMigrationsDir("sql"); !errors.Is(err, config.ErrMigrationsDirUnowned) {
		t.Errorf("--dir sql with several declared: err = %v, want ErrMigrationsDirUnowned", err)
	}
	if _, err := requireMigrationsDir("sql"); !errors.Is(err, config.ErrMigrationsDirUnowned) {
		t.Errorf("requireMigrationsDir(--dir sql) with several declared: err = %v, want ErrMigrationsDirUnowned", err)
	}

	// --db and --dir together must name one database (review R2.B1 on
	// #2211): another database's directory under --db is refused; its own,
	// spelled either way, passes.
	dbFlagName = "analytics"
	if _, err := selectMigrationsDir("db/migrations"); err == nil || !strings.Contains(err.Error(), `database "app"'s migrations, but --db names "analytics"`) {
		t.Errorf("--db analytics --dir db/migrations: err = %v, want the mismatch refused", err)
	}
	if _, err := requireMigrationsDir("db/migrations"); err == nil {
		t.Error("requireMigrationsDir (migrate up, seed, reset) accepted --db analytics with app's --dir")
	}
	for _, d := range []string{"analytics/migrations", "./analytics/migrations/", filepath.Join(proj, "analytics", "migrations")} {
		if got, err := selectMigrationsDir(d); err != nil || got != filepath.Join(proj, "analytics", "migrations") {
			t.Errorf("--db analytics --dir %s = %q, %v; want its own directory", d, got, err)
		}
	}
	dbFlagName = "nope"
	if _, err := selectMigrationsDir("db/migrations"); err == nil || !strings.Contains(err.Error(), `no database named "nope"`) {
		t.Errorf("unknown --db with --dir: %v", err)
	}

	dbFlagName = "analytics"
	got, err := selectMigrationsDir("")
	if err != nil || got != filepath.Join(proj, "analytics", "migrations") {
		t.Fatalf("--db analytics = %q, %v", got, err)
	}
	// Its directory resolves to its own tool.
	h, hooked, err := projectMigrationHook(got)
	if err != nil || !hooked || h.Tool != "goose" {
		t.Errorf("--db analytics hook = %+v, %v, %v; want goose", h, hooked, err)
	}
	// --dir naming no declared database's directory is refused, not handed
	// to the primary's tool.
	if _, hooked, err := projectMigrationHook(filepath.Join(proj, "sql")); hooked || !errors.Is(err, config.ErrMigrationsDirUnowned) {
		t.Errorf("--dir sql: hooked=%v err=%v, want ErrMigrationsDirUnowned", hooked, err)
	}
	dbFlagName = "app"
	got, _ = selectMigrationsDir("")
	if _, hooked, _ := projectMigrationHook(got); hooked {
		t.Error("--db app (forge's migrator) resolved a hook")
	}
	dbFlagName = "nope"
	if _, err := selectMigrationsDir(""); err == nil || !strings.Contains(err.Error(), `no database named "nope"`) {
		t.Errorf("unknown --db: %v", err)
	}

	// A project with one database: no flag needed, and `--db default` names
	// forge.yaml's.
	stubDeclarer(t, nil, nil)
	for _, name := range []string{"", "default"} {
		dbFlagName = name
		if got, err := selectMigrationsDir(""); err != nil || got != filepath.Join(proj, "db", "migrations") {
			t.Errorf("legacy --db %q = %q, %v", name, got, err)
		}
	}
}

// THE BYTE-IDENTICAL GUARANTEE. A project that declares no database schema
// in KCL — forge.yaml's legacy keys, or nothing at all, beside a deploy tree
// that provisions a ManagedDatabase as before — evaluates nothing, and every
// accessor generate, lint and the db commands read the database through
// answers exactly what it answered before the env declaration became a
// source (no declarer registered). Generated output is a function of those
// answers, so it is byte-identical.
func TestDeclaredDatabases_LegacyProjectsUnchanged(t *testing.T) {
	kcl := map[string]string{"prod": "import forge\noutput = forge.render(forge.Bundle {project = \"x\", databases = [forge.ManagedDatabase {name = \"db\"}]})\n"}
	for name, yaml := range map[string]string{
		"legacy keys": "name: x\nmodule_path: example.com/x\ndatabase:\n  migrations_dir: sql/migrations\n  orm:\n    package: internal/store\n  migration_safety:\n    destructive_change: warn\n",
		"tool hook":   "name: x\nmodule_path: example.com/x\ndatabase:\n  migrations:\n    dir: migrations\n    tool: goose\n",
		"none":        "name: x\nmodule_path: example.com/x\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir := writeDBProject(t, yaml, kcl)
			type answers struct {
				Dir, Path, Tool string
				DB              config.DatabaseConfig
				ORM             config.ORMConfig
				Primary         config.DatabaseConfig
				Loaded          config.DatabaseConfig
				Dbs             []config.DeclaredDatabase
			}
			read := func() answers {
				cfg, err := config.LoadProjectDir(dir)
				if err != nil {
					t.Fatal(err)
				}
				dbs, err := config.ProjectDatabases(dir)
				if err != nil {
					t.Fatal(err)
				}
				a := answers{
					Dir: config.MigrationsDirIn(dir), Path: config.MigrationsPathIn(dir), Tool: config.MigrationToolIn(dir),
					DB: config.DatabaseIn(dir), ORM: config.ORMPackage(cfg), Primary: cfg.PrimaryDatabase(), Dbs: dbs,
				}
				if err := cfg.UseDeclaredDatabases(); err != nil {
					t.Fatal(err)
				}
				a.Loaded = cfg.Database
				return a
			}
			before := func() answers {
				prev := config.RegisterDatabaseDeclarer(nil)
				defer func() { config.RegisterDatabaseDeclarer(prev) }()
				return read()
			}()
			renders := declarationRenders.Load()
			after := read()
			if !reflect.DeepEqual(before, after) {
				t.Errorf("the env declaration changed a legacy project's database:\nbefore %+v\nafter  %+v", before, after)
			}
			if n := declarationRenders.Load() - renders; n != 0 {
				t.Errorf("a project that declares no schema evaluated its envs %d time(s); want none", n)
			}
		})
	}
}

// A directory a person names (`forge db --dir`) is the migrations of the
// declared database that names it. With several declared, a directory none
// names is refused, never handed to the primary's tool (review B1 on #2211):
// that could run goose over a dbmate directory.
func TestMigrationsDirDatabase_NamedDir(t *testing.T) {
	main := config.DeclaredDatabase{Name: "default", Dialect: "postgres", Source: config.DatabaseSourceKCL,
		Migrations: config.MigrationsConfig{Dir: "db/migrations", Tool: "goose"}}
	events := config.DeclaredDatabase{Name: "events", Dialect: "postgres", Source: config.DatabaseSourceKCL,
		Migrations: config.MigrationsConfig{Dir: "db/events", Tool: "dbmate"}}
	dir := writeDBProject(t, "name: x\n", nil)
	abs := func(rel string) string { return filepath.Join(dir, filepath.FromSlash(rel)) }

	t.Run("several declared: each directory is its own database's", func(t *testing.T) {
		stubDeclarer(t, []config.DeclaredDatabase{main, events}, nil)
		for rel, tool := range map[string]string{"db/migrations": "goose", "db/events": "dbmate"} {
			db, err := config.MigrationsDirDatabase(dir, abs(rel))
			if err != nil || db.MigrationTool() != tool {
				t.Errorf("%s: tool %q err %v, want %s", rel, db.MigrationTool(), err, tool)
			}
			h, ok, err := migrationhook.ResolveForNamedDir(dir, abs(rel))
			if err != nil || !ok || h.Tool != tool {
				t.Errorf("%s: hook %q ok=%v err %v, want %s", rel, h.Tool, ok, err, tool)
			}
		}
	})

	t.Run("several declared: an unnamed directory is refused", func(t *testing.T) {
		stubDeclarer(t, []config.DeclaredDatabase{main, events}, nil)
		_, err := config.MigrationsDirDatabase(dir, abs("sql/other"))
		if !errors.Is(err, config.ErrMigrationsDirUnowned) || !strings.Contains(err.Error(), "--db") {
			t.Fatalf("err = %v, want ErrMigrationsDirUnowned pointing at --db", err)
		}
		if _, ok, err := migrationhook.ResolveForNamedDir(dir, abs("sql/other")); ok || !errors.Is(err, config.ErrMigrationsDirUnowned) {
			t.Fatalf("hook ok=%v err=%v, want a refusal and no hook", ok, err)
		}
		// The shadow-schema reader keeps its fallback: its directory comes
		// from the configuration, not from a person.
		if got := config.DatabaseForMigrationsDir(dir, abs("sql/other")).MigrationTool(); got != "goose" {
			t.Errorf("DatabaseForMigrationsDir fallback = %s, want the primary's goose", got)
		}
	})

	t.Run("one declared: any directory is the primary's", func(t *testing.T) {
		stubDeclarer(t, []config.DeclaredDatabase{events}, nil)
		db, err := config.MigrationsDirDatabase(dir, abs("sql/other"))
		if err != nil || db.MigrationTool() != "dbmate" {
			t.Fatalf("tool %q err %v, want the only database's dbmate", db.MigrationTool(), err)
		}
	})

	t.Run("none declared: forge.yaml's database", func(t *testing.T) {
		stubDeclarer(t, nil, nil)
		db, err := config.MigrationsDirDatabase(dir, abs("sql/other"))
		if err != nil || db.MigrationTool() != config.MigrationToolForge {
			t.Fatalf("tool %q err %v, want forge", db.MigrationTool(), err)
		}
	})

	// A declaration that cannot be read is not "none declared": which
	// databases exist is unknown, so a named directory gets no tool, not the
	// forge.yaml primary's (review R4.B1 on #2211).
	t.Run("unreadable declaration: refused", func(t *testing.T) {
		unreadable := errors.New("reading what deploy/kcl declares: env prod does not render")
		stubDeclarer(t, nil, unreadable)
		for _, rel := range []string{"db/migrations", "sql/other"} {
			if _, err := config.MigrationsDirDatabase(dir, abs(rel)); !errors.Is(err, unreadable) {
				t.Errorf("%s: err = %v, want the declaration's error", rel, err)
			}
			if _, ok, err := migrationhook.ResolveForNamedDir(dir, abs(rel)); ok || !errors.Is(err, unreadable) {
				t.Errorf("%s: hook ok=%v err=%v, want a refusal and no hook", rel, ok, err)
			}
		}
	})
}

// `forge db --dir` on a project whose declaration cannot be read refuses
// before any migrator runs, with or without --db (review R4.B1/B2 on #2211).
func TestDBSelect_DirWithUnreadableDeclaration(t *testing.T) {
	clearProjectDir(t)
	proj := writeDBProject(t, "name: x\nmodule_path: example.com/x\n", nil)
	elsewhere(t)
	if err := cmdutil.SetProjectDir(proj); err != nil {
		t.Fatal(err)
	}
	unreadable := errors.New("reading what deploy/kcl declares: env prod does not render")
	stubDeclarer(t, nil, unreadable)
	prev := dbFlagName
	t.Cleanup(func() { dbFlagName = prev })
	for _, name := range []string{"", "default"} {
		dbFlagName = name
		if _, err := selectMigrationsDir("db/migrations"); !errors.Is(err, unreadable) {
			t.Errorf("--db %q --dir db/migrations: err = %v, want the declaration's error", name, err)
		}
		if _, _, err := projectMigrationHook(filepath.Join(proj, "db", "migrations")); !errors.Is(err, unreadable) {
			t.Errorf("--db %q hook: err = %v, want the declaration's error", name, err)
		}
	}
}

// A declarer that answers an empty list is no declaration: the project keeps
// its forge.yaml database, and nothing indexes an empty slice (review B2 on
// #2211).
func TestUseDeclaredDatabases_EmptyDeclaration(t *testing.T) {
	stubDeclarer(t, []config.DeclaredDatabase{}, nil)
	dir := writeDBProject(t, "name: x\ndatabase:\n  migrations_dir: sql/migrations\n", nil)
	cfg, err := config.LoadProjectDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.UseDeclaredDatabases(); err != nil {
		t.Fatalf("UseDeclaredDatabases: %v", err)
	}
	if got := cfg.Database.MigrationsPath(); got != "sql/migrations" {
		t.Errorf("migrations = %s, want forge.yaml's sql/migrations", got)
	}
	if got, err := config.MigrationsDirDatabase(dir, filepath.Join(dir, "anything")); err != nil || got.MigrationsPath() != "sql/migrations" {
		t.Errorf("MigrationsDirDatabase = %s err %v, want forge.yaml's database", got.MigrationsPath(), err)
	}
}
