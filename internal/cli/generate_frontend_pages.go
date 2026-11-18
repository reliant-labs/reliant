package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/generator"
	"github.com/reliant-labs/forge/internal/templates"
)

// ensureFrontendComponents installs missing core UI components for all
// browser-targeted frontends (nextjs + vite-spa). Called during `forge
// generate` so existing projects pick up any new core components added in
// newer forge versions.
//
// In workspaces mode there is no per-frontend src/components/ui/ to
// populate — the shared component library lives at packages/ui-web/.
// We ensure it once and skip the per-frontend loop; the tsconfig path
// mapping (and Vite alias) emitted by the frontend templates routes
// `@/components/*` imports there.
//
// Returns the first scaffold error encountered. The pipeline caller
// (stepFrontendComponents) routes the result through ctx.warnOrFail so
// failures are warn-by-default and fatal under --strict.
func ensureFrontendComponents(cfg *config.ProjectConfig, projectDir string) error {
	if cfg.IsFrontendWorkspacesEnabled() {
		if err := generator.WriteUIWebPackageFiles(projectDir, cfg.Name, true); err != nil {
			return fmt.Errorf("ui-web package scaffold: %w", err)
		}
		return nil
	}
	for _, fe := range cfg.Frontends {
		feType := strings.ToLower(strings.TrimSpace(fe.Type))
		if feType != "nextjs" && feType != "vite-spa" {
			continue
		}
		feDir, ok := fe.Dir(projectDir)
		if !ok {
			// No directory in this repository — a cross-repo
			// source pin, or a path outside the project root.
			continue
		}
		if fe.Adopted {
			// The starter set is how a frontend forge SCAFFOLDS gets its
			// first UI. An adopted app already has one: installing 35
			// components beside it duplicated the ones it had installed on
			// purpose and collided with its own Button.tsx / Modal.tsx.
			// Components arrive one at a time, by request.
			fmt.Printf("  ℹ️  frontend %s was not scaffolded by forge — its starter components are not installed; "+
				"`%s component install <name> --dir %s` adds one to %s\n",
				fe.Name, Name(), feDir, fe.ComponentsDirRel())
			continue
		}
		frontendDir := filepath.Join(projectDir, feDir)
		if err := generator.EnsureCoreComponentsIn(frontendDir, fe.ComponentsDir); err != nil {
			return fmt.Errorf("component install for %s: %w", fe.Name, err)
		}
	}
	return nil
}

// generateFrontendPages generates CRUD page files for each entity that has
// CRUD-pattern RPCs across all browser-targeted frontends (nextjs + vite-spa).
// Only generates pages for CRUD-pattern RPCs whose entity name (e.g.
// "Daemon" from "ListDaemons") matches a real entity from the proto
// descriptor — without that filter, page templates produce broken output for
// services whose List/Get/Create RPCs don't follow the entity-name-as-field
// convention.
//
// Scaffold-once ("yours") lifecycle: every page template carries a
//
//	`// yours: scaffolded once, never touched again — forge will not overwrite this file`
//
// banner promising the user that hand-edits will survive subsequent
// `forge generate` runs. Honor that promise by skipping the write when
// the target file already exists on disk (write-if-absent), mirroring
// the `emitScaffoldOnceIfMissing` pattern that `generateFrontendNav`
// already uses for nav.tsx / page.tsx. Once forge has scaffolded a page it
// NEVER writes that path again — no flag — whether the user then edits the
// file or deletes it. To re-scaffold on purpose, drop the path's entry
// from .forge/scaffolded/.
//
// Per-kind dispatch:
//   - nextjs:   pages/ templates → src/app/<slug>/{,new/,view/,edit/}page.tsx
//   - vite-spa: vite-spa-pages/ templates → src/pages/<slug>/{List,Detail,Create,Edit}.tsx
//
// Next.js routes are all STATIC — detail and edit read the id from the query
// string (`/<slug>/view?id=…`) — so `output: "export"` builds them. A
// frontend that still has the dynamic `<slug>/[id]/` routes an older forge
// scaffolded keeps them: forge adds no view/edit pages beside them (two
// detail routes for one entity, neither linked to the other), and says so.
// Deleting `[id]/` is the migration's signal; the next generate scaffolds
// the static pair.
func generateFrontendPages(cfg *config.ProjectConfig, services []codegen.ServiceDef, projectDir string, entities []codegen.EntityDef, cs *checksums.FileChecksums) error {
	if len(services) == 0 {
		return nil
	}

	entities, dropped := codegen.FrontendEntities(entities)
	for _, why := range dropped {
		fmt.Printf("  ℹ️  %s\n", why)
	}
	entityByName := make(map[string]codegen.EntityDef, len(entities))
	for _, e := range entities {
		entityByName[strings.ToLower(e.Name)] = e
	}

	// The slug set the page generator considers LIVE this run — every entity
	// with a real proto definition behind its CRUD RPCs. A route dir under a
	// slug not in this set is an orphan of a renamed/removed entity (F7). We
	// collect it once here (it's frontend-independent) and hand it to the
	// per-frontend orphan reporter below.
	liveSlugs := liveEntitySlugs(services, entityByName)

	// Foreign-key referents: which CRUD entity a `<owner>_id` form field
	// points at, and the generated hooks that browse and resolve it. Built
	// once from the WHOLE project (an FK routinely crosses services) and
	// frontend-independent, like liveSlugs.
	//
	// This is what makes `patientId` render an <EntityPicker> instead of a
	// raw text input for a UUID. forge shipped that component and its own
	// page generator did not know it existed.
	fkReferents := codegen.BuildFKReferents(crudPagesWithMeta(services, entityByName))

	for _, fe := range cfg.Frontends {
		feType := strings.ToLower(strings.TrimSpace(fe.Type))
		if feType != "nextjs" && feType != "vite-spa" {
			continue
		}

		feDir, ok := fe.Dir(projectDir)
		if !ok {
			// No directory in this repository — a cross-repo
			// source pin, or a path outside the project root.
			continue
		}

		layout, err := pageLayoutForKind(feType)
		if err != nil {
			return err
		}

		// Per-frontend route allowlist (KCL forge.Frontend.routes, else the
		// frontend's package.json forge.routes). Empty
		// means "every CRUD entity" — the historical behavior, and the right
		// one for a project's only frontend.
		wantRoute, unknownRoutes := routeFilterFor(fe, liveSlugs)
		if len(unknownRoutes) > 0 {
			// Reported, not ignored: a typo'd or renamed slug otherwise
			// yields a frontend silently missing the page its author asked
			// for, and the omission looks identical to a generator bug.
			fmt.Printf("  ⚠️  frontend %s: routes %v match no CRUD entity (known: %v)\n",
				fe.Name, unknownRoutes, sortedSlugs(liveSlugs))
		}

		var pageCount, skipCount int
		// Entities whose detail/edit pages are still the dynamic `[id]`
		// routes an older forge scaffolded (nextjs only). Reported once per
		// frontend below.
		var legacySlugs []string

		for _, svc := range services {
			pages := codegen.ExtractCRUDEntities(svc)

			for _, entity := range pages {
				// Skip RPC-name-derived entities that don't have a real
				// entity definition behind them — the page templates would
				// emit broken field references.
				entityDef, ok := entityByName[strings.ToLower(entity.EntityName)]
				if !ok {
					continue
				}
				// Not in this frontend's declared route set — skip before
				// writing anything, so the pages are never created rather
				// than created-and-deleted.
				if !wantRoute(entity.EntitySlug) {
					continue
				}
				// The user's own `[id]` detail/edit routes stay the routes
				// for this entity until they delete them; a static pair
				// written beside them would be a second, unlinked copy.
				legacyIDRoutes := feType == "nextjs" && hasDynamicIDRoute(filepath.Join(projectDir, feDir), entity.EntitySlug)
				if legacyIDRoutes {
					legacySlugs = append(legacySlugs, entity.EntitySlug)
				}
				// Deleted on purpose: the `[id]` pages were scaffolded here once
				// and the owner removed them. The entity's detail/edit routes
				// stay removed — the static pair is for projects that never had
				// them, not a replacement for pages somebody chose to drop.
				deletedIDRoutes := feType == "nextjs" && !legacyIDRoutes &&
					dynamicIDRouteWasScaffoldedHere(projectDir, feDir, entity.EntitySlug)
				// Typed columns / search fields / detail rows: the
				// templates render explicit field declarations from the
				// proto entity instead of Object.keys reflection. svc
				// supplies the deep type graph for enum-column resolution.
				codegen.AttachEntityMeta(&entity, entityDef, svc)
				codegen.AttachForeignKeys(&entity, fkReferents)
				kinds := []struct {
					emit bool
					tmpl *template.Template
					rel  string
					kind string
				}{
					{entity.HasList, layout.listTmpl, layout.listPath(entity.EntitySlug), "list"},
					{entity.EmitsDetailPage() && !legacyIDRoutes && !deletedIDRoutes, layout.detailTmpl, layout.detailPath(entity.EntitySlug), "detail"},
					{entity.HasCreate, layout.createTmpl, layout.createPath(entity.EntitySlug), "create"},
					{entity.EmitsEditPage() && !legacyIDRoutes && !deletedIDRoutes, layout.editTmpl, layout.editPath(entity.EntitySlug), "edit"},
				}
				for _, k := range kinds {
					if !k.emit {
						continue
					}
					relPath := filepath.Join(feDir, k.rel)
					wrote, err := renderPageScaffoldIfMissing(k.tmpl, entity, projectDir, relPath)
					if err != nil {
						return fmt.Errorf("render %s page for %s: %w", k.kind, entity.EntityName, err)
					}
					if wrote {
						pageCount++
					} else {
						skipCount++
					}
				}
			}
		}

		if pageCount > 0 && feType == "nextjs" {
			// Every Next.js page imports @/lib/entity-routes. A frontend
			// scaffolded before the helper existed does not have it, and
			// the page just written would not compile without it.
			ensureEntityRoutesModule(projectDir, feDir, fe.Name)
		}
		if pageCount > 0 {
			fmt.Printf("  ✅ Generated %d CRUD page(s) for frontend %s\n", pageCount, fe.Name)
		}
		if skipCount > 0 {
			routinef("  ⏭️  Preserved %d existing CRUD page(s) for frontend %s (delete a file and regenerate to re-scaffold it)\n", skipCount, fe.Name)
		}
		reportDynamicIDRoutes(fe, feDir, legacySlugs)

		reportStaleFrontendRouteDirs(feType, filepath.Join(projectDir, feDir), fe.Name, liveSlugs)
	}

	return nil
}

// staticRoutesMigrationSkill is the playbook that converts a frontend's
// dynamic `[id]` CRUD routes into the static view/edit pair. `forge generate`
// points users at it by this name, and
// TestStaticRoutesMigration_DetectsDynamicIDRoutes fails if it is not shipped.
const staticRoutesMigrationSkill = "migrations/v0.1.44"

// dynamicIDSegment is the App Router directory an older forge put an
// entity's detail and edit pages under: src/app/<slug>/[id]/{,edit/}page.tsx.
const dynamicIDSegment = "[id]"

// hasDynamicIDRoute reports whether the Next.js frontend at frontendAbsDir
// still routes slug's detail/edit pages through a `[id]` dynamic segment.
// Any `<slug>/[id]/` directory counts — the user owns those pages, so what
// is in it is theirs; that it exists is what matters.
func hasDynamicIDRoute(frontendAbsDir, slug string) bool {
	info, err := os.Stat(filepath.Join(frontendAbsDir, "src", "app", slug, dynamicIDSegment))
	return err == nil && info.IsDir()
}

// dynamicIDRouteWasScaffoldedHere reports whether forge ever scaffolded slug's
// `[id]` detail/edit pages in this frontend, according to the scaffold-once
// birth ledger — whether or not they still exist.
//
// WHY THE LEDGER AND NOT THE DIRECTORY. A purpose-built console keeps a handful
// of entity routes and deletes the rest (the generated header says to). Deleting
// `<slug>/[id]/` is a DELETION, which scaffold-once must never undo; reading the
// absence of the directory as "migrated, so scaffold the static pair" re-creates
// exactly the pages the owner removed — and, for a project that never adopted
// the static pair's helpers, pages that do not typecheck against its own proto
// (control-plane v1.7.35: four entities, 10 type errors). A frontend that never
// had `[id]` pages (a fresh scaffold) has no such record and still gets the pair.
func dynamicIDRouteWasScaffoldedHere(projectDir, feDir, slug string) bool {
	base := filepath.ToSlash(filepath.Join(feDir, "src", "app", slug, dynamicIDSegment))
	for _, rel := range []string{base + "/page.tsx", base + "/edit/page.tsx"} {
		if checksums.ScaffoldRecorded(projectDir, rel) {
			return true
		}
	}
	return false
}

// reportDynamicIDRoutes tells the user which entities kept their dynamic
// `[id]` routes this run, and so got no static view/edit pages.
//
// LOUD when the frontend builds a static export: `next build` refuses a
// dynamic segment there, so these pages are a build failure waiting for the
// next `npm run build`. Verbose-only otherwise: under standalone the old
// routes work, and keeping them is a legitimate choice nobody needs to hear
// about on every generate.
func reportDynamicIDRoutes(fe config.FrontendConfig, feDir string, slugs []string) {
	if len(slugs) == 0 {
		return
	}
	sort.Strings(slugs)
	dirs := make([]string, len(slugs))
	for i, s := range slugs {
		dirs[i] = filepath.ToSlash(filepath.Join(feDir, "src", "app", s, dynamicIDSegment))
	}
	entities := plural(len(slugs), "entity", "entities")
	if fe.EffectiveOutput() == config.FrontendOutputStatic {
		fmt.Fprintf(os.Stderr, "\n⚠️  frontend %s is a static export (output: static), but %d %s still route detail/edit through a dynamic [id] segment, which `next build` refuses:\n",
			fe.Name, len(slugs), entities)
		for _, d := range dirs {
			fmt.Fprintf(os.Stderr, "  - %s/\n", d)
		}
		fmt.Fprintf(os.Stderr, "  forge adds no static view/edit pages beside them. Convert them: `%s skill load %s`.\n", Name(), staticRoutesMigrationSkill)
		return
	}
	routinef("  ℹ️  frontend %s: %d %s keep dynamic [id] detail/edit routes (%s); static view/edit pages are scaffolded once those are removed — `%s skill load %s`\n",
		fe.Name, len(slugs), entities, strings.Join(dirs, ", "), Name(), staticRoutesMigrationSkill)
}

// entityRoutesModules are the Next.js frontend files that build and read the
// static CRUD URLs (`/<slug>/view?id=…`). Every generated page imports the
// first; the second pins its URL shape. Both are part of the frontend
// template tree, so a new frontend is born with them.
var entityRoutesModules = []string{
	"src/lib/entity-routes.ts",
	"src/lib/entity-routes.test.ts",
}

// ensureEntityRoutesModule backfills the entity-route helper into a frontend
// that predates it, right after forge wrote pages that import it.
//
// Scaffold-once rules apply. A present file is the user's, and is left
// alone. A file the ledger says the user DELETED stays deleted. forge says
// so, because the pages it just wrote import that file.
func ensureEntityRoutesModule(projectDir, feDir, feName string) {
	for _, rel := range entityRoutesModules {
		content, err := templates.FrontendTemplates().Get(filepath.Join("nextjs", filepath.FromSlash(rel)))
		if err != nil {
			fmt.Fprintf(os.Stderr, "  ⚠️  frontend %s: read the %s template: %v\n", feName, rel, err)
			continue
		}
		relPath := filepath.Join(feDir, filepath.FromSlash(rel))
		wrote, err := checksums.WriteScaffoldIfMissing(projectDir, relPath, content)
		switch {
		case err != nil:
			fmt.Fprintf(os.Stderr, "  ⚠️  frontend %s: write %s: %v\n", feName, relPath, err)
		case wrote:
			fmt.Printf("  ✅ Scaffolded %s (the static CRUD routes' URL helper — yours to edit)\n", relPath)
		case rel == entityRoutesModules[0]:
			if _, statErr := os.Stat(filepath.Join(projectDir, relPath)); os.IsNotExist(statErr) {
				fmt.Fprintf(os.Stderr, "  ⚠️  frontend %s: the pages just generated import @/lib/entity-routes, which you deleted; restore it with `%s project rescaffold %s`\n",
					feName, Name(), filepath.ToSlash(relPath))
			}
		}
	}
}

// crudPagesWithMeta returns every CRUD entity page the generator considers
// live this run, with entity metadata attached — the same gate
// generateFrontendPages applies before writing a page. It is the input to
// foreign-key resolution, which needs the WHOLE project's pages (an FK
// crosses services routinely) and the entity-derived fields AttachEntityMeta
// supplies (PkFieldCamel, DisplayField).
func crudPagesWithMeta(services []codegen.ServiceDef, entityByName map[string]codegen.EntityDef) []codegen.PageTemplateData {
	var pages []codegen.PageTemplateData
	for _, svc := range services {
		for _, entity := range codegen.ExtractCRUDEntities(svc) {
			entityDef, ok := entityByName[strings.ToLower(entity.EntityName)]
			if !ok {
				continue
			}
			codegen.AttachEntityMeta(&entity, entityDef, svc)
			pages = append(pages, entity)
		}
	}
	return pages
}

// liveEntitySlugs returns the set of route slugs the page generator emits
// this run — the EntitySlug of every CRUD entity that has a real proto entity
// behind it (the same gate generateFrontendPages applies before writing a
// page). Used to spot orphaned route dirs left behind by a rename/removal.
func liveEntitySlugs(services []codegen.ServiceDef, entityByName map[string]codegen.EntityDef) map[string]bool {
	live := map[string]bool{}
	for _, svc := range services {
		for _, entity := range codegen.ExtractCRUDEntities(svc) {
			if _, ok := entityByName[strings.ToLower(entity.EntityName)]; !ok {
				continue
			}
			if entity.EntitySlug != "" {
				live[entity.EntitySlug] = true
			}
		}
	}
	return live
}

// reportStaleFrontendRouteDirs warns (report-only, never deletes) about
// per-entity CRUD route directories whose slug is no longer a live entity —
// the classic residue of renaming or removing an entity (F7). It is
// deliberately NON-destructive: generated pages are scaffold-once and
// USER-OWNED (they carry no certification marker and the user may have
// hand-edited them), so forge must not delete them. Naming them, with the
// exact `rm` and the reason, is the safe half forge can own.
//
// False positives are avoided by keying on the DISTINCTIVE generated-CRUD
// shape rather than "any directory whose name isn't a live slug":
//
//   - nextjs:   a `<slug>/` dir with BOTH a list page and a `view/page.tsx`
//     detail page (forge emits `<slug>/page.tsx` + `<slug>/view/page.tsx`),
//     or the `<slug>/[id]/page.tsx` dynamic detail an older forge emitted. A
//     hand-authored route seldom reproduces either pair by coincidence.
//   - vite-spa: a `<slug>/` dir containing BOTH `List.tsx` and `Detail.tsx`
//     (the generated pair).
func reportStaleFrontendRouteDirs(feType, frontendAbsDir, feName string, liveSlugs map[string]bool) {
	var routesRoot string
	switch feType {
	case "nextjs":
		routesRoot = filepath.Join(frontendAbsDir, "src", "app")
	case "vite-spa":
		routesRoot = filepath.Join(frontendAbsDir, "src", "pages")
	default:
		return
	}

	entries, err := os.ReadDir(routesRoot)
	if err != nil {
		return // no routes dir yet (or unreadable) — nothing to report
	}

	var stale []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		slug := e.Name()
		if liveSlugs[slug] {
			continue
		}
		if looksLikeGeneratedCRUDRouteDir(feType, filepath.Join(routesRoot, slug)) {
			rel := filepath.Join(routesRoot, slug)
			stale = append(stale, rel)
		}
	}
	if len(stale) == 0 {
		return
	}

	fmt.Fprintf(os.Stderr, "\n⚠️  frontend %s: %d generated CRUD route dir(s) no longer match a live entity (renamed or removed). "+
		"forge won't delete them (they're yours to edit); remove any that are dead:\n", feName, len(stale))
	for _, p := range stale {
		fmt.Fprintf(os.Stderr, "  - %s/  (rm -rf once you've confirmed it's dead)\n", p)
	}
}

// looksLikeGeneratedCRUDRouteDir reports whether dir has the shape forge's
// CRUD page generator emits, used to keep reportStaleFrontendRouteDirs from
// flagging a user's hand-authored routes.
func looksLikeGeneratedCRUDRouteDir(feType, dir string) bool {
	switch feType {
	case "nextjs":
		// The list + static detail pair is the fingerprint; so is the
		// dynamic detail route `<slug>/[id]/page.tsx` an older forge wrote.
		_, listErr := os.Stat(filepath.Join(dir, "page.tsx"))
		_, viewErr := os.Stat(filepath.Join(dir, "view", "page.tsx"))
		if listErr == nil && viewErr == nil {
			return true
		}
		if _, err := os.Stat(filepath.Join(dir, dynamicIDSegment, "page.tsx")); err == nil {
			return true
		}
		return false
	case "vite-spa":
		_, listErr := os.Stat(filepath.Join(dir, "List.tsx"))
		_, detailErr := os.Stat(filepath.Join(dir, "Detail.tsx"))
		return listErr == nil && detailErr == nil
	default:
		return false
	}
}

// pageLayout bundles parsed templates with the per-kind output-path policy
// used when emitting CRUD pages. Output paths are framework-specific
// (Next.js App Router routes are directories — static ones, the id rides in
// the query string; tanstack-router code-based routing has no on-disk route
// convention so we write to src/pages/).
type pageLayout struct {
	listTmpl   *template.Template
	detailTmpl *template.Template
	createTmpl *template.Template
	editTmpl   *template.Template

	listPath   func(slug string) string
	detailPath func(slug string) string
	createPath func(slug string) string
	editPath   func(slug string) string
}

// pageLayoutForKind returns the parsed templates and path policy for the
// given frontend kind. The kind is the resolved `Type` field on the
// frontend config ("nextjs" or "vite-spa").
func pageLayoutForKind(feType string) (*pageLayout, error) {
	switch feType {
	case "nextjs":
		listTmpl, err := loadPageTemplate("pages", "list-page.tsx.tmpl")
		if err != nil {
			return nil, err
		}
		detailTmpl, err := loadPageTemplate("pages", "detail-page.tsx.tmpl")
		if err != nil {
			return nil, err
		}
		createTmpl, err := loadPageTemplate("pages", "create-page.tsx.tmpl")
		if err != nil {
			return nil, err
		}
		editTmpl, err := loadPageTemplate("pages", "edit-page.tsx.tmpl")
		if err != nil {
			return nil, err
		}
		// Every route is static so `output: "export"` can build it: the
		// detail and edit pages are /<slug>/view?id=… and /<slug>/edit?id=…,
		// reading the id in the browser (src/lib/entity-routes.ts). A
		// `[id]` segment would need generateStaticParams(), and an entity
		// id only exists at runtime.
		appDir := filepath.Join("src", "app")
		return &pageLayout{
			listTmpl: listTmpl, detailTmpl: detailTmpl, createTmpl: createTmpl, editTmpl: editTmpl,
			listPath:   func(slug string) string { return filepath.Join(appDir, slug, "page.tsx") },
			detailPath: func(slug string) string { return filepath.Join(appDir, slug, "view", "page.tsx") },
			createPath: func(slug string) string { return filepath.Join(appDir, slug, "new", "page.tsx") },
			editPath:   func(slug string) string { return filepath.Join(appDir, slug, "edit", "page.tsx") },
		}, nil
	case "vite-spa":
		listTmpl, err := loadPageTemplate("vite-spa-pages", "list-page.tsx.tmpl")
		if err != nil {
			return nil, err
		}
		detailTmpl, err := loadPageTemplate("vite-spa-pages", "detail-page.tsx.tmpl")
		if err != nil {
			return nil, err
		}
		createTmpl, err := loadPageTemplate("vite-spa-pages", "create-page.tsx.tmpl")
		if err != nil {
			return nil, err
		}
		editTmpl, err := loadPageTemplate("vite-spa-pages", "edit-page.tsx.tmpl")
		if err != nil {
			return nil, err
		}
		pagesDir := filepath.Join("src", "pages")
		return &pageLayout{
			listTmpl: listTmpl, detailTmpl: detailTmpl, createTmpl: createTmpl, editTmpl: editTmpl,
			listPath:   func(slug string) string { return filepath.Join(pagesDir, slug, "List.tsx") },
			detailPath: func(slug string) string { return filepath.Join(pagesDir, slug, "Detail.tsx") },
			createPath: func(slug string) string { return filepath.Join(pagesDir, slug, "Create.tsx") },
			editPath:   func(slug string) string { return filepath.Join(pagesDir, slug, "Edit.tsx") },
		}, nil
	default:
		return nil, fmt.Errorf("unsupported frontend type for page generation: %q", feType)
	}
}

// pagePartialsPath holds the framework-neutral page fragments both the
// Next.js and the Vite page templates invoke with {{template ...}} — today
// the foreign-key <EntityPicker> control and the <EntityName> detail row.
// One definition, both trees: the FK control is subtle enough that four
// hand-kept copies would drift.
const pagePartialsPath = "pages/_partials.tmpl"

// loadPageTemplate reads and parses a page template from the embedded FS,
// with the shared partials parsed into the same template set so a page can
// {{template "fkPickerField" .}}.
// `dir` is the per-kind template subdirectory under internal/templates/frontend/
// (e.g. "pages" for nextjs, "vite-spa-pages" for vite-spa).
func loadPageTemplate(dir, name string) (*template.Template, error) {
	content, err := templates.FrontendTemplates().Get(filepath.Join(dir, name))
	if err != nil {
		return nil, fmt.Errorf("read page template %s/%s: %w", dir, name, err)
	}

	tmpl, err := template.New(name).Funcs(templates.FuncMap()).Parse(string(content))
	if err != nil {
		return nil, fmt.Errorf("parse page template %s/%s: %w", dir, name, err)
	}

	partials, err := templates.FrontendTemplates().Get(pagePartialsPath)
	if err != nil {
		return nil, fmt.Errorf("read page partials %s: %w", pagePartialsPath, err)
	}
	if _, err := tmpl.Parse(string(partials)); err != nil {
		return nil, fmt.Errorf("parse page partials %s: %w", pagePartialsPath, err)
	}

	return tmpl, nil
}

// renderPageScaffoldIfMissing renders a page template to disk under
// scaffold-once ("yours:" banner) semantics: the file is written once at
// scaffold time and NEVER overwritten on subsequent `forge generate`
// runs, matching the leading banner comment every page template carries.
// Once forge has scaffolded the page it leaves that path alone — no flag,
// no exception — whether the user edited it or deleted it. To re-scaffold
// on purpose, drop the path's entry from .forge/scaffolded/.
//
// Returns (wrote, err) — wrote=false when the destination already
// existed and was preserved, so the caller can distinguish freshly-
// scaffolded pages from preserved ones in the summary log.
//
// Scaffold pages carry no certification marker, which is why the
// stomp-guard reader *skips* them in the Tier-1 drift scan — they are
// expected to drift from any prior render, that's the whole point.
func renderPageScaffoldIfMissing(tmpl *template.Template, data codegen.PageTemplateData, projectDir, relPath string) (bool, error) {
	fullPath := filepath.Join(projectDir, relPath)

	// Scaffold-once: skip both the file the user already has AND the one
	// they deliberately deleted. The WriteScaffoldIfMissing gate below
	// decides this authoritatively; asking the ledger here just avoids
	// rendering a template whose output we would discard.
	//
	// This early return must ask the LEDGER, not os.Stat: a presence check
	// here would re-render (and, before the gate learned better, re-write)
	// a page the user removed on purpose.
	if !checksums.ScaffoldOnceDecision(projectDir, relPath) {
		return false, nil
	}
	if _, err := os.Stat(fullPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false, fmt.Errorf("stat %s: %w", relPath, err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return false, err
	}

	// Import ORDER is derived here, not authored in the template. A page's
	// import set is conditional on the entity's shape and two of its
	// specifiers (the service hooks module, the enums' protobuf-es module)
	// are only known at render time, so no fixed line order in the template
	// can be canonical for every entity — the scaffold has to sort what it
	// actually emitted. Same contract as the gofmt/goimports pass the Tier-1
	// writer runs over .go renders.
	wrote, err := checksums.WriteScaffoldIfMissing(projectDir, relPath, finalizeScaffoldTSX(templates.CanonicalTSImportOrder(buf.Bytes())))
	if wrote && err == nil {
		// Born in the shape the project's own prettier expects; best-effort,
		// and only ever on the file forge just created.
		formatScaffoldedFrontendFile(fullPath)
	}
	return wrote, err
}

// routeFilterFor builds this frontend's route predicate from its declared
// allowlist, plus the list of declared slugs that match no real CRUD entity.
//
// An EMPTY allowlist means "every entity" — the behavior every project had
// before frontends[].routes existed, and the one that stays correct for a
// project with a single frontend. The filter only narrows when a frontend
// explicitly says which routes it wants.
//
// Slugs are compared case-insensitively and tolerate a leading "/" so both
// "llm-keys" and "/llm-keys" work; the URL form is what an author is likely to
// copy out of a browser.
func routeFilterFor(fe config.FrontendConfig, liveSlugs map[string]bool) (func(string) bool, []string) {
	if len(fe.Routes) == 0 {
		return func(string) bool { return true }, nil
	}
	// `routes: [none]` — no generated pages, and nothing to warn about: it
	// is a value, not a slug that failed to match an entity.
	if fe.RoutesNone() {
		return func(string) bool { return false }, nil
	}

	want := make(map[string]bool, len(fe.Routes))
	var unknown []string
	for _, r := range fe.Routes {
		slug := strings.ToLower(strings.Trim(strings.TrimSpace(r), "/"))
		if slug == "" {
			continue
		}
		want[slug] = true
		if !liveSlugs[slug] {
			unknown = append(unknown, r)
		}
	}
	return func(slug string) bool { return want[strings.ToLower(slug)] }, unknown
}

// filterNavPagesForFrontend narrows the project-wide nav page set to the
// routes this frontend actually has — the same predicate the page generator
// applies, so the sidebar never links a page that was deliberately not
// written (an allowlist, or `routes: [none]`).
func filterNavPagesForFrontend(pages []templates.NavPageData, fe config.FrontendConfig, liveSlugs map[string]bool) []templates.NavPageData {
	want, _ := routeFilterFor(fe, liveSlugs)
	out := make([]templates.NavPageData, 0, len(pages))
	for _, p := range pages {
		if want(p.Slug) {
			out = append(out, p)
		}
	}
	return out
}

// sortedSlugs renders a slug set deterministically for the unknown-route
// warning — an unordered map would make the same misconfiguration print
// differently on every run.
func sortedSlugs(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
