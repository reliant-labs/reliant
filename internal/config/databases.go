package config

// THE PROJECT'S DATABASES: which databases a project owns the schema of, and
// where each one's migrations, ORM and migration-safety settings come from.
//
// A database's schema is declared in ONE of two places:
//
//   - the env declaration (KCL, `deploy/kcl/<env>/main.k`): a
//     forge.ManagedDatabase, forge.ExternalDatabase or schema-only
//     forge.Database that sets `migrations`. Several may (a monorepo), each
//     with its own directory and ORM package. A database's schema is a fact of
//     the project, not of one env, so every env's declarations are read and
//     unioned, and two envs that declare one database differently are an
//     error. KCL is the long-term home, and it wins.
//   - forge.yaml's legacy `database:` block (`migrations_dir`, `migrations:`,
//     `orm:`, `migration_safety:`), or nothing at all: ONE database, named
//     `default`, with forge's defaults. It keeps working unchanged.
//
// config cannot evaluate KCL (internal/cli owns the render), so the env
// declaration reaches it through a registered DatabaseDeclarer. With none
// registered — a codegen unit test, a binary that never imports the cli —
// every project is its forge.yaml, exactly as before.
//
// Declaring migrations in both places is a load error: the two could name
// different directories, and every reader would act on one while the author
// edits the other. forge.yaml's `orm:` and `migration_safety:` describe the
// primary database; they fill what the primary's declaration leaves unset.

import (
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
)

// DefaultDatabaseName is the name of the project's primary database: the one
// forge.yaml's `database:` block describes, and the one `forge db` acts on
// without `--db` when the env declaration names several.
const DefaultDatabaseName = "default"

// DialectPostgres is the SQL dialect forge supports today.
const DialectPostgres = "postgres"

// Where a DeclaredDatabase was declared.
const (
	// DatabaseSourceKCL is the env declaration (KCL, or a document of its
	// render output).
	DatabaseSourceKCL = "kcl"
	// DatabaseSourceForgeYAML is forge.yaml's legacy `database:` block, or
	// its absence: the defaults.
	DatabaseSourceForgeYAML = "forge.yaml"
)

// DeclaredDatabase is one database whose schema the project owns: its
// migrations (and the tool that applies them), where its ORM lands and how
// its migrations are linted.
type DeclaredDatabase struct {
	// Name names it to `forge db --db` and prefixes its lint findings when a
	// project declares several.
	Name string
	// Dialect is its SQL dialect: postgres.
	Dialect string
	// Migrations is where its migrations live and which tool applies them.
	// Driver is Migrations.Tool.
	Migrations MigrationsConfig
	// ORM places its generated data layer.
	ORM ORMConfig
	// MigrationSafety holds its `forge lint --migration-safety` dials.
	MigrationSafety MigrationSafetyConfig
	// Source is DatabaseSourceKCL or DatabaseSourceForgeYAML.
	Source string
	// Envs are the envs whose declaration names it (KCL only), sorted.
	Envs []string
}

// MigrationsPath is the database's project-relative, slash-separated
// migrations directory.
func (d DeclaredDatabase) MigrationsPath() string {
	return DatabaseConfig{Migrations: d.Migrations}.MigrationsPath()
}

// Config lowers the database onto base, forge.yaml's `database:` block: the
// DatabaseConfig every reader of a project's database already consumes. The
// declaration's migrations replace base's; its ORM and migration safety
// replace base's when it declares them; everything else (postgres_version,
// the derived driver) is base's.
func (d DeclaredDatabase) Config(base DatabaseConfig) DatabaseConfig {
	out := base
	out.MigrationsDir = ""
	out.Migrations = d.Migrations
	if strings.TrimSpace(d.ORM.Package) != "" {
		out.ORM = d.ORM
	}
	if !reflect.DeepEqual(d.MigrationSafety, MigrationSafetyConfig{}) {
		out.MigrationSafety = d.MigrationSafety
	}
	return out
}

// DatabaseDeclarer returns the databases the env declaration of the project
// at projectDir declares a schema for (DatabaseSourceKCL), unioned across its
// envs, and nil when it declares none. internal/cli registers the one that
// renders the project's envs; it caches per project, since the readers that
// call it run many times in one command.
type DatabaseDeclarer func(projectDir string) ([]DeclaredDatabase, error)

var databaseDeclarer struct {
	mu sync.RWMutex
	fn DatabaseDeclarer
}

// RegisterDatabaseDeclarer installs the env declaration's database source.
// It returns the previously registered one, so a test can restore it.
func RegisterDatabaseDeclarer(fn DatabaseDeclarer) (previous DatabaseDeclarer) {
	databaseDeclarer.mu.Lock()
	defer databaseDeclarer.mu.Unlock()
	previous, databaseDeclarer.fn = databaseDeclarer.fn, fn
	return previous
}

func declaredDatabases(projectDir string) ([]DeclaredDatabase, error) {
	databaseDeclarer.mu.RLock()
	fn := databaseDeclarer.fn
	databaseDeclarer.mu.RUnlock()
	if fn == nil || projectDir == "" {
		return nil, nil
	}
	return fn(projectDir)
}

// ErrDatabaseDeclaredTwice is the load error for migrations both forge.yaml
// and the env declaration declare.
var ErrDatabaseDeclaredTwice = errors.New("database migrations declared twice")

// DeclaresDatabase reports whether forge.yaml's block declares anything about
// the project's database: its migrations, their tool, its ORM package or its
// migration-safety dials. A project whose block declares none has its
// database derived (db/migrations exists).
func (c DatabaseConfig) DeclaresDatabase() bool {
	return c.declaresMigrations() || strings.TrimSpace(c.ORM.Package) != "" ||
		!reflect.DeepEqual(c.MigrationSafety, MigrationSafetyConfig{})
}

// declaresMigrations reports whether forge.yaml's block declares where the
// migrations are or what applies them.
func (c DatabaseConfig) declaresMigrations() bool {
	return strings.TrimSpace(c.MigrationsDir) != "" || !reflect.DeepEqual(c.Migrations, MigrationsConfig{})
}

// legacyDatabase is forge.yaml's `database:` block (or its absence) as the
// project's one database.
func legacyDatabase(db DatabaseConfig) DeclaredDatabase {
	m := db.Migrations
	m.Dir = db.MigrationsPath()
	return DeclaredDatabase{
		Name:            DefaultDatabaseName,
		Dialect:         DialectPostgres,
		Migrations:      m,
		ORM:             db.ORM,
		MigrationSafety: db.MigrationSafety,
		Source:          DatabaseSourceForgeYAML,
	}
}

// ProjectDatabases returns every database whose schema the project at
// projectDir declares, in declaration order (sorted by name for KCL): the env
// declaration's when it declares any, else forge.yaml's one. It errors when
// the env declaration cannot be read, or when both declare migrations.
func ProjectDatabases(projectDir string) ([]DeclaredDatabase, error) {
	legacy, _ := readYAMLDatabaseBlock(projectDir)
	declared, err := declaredDatabases(projectDir)
	if err != nil {
		return nil, err
	}
	if len(declared) == 0 {
		return []DeclaredDatabase{legacyDatabase(legacy)}, nil
	}
	if legacy.declaresMigrations() {
		names := make([]string, 0, len(declared))
		for _, d := range declared {
			names = append(names, d.Name)
		}
		return nil, fmt.Errorf("%w: forge.yaml declares `database.migrations` / `database.migrations_dir`, and the env declaration (deploy/kcl) declares migrations for %s\n"+
			"fix: keep one. The env declaration is the long-term home: delete forge.yaml's database.migrations and database.migrations_dir, "+
			"or drop `migrations` from the KCL databases (forge skill load db)", ErrDatabaseDeclaredTwice, strings.Join(names, ", "))
	}
	out := append([]DeclaredDatabase(nil), declared...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	// forge.yaml's orm and migration_safety describe the primary: they fill
	// what its declaration leaves unset.
	if i, ok := primaryIndex(out); ok {
		if strings.TrimSpace(out[i].ORM.Package) == "" {
			out[i].ORM = legacy.ORM
		}
		if reflect.DeepEqual(out[i].MigrationSafety, MigrationSafetyConfig{}) {
			out[i].MigrationSafety = legacy.MigrationSafety
		}
	}
	return out, nil
}

// primaryIndex is the index of the primary database in dbs: the one named
// DefaultDatabaseName, else the only one.
func primaryIndex(dbs []DeclaredDatabase) (int, bool) {
	for i, d := range dbs {
		if d.Name == DefaultDatabaseName {
			return i, true
		}
	}
	if len(dbs) == 1 {
		return 0, true
	}
	return 0, false
}

// PrimaryDatabase returns the project's primary database among dbs: the one
// named `default`, else the only one. ok is false when there are several and
// none is named `default`.
func PrimaryDatabase(dbs []DeclaredDatabase) (DeclaredDatabase, bool) {
	i, ok := primaryIndex(dbs)
	if !ok {
		return DeclaredDatabase{}, false
	}
	return dbs[i], true
}

// SelectDatabase picks the database name names among dbs, or the primary when
// name is empty. It refuses an unknown name, and an empty one when there are
// several databases and none is named `default`.
func SelectDatabase(dbs []DeclaredDatabase, name string) (DeclaredDatabase, error) {
	names := make([]string, 0, len(dbs))
	for _, d := range dbs {
		names = append(names, d.Name)
	}
	name = strings.TrimSpace(name)
	if name == "" {
		if d, ok := PrimaryDatabase(dbs); ok {
			return d, nil
		}
		return DeclaredDatabase{}, fmt.Errorf("the project declares %d databases (%s) and none is named %q: pass --db <name>, or name the primary one %q",
			len(dbs), strings.Join(names, ", "), DefaultDatabaseName, DefaultDatabaseName)
	}
	for _, d := range dbs {
		if d.Name == name {
			return d, nil
		}
	}
	return DeclaredDatabase{}, fmt.Errorf("no database named %q: the project declares %s", name, strings.Join(names, ", "))
}

// DatabaseFor returns the DatabaseConfig of the database name names in the
// project at projectDir (the primary when name is empty), lowered onto
// forge.yaml's block: what a reader of one database consumes.
func DatabaseFor(projectDir, name string) (DatabaseConfig, DeclaredDatabase, error) {
	dbs, err := ProjectDatabases(projectDir)
	if err != nil {
		return DatabaseConfig{}, DeclaredDatabase{}, err
	}
	d, err := SelectDatabase(dbs, name)
	if err != nil {
		return DatabaseConfig{}, DeclaredDatabase{}, err
	}
	legacy, _ := readYAMLDatabaseBlock(projectDir)
	if d.Source == DatabaseSourceForgeYAML {
		return legacy, d, nil
	}
	return d.Config(legacy), d, nil
}

// DatabaseForMigrationsDir returns the DatabaseConfig of the declared database
// whose migrations live in absDir (an absolute directory), and the primary's
// when none does. It is how a reader that has a migrations directory and no
// database name — the shadow schema — finds the tool that applies it.
func DatabaseForMigrationsDir(projectDir, absDir string) DatabaseConfig {
	primary, _ := readDatabaseBlock(projectDir)
	dbs, err := ProjectDatabases(projectDir)
	if err != nil || len(dbs) == 0 || dbs[0].Source != DatabaseSourceKCL {
		return primary
	}
	legacy, _ := readYAMLDatabaseBlock(projectDir)
	for _, d := range dbs {
		if sameDir(projectDir, d.MigrationsPath(), absDir) {
			return d.Config(legacy)
		}
	}
	return primary
}

// MigrationsDirOwner returns the database the env declaration declares whose
// migrations live in absDir, and false when none does (or none is declared).
func MigrationsDirOwner(projectDir, absDir string) (DeclaredDatabase, bool) {
	dbs, err := ProjectDatabases(projectDir)
	if err != nil || len(dbs) == 0 || dbs[0].Source != DatabaseSourceKCL {
		return DeclaredDatabase{}, false
	}
	for _, d := range dbs {
		if sameDir(projectDir, d.MigrationsPath(), absDir) {
			return d, true
		}
	}
	return DeclaredDatabase{}, false
}

// ErrMigrationsDirUnowned is MigrationsDirDatabase's error for a directory no
// declared database names while several are declared: which tool applies it
// cannot be told, and guessing the primary's could run the wrong migrator.
var ErrMigrationsDirUnowned = errors.New("migrations directory belongs to no declared database")

// MigrationsDirDatabase is DatabaseForMigrationsDir for a directory a person
// named (`forge db --dir`). A directory a declared database names is that
// database's; with one database (or none declared in the env declaration) any
// directory is the primary's, as before databases were declared. With several
// declared, a directory none of them names is refused rather than handed to
// the primary's tool. A declaration that cannot be read (an env that does not
// render, envs that disagree, migrations declared twice) is an error too:
// which databases exist is unknown, so no tool can be chosen for the
// directory. Only a project whose envs declare no database schema keeps the
// forge.yaml primary.
func MigrationsDirDatabase(projectDir, absDir string) (DatabaseConfig, error) {
	primary, _ := readDatabaseBlock(projectDir)
	dbs, err := ProjectDatabases(projectDir)
	if err != nil {
		return DatabaseConfig{}, err
	}
	if len(dbs) == 0 || dbs[0].Source != DatabaseSourceKCL {
		return primary, nil
	}
	legacy, _ := readYAMLDatabaseBlock(projectDir)
	names := make([]string, 0, len(dbs))
	for _, d := range dbs {
		if sameDir(projectDir, d.MigrationsPath(), absDir) {
			return d.Config(legacy), nil
		}
		names = append(names, d.Name+" ("+d.MigrationsPath()+")")
	}
	if len(dbs) == 1 {
		return dbs[0].Config(legacy), nil
	}
	return DatabaseConfig{}, fmt.Errorf("%w: %s is not the migrations directory of any database the env declaration declares: %s\n"+
		"fix: pass --db <name> instead of --dir, or point --dir at one of those directories", ErrMigrationsDirUnowned, absDir, strings.Join(names, ", "))
}

// UseDeclaredDatabases lowers the env declaration's primary database onto
// c.Database, and re-derives the driver from the directory it names, so every
// reader of the loaded config sees the database the project declares. A
// command that reads the project's database calls it once after loading;
// config.Load does not, because evaluating the env declaration is a render
// of every env, which a command that never touches the database must not pay
// for (or fail on).
//
// It errors when the declaration cannot be read, when forge.yaml and the
// declaration both declare migrations, or when the declaration names several
// databases and none is the primary.
func (c *ProjectConfig) UseDeclaredDatabases() error {
	if c == nil || c.projectDir == "" {
		return nil
	}
	dbs, err := ProjectDatabases(c.projectDir)
	if err != nil {
		return err
	}
	// ProjectDatabases falls back to forge.yaml's database when nothing is
	// declared, so dbs is never empty; the length check keeps an empty result
	// from a future declarer a no-op rather than a panic.
	if len(dbs) == 0 || dbs[0].Source != DatabaseSourceKCL {
		return nil
	}
	d, primary := PrimaryDatabase(dbs)
	if primary {
		c.Database = d.Config(c.Database)
	}
	// The derived driver, and the features that read it, follow the
	// declared databases: the project has a database when any declared
	// one's migrations directory exists.
	c.Database.Driver = "none"
	for _, db := range dbs {
		if dirExists(filepath.Join(c.projectDir, filepath.FromSlash(db.MigrationsPath()))) {
			c.Database.Driver = DialectPostgres
			break
		}
	}
	c.Features.derived = followPinnedRequirements(DeriveFeatureDefaults(c), c.Features, c)
	if !primary {
		_, err := SelectDatabase(dbs, "")
		return err
	}
	return nil
}

// PrimaryDatabase is c.Database with the env declaration's primary database
// lowered onto it when the project declares one: the primary database every
// reader of a loaded config consumes. It never errors — a declaration that
// cannot be read, or names no primary, leaves c.Database as loaded, and the
// command that reads databases reports why (UseDeclaredDatabases,
// ProjectDatabases).
func (c *ProjectConfig) PrimaryDatabase() DatabaseConfig {
	if c == nil {
		return DatabaseConfig{}
	}
	if c.projectDir == "" {
		return c.Database
	}
	dbs, err := ProjectDatabases(c.projectDir)
	if err != nil || len(dbs) == 0 || dbs[0].Source != DatabaseSourceKCL {
		return c.Database
	}
	d, ok := PrimaryDatabase(dbs)
	if !ok {
		return c.Database
	}
	return d.Config(c.Database)
}

// ProjectDir is the directory the config was loaded from, "" for a config
// built in memory.
func (c *ProjectConfig) ProjectDir() string {
	if c == nil {
		return ""
	}
	return c.projectDir
}
