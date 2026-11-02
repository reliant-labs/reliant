package cli

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/jinzhu/inflection"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/database"
	"github.com/reliant-labs/forge/internal/devpg"
	"github.com/reliant-labs/forge/internal/hostinfra"
	"github.com/reliant-labs/forge/internal/projectstore"
	"github.com/reliant-labs/forge/pkg/pgtest"
	"github.com/reliant-labs/forge/pkg/seedplan"
)

// ensureDevDatabase creates the dev database the host services are about to
// dial when it does not already exist — so a `forge env up` against a freshly
// scaffolded project boots alive. It is the runtime counterpart to forge's
// generate-time shadow DB, which pgtest already ensure-creates on the fly:
// the scaffolded dev DSN (postgres://…:5434/<project>) names a database
// nothing has issued CREATE DATABASE for, so the app's first boot would die
// with `FATAL: database "<project>" does not exist` before AUTO_MIGRATE could
// apply the schema.
//
// Dev-only (seedTargetIsDev — the same fail-closed classifier the auto-seed
// gate reads) and only when a DSN is actually resolved. The maintenance
// connection failing is a HARD error: the app cannot boot without the DB
// server, so `forge env up` says so loudly here rather than let the app fail
// later with an opaque connect error.
func ensureDevDatabase(cfg *config.ProjectConfig, entities *KCLEntities, env string) error {
	dev, err := seedTargetIsDev(env)
	if err != nil || !dev {
		return nil
	}
	primary := resolveSeedDSN(entities, cfg, env)
	if primary == "" {
		return nil
	}
	// Reconcile BEFORE the first write. This is the last point at which
	// forge can tell the DSN apart from the database it is supposed to
	// name: the very next call issues CREATE DATABASE against whatever is
	// listening at the DSN's coordinates, and after that the project's
	// tables and seed rows are in there regardless of whose postgres it is.
	//
	// A scaffolded project's DSN is derived from POSTGRES_PORT (see
	// codegen.devDatabaseDSN), but it is derived ONCE, at scaffold time.
	// Run the same project later under a different POSTGRES_PORT and
	// compose moves postgres while the committed DSN stays put — the exact
	// divergence that had projects creating their schema inside another
	// stack's database while their own postgres sat empty, with `forge env up`
	// reporting success throughout. Refuse loudly instead.
	//
	// Only the PRIMARY DSN is reconciled. It is the one the seed hook and
	// the discovery facts write through, and the one whose port could have
	// drifted from compose; the additional DSNs below are collected from
	// the KCL that this same render produced, so they cannot disagree with
	// it about which server is the project's.
	if err := reconcileDevDatabasePort(primary, entities); err != nil {
		return err
	}
	for _, dsn := range devDatabaseDSNs(entities, primary) {
		if err := pgtest.EnsureDatabase(dsn); err != nil {
			return fmt.Errorf("ensure dev database %q: %w", devpg.DatabaseOf(dsn), err)
		}
	}
	return nil
}

// ensureDevDatabaseHook is ensureDevDatabase as the deploy dispatch's
// before-clusters hook (dispatchDeployGroupsBeforeClusters): the dev
// databases are created once the infrastructure groups have brought their
// server up and before the first cluster workload is applied.
//
// WHY THE DEPLOY, NOT ONLY THE HOST PHASE. A dev env may run a workload
// IN-CLUSTER against a database on the host's docker-compose postgres (its DSN
// names host.k3d.internal, which devDatabaseDSNs already resolves). The host
// phase is too late for it: `forge env up` reaches the host phase only after
// the cluster rollout succeeds, and that rollout is waiting on a pod that
// crash-loops on `database "…" does not exist` — a deadlock on every fresh
// dev stack. `forge env deploy dev` never runs a host phase at all.
//
// The host phase keeps its own call: it covers an env with no cluster group
// (where this hook never fires) and a `forge env up --no-deploy`. Both are
// idempotent, so the second is a no-op.
//
// nil under --dry-run (a preview creates nothing) and for an env that is not
// dev (ensureDevDatabase's own fail-closed classifier still applies).
func ensureDevDatabaseHook(cfg *config.ProjectConfig, entities *KCLEntities, env string, dryRun bool) func(context.Context) error {
	if dryRun {
		return nil
	}
	return func(context.Context) error {
		return ensureDevDatabase(cfg, entities, env)
	}
}

// devDatabaseDSNs returns every distinct dev database forge should
// ensure-create this run: the primary DSN plus every OTHER DATABASE_URL the
// env's services declare, de-duplicated by (server, database).
//
// WHY MORE THAN ONE. resolveSeedDSN answers "which database does this
// project seed and report on", so it returns the FIRST match and stops —
// correct for its own callers, and the reason this function exists rather
// than changing it. But an env is not limited to one database: a project
// with sibling services (control-plane's own DB plus the reliant DB its
// daemon-gateway dials) declares several, and forge created exactly one of
// them. The rest surfaced as a crash-looping pod reporting
//
//	FATAL: database "reliant_<worktree>" does not exist (SQLSTATE 3D000)
//
// which names the database but not the reason it is absent — and on a
// per-worktree name, nothing on disk had ever created it. Ensuring every
// declared DSN closes that by construction.
//
// Ordering is deterministic: the primary first, then declaration order.
func devDatabaseDSNs(entities *KCLEntities, primary string) []string {
	out := []string{primary}
	seen := map[string]bool{devDatabaseKey(primary): true}
	for _, dsn := range declaredDatabaseURLs(entities) {
		reachable := hostReachableDSN(dsn)
		if reachable == "" {
			continue
		}
		key := devDatabaseKey(reachable)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, reachable)
	}
	return out
}

// declaredDatabaseURLs returns every DATABASE_URL declared across the env's
// workloads, in declaration order, including duplicates — the caller
// de-duplicates.
func declaredDatabaseURLs(entities *KCLEntities) []string {
	if entities == nil {
		return nil
	}
	var out []string
	add := func(vars []KCLEnvVar) {
		if v := envVarValue(vars, "DATABASE_URL"); v != "" {
			out = append(out, v)
		}
	}
	for _, w := range entities.Workloads {
		add(w.EnvVars())
	}
	return out
}

// hostReachableDSN rewrites a DSN into one forge can dial FROM THE HOST, or
// returns "" when it names a server forge has no route to.
//
// A cluster service's DSN is written from the POD's point of view, so it
// reaches the developer's machine through the docker host-gateway alias
// (`host.k3d.internal`, the same constant cluster_phase.go plumbs into each
// cluster's DNS). That name deliberately does not resolve on the host —
// dialing it here fails with "no such host" — but it denotes the very
// machine forge is running on, so the database it names is reachable on
// loopback at the same port.
//
// Anything else is left alone and skipped by the caller: a DSN pointing at
// a managed cloud database is not forge's to create, and guessing a route
// to it would be how `CREATE DATABASE` lands somewhere it was never meant
// to. Only an explicitly host-denoting alias is rewritten.
func hostReachableDSN(dsn string) string {
	host := devpg.HostOf(dsn)
	if host == "" {
		return ""
	}
	if isHostGatewayAlias(host) {
		return strings.Replace(dsn, host, "localhost", 1)
	}
	if isLoopbackHost(host) {
		return dsn
	}
	return ""
}

// isHostGatewayAlias reports whether host is a docker/k3d alias for the
// machine forge itself runs on.
func isHostGatewayAlias(host string) bool {
	return host == k3dHostGatewayAlias || host == "host.docker.internal"
}

// isLoopbackHost mirrors devpg's loopback set. Declared here (rather than
// exported from devpg) because this is the consumer's question — "can I
// dial this from here" — not devpg's port-reconciliation question.
func isLoopbackHost(host string) bool {
	switch host {
	case "localhost", "127.0.0.1", "::1", "0.0.0.0":
		return true
	}
	return false
}

// devDatabaseKey identifies a database by SERVER and NAME, so two DSNs that
// differ only in credentials or query parameters are recognized as the same
// database and it is not created twice.
func devDatabaseKey(dsn string) string {
	name := devpg.DatabaseOf(dsn)
	if name == "" {
		return ""
	}
	return devpg.HostOf(dsn) + ":" + devpg.PortOf(dsn) + "/" + name
}

// reconcileDevDatabasePort checks the dev DSN against the port this
// project's dev database ACTUALLY listens on, and returns a runbook error
// when they disagree. A project whose dev database forge cannot locate, or
// whose DSN is aimed off-box, has nothing to reconcile and passes.
//
// Where that port comes from depends on how the env declares its database,
// and asking the wrong source is worse than not asking: a guard that
// refuses a CORRECT configuration teaches people to route around it, and
// the workaround is permanent while the false alarm was not.
//
//   - HOST-RUN (`forge.HostInfra`, the scaffolded default) — the port is in
//     the declaration, and the DSN is composed from that same variable, so
//     the two cannot drift. The check is a tautology and is skipped.
//   - CONTAINERIZED (`forge.Compose`) — the port lives in the compose file's
//     `${POSTGRES_PORT:-5432}` while the DSN is a separate string, so they
//     CAN drift. Resolve it with docker compose's own precedence (shell over
//     the project's `.env` over the compose default), and when forge cannot
//     faithfully reproduce that interpolation, say so and stand down rather
//     than refuse on a port it is not sure of.
func reconcileDevDatabasePort(dsn string, entities *KCLEntities) error {
	if declaresHostInfraPostgres(entities) {
		return nil
	}
	dir := projectDirForKCL()
	port, unknown := devpg.ResolveComposePort(dir, postgresComposeEnvFiles(entities))
	if unknown != "" {
		fmt.Printf("  Note: skipping the dev database port check — %s.\n"+
			"        DATABASE_URL (port %s) was NOT verified against the port compose publishes.\n",
			unknown, devpg.PortOf(dsn))
		return nil
	}
	return devpg.Reconcile(dsn, port)
}

// declaresHostInfraPostgres reports whether the env runs its database as a
// forge-supervised host process rather than a container.
func declaresHostInfraPostgres(entities *KCLEntities) bool {
	if entities == nil {
		return false
	}
	for _, hi := range entities.Infra {
		if hi.Engine == hostinfra.EnginePostgres {
			return true
		}
	}
	return false
}

// postgresComposeEnvFiles returns the `--env-file` paths forge itself will
// pass when it brings the postgres compose service up (deploytarget/compose.go
// forwards a compose service's declared env_file as --env-file). That flag
// REPLACES compose's default `.env`, so the reconcile has to interpolate from
// the same file or it would compare against a port compose never uses.
//
// Nil — the common case — means no env_file is declared and compose falls
// back to the project's `.env`.
func postgresComposeEnvFiles(entities *KCLEntities) []string {
	if entities == nil {
		return nil
	}
	for _, w := range entities.WorkloadsOn(RuntimeCompose) {
		if w.Runtime.Compose.Service != "postgres" && w.Name != "postgres" {
			continue
		}
		if f := w.Runtime.Compose.EnvFile; f != "" {
			return []string{f}
		}
	}
	return nil
}

// maybeAutoSeed is the `forge env up` first-boot
// auto-seed hook. It runs after the host-services readiness gate (so the
// app's AUTO_MIGRATE has already applied migrations) and materializes the
// deterministic dev dataset exactly once — when the target is dev, the DB is
// reachable, and every seedable table is empty. Every failure mode (no
// DATABASE_URL, unreachable DB, non-empty tables, apply error) is a warning,
// never fatal to the dev loop.
func maybeAutoSeed(ctx context.Context, store *projectstore.Store, cfg *config.ProjectConfig, entities *KCLEntities, opts upOptions) {
	if opts.noSeed {
		// quiet: the user passed --no-seed; echoing their own flag back is noise.
		return
	}
	dev, err := seedTargetIsDev(opts.env)
	if err != nil || !dev {
		// quiet: auto-seed is a dev-only affordance. On staging/prod the
		// absence of demo rows is the correct and expected state.
		return
	}
	dsn := resolveSeedDSN(entities, cfg, opts.env)
	if dsn == "" {
		// Nothing to seed against. This used to return in silence on the
		// theory that a host-only run legitimately starts no database — but
		// the app itself resolves a DSN through its own config layering, so a
		// run where forge cannot find one still boots a live app against a
		// real database and simply never seeds it. Silence there reads as
		// "seeded, and the domain has no rows", which is the state the
		// charter tells the next phase to treat as a blocker.
		fmt.Printf("[up] auto-seed skipped: no DATABASE_URL resolved for env %q (checked the environment, forge.yaml config, the env's secret provider, and the host-service KCL env)\n", opts.env)
		return
	}
	// Seeding WRITES. ensureDevDatabase already reconciles the DSN against
	// the compose port before the host phase boots anything, while
	// this hook runs after the readiness gate — so the check is
	// repeated here at the write boundary rather than assumed. Unlike every
	// other skip in this function this one is not a soft warning about
	// missing rows: it means the rows would land in a database this project
	// does not own.
	if err := reconcileDevDatabasePort(dsn, entities); err != nil {
		fmt.Printf("[up] auto-seed REFUSED: %v\n", err)
		return
	}
	db, err := database.ConnectDB(ctx, dsn)
	if err != nil {
		fmt.Printf("[up] auto-seed skipped: database not reachable (%v)\n", err)
		return
	}
	defer func() { _ = db.Close() }()

	seedCfg := autoSeedConfig(liveSeedConfig(), crudEntityTablesForSeed)
	if seedCfg.Tables != nil && len(seedCfg.Tables) == 0 {
		// quiet-ish: nothing is in scope — no CRUD entity, or `tables: []`.
		// One line, because "why is my dev DB empty?" deserves an answer.
		fmt.Println("[up] auto-seed: no tables in scope (no CRUD entities, and database.seed.tables unset) — nothing seeded")
		return
	}
	plan, err := seedplan.BuildLivePlan(ctx, db, resolveMigrationsDir(""), seedShadowServer(dbProjectRoot()), seedCfg)
	if err != nil {
		fmt.Printf("[up] auto-seed skipped: %v\n", err)
		return
	}
	// Vocabulary validation + constraint-satisfaction warnings: worth a line
	// even on the quiet first-boot path — this is where most users first meet
	// seeds, and a row target capped by a UNIQUE column is surprising in silence.
	for _, w := range plan.Warnings() {
		fmt.Printf("[up] %s\n", w)
	}
	// First-boot only: never touch a dev DB that already has data. The two
	// non-seeding outcomes are different events and used to collapse into
	// one silent return: "already has rows" is the expected steady state,
	// while a count ERROR means forge could not tell — typically the schema
	// is not applied yet, i.e. exactly the fresh-database case auto-seed
	// exists to serve. Reporting only the second keeps the happy path quiet
	// without letting a failed probe masquerade as "nothing to do".
	empty, err := seedplan.AllSeedableTablesEmpty(ctx, db, plan)
	if err != nil {
		fmt.Printf("[up] auto-seed skipped: could not tell whether the seedable tables are empty (%v)\n", err)
		return
	}
	if !empty {
		// quiet: the steady state. The database already holds rows, so
		// first-boot seeding has nothing to do and never had.
		return
	}
	res, err := seedplan.Apply(ctx, db, plan)
	if err != nil {
		fmt.Printf("[up] auto-seed skipped: %v\n", err)
		return
	}
	if res.Total() > 0 {
		// Name the tables and BOTH opt-outs. The per-project one used to go
		// unmentioned, so the only discoverable way to stop a project from
		// being seeded was a flag every run had to remember.
		fmt.Printf("[up] auto-seeded %d rows across %d tables (%s) — first boot only.\n"+
			"[up]   skip once: --no-seed · never for this project: database.seed.auto: false · choose tables: database.seed.tables in forge.yaml\n",
			res.Total(), len(res.Tables), strings.Join(seededTableNames(res), ", "))
	}
}

func seededTableNames(res *seedplan.Result) []string {
	out := make([]string, 0, len(res.Tables))
	for _, t := range res.Tables {
		out = append(out, t.Table)
	}
	return out
}

// autoSeedConfig scopes the first-boot auto-seed.
//
// An explicit database.seed.tables wins (the project decided). Otherwise the
// scope is the tables behind the project's CRUD entities — the rows the
// generated list/detail pages exist to show — plus whatever those require
// through a NOT NULL foreign key (seedplan.ScopeTables).
//
// It used to be EVERY table. That is the right default only for a schema that
// is nothing but CRUD entities, and exactly wrong for the rest: a table with
// no CRUD RPCs is plain schema owned by hand-written code — a payments
// ledger, a webhook-idempotency log, a reservations table whose rows mean
// money moved — and 20 synthesized rows there are not demo data, they are
// fabricated facts the app then acts on (Bark Social booted with "paid"
// founding deposits nobody paid, counting against a 100-member cap).
//
// entityTables returns nil when forge cannot tell (no descriptor yet); then
// the historical every-table behaviour stands rather than silently seeding
// nothing on a project whose entities simply have not been generated.
func autoSeedConfig(base seedplan.Config, entityTables func() []string) seedplan.Config {
	if base.Tables != nil {
		return base
	}
	if tables := entityTables(); tables != nil {
		base.Tables = tables
	}
	return base
}

// crudEntityTablesForSeed lists the tables behind the project's CRUD entities
// — each entity a service declares Create/Get/List/Update/Delete RPCs for,
// mapped to the tables codegen.EntityTableCandidates names for it (the
// pluralized snake_case of the entity name, then the singular). Tables that
// do not exist are harmless: seedplan.ScopeTables ignores unknown roots.
//
// nil (not empty) when the service descriptor cannot be read, so the caller
// can tell "no entities" from "could not look".
func crudEntityTablesForSeed() []string {
	root, err := projectRoot()
	if err != nil {
		return nil
	}
	services, err := codegen.ParseServicesFromProtos("", root)
	if err != nil || services == nil {
		return nil
	}
	return crudEntityTables(services)
}

func crudEntityTables(services []codegen.ServiceDef) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, svc := range services {
		for _, m := range svc.Methods {
			op, name := codegen.CRUDShape(m) // unary, not forge:custom
			if op == "" {
				continue
			}
			if op == "list" {
				name = inflection.Singular(name)
			}
			for _, table := range codegen.EntityTableCandidates(name) {
				if !seen[table] {
					seen[table] = true
					out = append(out, table)
				}
			}
		}
	}
	return out
}

// resolveSeedDSN finds the DATABASE_URL that ensureDevDatabase, the
// auto-seed hook and the discovery facts should use.
//
// THE ORDER IS THE CONTRACT, and it must match the precedence the host
// processes themselves see (hostlaunch.LayerHostEnv): the shell wins, then
// the KCL declaration, then the secret store, then per-env project config.
// Anything else and forge prepares one database while the app dials
// another — which is not a cosmetic disagreement: it creates the database,
// applies migrations and seeds rows somewhere the app will never look,
// reporting success the whole way.
//
// The KCL DECLARATION ahead of the secret store is the half that took an
// incident to get right. `secrets/dev.yaml` is seeded once, at scaffold
// time, with a DSN naming whatever port was free THEN; the env's KCL
// composes its DSN from the port it declares the database on TODAY. When
// those disagree the declaration is the one that is true — it is what the
// server actually bound and what the app's own env carries — and the stored
// copy is a stale artifact of the day the project was created.
func resolveSeedDSN(entities *KCLEntities, cfg *config.ProjectConfig, env string) string {
	if v := os.Getenv("DATABASE_URL"); v != "" {
		return v
	}
	// The KCL-declared value, from the same env stream the host workloads
	// get.
	if entities != nil {
		for _, w := range entities.Workloads {
			if v := envVarValue(w.EnvVars(), "DATABASE_URL"); v != "" {
				return v
			}
		}
	}
	// The env's SECRET PROVIDER. Load-bearing for a project whose DSN is
	// genuinely only a secret: DATABASE_URL is a `sensitive` config field,
	// so the KCL projection emits a Secret REFERENCE rather than a value,
	// and a project that has not declared a DSN in KCL keeps its real one
	// here. An `external` provider resolves nothing (by design), so cloud
	// envs fall through unchanged.
	if prov, err := secretProviderFromEntities(entities, projectDirForKCL()); err == nil {
		if v, ok := prov.Resolve("DATABASE_URL"); ok && v != "" {
			return v
		}
	}
	if m := loadProjectConfigEnv(cfg, env); m["DATABASE_URL"] != "" {
		return m["DATABASE_URL"]
	}
	return ""
}
