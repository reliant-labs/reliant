package serverkit

// compose.go — the INVARIANT half of a forge composition root.
//
// cmd/<bin>/cmd/serve.go is scaffolded ONCE and then owned by the project:
// it is the file where an application author changes the auth posture, the
// interceptor order, the payload caps, the CORS policy, the readiness set
// and the teardown. None of those choices belong to forge.
//
// Everything in THIS file is the other half — the steps that are identical
// in every forge project and that an author has no reason to re-derive:
// building the logger and the mux, running migrations before anything
// mounts, recording mounted services for the completeness gate, checking
// them against the project's own services, and adding an
// already-selected worker set. They live here so serve.go can be short
// enough to read, and so improvements to them reach every project without
// anyone re-scaffolding.
//
// The steps are deliberately SEPARATE and individually callable rather than
// one Compose(...) god-function: a composition root that needs to migrate
// after mounting, skip the readiness pool, or wrap the mux has to be able to
// reorder and replace pieces. A step that can only be used in one position
// is not a library, it is a template with extra syntax.

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"sort"

	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	"github.com/reliant-labs/forge/pkg/mountkit/inventory"
)

// Boot performs the two startup steps that must happen before a composition
// root can do anything else, and that every composition root does the same
// way: build the root logger from cfg and install it as slog's default, then
// create the mux services will mount onto.
//
// The logger is built ONCE, here, and threaded through mount time and run
// time by the caller so mount-time and run-time logs agree — serverkit.Run
// will build its own only if Server.Logger is left nil, which is exactly the
// case this exists to avoid.
//
// Boot does NOT normalize cfg. Normalization is the caller's step because
// the caller is what reads Config fields (the payload caps, the migration
// driver) before Run is entered; see Config.Normalize.
func Boot(cfg Config) (*slog.Logger, *http.ServeMux) {
	logger := newLogger(cfg)
	slog.SetDefault(logger)
	return logger, http.NewServeMux()
}

// AutoMigrate runs the project's migration function against a DEDICATED,
// short-lived connection, and is a no-op when cfg.AutoMigrate is false.
//
// The whole ceremony is here — dial, apply the pool tuning, migrate, close —
// because getting it wrong is silent in one direction and fatal in the
// other. Callers invoke it BEFORE constructing components and mounting
// services: a service that starts serving against a schema its migrations
// have not reached yet answers requests with errors that look like bugs.
// That ordering is the caller's to keep (it is a line position in serve.go),
// but everything inside it is not.
//
// The connection is closed on every path, including the migration-failure
// path, so a failed boot does not leak a pool into a crash loop. It is
// deliberately NOT the serving pool: migrations run once, at boot, and hold
// locks the serving pool must not be waiting behind.
//
// migrate is the project's own generated pkg/app.AutoMigrate. Passing it in
// (rather than serverkit reaching for it) is what keeps serverkit free of
// any compile-time dependency on the project.
func AutoMigrate(cfg Config, logger *slog.Logger, migrate func(*sql.DB, *slog.Logger) error) error {
	return AutoMigrateContext(context.Background(), cfg, logger, migrate)
}

// AutoMigrateContext is AutoMigrate with the boot context. The connect to the
// database RETRIES (capped exponential backoff with jitter, bounded by
// Config.DBConnectTimeout) instead of failing the process on the first
// refused dial: a database or pooler that comes up seconds after its clients
// is routine in a rollout, and a crash loop for it is a self-inflicted
// incident. Pass the ctx from StartBootProbes so /readyz names the database
// as pending while this waits. Only the migration itself is not retried — a
// failed or dirty migration is a human's problem, not a transient.
func AutoMigrateContext(ctx context.Context, cfg Config, logger *slog.Logger, migrate func(*sql.DB, *slog.Logger) error) error {
	if !cfg.AutoMigrate {
		return nil
	}
	if cfg.DatabaseURL == "" {
		return fmt.Errorf("auto-migrate is enabled but DatabaseURL is not set")
	}
	// Normalize defensively: DBDriver reaches sql.Open by NAME, and a zero
	// value there is `unknown driver ""` rather than the intended default.
	// Normalize is idempotent, so a caller that already normalized pays
	// nothing for the guarantee.
	cfg.Normalize()

	db, err := OpenDB(ctx, cfg.DBDriver, cfg.DatabaseURL, cfg.DBPoolTuning, cfg.ConnectPolicy(logger))
	if err != nil {
		return fmt.Errorf("failed to connect to database for migration: %w", err)
	}
	defer func() { _ = db.Close() }()

	if err := migrate(db, logger); err != nil {
		return fmt.Errorf("auto-migration failed: %w", err)
	}
	if logger != nil {
		logger.InfoContext(ctx, "db migration completed")
	}
	return nil
}

// schemaDependency is the name a process waiting on another's migrations
// lists as pending in the boot /readyz body.
const schemaDependency = "schema"

// AwaitMigrationsContext is AutoMigrateContext's counterpart for a process
// that leaves the schema to another (a root declared migrate=wait): it dials
// the database the same way — retrying within Config.DBConnectTimeout — and
// then blocks in await until the schema is current, with "schema" listed as
// pending in the boot /readyz body for the whole wait. Unlike
// AutoMigrateContext it does not consult Config.AutoMigrate: whether some
// process applies the migrations at boot or a deploy job does, a waiter
// serves only a current schema.
//
// await is migratekit.AwaitMigrations over the binary's embedded migrations;
// passing it in keeps golang-migrate out of serverkit's link graph, as
// AutoMigrateContext's migrate does. The connection is closed on every path.
func AwaitMigrationsContext(ctx context.Context, cfg Config, logger *slog.Logger, await func(context.Context, *sql.DB, *slog.Logger) error) error {
	if cfg.DatabaseURL == "" {
		return fmt.Errorf("the process waits for its database's migrations, but DatabaseURL is not set")
	}
	cfg.Normalize()

	db, err := OpenDB(ctx, cfg.DBDriver, cfg.DatabaseURL, cfg.DBPoolTuning, cfg.ConnectPolicy(logger))
	if err != nil {
		return fmt.Errorf("failed to connect to database to wait for its migrations: %w", err)
	}
	defer func() { _ = db.Close() }()

	boot := BootProbesFrom(ctx)
	boot.pend(schemaDependency)
	defer boot.clear(schemaDependency)
	if err := await(ctx, db, logger); err != nil {
		return fmt.Errorf("waiting for database migrations: %w", err)
	}
	return nil
}

// RecordMounted records every Connect path a typed mount returned, so the
// boot-time completeness gate can tell mounted services from declared ones.
//
// It is the loop that always follows a mount call. Keeping it as a step
// rather than folding it into the mount itself matters: a composition root
// mounts through its OWN typed Mount<Svc> methods (compile-time selection,
// no string lookup), so serverkit never sees the mount happen and cannot
// record the result on the caller's behalf.
func (s *Server) RecordMounted(connectPaths []string) {
	for _, p := range connectPaths {
		s.Mounted(p)
	}
}

// AddWorkers registers an already-selected worker set for supervision.
//
// Selection happened above serverkit — a per-worker subcommand hands a
// one-element slice, the all-services command hands its full set. serverkit
// supervises exactly what it is given and never filters by name.
func (s *Server) AddWorkers(workers []Worker) {
	for _, w := range workers {
		s.AddWorker(w)
	}
}

// RequireComplete is the boot-time mount-completeness gate, scoped to the
// project's OWN declared services: every service in inv must have been
// recorded as mounted, or boot fails. A composition root that deliberately
// mounts a SUBSET (a per-service subcommand) simply does not call this.
//
// The scope is exactly the inventory's services — never every service in
// their proto files, nor protoregistry.GlobalFiles. GlobalFiles also carries
// framework and transitively-imported services this binary does not serve
// (grpc.health.v1.Health, which serverkit mounts itself, and the OTel
// collector services the SDK pulls in). And a proto file can declare a
// service forge does not manage (`// forge:unmanaged`) beside one it does;
// the inventory leaves the first out, so a gate that widened to the whole
// file would refuse to boot over a service that is not forge's to mount.
//
// A service the inventory names but the binary never linked is a hard
// error: the inventory and the generated packages must agree, and a
// disagreement means the gate would be checking something other than what
// the binary serves.
func (s *Server) RequireComplete(inv []inventory.ComponentInfo) error {
	var missing []string
	for _, info := range inv {
		desc, err := protoregistry.GlobalFiles.FindDescriptorByName(protoreflect.FullName(info.ConnectPath))
		if err != nil {
			return fmt.Errorf("server completeness check: service %q (%s) not found in global proto registry: %w", info.Name, info.ConnectPath, err)
		}
		if _, ok := desc.(protoreflect.ServiceDescriptor); !ok {
			return fmt.Errorf("server completeness check: %q (%s) names a %T, not a service", info.Name, info.ConnectPath, desc)
		}
		if _, ok := s.mounted[info.ConnectPath]; !ok {
			missing = append(missing, info.ConnectPath)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	sort.Strings(missing)
	return fmt.Errorf(
		"server completeness check: %d declared proto service(s) have no mounted handler: %v — "+
			"the binary's inventory lists them but nothing mounted them (a forgotten Mount<Svc> call, "+
			"or a Mounted() recording omitted). Mount them, or run a subcommand that deliberately "+
			"serves a subset and does not ask for completeness",
		len(missing), missing,
	)
}
