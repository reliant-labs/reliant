package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/naming"
	"github.com/reliant-labs/forge/pkg/dbgrants"
)

// generateHybridComposition emits the internal/app composition layer
// (PASS 1, additive). Scaffold-once owned files (providers.go, post_build.go)
// are written before the generated injector so its Infra-field resolution
// sees the Infra struct on the first pass too. It returns a graph-mode
// project's roots, the reach config_gen.k projects as ROOTS; a legacy
// composition has none.
func generateHybridComposition(services []codegen.ServiceDef, packages []codegen.BootstrapPackageData, modulePath, databaseDriver string, ormEnabled bool, projectDir string, webhookServices map[string]bool, cs *checksums.FileChecksums) ([]codegen.GraphRoot, error) {
	// NO len()==0 early-return: the generated cmd/server.go imports
	// internal/app unconditionally (OpenInfra → Build → PostBuild → mount via
	// Inventory → serverkit.Run), so internal/app must be a non-empty,
	// compilable package even when no component is discovered (degenerate
	// trees with no parseable proto service / no descriptor). The generators
	// + templates below emit valid empty Build/Inventory/Services/lifecycle so
	// `go mod tidy` resolves internal/app LOCALLY instead of 404ing.
	//
	// The supervised set (workers + operators), read from the project's real
	// sources: the internal/{workers,operators}/<pkg>/ package directories the
	// add verbs create. compose.go + lifecycle.go stay OWNED — forge never
	// re-renders them wholesale — but they are reconciled ADDITIVELY against
	// this set, because the per-component subcommand forge scaffolds
	// (cmd/<bin>/cmd/workers/<name>.go) calls c.Worker<X>() and the field it
	// reads lives in compose.go. Without the inventory both files stay frozen
	// at first-emit and every later `forge scaffold worker|operator` leaves
	// a tree that does not compile.
	workers, operators, err := discoverSupervised(projectDir)
	if err != nil {
		return nil, err
	}

	// The composition graph (docs/design/composition-graph.md): provider
	// funcs in providers.go, one constructor method per node in compose.go,
	// the generated accessors and roots in graph_gen.go, the per-audience
	// route sets in routes_gen.go. A project whose internal/app still holds
	// the legacy Infra / NewComponents composition keeps the legacy
	// generators below until it migrates.
	if codegen.GraphComposition(projectDir) {
		if err := codegen.GenerateGraphProviders(modulePath, databaseDriver, projectDir); err != nil {
			return nil, fmt.Errorf("failed to scaffold internal/app/providers.go: %w", err)
		}
		if err := generateLoginBroker(modulePath, projectDir); err != nil {
			return nil, err
		}
		var roots []codegen.GraphRoot
		if err := codegen.GenerateGraph(codegen.GraphGenInput{
			GenContext:      codegen.GenContext{ProjectDir: projectDir, ModulePath: modulePath, Checksums: cs},
			Services:        services,
			Packages:        packages,
			Workers:         workers,
			Operators:       operators,
			WebhookServices: webhookServices,
			Roots:           &roots,
		}); err != nil {
			return nil, fmt.Errorf("failed to generate the composition graph (internal/app): %w", err)
		}
		fmt.Println("  ✅ Generated internal/app composition graph (compose.go + graph_gen.go + routes_gen.go)")
		return roots, nil
	}

	// Owned, scaffold-once (never overwritten after first emit).
	if err := codegen.GenerateProviders(modulePath, databaseDriver, ormEnabled, projectDir); err != nil {
		return nil, fmt.Errorf("failed to scaffold internal/app/providers.go: %w", err)
	}

	// Owned, scaffold-once internal/app/auth.go (SetupAuth). Co-located
	// with providers.go: both live in package app, import pkg/config, and
	// exist for exactly the server-shaped trees whose generated cmd serve
	// calls app.SetupAuth. Authentication is code now, not forge.yaml
	// config — the user edits SetupAuth to pick the validator.
	if err := generateAuthSetup(modulePath, projectDir); err != nil {
		return nil, fmt.Errorf("failed to scaffold internal/app/auth.go: %w", err)
	}

	// Explicit per-binary component construction site (compose.go: Components +
	// NewComponents), SCAFFOLD-ONCE owned code. A MissingProvider here is a LOUD
	// first-emit error naming the type + component + field the user must add to
	// Infra.
	if err := codegen.GenerateCompose(codegen.InjectGenInput{
		GenContext: codegen.GenContext{ProjectDir: projectDir, ModulePath: modulePath, Checksums: cs},
		Services:   services,
		Packages:   packages,
		Workers:    workers,
		Operators:  operators,
	}); err != nil {
		return nil, fmt.Errorf("failed to generate internal/app/compose.go: %w", err)
	}

	// Supervised-component surface (workers/operators) over *Components —
	// SCAFFOLD-ONCE owned code, additively reconciled against the discovered
	// supervised set (see GenerateLifecycle).
	if err := codegen.GenerateLifecycle(codegen.InjectGenInput{
		GenContext: codegen.GenContext{ProjectDir: projectDir, ModulePath: modulePath, Checksums: cs},
		Services:   services,
		Packages:   packages,
		Workers:    workers,
		Operators:  operators,
	}); err != nil {
		return nil, fmt.Errorf("failed to generate internal/app/lifecycle.go: %w", err)
	}

	// Typed per-service mount surface + data-only inventory (mounts_services.go).
	// PROTO-DERIVED: regenerates every run from the service set. Workers and
	// operators never appear in the mount surface, but they are counted for
	// collision naming exactly as compose.go counts them: a service whose
	// package a worker shares is c.SvcX there, and Mount<Svc> must read it.
	if err := codegen.GenerateInventory(codegen.InventoryGenInput{
		GenContext:      codegen.GenContext{ProjectDir: projectDir, ModulePath: modulePath, Checksums: cs},
		Services:        services,
		Packages:        packages,
		Workers:         workers,
		Operators:       operators,
		WebhookServices: webhookServices,
	}); err != nil {
		return nil, fmt.Errorf("failed to generate internal/app/mounts_services.go: %w", err)
	}

	// NOTE: the REAL per-component cmd-group subcommands (dir-nested under
	// cmd/<bin>/cmd/{services,workers,operators}) and the composition root
	// (cmd/<bin>/main.go) are NOT emitted here. They are written by the
	// dedicated stepCmdGroups pipeline step, which runs AFTER
	// stepRegenerateInfra has (re)created cmd/<bin>/cmd/serve.go. Doing it here
	// would silently no-op on a flat→nested migration: serve.go doesn't exist
	// yet at composition time, so the group subpackages would never get
	// anchored — yet main.go imports them, and the next `go mod tidy` /
	// `go build` would 404 the empty (Go-file-less) local group dirs. See
	// generateCmdGroups + stepCmdGroups.

	fmt.Println("  ✅ Generated internal/app composition layer (compose.go + mounts_services.go + lifecycle.go)")
	return nil, nil
}

// generateCmdGroups anchors the dir-nested per-component command-group
// subpackages under cmd/<bin>/cmd/{services,workers,operators}: one
// services/<name>.go per service whose RunE calls cmd.Serve() with the TYPED
// mount method expression (*app.Components).Mount<Svc> (no string selection);
// one workers/<name>.go and operators/<name>.go per worker/operator
// (cmd.MountNone + a named supervised subset). Each group also gets an anchor
// so the subpackage compiles (and main.go's blank import resolves) even with
// ZERO items — Tier-1 register_gen.go for services (it projects the built-in
// collision NOTEs), scaffold-once register.go for workers/operators.
//
// Driven by the SAME `services`/`workers`/`operators` rows the composition
// layer is, so each subcommand lines up with a typed mount / Worker<X>()
// / Operator<X>() accessor.
//
// Emitted only when the primary binary's cmd/<bin>/cmd/serve.go exists —
// CLI/library kinds and codegen-less trees have no serve pipeline to delegate
// to. The caller (stepCmdGroups) sequences this AFTER stepRegenerateInfra so
// that on a flat→nested migration — where serve.go does not exist until infra
// regen creates it — the group subpackages still get anchored before any
// `go mod tidy` / build-validate that imports them. Idempotent: re-running on
// an already-nested project rewrites byte-identical content.
func generateCmdGroups(services []codegen.ServiceDef, projectDir string, cs *checksums.FileChecksums) error {
	bin := bootstrapBinaryName(projectDir)
	if _, statErr := os.Stat(filepath.Join(projectDir, "cmd", bin, "cmd", "serve.go")); statErr != nil {
		return nil
	}
	names := make([]string, 0, len(services))
	for _, svc := range services {
		names = append(names, svc.Name)
	}
	// Pass internal packages so the cmd-group generator can derive the SAME
	// collision-aware mount FieldName inventory_gen does (a handler service
	// whose package collides cross-role with an internal package mounts as
	// Mount<SvcPkg>, not Mount<Pkg>). Discovery failure is non-fatal: an empty
	// package set just means no cross-role collisions, which is the common case.
	packages, pkgErr := discoverPackages(projectDir)
	if pkgErr != nil {
		packages = nil
	}
	// Workers and operators get no subcommand here — theirs are OWNED code
	// scaffolded once by `forge scaffold` — but they are counted for the same
	// collision naming compose.go and the inventory use, so a service whose
	// package a worker shares calls MountSvcX, the mount that exists. The
	// composition step already ran this discovery and failed on an error, so
	// one here is not swallowed into a silently wrong name either.
	workers, operators, err := discoverSupervised(projectDir)
	if err != nil {
		return err
	}
	if err := codegen.GenerateCmdGroups(codegen.CmdServiceGroupInput{
		Bin:       bin,
		Services:  names,
		Packages:  packages,
		Workers:   workers,
		Operators: operators,
	}, projectDir, cs); err != nil {
		return fmt.Errorf("failed to generate cmd/%s command-group subcommands: %w", bin, err)
	}
	return nil
}

// discoverSupervised reads the project's workers and operators from their
// internal/{workers,operators}/<pkg>/ directories. Every generator that
// resolves component names calls this one function, so the collision counts
// behind compose.go, the mount surface and the cmd groups cannot disagree.
func discoverSupervised(projectDir string) ([]codegen.BootstrapWorkerData, []codegen.BootstrapOperatorData, error) {
	workers, err := codegen.WorkerDataFromSpecs(codegen.DiscoverWorkerSpecs(projectDir), projectDir)
	if err != nil {
		return nil, nil, fmt.Errorf("discover workers: %w", err)
	}
	operators, err := codegen.OperatorDataFromSpecs(codegen.DiscoverOperatorSpecs(projectDir), projectDir)
	if err != nil {
		return nil, nil, fmt.Errorf("discover operators: %w", err)
	}
	return workers, operators, nil
}

// generateBootstrapTesting regenerates the per-component test helpers (one
// internal/handlers/<svc>/helpers_gen_test.go per service, plus one per
// internal package).
func generateBootstrapTesting(services []codegen.ServiceDef, modulePath string, projectDir string, cs *checksums.FileChecksums) error {
	fmt.Println("🔧 Generating per-service test helpers...")

	packages, err := discoverPackages(projectDir)
	if err != nil {
		return fmt.Errorf("discover internal packages: %w", err)
	}

	if len(services) == 0 && len(packages) == 0 {
		return nil
	}

	// No worker/operator inputs: testing.go is service+package scoped (it never
	// reads Workers/Operators), and generate performs zero worker/operator
	// discovery.
	if err := codegen.GenerateBootstrapTesting(codegen.BootstrapTestingGenInput{
		GenContext: codegen.GenContext{
			ProjectDir: projectDir,
			ModulePath: modulePath,
			Checksums:  cs,
		},
		Services: services,
		Packages: packages,
	}); err != nil {
		return fmt.Errorf("failed to generate bootstrap testing: %w", err)
	}

	fmt.Println("  ✅ Generated per-service test helpers")
	return nil
}

// generateMigrate writes db/embed.go — the project's embedded migration set.
//
// The migration LOGIC it used to emit beside this (pkg/app/migrate.go) is now
// pkg/migratekit.AutoMigrate; GenerateMigrate retires any copy still on disk.
func generateMigrate(projectDir, modulePath string, cs *checksums.FileChecksums) error {
	fmt.Println("🔧 Generating db/embed.go...")

	has := hasSQLMigrations(projectDir)
	if has {
		// BEFORE the embed, which names the grants file only once it exists.
		services, err := codegen.ParseReadOnlyGrantsServices(projectDir)
		if err != nil {
			return fmt.Errorf("read services for the read-only grants: %w", err)
		}
		wrote, err := codegen.GenerateReadOnlyGrants(projectDir, services, cs)
		if err != nil {
			return fmt.Errorf("failed to generate %s: %w", dbgrants.GrantsPath, err)
		}
		if wrote {
			fmt.Println("  ✅ Generated " + dbgrants.GrantsPath + " (the read-only role's grants)")
		}
	}
	if err := codegen.GenerateMigrate(projectDir, modulePath, has, cs); err != nil {
		return fmt.Errorf("failed to generate db/embed.go: %w", err)
	}

	if has {
		fmt.Println("  ✅ Generated db/embed.go (migrations embedded)")
	} else {
		fmt.Println("  ⏭️  No migrations yet — nothing to embed")
	}
	return nil
}

// bootstrapBinaryName resolves the primary binary name — the cmd/<bin>/
// directory leaf the command tree lives under. It is the forge.yaml project
// name; falls back to the project directory's base name when the config is
// unreadable (degenerate/standalone trees), mirroring the generator's
// binaryName().
func bootstrapBinaryName(projectDir string) string {
	cfgPath := filepath.Join(projectDir, defaultProjectConfigFile)
	if store, err := loadProjectStoreFrom(cfgPath); err == nil && store != nil {
		if name := store.Config().Name; name != "" {
			return name
		}
	}
	return filepath.Base(projectDir)
}

// discoverPackages returns BootstrapPackageData for the project's internal
// contract packages. The SET comes from codegen.DiscoverInternalPackages —
// the one walk every forge surface resolves packages through, reporters
// included — and this adds the naming/fallibility derivation the bootstrap
// template needs (FieldName, VarName, import path).
//
// A walk error is returned so the caller can fail the pipeline rather than
// silently emit a partial bootstrap (which would surface later as a
// mysterious "undefined: pkg" go build error in pkg/app/bootstrap.go).
func discoverPackages(projectDir string) ([]codegen.BootstrapPackageData, error) {
	names, err := codegen.DiscoverInternalPackageNames(projectDir)
	if err != nil {
		return nil, err
	}
	return codegen.PackageDataFromNames(names, projectDir)
}

// hasComponentDir reports whether roleRoot (e.g. "internal/workers") exists and
// holds at least one immediate subdirectory — a cheap BOOLEAN presence gate for
// the pipeline (HasWorkers / HasOperators), NOT an enumeration of the component
// SET. `forge generate` deliberately never enumerates the worker/operator set:
// the command tree + wiring are scaffold-once OWNED code, so generate needs to
// know only *whether* the bootstrap family of steps applies, never *which*
// workers/operators exist. (Introspection — forge project map/graph — walks disk
// read-only, entirely separate from this generate path.)
func hasComponentDir(projectDir, roleRoot string) bool {
	rootDir := filepath.Join(projectDir, filepath.FromSlash(roleRoot))
	entries, err := os.ReadDir(rootDir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() && e.Name() != "testdata" {
			return true
		}
	}
	return false
}

// discoverWebhookServices returns a set of snake-case service package
// names that carry one or more webhook_<name>.go handlers. The bootstrap
// template uses this to emit `RegisterWebhookRoutes(mux, stack)` after
// `RegisterHTTP(...)` so generated webhook routes get auto-mounted on the
// mux without the user having to hand-edit the user-owned `RegisterHTTP`
// body in handlers/<svc>/service.go.
//
// Keying matches `naming.ServicePackage(comp.Name)`: a hyphenated CLI name
// ("admin-server") and the proto's PascalCase form ("AdminServerService")
// both normalize to "admin_server" (post-2026-06-08 snake-canonicalisation),
// which is also the directory leaf under handlers/.
func discoverWebhookServices(projectDir string) map[string]bool {
	cfgPath := filepath.Join(projectDir, defaultProjectConfigFile)
	store, err := loadProjectStoreFrom(cfgPath)
	if err != nil || store == nil {
		return nil
	}
	cfg := store.Config()
	// Best-effort registration view: webhooks on an unregistered service
	// are a hard error earlier in the pipeline (generateWebhookRoutes),
	// but this map is also built on standalone paths, so filter here too
	// rather than emitting a RegisterWebhookRoutes call into a row
	// constructor whose service the binary doesn't serve. A parse error
	// falls open to "registered" — the build/pipeline reports it.
	reg, regErr := loadServiceRegistry(projectDir)
	if regErr != nil {
		reg = &serviceRegistry{Exists: false}
	}

	out := map[string]bool{}
	for _, comp := range codegen.DiscoverProjectComponents(projectDir, cfg.Name) {
		// Webhooks are discovered from the webhook_<name>.go files on disk,
		// not a declared config list.
		res, resErr := codegen.ResolveServiceComponent(projectDir, comp.Name)
		if resErr != nil || !res.FromDisk {
			continue
		}
		handlerDir := filepath.Join(projectDir, "internal", "handlers", filepath.FromSlash(res.ImportLeaf))
		if !codegen.ServiceHasWebhooks(handlerDir) {
			continue
		}
		if isConnectServiceConfig(comp) && !reg.registered(comp.Name) {
			continue
		}
		out[naming.ServicePackage(comp.Name)] = true
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
