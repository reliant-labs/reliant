package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/generator"
	"github.com/reliant-labs/forge/internal/naming"
	"github.com/reliant-labs/forge/internal/templates"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// generateFrontendNav re-renders the sidebar navigation and dashboard page
// for each Next.js frontend using entity data derived from CRUD service
// methods. Called during `forge generate` after services are parsed.
//
// Every file it writes is scaffold-once ("yours"): components/nav.tsx,
// app/dashboard.tsx and app/page.tsx are written when absent and never
// touched again.
//
// None of them is Tier-1, and the reason is the rule Tier-1 exists for: a
// regenerated file must have a direct input forge re-derives it FROM — a
// proto to its stubs, the applied schema to an ORM struct, contract.go to
// its mocks. A nav and a dashboard are layouts. The entity set seeds them,
// but which routes belong in a sidebar and which numbers belong on a
// dashboard are product decisions with nothing to re-derive from, and
// regenerating them would mean forge holding an opinion about someone
// else's product and enforcing it with a hash guard.
//
// The entity SET is a projection, though, so it is generated beside them:
// each entity's sidebar route and dashboard tile is its own module, gathered
// into ENTITY_ROUTES and DASHBOARD_TILES (emitNavRegistries), which the owned
// files import. Adding an entity therefore links it without editing the
// nav, and these files can still be rewritten, which for starter code is
// the point.
func generateFrontendNav(cfg *config.ProjectConfig, services []codegen.ServiceDef, projectDir string, entities []codegen.EntityDef, cs *checksums.FileChecksums) error {
	// CRITICAL: nav/dashboard routes derive from the SAME entity set that
	// gates CRUD page emission (generateFrontendPages skips RPC-name-derived
	// entities with no proto entity definition behind them). Before this
	// filter the two generators disagreed and a pristine scaffold's nav
	// advertised routes that 404'd.
	pages := buildNavPages(services, entities)

	for _, fe := range cfg.Frontends {
		if !strings.EqualFold(fe.Type, "nextjs") {
			continue
		}
		if err := generateNextjsFrontend(cfg, projectDir, fe, pages, cs); err != nil {
			return err
		}
	}

	// ── Vite SPA frontends ──
	// The Next-only nav/dashboard/basepath files above don't apply here, so
	// this is a separate, minimal pass.
	for _, fe := range cfg.Frontends {
		if !strings.EqualFold(fe.Type, "vite-spa") {
			continue
		}
		if err := generateViteFrontend(cfg, projectDir, fe, cs); err != nil {
			return err
		}
	}

	// ── React Native frontends: emit the apiurl_gen dev floor ──
	// RN scaffolds carry no nav/dashboard/runtime and, like Vite, used to
	// lean on a scaffold-once .env.local for EXPO_PUBLIC_API_URL. With that
	// file gone, the regenerated apiurl_gen.ts is the dev floor connect.ts
	// falls back to (per-env EXPO_PUBLIC_API_URL still comes from KCL).
	for _, fe := range cfg.Frontends {
		if !strings.EqualFold(fe.Type, "react-native") && !strings.EqualFold(fe.Type, "rn") {
			continue
		}
		if err := generateReactNativeFrontend(cfg, projectDir, fe, cs); err != nil {
			return err
		}
	}

	// ── The dev web-runtime bridge (gitignored, project-wide) ──
	// Per-project rather than per-frontend: it is one npm workspace root
	// covering frontends/*, so it is reconciled once after every frontend's
	// tracked manifest has been settled at its published range.
	generator.EnsureDevWebRuntimeLink(projectDir)

	return syncFrontendLocks(cfg, projectDir, cs)
}

// syncFrontendLocks refreshes the lockfile of every frontend whose package.json
// forge manages. The reconcile above edits dependency ranges in package.json
// only, which leaves a lockfile recording the old range and fails `npm ci`;
// this runs after every frontend is settled so one failure does not skip the
// rest, and reports every lockfile it could not fix with the command to run.
// A disowned manifest is never touched (frontendManifestDisowned), so its
// lockfile is not either.
func syncFrontendLocks(cfg *config.ProjectConfig, projectDir string, cs *checksums.FileChecksums) error {
	var errs []error
	for _, fe := range cfg.Frontends {
		feDir, ok := fe.Dir(projectDir)
		if !ok {
			continue
		}
		if frontendManifestDisowned(feDir, cs) {
			continue
		}
		if err := generator.SyncFrontendLock(projectDir, feDir); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// generateNextjsFrontend emits one Next.js frontend's Tier-1 *_gen.ts files,
// reconciles its package.json, and seeds its scaffold-once nav, dashboard and
// home page.
func generateNextjsFrontend(cfg *config.ProjectConfig, projectDir string, fe config.FrontendConfig, pages []templates.NavPageData, cs *checksums.FileChecksums) error {
	feDir, ok := fe.Dir(projectDir)
	if !ok {
		// No directory in this repository — a cross-repo
		// source pin, or a path outside the project root.
		return nil
	}

	// Only the routes THIS frontend has — `routes:` narrows pages, so it
	// must narrow the links to them too.
	fePages := filterNavPagesForFrontend(pages, fe, navSlugSet(pages))
	data := templates.FrontendTemplateData{
		FrontendName:   fe.Name,
		ProjectName:    cfg.Name,
		Pages:          fePages,
		NavHookImports: buildNavHookImports(fePages),
		BasePath:       strings.TrimSpace(fe.BasePath),
		APIURL:         devAPIURL(cfg, projectDir),
	}

	if err := os.MkdirAll(filepath.Join(projectDir, feDir, "src", "components"), 0o755); err != nil {
		return fmt.Errorf("create components dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(projectDir, feDir, "src", "app"), 0o755); err != nil {
		return fmt.Errorf("create app dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(projectDir, feDir, "src", "lib"), 0o755); err != nil {
		return fmt.Errorf("create lib dir: %w", err)
	}

	if err := emitNextjsTier1Gen(projectDir, feDir, fe, data, cs); err != nil {
		return err
	}

	// ── Scaffold ("yours"): nav.tsx + dashboard.tsx ──
	//
	// These are LAYOUTS, not projections. Tier-1 is for a file with a
	// direct input that forge re-derives — a proto to its stubs, the
	// applied schema to an ORM struct, contract.go to its mocks. The set
	// of entities is such an input; how those entities are ARRANGED on a
	// page is a design decision with no input to re-derive from, and
	// regenerating it would mean forge holding an opinion about someone
	// else's product.
	//
	// They were Tier-1 and the contradiction showed: the dashboard's own
	// header called it "exemplar code", and exemplar code that trips a
	// hash guard when edited teaches nothing.
	// This is the ONLY writer of these two: the frontend scaffold defers
	// them here (generator.deferredToNavGenerator) so they are seeded from
	// the entity set rather than from the empty one that exists while the
	// frontend tree is being written.
	//
	// They cannot simply be withheld until the first entity: layout.tsx
	// imports { Nav } and page.tsx imports the dashboard, so a project
	// with no entities yet would not compile. So they are written
	// immediately AND refreshed while untouched — see
	// emitScaffoldUntilTouched. A greenfield project therefore picks up
	// its first entity's route, and the moment the user edits either file
	// it is theirs and forge stops.
	// ── package.json: the @reliantlabs/forge-web-runtime specifier ──
	// Re-resolved every run so a project that moved on disk, or one
	// picked up by a differently-built forge, gets a specifier that
	// still points somewhere real.
	reconcileFrontendDependencies(projectDir, feDir, fe, cs)

	// An adopted app has its own layout, home page and navigation;
	// forge's nav, dashboard and home page are starter code for a
	// frontend it scaffolded, and are not written into one it did not.
	if fe.Adopted {
		return nil
	}
	if err := emitNavRegistries(projectDir, feDir, data, cs); err != nil {
		return err
	}
	navRel := filepath.Join(feDir, "src", "components", "nav.tsx")
	if err := emitScaffoldUntilTouched(projectDir, navRel, "nextjs/src/components/nav.tsx.tmpl", data, cs); err != nil {
		return err
	}
	dashRel := filepath.Join(feDir, "src", "app", "dashboard.tsx")
	if err := emitScaffoldUntilTouched(projectDir, dashRel, "nextjs/src/app/dashboard.tsx.tmpl", data, cs); err != nil {
		return err
	}

	// The cost of not regenerating the nav: an entity added later has
	// pages but no link to them. Silence there would be the worst of both
	// worlds — forge stopped maintaining the file but never said so, and
	// the route is only missing, never broken, so nothing else surfaces
	// it. Name the routes and stop; editing is the user's call.
	reportUnlinkedRoutes(projectDir, navRel, fePages, fe.Name)

	// ── Scaffold ("yours"): page.tsx (user-owned, scaffold-once) ──
	pageRel := filepath.Join(feDir, "src", "app", "page.tsx")
	if err := emitScaffoldOnceIfMissing(projectDir, pageRel, "nextjs/src/app/page.tsx.tmpl", data); err != nil {
		return err
	}

	if len(fePages) > 0 {
		fmt.Printf("  ✅ Scaffolded nav.tsx + dashboard.tsx with %d page(s) for frontend %s (yours to edit)\n", len(fePages), fe.Name)
	}
	return nil
}

// emitNavRegistries writes what the owned nav.tsx and dashboard.tsx read the
// entity set from: one generated module per entity with pages — its sidebar
// route (src/lib/nav-routes/<slug>_gen.ts) and its dashboard tile
// (src/components/dashboard-tiles/<slug>_gen.tsx) — and the two registries
// that gather those directories with the bundler's own glob.
//
// The entity set is a projection; how it is laid out is the user's. So the
// projection is generated, one file per entity (two branches that each add one
// merge as files), and the layout imports it and is never edited for an
// entity: nav.tsx and dashboard.tsx render the same for every entity set, so
// they no longer churn while pristine, and an edited one still gains new
// entities. A dropped entity's modules are reclaimed by the stale-file sweep.
func emitNavRegistries(projectDir, feDir string, data templates.FrontendTemplateData, cs *checksums.FileChecksums) error {
	for _, f := range []struct{ tmpl, rel string }{
		{"nextjs/src/lib/nav-routes_gen.ts.tmpl", filepath.Join(feDir, "src", "lib", "nav-routes_gen.ts")},
		{"nextjs/src/components/dashboard-tiles_gen.ts.tmpl", filepath.Join(feDir, "src", "components", "dashboard-tiles_gen.ts")},
	} {
		content, err := templates.FrontendTemplates().Render(f.tmpl, data)
		if err != nil {
			return fmt.Errorf("render %s: %w", f.tmpl, err)
		}
		if _, err := checksums.WriteGeneratedFileTier1(projectDir, f.rel, content, cs, true); err != nil {
			return fmt.Errorf("write %s: %w", f.rel, err)
		}
	}
	for _, p := range data.Pages {
		for _, f := range []struct{ tmpl, rel string }{
			{"nav/nav-route.ts.tmpl", filepath.Join(feDir, "src", "lib", "nav-routes", p.Slug+"_gen.ts")},
			{"nav/dashboard-tile.tsx.tmpl", filepath.Join(feDir, "src", "components", "dashboard-tiles", p.Slug+"_gen.tsx")},
		} {
			content, err := templates.FrontendTemplates().Render(f.tmpl, p)
			if err != nil {
				return fmt.Errorf("render %s for %s: %w", f.tmpl, p.Slug, err)
			}
			if err := os.MkdirAll(filepath.Join(projectDir, filepath.Dir(f.rel)), 0o755); err != nil {
				return fmt.Errorf("mkdir for %s: %w", f.rel, err)
			}
			if _, err := checksums.WriteGeneratedFileTier1(projectDir, f.rel, content, cs, true); err != nil {
				return fmt.Errorf("write %s: %w", f.rel, err)
			}
		}
	}
	return nil
}

// emitNextjsTier1Gen writes the always-regenerated, checksum-guarded files of
// a Next.js frontend: basepath_gen.ts, apiurl_gen.ts and otel_gen.ts.
func emitNextjsTier1Gen(projectDir, feDir string, fe config.FrontendConfig, data templates.FrontendTemplateData, cs *checksums.FileChecksums) error {
	// ── Tier-1: src/lib/basepath_gen.ts (always regenerated) ──
	// BASE_PATH + joinBasePath() sourced from forge.yaml's
	// frontends[].base_path. Regenerated every run so editing
	// base_path in forge.yaml propagates without re-scaffolding;
	// next.config.ts (Tier-2, scaffold-once) reads the same value
	// via the NEXT_PUBLIC_BASE_PATH env var or its baked default.
	bpGenContent, err := templates.FrontendTemplates().Render(
		filepath.Join("nextjs", "src", "lib", "basepath_gen.ts.tmpl"), data)
	if err != nil {
		return fmt.Errorf("render basepath_gen.ts for %s: %w", fe.Name, err)
	}
	bpGenRel := filepath.Join(feDir, "src", "lib", "basepath_gen.ts")
	if _, err := checksums.WriteGeneratedFileTier1(projectDir, bpGenRel, bpGenContent, cs, true); err != nil {
		return fmt.Errorf("write basepath_gen.ts: %w", err)
	}

	// ── Tier-1: src/lib/apiurl_gen.ts (always regenerated) ──
	// DEV_API_URL baked from forge.yaml's first service port, refreshed
	// on every `forge generate`. connect.ts uses it as the non-mock dev
	// fallback when NEXT_PUBLIC_API_URL is unset — and fails LOUD when
	// both are empty, instead of silently pointing at a port nobody is
	// listening on (downstream projects hand-patched a stale
	// localhost:8080 default twice before this existed).
	auGenContent, err := templates.FrontendTemplates().Render(
		filepath.Join("nextjs", "src", "lib", "apiurl_gen.ts.tmpl"), data)
	if err != nil {
		return fmt.Errorf("render apiurl_gen.ts for %s: %w", fe.Name, err)
	}
	auGenRel := filepath.Join(feDir, "src", "lib", "apiurl_gen.ts")
	if _, err := checksums.WriteGeneratedFileTier1(projectDir, auGenRel, auGenContent, cs, true); err != nil {
		return fmt.Errorf("write apiurl_gen.ts: %w", err)
	}

	// ── Tier-1: src/lib/otel_gen.ts (always regenerated) ──
	// The browser OpenTelemetry CONFIGURATION — the NEXT_PUBLIC_* env
	// reads Next.js inlines at build time, handed to the SDK wiring in
	// @reliantlabs/forge-web-runtime/otel. It has carried the canonical
	// "Code generated by forge. DO NOT EDIT. / regenerated every run"
	// banner since it was written, and the banner linter's isKnownTier1
	// list names it — but nothing ever regenerated it, so the claim was
	// false in both directions: a hand-edit survived (and the drift lint
	// could not see it, there being no recorded hash), and DELETING it
	// left providers.tsx importing a module that no longer existed, so
	// `tsc --noEmit` failed and no amount of `forge generate` brought it
	// back. Emitting it here makes the banner true.
	otelContent, err := templates.FrontendTemplates().Render(
		filepath.Join("nextjs", "src", "lib", "otel_gen.ts.tmpl"), data)
	if err != nil {
		return fmt.Errorf("render otel_gen.ts for %s: %w", fe.Name, err)
	}
	otelRel := filepath.Join(feDir, "src", "lib", "otel_gen.ts")
	if _, err := checksums.WriteGeneratedFileTier1(projectDir, otelRel, otelContent, cs, true); err != nil {
		return fmt.Errorf("write otel_gen.ts: %w", err)
	}
	// Retire the pre-`_gen` copy from a project generated before the
	// rename. providers.tsx is scaffold-once, so an older project's copy
	// still names "@/lib/otel" — see rewriteRenamedFrontendImports, which
	// repoints it. Leaving the old module in place instead would let that
	// stale import keep resolving, to a file nothing regenerates.
	codegen.RetireRenamedGenerated(projectDir, filepath.Join(feDir, "src", "lib", "otel.ts"), cs)

	// next.config.ts is Tier-2 (user-owned, scaffold-once), so forge
	// can't rewrite it when base_path is added to forge.yaml later.
	// A config that never reads NEXT_PUBLIC_BASE_PATH will serve the
	// app at "/" while basepath_gen.ts prefixes hand-built URLs with
	// the declared base_path — exactly the silent split-brain this
	// feature exists to kill. Warn loudly (non-fatal: the user may
	// have wired basePath by other means).
	if data.BasePath != "" {
		warnIfNextConfigIgnoresBasePath(projectDir, feDir, fe.Name, data.BasePath)
	}

	// Older scaffolds put the dev-log receiver in a `_`-prefixed (private,
	// never-routed) App Router folder; move it to the spelling that serves.
	if err := relocatePrivateDevLogRoute(projectDir, feDir, fe.Name, cs); err != nil {
		return fmt.Errorf("frontend %s: %w", fe.Name, err)
	}
	return nil
}

// generateViteFrontend emits one Vite SPA frontend's Tier-1 files and
// reconciles its package.json.
func generateViteFrontend(cfg *config.ProjectConfig, projectDir string, fe config.FrontendConfig, cs *checksums.FileChecksums) error {
	feDir, ok := fe.Dir(projectDir)
	if !ok {
		// No directory in this repository — a cross-repo
		// source pin, or a path outside the project root.
		return nil
	}
	data := templates.FrontendTemplateData{
		FrontendName: fe.Name,
		ProjectName:  cfg.Name,
		APIURL:       devAPIURL(cfg, projectDir),
	}
	// ── Tier-1: src/lib/apiurl_gen.ts (always regenerated) ──
	// The dev-mode API-URL FLOOR connect.ts falls back to when
	// VITE_API_URL is unset. Vite frontends used to rely on a
	// scaffold-once .env.local for this; that file is gone, so the floor
	// is now a regenerated gen file exactly like Next.js's — it can't
	// drift from the port the backend binds.
	if err := emitAPIURLGen(projectDir, feDir, "vite-spa", data, cs); err != nil {
		return fmt.Errorf("emit apiurl_gen.ts for %s: %w", fe.Name, err)
	}
	// ── Tier-1: src/lib/otel_gen.ts (always regenerated) ──
	// The browser telemetry configuration — the VITE_* env reads Vite
	// inlines at build time, handed to the HyperDX SDK wiring in
	// @reliantlabs/forge-web-runtime/telemetry. Same module the Next.js
	// loop emits, with the Vite spelling of the env reads.
	if err := emitOtelGen(projectDir, feDir, "vite-spa", data, cs); err != nil {
		return fmt.Errorf("emit otel_gen.ts for %s: %w", fe.Name, err)
	}
	// use-query-resource.ts pairs with <Resource>. It ships in the Vite
	// static scaffold tree (new projects), so ensure EXISTING projects
	// pick it up too — emit-if-missing keeps any user edits. Starter
	// code (only the generated pages import it), so not for an adopted
	// frontend.
	if !fe.Adopted {
		if err := ensureViteQueryResourceHook(projectDir, feDir); err != nil {
			return fmt.Errorf("ensure use-query-resource for %s: %w", fe.Name, err)
		}
	}
	// ── package.json: the @reliantlabs/forge-web-runtime specifier ──
	reconcileFrontendDependencies(projectDir, feDir, fe, cs)
	return nil
}

// generateReactNativeFrontend emits one React Native frontend's apiurl_gen
// dev floor and settles its web-runtime dependency.
func generateReactNativeFrontend(cfg *config.ProjectConfig, projectDir string, fe config.FrontendConfig, cs *checksums.FileChecksums) error {
	feDir, ok := fe.Dir(projectDir)
	if !ok {
		// No directory in this repository — a cross-repo
		// source pin, or a path outside the project root.
		return nil
	}
	data := templates.FrontendTemplateData{
		FrontendName: fe.Name,
		ProjectName:  cfg.Name,
		APIURL:       devAPIURL(cfg, projectDir),
	}
	if err := emitAPIURLGen(projectDir, feDir, "react-native", data, cs); err != nil {
		return fmt.Errorf("emit apiurl_gen.ts for %s: %w", fe.Name, err)
	}
	// ── package.json: the @reliantlabs/forge-web-runtime specifier ──
	if frontendManifestDisowned(feDir, cs) {
		printDependencyNotices(fe.Name, feDir, generator.FrontendDependencyNotices(projectDir, feDir))
	} else {
		generator.EnsureWebRuntimeDependency(projectDir, feDir, fe.Name)
	}
	return nil
}

// reconcileFrontendDependencies keeps a web frontend's package.json declaring
// what forge's generated code imports: the web-runtime range raised to the
// floor this forge generates against, and the browser-telemetry SDK
// otel_gen.ts imports.
//
// ADOPTED frontends are reconciled too. They used to get a notice instead,
// on the reasoning that an adopted app's manifest is its own. But the floor
// is not a preference: the hooks forge just regenerated into that app call
// runtime APIs the old range does not have, so leaving it below the floor
// hands the owner a tree that does not typecheck plus a line to act on by
// hand, every run. The edit is the same surgical one a scaffolded frontend
// gets — one value raised, never lowered (a project ahead of forge keeps its
// range) — and the lockfile is refreshed with it (syncFrontendLocks).
//
// The opt-out is the general one: `forge project disown
// <frontend>/package.json` makes the manifest the project's alone, and forge
// then reports what it would change and changes nothing.
func reconcileFrontendDependencies(projectDir, feDir string, fe config.FrontendConfig, cs *checksums.FileChecksums) {
	if frontendManifestDisowned(feDir, cs) {
		printDependencyNotices(fe.Name, feDir, generator.FrontendDependencyNotices(projectDir, feDir))
		return
	}
	generator.EnsureWebRuntimeDependency(projectDir, feDir, fe.Name)
	generator.EnsureBrowserTelemetryDependencies(projectDir, feDir, fe.Name)
}

// frontendManifestDisowned reports whether the project disowned the
// frontend's package.json (`forge project disown`), which is the one thing
// that stops forge reconciling its dependency ranges.
//
// It also records the manifest as a path forge edits every run. The
// manifest carries no forge:hash marker and goes through no Tier-1 writer,
// so without the record obsolete-disown retirement would read a disowned
// manifest as one forge no longer touches, drop the disown, and edit the
// file on the following run.
func frontendManifestDisowned(feDir string, cs *checksums.FileChecksums) bool {
	rel := filepath.Join(feDir, "package.json")
	checksums.MarkTier1Target(rel)
	return cs.IsDisowned(rel)
}

// isFrontendManifestRelPath reports whether a project-relative path is a
// frontend's package.json: unmarked, but edited by `forge generate`
// (reconcileFrontendDependencies), so `forge project disown` accepts it.
func isFrontendManifestRelPath(root, rel string) bool {
	rel = filepath.ToSlash(filepath.Clean(rel))
	if filepath.Base(rel) != "package.json" {
		return false
	}
	cfg, err := loadProjectConfigFrom(filepath.Join(root, "forge.yaml"))
	if err != nil {
		return false
	}
	for _, fe := range cfg.Frontends {
		if feDir, ok := fe.Dir(root); ok && filepath.ToSlash(filepath.Join(feDir, "package.json")) == rel {
			return true
		}
	}
	return false
}

func printDependencyNotices(feName, feDir string, notices []string) {
	for _, n := range notices {
		fmt.Printf("  ℹ️  frontend %s (%s is disowned, so forge leaves it as is): %s\n",
			feName, filepath.ToSlash(filepath.Join(feDir, "package.json")), n)
	}
}

// emitOtelGen renders <tmplSubdir>/src/lib/otel_gen.ts.tmpl into the
// frontend's src/lib/otel_gen.ts as a Tier-1 (always-regenerated,
// checksum-guarded) file.
func emitOtelGen(projectDir, feDir, tmplSubdir string, data templates.FrontendTemplateData, cs *checksums.FileChecksums) error {
	if err := os.MkdirAll(filepath.Join(projectDir, feDir, "src", "lib"), 0o755); err != nil {
		return fmt.Errorf("create lib dir: %w", err)
	}
	content, err := templates.FrontendTemplates().Render(
		filepath.Join(tmplSubdir, "src", "lib", "otel_gen.ts.tmpl"), data)
	if err != nil {
		return fmt.Errorf("render otel_gen.ts: %w", err)
	}
	rel := filepath.Join(feDir, "src", "lib", "otel_gen.ts")
	if _, err := checksums.WriteGeneratedFileTier1(projectDir, rel, content, cs, true); err != nil {
		return fmt.Errorf("write otel_gen.ts: %w", err)
	}
	return nil
}

// emitAPIURLGen renders <tmplSubdir>/src/lib/apiurl_gen.ts.tmpl into the
// frontend's src/lib/apiurl_gen.ts as a Tier-1 (always-regenerated,
// checksum-guarded) file. Shared by the Vite and React Native loops so
// their dev-URL floor tracks the backend port on every `forge generate`,
// exactly the way the Next.js loop already emits its own apiurl_gen.
func emitAPIURLGen(projectDir, feDir, tmplSubdir string, data templates.FrontendTemplateData, cs *checksums.FileChecksums) error {
	if err := os.MkdirAll(filepath.Join(projectDir, feDir, "src", "lib"), 0o755); err != nil {
		return fmt.Errorf("create lib dir: %w", err)
	}
	content, err := templates.FrontendTemplates().Render(
		filepath.Join(tmplSubdir, "src", "lib", "apiurl_gen.ts.tmpl"), data)
	if err != nil {
		return fmt.Errorf("render apiurl_gen.ts: %w", err)
	}
	rel := filepath.Join(feDir, "src", "lib", "apiurl_gen.ts")
	if _, err := checksums.WriteGeneratedFileTier1(projectDir, rel, content, cs, true); err != nil {
		return fmt.Errorf("write apiurl_gen.ts: %w", err)
	}
	return nil
}

// warnIfNextConfigIgnoresBasePath prints an advisory when forge.yaml
// declares frontends[].base_path but the frontend's (user-owned)
// next.config.ts never references the canonical env var / basePath key.
// Scaffolds produced after basePath support landed always contain
// `NEXT_PUBLIC_BASE_PATH`; older hand-rolled configs need the block
// added by hand (see the frontend skill's "Serving under a path prefix"
// section). Missing next.config.ts is skipped silently — the scaffold
// step owns that complaint.
func warnIfNextConfigIgnoresBasePath(projectDir, feDir, feName, basePath string) {
	body, err := os.ReadFile(filepath.Join(projectDir, feDir, "next.config.ts"))
	if err != nil {
		return
	}
	s := string(body)
	if strings.Contains(s, "NEXT_PUBLIC_BASE_PATH") || strings.Contains(s, "basePath") {
		return
	}
	fmt.Printf("  ⚠️  frontend %s: forge.yaml declares base_path %q but next.config.ts never reads NEXT_PUBLIC_BASE_PATH or sets basePath.\n"+
		"      Routes/assets will be served at \"/\" while generated helpers prefix URLs with %q. Add to next.config.ts:\n"+
		"        const basePath = process.env.NEXT_PUBLIC_BASE_PATH ?? %q;\n"+
		"        ...(basePath ? { basePath, assetPrefix: basePath } : {}),\n",
		feName, basePath, basePath, basePath)
}

// emitScaffoldOnceIfMissing writes a scaffold ("yours:" banner) template
// only when the destination file does not yet exist on disk. Scaffold
// files are user-owned from birth: forge writes them once at scaffold
// time and NEVER writes that path again — no flag, no exception. That
// covers deletion as well as editing; to re-scaffold on purpose, drop the
// path's entry from .forge/scaffolded/.
func emitScaffoldOnceIfMissing(projectDir, relPath, tmplPath string, data templates.FrontendTemplateData) error {
	full := filepath.Join(projectDir, relPath)
	if !checksums.ScaffoldOnceDecision(projectDir, relPath) {
		return nil // present, or deliberately deleted — either way, not ours
	}
	if _, statErr := os.Stat(full); statErr != nil && !os.IsNotExist(statErr) {
		return fmt.Errorf("stat %s: %w", relPath, statErr)
	}
	content, err := templates.FrontendTemplates().Render(tmplPath, data)
	if err != nil {
		return fmt.Errorf("render %s: %w", tmplPath, err)
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return fmt.Errorf("mkdir for %s: %w", relPath, err)
	}
	checksums.RecordPreWriteAbs(full) // journaled: a failed run removes it
	if err := atomicfile.WriteFile(full, content, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", relPath, err)
	}
	checksums.RecordScaffold(projectDir, relPath)
	fmt.Printf("  ✅ Scaffolded %s (yours to edit)\n", relPath)
	return nil
}

// buildNavPages derives navigation page entries from CRUD service methods.
// A nav entry is created only for entities that (a) have a List RPC — the
// nav links the list page — AND (b) exist in the entity set, the SAME
// predicate generateFrontendPages applies before emitting page files.
// Anything looser advertises 404s on a pristine scaffold.
//
// The two halves are matched on the kebab SLUG — the actual route
// identity — not on the raw EntityDef.Name string. The entity set
// (BuildSchemaEntities) carries the singular as EntityDef.Name; the route
// side (ExtractCRUDEntities) re-derives plural + slug. Keying the gate on
// PascalToKebab(Pluralize(Name)) ties it to the SAME transform that
// produces e.EntitySlug, so the match tracks the route the page generator
// actually emits rather than an incidental name string that an entity-
// projection change can reshape underneath it. The applied-schema entity
// join (BuildSchemaEntities) replaced the old proto-annotation entity
// names with singular CRUD-RPC names; matching on the derived slug keeps
// this gate stable across that projection churn — if the slug a route is
// emitted under matches an entity in the set, the route is kept, full
// stop. A regression here empties ALL_ROUTES silently (no error), dropping
// every dashboard tile, so the match is pinned by
// TestBuildNavPages_ControlPlaneEntitySet.
// navSlugSet is the set of slugs a nav page list covers — the live-entity
// set routeFilterFor checks declared routes against.
func navSlugSet(pages []templates.NavPageData) map[string]bool {
	out := make(map[string]bool, len(pages))
	for _, p := range pages {
		out[p.Slug] = true
	}
	return out
}

func buildNavPages(services []codegen.ServiceDef, entities []codegen.EntityDef) []templates.NavPageData {
	entities, _ = codegen.FrontendEntities(entities)
	entitySet := make(map[string]struct{}, len(entities))
	for _, e := range entities {
		entitySet[codegen.PascalToKebab(naming.Pluralize(e.Name))] = struct{}{}
	}

	seen := make(map[string]bool)
	var pages []templates.NavPageData

	for _, svc := range services {
		crudEntities := codegen.ExtractCRUDEntities(svc)
		for _, e := range crudEntities {
			if !e.HasList {
				continue
			}
			if _, ok := entitySet[e.EntitySlug]; !ok {
				continue
			}
			if seen[e.EntitySlug] {
				continue
			}
			seen[e.EntitySlug] = true

			pages = append(pages, templates.NavPageData{
				Label:           naming.Humanize(e.EntityNamePlural),
				LabelLower:      naming.HumanizeLower(e.EntityNamePlural),
				LabelSingular:   naming.Humanize(e.EntityName),
				Slug:            e.EntitySlug,
				HasCreate:       e.HasCreate,
				ListHook:        "use" + e.ListRPC,
				HooksModule:     e.HooksImportPath,
				ItemsField:      e.ItemsField,
				HasTotalCount:   e.HasTotalCount,
				TotalCountField: e.TotalCountField,
				HasPageSize:     e.HasPageSize,
				ComponentIdent:  e.EntityNamePlural,
			})
		}
	}

	return pages
}

// buildNavHookImports merges the dashboard tiles' list-hook imports by
// module so the template emits one import statement per generated hooks
// file (two entities on one service share a module).
func buildNavHookImports(pages []templates.NavPageData) []templates.NavHookImport {
	byModule := map[string][]string{}
	var order []string
	for _, p := range pages {
		if p.HooksModule == "" || p.ListHook == "" {
			continue
		}
		if _, ok := byModule[p.HooksModule]; !ok {
			order = append(order, p.HooksModule)
		}
		byModule[p.HooksModule] = append(byModule[p.HooksModule], p.ListHook)
	}
	sort.Strings(order)
	out := make([]templates.NavHookImport, 0, len(order))
	for _, m := range order {
		syms := byModule[m]
		sort.Strings(syms)
		out = append(out, templates.NavHookImport{Module: m, Symbols: syms})
	}
	return out
}

// devAPIURL derives the dev-mode API base URL that connect.ts targets when
// NEXT_PUBLIC_API_URL is unset. Empty only when the project has no backend
// at all (CLI/library kind with a stray frontend) — connect.ts then refuses
// to guess and fails loud in non-mock dev.
func devAPIURL(cfg *config.ProjectConfig, projectDir string) string {
	port := resolveDevAPIPort(cfg, projectDir)
	if port == 0 {
		return ""
	}
	return fmt.Sprintf("http://localhost:%d", port)
}

// resolveDevAPIPort resolves the dev-mode API port the same way the server /
// `forge api` do: a backend exists when the proto descriptor exposes a
// Connect service (codegen.IntrospectComponents) OR the project is
// service-kind — its dev server binds the mux and serves /healthz even before
// the first service is added — and every backend listens on
// config.DefaultServePort. No backend → 0, devAPIURL returns "" and connect.ts
// fails loud rather than guessing a port.
//
// This is a GENERATE-time value baked into apiurl_gen.ts, so it cannot be the
// ephemeral port `forge env up` allocate at launch: those inject
// the live URL through NEXT_PUBLIC_API_URL / VITE_API_URL / EXPO_PUBLIC_API_URL,
// which always wins over this baked fallback (see src/lib/connect.ts).
func resolveDevAPIPort(cfg *config.ProjectConfig, projectDir string) int {
	if len(codegen.IntrospectComponents(projectDir)) > 0 || cfg.IsServiceKind() {
		return config.DefaultServePort
	}
	return 0
}

// reportUnlinkedRoutes names entity pages that exist but that the user-owned
// nav does not link to.
//
// nav.tsx is scaffold-once, so an entity added after the first scaffold gets
// pages that nothing navigates to. That is the deliberate cost of the file
// being the user's; what would NOT be acceptable is discovering it by noticing
// a missing sidebar link. A missing route breaks no build and fails no test,
// so this notice — plus its duplicate in the post-generation warnings block
// (unlinkedRouteWarnings, collected by validateGeneratedProject) — is the
// only thing that surfaces it. The inline ℹ️ line here is easy to miss: it is
// one line among the ~150 a full `forge generate` prints, sandwiched between
// walls of ✅. The warnings block is read every time because it is also where
// hard-to-miss findings like the pkg/config-import check land, so the same
// finding is promoted there rather than relocated — losing the inline line
// would mean a project with only ONE frontend loses its immediate, in-context
// notice for no benefit.
//
// It reads the nav as TEXT rather than parsing it, because by this point the
// file is arbitrary user code: it may map, filter, or build its routes from a
// CMS. A substring hit means the path is mentioned somewhere, which is enough
// to conclude the user knows about it — and being wrong in that direction
// costs nothing, while a false alarm on every generate would train people to
// ignore the line.
func reportUnlinkedRoutes(projectDir, navRel string, pages []templates.NavPageData, feName string) {
	missing := missingNavRoutes(projectDir, navRel, pages)
	if len(missing) == 0 {
		return
	}
	fmt.Printf("  ℹ️  %s: %d page(s) with no link in %s — %s\n",
		feName, len(missing), navRel, strings.Join(missing, ", "))
	fmt.Printf("      nav.tsx is yours, so forge does not edit it. Add them to ALL_ROUTES if you want them in the sidebar.\n")
}

// missingNavRoutes is the detection half of reportUnlinkedRoutes, split out
// so validateGeneratedProject's post-generation warnings pass (see
// unlinkedRouteWarnings below) can reuse the exact same pristine-suppression
// and substring-match rules instead of re-deriving them and risking drift
// between the inline notice and the warnings-block one.
func missingNavRoutes(projectDir, navRel string, pages []templates.NavPageData) []string {
	if len(pages) == 0 {
		return nil
	}
	// While the nav is still pristine forge is keeping it current, so a route
	// missing from it is about to be added rather than dropped — warning then
	// would fire on every greenfield run and be wrong every time.
	if checksums.ScaffoldIsPristine(projectDir, navRel) {
		return nil
	}
	src, err := os.ReadFile(filepath.Join(projectDir, navRel))
	if err != nil {
		return nil // deliberately deleted, or unreadable
	}
	nav := string(src)
	// A nav that spreads the generated registry links every entity, now
	// and later.
	if strings.Contains(nav, "ENTITY_ROUTES") {
		return nil
	}

	// src/app, derived from the nav's own path: navRel is
	// <feDir>/src/components/nav.tsx, so its grandparent is <feDir>/src.
	appDir := filepath.Join(filepath.Dir(filepath.Dir(navRel)), "app")

	var missing []string
	for _, p := range pages {
		if strings.Contains(nav, `"/`+p.Slug+`"`) {
			continue
		}
		// A page the user DELETED cannot be unlinked — there is nothing
		// to link to. The pages list is projected from services and
		// entities, so it names every route forge would scaffold,
		// including ones the user has since removed. Deleting a
		// scaffold-once file is an act of ownership the ledger records
		// (recorded && absent), and forge honors it everywhere else:
		// the same run prints "scaffold-once file(s) … left absent on
		// purpose" for exactly these paths.
		//
		// Without this, control-plane's internal-console was told on
		// every generate to add /daemons, /plans, /deployments and
		// /llm-keys to ALL_ROUTES, while the same output listed all
		// thirteen of those pages' files as deliberately absent. Acting
		// on the advice would have pointed the sidebar at four 404s.
		//
		// The test is simply whether the page is THERE. Deletion is the
		// common way a route goes missing, but it is not the only one:
		// control-plane added an org_member_grants entity whose pages
		// this frontend never scaffolded at all, and forge advised
		// linking /org-member-grants — a route with no page behind it
		// and no scaffold-ledger entry either. Distinguishing "deleted"
		// from "never written" would be a distinction the reader cannot
		// act on differently: both make the advice produce a 404.
		if _, err := os.Stat(filepath.Join(projectDir, appDir, p.Slug, "page.tsx")); err != nil {
			continue
		}
		missing = append(missing, "/"+p.Slug)
	}
	return missing
}

// unlinkedRouteWarnings recomputes reportUnlinkedRoutes' finding as entries
// for the post-generation warnings block. It cannot reuse a value stashed by
// stepFrontendNav — pipeline steps communicate forward through ctx fields,
// not backward through return values — so it re-derives the same
// (services, entities) → pages projection stepFrontendNav used and re-reads
// each Next.js frontend's nav.tsx. Cheap (a handful of file reads) and safe
// to recompute: it is a property of the file's contents at validate time, not
// of the earlier step's run.
func unlinkedRouteWarnings(cfg *config.ProjectConfig, projectDir string, services []codegen.ServiceDef, entities []codegen.EntityDef) []string {
	if cfg == nil {
		return nil
	}
	pages := buildNavPages(services, entities)
	if len(pages) == 0 {
		return nil
	}
	var warnings []string
	for _, fe := range cfg.Frontends {
		if !strings.EqualFold(fe.Type, "nextjs") {
			continue
		}
		feDir, ok := fe.Dir(projectDir)
		if !ok {
			// No directory in this repository — a cross-repo
			// source pin, or a path outside the project root.
			continue
		}
		navRel := filepath.Join(feDir, "src", "components", "nav.tsx")
		missing := missingNavRoutes(projectDir, navRel, filterNavPagesForFrontend(pages, fe, navSlugSet(pages)))
		if len(missing) == 0 {
			continue
		}
		warnings = append(warnings, fmt.Sprintf(
			"frontend %s: %d page(s) with no link in %s — %s. "+
				"nav.tsx is yours, so forge does not edit it; add them to ALL_ROUTES if you want them in the sidebar.",
			fe.Name, len(missing), navRel, strings.Join(missing, ", ")))
	}
	return warnings
}

// emitScaffoldUntilTouched writes a scaffold and KEEPS it current until the
// user edits it, at which point it becomes theirs permanently.
//
// Ordinary scaffolds are write-once (emitScaffoldOnceIfMissing): forge writes
// them if absent and never looks again. That is wrong for nav.tsx and
// dashboard.tsx for one reason — they must exist before they can be correct.
// layout.tsx imports { Nav } and page.tsx imports the dashboard, so they have
// to be on disk from the first render, which happens before any entity
// exists; write-once would therefore freeze the empty version and every
// project's first entity would arrive unlinked.
//
// Refreshing while pristine resolves that without weakening ownership. A
// greenfield project accumulates its routes right up until the user takes
// over, and taking over is a single edit — not a flag, not a disown, not a
// file to delete. Pristine is decided by the birth hash rather than by
// timestamps, so a file rewritten with identical content, or touched by a
// formatter that changed nothing, is still forge's; one changed byte is not.
func emitScaffoldUntilTouched(projectDir, relPath, tmplPath string, data templates.FrontendTemplateData, cs *checksums.FileChecksums) error {
	if cs != nil && cs.IsDisowned(relPath) {
		return nil
	}
	full := filepath.Join(projectDir, relPath)
	_, statErr := os.Stat(full)
	switch {
	case statErr == nil:
		// Present: refresh only while it is still exactly what forge wrote.
		if !checksums.ScaffoldIsPristine(projectDir, relPath) {
			return nil
		}
	case os.IsNotExist(statErr):
		// Absent: a deliberate deletion stays deleted, same as any scaffold.
		if checksums.ScaffoldRecorded(projectDir, relPath) {
			return nil
		}
	default:
		return fmt.Errorf("stat %s: %w", relPath, statErr)
	}

	content, err := templates.FrontendTemplates().Render(tmplPath, data)
	if err != nil {
		return fmt.Errorf("render %s: %w", tmplPath, err)
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return fmt.Errorf("mkdir for %s: %w", relPath, err)
	}
	checksums.RecordPreWriteAbs(full) // journaled: a failed run removes it
	if err := atomicfile.WriteFile(full, content, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", relPath, err)
	}
	checksums.RecordScaffoldWithHash(projectDir, relPath, content)
	return nil
}
