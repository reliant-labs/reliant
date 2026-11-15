package cli

// The env declaration's databases, as config's DatabaseDeclarer.
//
// A database's schema — its dialect, migrations, ORM package and
// migration-safety dials — may be declared on any database of an env
// (forge.ManagedDatabase, forge.ExternalDatabase, or a schema-only
// forge.Database). forge's readers (`forge lint --migration-safety`, `forge db
// … --db`, the ORM projection, the seeder) need the PROJECT's databases, not
// one env's, so this reads every env's declaration and unions them:
//
//   - every env is read, and a database declared in several must declare the
//     same schema in each (a release is built once and promoted, so its
//     migrations cannot differ by env); absence in an env is not a
//     disagreement;
//   - two databases never share a migrations directory or an ORM package,
//     across envs as within one.
//
// Reading the declaration means evaluating every env, which costs seconds. It
// is paid only by a project whose KCL can declare a schema at all
// (kclMayDeclareDatabaseSchemas: a `migrations =`, `DatabaseMigrations` or
// `schema_only_databases` anywhere under deploy/kcl), so a project that
// declares none — every project that predates this — evaluates nothing and
// reads forge.yaml exactly as before. A declaring project pays it once per
// process and state of its deploy tree (the result is cached on the .k
// files' sizes and modification times), and inside `forge generate` the
// evaluations share the run's render memo.

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/reliant-labs/forge/internal/config"
)

// DatabaseSchemaEntity is the schema half of a database declaration
// (forge.Database): present on a database only when it declares migrations.
type DatabaseSchemaEntity struct {
	// Dialect is the database's SQL dialect: postgres.
	Dialect string `json:"dialect,omitempty"`
	// Migrations is where its migrations live and which tool applies them.
	Migrations *DatabaseMigrationsEntity `json:"migrations,omitempty"`
	// ORM places its generated data layer.
	ORM *DatabaseORMEntity `json:"orm,omitempty"`
	// MigrationSafety holds its `forge lint --migration-safety` dials.
	MigrationSafety *DatabaseMigrationSafetyEntity `json:"migration_safety,omitempty"`
}

// DatabaseMigrationsEntity is forge.DatabaseMigrations: forge.yaml's
// `database.migrations:` block, declared per database (Driver is its `tool`).
type DatabaseMigrationsEntity struct {
	// Dir is the repository-relative directory holding the migrations.
	Dir string `json:"dir"`
	// Driver is the tool that applies them: forge, golang-migrate, goose,
	// atlas, flyway, dbmate or custom.
	Driver string `json:"driver"`
	// Apply, Status and Check are argv (no shell) overriding a built-in
	// driver's; see config.MigrationsConfig.
	Apply  []string `json:"apply,omitempty"`
	Status []string `json:"status,omitempty"`
	Check  []string `json:"check,omitempty"`
}

// DatabaseORMEntity is forge.DatabaseORM: the repository-relative package
// directory the database's ORM projects into.
type DatabaseORMEntity struct {
	Package string `json:"package"`
}

// DatabaseMigrationSafetyEntity is forge.DatabaseMigrationSafety: the
// migration-safety lint's dials for one database.
type DatabaseMigrationSafetyEntity struct {
	Enabled                  *bool  `json:"enabled,omitempty"`
	UnsafeAddColumn          string `json:"unsafe_add_column,omitempty"`
	DestructiveChange        string `json:"destructive_change,omitempty"`
	VolatileDefault          string `json:"volatile_default,omitempty"`
	RequireCompatDeclaration bool   `json:"require_compat_declaration,omitempty"`
}

// SchemaOnlyDatabaseEntity is one database declared for its schema only (a
// bare forge.Database on Bundle.schema_only_databases): nothing runs or is
// reached for it.
type SchemaOnlyDatabaseEntity struct {
	Name string `json:"name"`
	DatabaseSchemaEntity
}

// declaredSchema is one database's schema as one env declares it.
type declaredSchema struct {
	name   string
	schema DatabaseSchemaEntity
}

// schemasOf lists the databases e declares a schema for, of every kind.
func schemasOf(e *KCLEntities) []declaredSchema {
	if e == nil {
		return nil
	}
	var out []declaredSchema
	for _, d := range e.Databases {
		if d.Migrations != nil {
			out = append(out, declaredSchema{d.Name, d.DatabaseSchemaEntity})
		}
	}
	for _, x := range e.ExternalDatabases {
		if x.Migrations != nil {
			out = append(out, declaredSchema{x.Name, x.DatabaseSchemaEntity})
		}
	}
	for _, s := range e.SchemaOnlyDatabases {
		if s.Migrations != nil {
			out = append(out, declaredSchema{s.Name, s.DatabaseSchemaEntity})
		}
	}
	return out
}

// normalizedSchema is s with its paths cleaned and its defaults spelled, so
// two envs that declare one schema in two spellings compare equal.
func normalizedSchema(s DatabaseSchemaEntity) DatabaseSchemaEntity {
	out := s
	if out.Dialect == "" {
		out.Dialect = config.DialectPostgres
	}
	if s.Migrations != nil {
		m := *s.Migrations
		m.Dir = cleanRepoPath(m.Dir)
		if m.Dir == "" {
			m.Dir = config.DefaultMigrationsDir
		}
		if m.Driver == "" {
			m.Driver = config.MigrationToolForge
		}
		out.Migrations = &m
	}
	if s.ORM != nil {
		o := DatabaseORMEntity{Package: cleanRepoPath(s.ORM.Package)}
		out.ORM = &o
	}
	if s.MigrationSafety != nil {
		ms := *s.MigrationSafety
		if ms.Enabled != nil && *ms.Enabled {
			ms.Enabled = nil // enabled is the default
		}
		out.MigrationSafety = &ms
	}
	return out
}

// cleanRepoPath is a repository-relative path in the slash-separated form
// every reader compares: "./db/migrations/" and "db/migrations" are one.
func cleanRepoPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	return path.Clean(filepath.ToSlash(p))
}

// declaredDatabase lowers one database's schema into config's model.
func (s declaredSchema) declaredDatabase(envs []string) config.DeclaredDatabase {
	n := normalizedSchema(s.schema)
	d := config.DeclaredDatabase{
		Name:    s.name,
		Dialect: n.Dialect,
		Source:  config.DatabaseSourceKCL,
		Envs:    envs,
	}
	if m := n.Migrations; m != nil {
		d.Migrations = config.MigrationsConfig{Dir: m.Dir, Tool: m.Driver, Apply: m.Apply, Status: m.Status, Check: m.Check}
	}
	if o := n.ORM; o != nil {
		d.ORM = config.ORMConfig{Package: o.Package}
	}
	if ms := n.MigrationSafety; ms != nil {
		d.MigrationSafety = config.MigrationSafetyConfig{
			Enabled:                  ms.Enabled,
			UnsafeAddColumn:          ms.UnsafeAddColumn,
			DestructiveChange:        ms.DestructiveChange,
			VolatileDefault:          ms.VolatileDefault,
			RequireCompatDeclaration: ms.RequireCompatDeclaration,
		}
	}
	return d
}

// unionDeclaredDatabases unions the databases every env declares a schema
// for (perEnv: env → its declaration), sorted by name. It is the pure half of
// the declarer, and the seam any other source of an env's declaration (an env
// document) feeds.
func unionDeclaredDatabases(perEnv map[string]*KCLEntities) ([]config.DeclaredDatabase, error) {
	envs := make([]string, 0, len(perEnv))
	for env := range perEnv {
		envs = append(envs, env)
	}
	sort.Strings(envs)

	type seen struct {
		schema declaredSchema
		norm   DatabaseSchemaEntity
		envs   []string
	}
	byName := map[string]*seen{}
	var order []string
	for _, env := range envs {
		for _, s := range schemasOf(perEnv[env]) {
			n := normalizedSchema(s.schema)
			if prev, ok := byName[s.name]; ok {
				if !reflect.DeepEqual(prev.norm, n) {
					return nil, fmt.Errorf("database %q declares a different schema in env %s than in env %s (dialect, migrations, orm or migration_safety)\n"+
						"fix: a database's schema is a fact of the project, not of one env — its release is built once and promoted — so declare it identically in every env (a shared deploy/kcl module both import keeps them so)",
						s.name, env, strings.Join(prev.envs, ", "))
				}
				prev.envs = append(prev.envs, env)
				continue
			}
			byName[s.name] = &seen{schema: s, norm: n, envs: []string{env}}
			order = append(order, s.name)
		}
	}
	sort.Strings(order)

	out := make([]config.DeclaredDatabase, 0, len(order))
	dirs := map[string]string{}
	orms := map[string]string{}
	for _, name := range order {
		d := byName[name].schema.declaredDatabase(byName[name].envs)
		if other, ok := dirs[d.Migrations.Dir]; ok {
			return nil, fmt.Errorf("databases %q and %q both declare migrations dir %s: each database's migrations live in a directory of their own", other, name, d.Migrations.Dir)
		}
		dirs[d.Migrations.Dir] = name
		if pkg := d.ORM.Package; pkg != "" {
			if other, ok := orms[pkg]; ok {
				return nil, fmt.Errorf("databases %q and %q both declare ORM package %s: each database's generated data layer lands in a package of its own", other, name, pkg)
			}
			orms[pkg] = name
		}
		out = append(out, d)
	}
	return out, nil
}

// kclSchemaDeclarationRE matches KCL source that can declare a database's
// schema. It is a cheap pre-check, not the parse: a false positive costs an
// evaluation; a declaration it would miss would have to spell none of these
// names in the project's own KCL.
var kclSchemaDeclarationRE = regexp.MustCompile(`(?m)^[^#\n]*(\bmigrations\s*=|\bDatabaseMigrations\b|\bschema_only_databases\b)`)

// kclMayDeclareDatabaseSchemas reports whether any .k file under
// projectDir/deploy/kcl can declare a database schema.
func kclMayDeclareDatabaseSchemas(projectDir string) bool {
	return kclSourceMatches(projectDir, kclSchemaDeclarationRE)
}

// kclSourceMatches reports whether any .k file under projectDir/deploy/kcl
// matches re.
func kclSourceMatches(projectDir string, re *regexp.Regexp) bool {
	root := filepath.Join(projectDir, "deploy", "kcl")
	found := false
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || found {
			return fs.SkipDir
		}
		if d.IsDir() || filepath.Ext(p) != ".k" {
			return nil
		}
		b, rerr := os.ReadFile(p) //nolint:gosec // a .k file of the project's own deploy tree
		if rerr == nil && re.Match(b) {
			found = true
			return fs.SkipAll
		}
		return nil
	})
	return found
}

type envDeclarationsEntry struct {
	once   sync.Once
	perEnv map[string]*KCLEntities
	err    error
}

// envDeclarationsCache memoizes every env's render per project and per state
// of its deploy tree (kclTreeFingerprint) for the life of the process: the
// readers call it many times in one command, and a long-lived process (the
// MCP server) must still see an edit. The databases and the APIs read the
// same renders.
var envDeclarationsCache sync.Map // string → *envDeclarationsEntry

// kclTreeFingerprint names the state of every .k file under
// projectDir/deploy/kcl by path, size and modification time — a stat per
// file, no read. ok is false when there is none.
func kclTreeFingerprint(projectDir string) (fp string, ok bool) {
	var b strings.Builder
	_ = filepath.WalkDir(filepath.Join(projectDir, "deploy", "kcl"), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return fs.SkipDir
		}
		if d.IsDir() || filepath.Ext(p) != ".k" {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		ok = true
		fmt.Fprintf(&b, "%s\x00%d\x00%d\n", p, info.Size(), info.ModTime().UnixNano())
		return nil
	})
	return b.String(), ok
}

// declaredEnvs returns every env's declaration of the project at projectDir
// (env → entities), evaluated once per state of its deploy tree, or nil when
// may — the caller's cheap pre-check of the KCL source — says nothing it
// reads can be declared there.
func declaredEnvs(projectDir string, may func(string) bool) (map[string]*KCLEntities, error) {
	abs, err := filepath.Abs(projectDir)
	if err != nil {
		abs = projectDir
	}
	fp, ok := kclTreeFingerprint(abs)
	if !ok || !may(abs) {
		return nil, nil
	}
	v, _ := envDeclarationsCache.LoadOrStore(abs+"\x00"+fp, &envDeclarationsEntry{})
	e := v.(*envDeclarationsEntry)
	e.once.Do(func() { e.perEnv, e.err = renderEnvDeclarations(abs) })
	return e.perEnv, e.err
}

// declaredDatabasesOfEnvs is config's DatabaseDeclarer: the databases the
// project's envs declare a schema for, unioned (unionDeclaredDatabases).
func declaredDatabasesOfEnvs(projectDir string) ([]config.DeclaredDatabase, error) {
	perEnv, err := declaredEnvs(projectDir, kclMayDeclareDatabaseSchemas)
	if err != nil || perEnv == nil {
		return nil, err
	}
	return unionDeclaredDatabases(perEnv)
}

// renderEnvDeclarations evaluates every env of the project (renderEnvsPure:
// concurrently, side-effect free, memoized inside a generate run). An env
// that does not render is an error: its KCL may declare what the caller
// reads, and a reader that silently skipped it would act on the wrong
// declaration.
func renderEnvDeclarations(projectDir string) (map[string]*KCLEntities, error) {
	envs, err := ListEnvs(projectDir)
	if err != nil || len(envs) == 0 {
		return nil, err
	}
	perEnv := make(map[string]*KCLEntities, len(envs))
	for _, r := range renderEnvsPure(context.Background(), projectDir, envs) {
		if r.err != nil {
			return nil, fmt.Errorf("reading what deploy/kcl declares: env %s does not render: %w", r.env, r.err)
		}
		perEnv[r.env] = r.entities
	}
	return perEnv, nil
}

func init() {
	config.RegisterDatabaseDeclarer(declaredDatabasesOfEnvs)
}
