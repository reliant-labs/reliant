package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
)

// staticPagesFixture is the full CRUD quintet for one entity — every page
// kind and every link between them is emitted.
func staticPagesFixture() ([]codegen.ServiceDef, []codegen.EntityDef) {
	services := []codegen.ServiceDef{{
		Name:    "ClinicService",
		Package: "demo.v1",
		Methods: []codegen.Method{
			{Name: "ListPatients", InputType: "ListPatientsRequest", OutputType: "ListPatientsResponse"},
			{Name: "GetPatient", InputType: "GetPatientRequest", OutputType: "GetPatientResponse"},
			{Name: "CreatePatient", InputType: "CreatePatientRequest", OutputType: "CreatePatientResponse"},
			{Name: "UpdatePatient", InputType: "UpdatePatientRequest", OutputType: "UpdatePatientResponse"},
			{Name: "DeletePatient", InputType: "DeletePatientRequest", OutputType: "DeletePatientResponse"},
		},
	}}
	return services, []codegen.EntityDef{{Name: "Patient"}}
}

// dynamicLinkRE matches a link built by interpolating an id into the PATH —
// `/patients/${id}` or `/patients/${item.id}/edit` — the shape a static
// export cannot serve.
var dynamicLinkRE = regexp.MustCompile("`/[a-z0-9-]+/\\$\\{")

// TestGenerateFrontendPages_NextjsRoutesAreStatic pins the route shape that
// lets `output: "export"` build a project with entities. Before it, the
// detail and edit pages were `src/app/<slug>/[id]/{,edit/}page.tsx`, and a
// static export of any project with one CRUD entity failed:
//
//	Error: Page "/patients/[id]" is missing "generateStaticParams()" so it
//	cannot be used with "output: export" config.
//
// So: no generated page path may contain a dynamic segment, the detail and
// edit pages must read their id from the query string under a Suspense
// boundary (Next refuses to export a useSearchParams reader without one),
// and no page may link to the old `/<slug>/<id>` shape.
func TestGenerateFrontendPages_NextjsRoutesAreStatic(t *testing.T) {
	projectDir := t.TempDir()
	cfg := &config.ProjectConfig{
		Name:      "demo",
		Frontends: []config.FrontendConfig{{Name: "web", Type: "nextjs", Output: config.FrontendOutputStatic}},
	}
	services, entities := staticPagesFixture()
	if err := generateFrontendPages(cfg, services, projectDir, entities, &checksums.FileChecksums{}); err != nil {
		t.Fatalf("generateFrontendPages: %v", err)
	}

	appDir := filepath.Join(projectDir, "frontends", "web", "src", "app")
	var pages []string
	if err := filepath.WalkDir(appDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.Contains(d.Name(), "[") {
			t.Errorf("generated route %s has a dynamic segment — `output: \"export\"` cannot build it", path)
		}
		if !d.IsDir() && d.Name() == "page.tsx" {
			rel, _ := filepath.Rel(appDir, path)
			pages = append(pages, filepath.ToSlash(rel))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	want := []string{"patients/edit/page.tsx", "patients/new/page.tsx", "patients/page.tsx", "patients/view/page.tsx"}
	if strings.Join(pages, ",") != strings.Join(want, ",") {
		t.Fatalf("generated pages = %v, want %v", pages, want)
	}

	for _, rel := range []string{"patients/view/page.tsx", "patients/edit/page.tsx"} {
		page := readPageFile(t, filepath.Join(appDir, rel))
		for _, needle := range []string{"useEntityIdParam()", "<Suspense", `from "@/lib/entity-routes"`} {
			if !strings.Contains(page, needle) {
				t.Errorf("%s must read its id from the query string under Suspense; missing %q", rel, needle)
			}
		}
		if strings.Contains(page, "useParams") {
			t.Errorf("%s still reads a path param (useParams) — there is no [id] segment to read", rel)
		}
	}

	// The create page's land-on-the-new-row redirect needs the entity's
	// response field, which this metadata-free fixture does not have; the
	// scaffold e2e builds and clicks through it.
	wantLinks := map[string][]string{
		"patients/page.tsx":      {`entityViewHref("patients", item.id)`},
		"patients/new/page.tsx":  {`router.push("/patients")`},
		"patients/view/page.tsx": {`entityEditHref("patients", id)`, `router.push("/patients")`},
		"patients/edit/page.tsx": {`router.push(entityViewHref("patients", id))`, `href: entityViewHref("patients", id)`, `href={entityViewHref("patients", id)}`},
	}
	for rel, needles := range wantLinks {
		page := readPageFile(t, filepath.Join(appDir, rel))
		for _, needle := range needles {
			if !strings.Contains(page, needle) {
				t.Errorf("%s: missing link %q", rel, needle)
			}
		}
		if m := dynamicLinkRE.FindString(page); m != "" {
			t.Errorf("%s builds a path-interpolated link (%s…) — the static routes take the id as ?id=", rel, m)
		}
	}
}

// TestGenerateFrontendPages_BackfillsEntityRoutesModule covers a frontend
// born before src/lib/entity-routes.ts existed (every project scaffolded by
// forge ≤ v0.1.43) gaining a new entity. The pages forge writes import the
// helper, so generate must add it — or the frontend stops compiling. A copy
// the user already has is theirs and is left untouched.
func TestGenerateFrontendPages_BackfillsEntityRoutesModule(t *testing.T) {
	projectDir := t.TempDir()
	cfg := &config.ProjectConfig{
		Name:      "demo",
		Frontends: []config.FrontendConfig{{Name: "web", Type: "nextjs"}},
	}
	services, entities := staticPagesFixture()
	if err := generateFrontendPages(cfg, services, projectDir, entities, &checksums.FileChecksums{}); err != nil {
		t.Fatalf("generateFrontendPages: %v", err)
	}
	libDir := filepath.Join(projectDir, "frontends", "web", "src", "lib")
	helper := readPageFile(t, filepath.Join(libDir, "entity-routes.ts"))
	for _, export := range []string{"export function entityViewHref", "export function entityEditHref", "export function useEntityIdParam"} {
		if !strings.Contains(helper, export) {
			t.Errorf("backfilled entity-routes.ts is missing %q", export)
		}
	}
	readPageFile(t, filepath.Join(libDir, "entity-routes.test.ts"))

	// A user's copy survives the next run byte for byte.
	mine := filepath.Join(libDir, "entity-routes.ts")
	if err := os.WriteFile(mine, []byte("// mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(projectDir, "frontends", "web", "src", "app", "patients", "view")); err != nil {
		t.Fatal(err)
	}
	if err := generateFrontendPages(cfg, services, projectDir, entities, &checksums.FileChecksums{}); err != nil {
		t.Fatalf("generateFrontendPages (second run): %v", err)
	}
	if got := readPageFile(t, mine); got != "// mine\n" {
		t.Errorf("generate rewrote the user's entity-routes.ts: %q", got)
	}
}

// TestGenerateFrontendPages_KeepsLegacyIDRoutes is the existing-project
// half. A frontend scaffolded before the static routes has
// `<slug>/[id]/page.tsx` — the user's file, possibly edited. forge must not
// add a second detail/edit pair beside it (two routes, neither linked to the
// other); it leaves the entity alone and names the migration. Once the user
// deletes `[id]/`, the next generate scaffolds the static pair.
func TestGenerateFrontendPages_KeepsLegacyIDRoutes(t *testing.T) {
	projectDir := t.TempDir()
	cfg := &config.ProjectConfig{
		Name:      "demo",
		Frontends: []config.FrontendConfig{{Name: "web", Type: "nextjs"}},
	}
	services, entities := staticPagesFixture()
	appDir := filepath.Join(projectDir, "frontends", "web", "src", "app")
	legacy := filepath.Join(appDir, "patients", "[id]", "page.tsx")
	mkfile(t, legacy)

	if err := generateFrontendPages(cfg, services, projectDir, entities, &checksums.FileChecksums{}); err != nil {
		t.Fatalf("generateFrontendPages: %v", err)
	}
	for _, rel := range []string{"view", "edit"} {
		if _, err := os.Stat(filepath.Join(appDir, "patients", rel, "page.tsx")); !os.IsNotExist(err) {
			t.Errorf("patients/%s/page.tsx was scaffolded beside the user's [id] route (stat err = %v)", rel, err)
		}
	}
	if body := readPageFile(t, legacy); body != "x" {
		t.Errorf("the user's [id] page was rewritten: %q", body)
	}
	// List and create have no [id] counterpart and are still scaffolded.
	for _, rel := range []string{"page.tsx", "new/page.tsx"} {
		if _, err := os.Stat(filepath.Join(appDir, "patients", rel)); err != nil {
			t.Errorf("patients/%s should still be scaffolded: %v", rel, err)
		}
	}

	// The migration's signal: [id]/ is gone, so the static pair arrives.
	if err := os.RemoveAll(filepath.Join(appDir, "patients", "[id]")); err != nil {
		t.Fatal(err)
	}
	if err := generateFrontendPages(cfg, services, projectDir, entities, &checksums.FileChecksums{}); err != nil {
		t.Fatalf("generateFrontendPages (after removing [id]): %v", err)
	}
	for _, rel := range []string{"view", "edit"} {
		if _, err := os.Stat(filepath.Join(appDir, "patients", rel, "page.tsx")); err != nil {
			t.Errorf("patients/%s/page.tsx should be scaffolded once [id]/ is removed: %v", rel, err)
		}
	}
}

// TestGenerateFrontendPages_DoesNotResurrectDeletedIDRoutes is the control-plane
// v1.7.35 regression. Its operator console scaffolded `<slug>/[id]/` pages long
// ago and the owner DELETED them (keeping only list and new). The birth ledger
// still records them. Reading "no [id] directory" as "migrated" scaffolded a
// static view/edit pair back for four entities — pages that did not typecheck
// against the project's own proto (`id` is not a field of GetDaemonRequest).
// A deletion is a decision; scaffold-once must never undo it.
func TestGenerateFrontendPages_DoesNotResurrectDeletedIDRoutes(t *testing.T) {
	projectDir := t.TempDir()
	cfg := &config.ProjectConfig{
		Name:      "demo",
		Frontends: []config.FrontendConfig{{Name: "web", Type: "nextjs"}},
	}
	services, entities := staticPagesFixture()
	appDir := filepath.Join(projectDir, "frontends", "web", "src", "app")

	// forge scaffolded the dynamic pages once (the ledger remembers) ...
	checksums.RecordScaffold(projectDir, "frontends/web/src/app/patients/[id]/page.tsx")
	checksums.RecordScaffold(projectDir, "frontends/web/src/app/patients/[id]/edit/page.tsx")
	// ... and the owner deleted them: no [id] directory on disk.

	if err := generateFrontendPages(cfg, services, projectDir, entities, &checksums.FileChecksums{}); err != nil {
		t.Fatalf("generateFrontendPages: %v", err)
	}
	for _, rel := range []string{"view", "edit"} {
		if _, err := os.Stat(filepath.Join(appDir, "patients", rel, "page.tsx")); !os.IsNotExist(err) {
			t.Errorf("patients/%s/page.tsx was re-created although its [id] pages were deliberately deleted (stat err = %v)", rel, err)
		}
	}
	// List and create were not deleted and are still maintained as before.
	for _, rel := range []string{"page.tsx", "new/page.tsx"} {
		if _, err := os.Stat(filepath.Join(appDir, "patients", rel)); err != nil {
			t.Errorf("patients/%s should still be scaffolded: %v", rel, err)
		}
	}
}

// A frontend that never had [id] pages (no ledger record) still gets the static
// pair — the guard above must not turn the feature off for new projects.
func TestGenerateFrontendPages_FreshFrontendStillGetsStaticPair(t *testing.T) {
	projectDir := t.TempDir()
	cfg := &config.ProjectConfig{
		Name:      "demo",
		Frontends: []config.FrontendConfig{{Name: "web", Type: "nextjs", Output: config.FrontendOutputStatic}},
	}
	services, entities := staticPagesFixture()
	if err := generateFrontendPages(cfg, services, projectDir, entities, &checksums.FileChecksums{}); err != nil {
		t.Fatalf("generateFrontendPages: %v", err)
	}
	appDir := filepath.Join(projectDir, "frontends", "web", "src", "app")
	for _, rel := range []string{"view", "edit"} {
		if _, err := os.Stat(filepath.Join(appDir, "patients", rel, "page.tsx")); err != nil {
			t.Errorf("a fresh frontend must get patients/%s/page.tsx: %v", rel, err)
		}
	}
}
