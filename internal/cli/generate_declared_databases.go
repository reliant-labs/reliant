package cli

// `forge generate` and the databases the env declaration declares.
//
// The PRIMARY database (the one named `default`, else the only one) is what
// every generate step has always read as "the project's database": its
// migrations feed the shadow schema, its ORM package receives the projection
// the services' CRUD wiring, factories, mocks and seeds compile against.
// stepReadDeclaredDatabases lowers it onto the loaded config, so those steps
// read the declaration with no change of their own.
//
// Every OTHER declared database that names an ORM package (`orm = forge.
// DatabaseORM {package = ...}`) gets its own projection — entity structs,
// stores, the store aggregate — from its own migrations, applied by its own
// tool (stepSecondaryORMs). What a second database does NOT get yet: CRUD
// wiring, entity factories, frontend mocks and auto-seeding, which stay on the
// primary. `forge db seed apply --db <name>` seeds any of them.

import (
	"fmt"
	"path/filepath"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/generator"
)

// stepReadDeclaredDatabases lowers the env declaration's primary database onto
// ctx.Cfg (config.ProjectConfig.UseDeclaredDatabases). A project whose KCL
// declares no database schema evaluates nothing and is unchanged. It fails the
// run when the declaration cannot be read, when forge.yaml and the
// declaration both declare migrations, or when several databases are declared
// and none is the primary — every later step would otherwise act on a
// database the author did not mean.
func stepReadDeclaredDatabases(ctx *pipelineContext) error {
	if err := ctx.Cfg.UseDeclaredDatabases(); err != nil {
		return fmt.Errorf("read the project's databases: %w", err)
	}
	return nil
}

// secondaryORMDatabases lists the declared databases, other than the primary,
// that name an ORM package: the ones stepSecondaryORMs projects.
func secondaryORMDatabases(projectDir string) ([]config.DeclaredDatabase, error) {
	dbs, err := config.ProjectDatabases(projectDir)
	if err != nil {
		return nil, err
	}
	if len(dbs) < 2 {
		return nil, nil
	}
	primary, _ := config.PrimaryDatabase(dbs)
	var out []config.DeclaredDatabase
	for _, d := range dbs {
		if d.Name == primary.Name || d.ORM.Package == "" {
			continue
		}
		out = append(out, d)
	}
	return out, nil
}

// stepSecondaryORMs projects the ORM of every declared database other than the
// primary that names an ORM package. Each projection reads its own database's
// applied schema; no service's CRUD rpcs join against it, so every table
// projects under its own name.
func stepSecondaryORMs(ctx *pipelineContext) error {
	dbs, err := secondaryORMDatabases(ctx.ProjectDir)
	if err != nil || len(dbs) == 0 {
		return err
	}
	for _, d := range dbs {
		migDir := filepath.Join(ctx.ProjectDir, filepath.FromSlash(d.MigrationsPath()))
		entities, err := codegen.BuildORMEntitiesFrom(ctx.ProjectDir, migDir, d.ORM.PackageDir(), nil)
		if err != nil {
			return ctx.warnOrFail(fmt.Sprintf("ORM generation for database %s", d.Name), err)
		}
		if len(entities) == 0 {
			continue
		}
		if err := generator.GeneratePlanORM(ctx.ProjectDir, d.ORM, codegen.EntityDefsToPlanEntities(entities), ctx.Checksums); err != nil {
			return ctx.warnOrFail(fmt.Sprintf("ORM generation for database %s", d.Name), err)
		}
		fmt.Printf("  ✅ Generated %s/ (%d entity ORM files, database %s)\n", d.ORM.PackageDir(), len(entities), d.Name)
	}
	return nil
}
