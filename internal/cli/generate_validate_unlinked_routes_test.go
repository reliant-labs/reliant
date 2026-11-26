// Regression test for the dogfooding finding: `forge generate` reported
// unlinked nav routes via reportUnlinkedRoutes' inline ℹ️ line, but that
// line sits mid-run among ~150 lines of ✅ output and was missed. The
// post-generation warnings block (validateGeneratedProject, printed by
// stepPostGenValidate) is read every time — this pins that the same
// finding also lands there.
package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
)

func unlinkedRoutesFixtureServices() []codegen.ServiceDef {
	return []codegen.ServiceDef{
		{
			Name:      "CustomerService",
			ProtoFile: "proto/services/customers/v1/customers.proto",
			Methods: []codegen.Method{
				{Name: "ListCustomers", InputType: "ListCustomersRequest", OutputType: "ListCustomersResponse"},
				{Name: "CreateCustomer", InputType: "CreateCustomerRequest", OutputType: "CreateCustomerResponse"},
			},
		},
	}
}

func unlinkedRoutesFixtureEntities() []codegen.EntityDef {
	return []codegen.EntityDef{{Name: "Customer", TableName: "customers"}}
}

// TestValidateGeneratedProject_ReportsUnlinkedNavRoutes is the positive
// case: a nav.tsx that the user has taken over (touched, so no longer
// pristine) and that never mentions a live entity's route must surface in
// validateGeneratedProject's warnings — the same list stepPostGenValidate
// prints under "⚠️  Post-generation warnings:".
func TestValidateGeneratedProject_ReportsUnlinkedNavRoutes(t *testing.T) {
	projectDir := t.TempDir()
	cfg := &config.ProjectConfig{
		Name: "demo",
		Frontends: []config.FrontendConfig{
			{Name: "web", Type: "nextjs"},
		},
	}
	services := unlinkedRoutesFixtureServices()
	entities := unlinkedRoutesFixtureEntities()

	// Scaffold the nav for real via the generator, then simulate the user
	// taking ownership of it (an edit) with no mention of "/customers" —
	// exactly the state a nav edited before the Customer entity existed
	// would be in.
	cs := &checksums.FileChecksums{}
	if err := generateFrontendNav(cfg, services, projectDir, entities, cs); err != nil {
		t.Fatalf("generateFrontendNav: %v", err)
	}
	navPath := filepath.Join(projectDir, "frontends", "web", "src", "components", "nav.tsx")
	if err := os.WriteFile(navPath, []byte("// hand-rolled nav, no /customers link\nexport const ALL_ROUTES = [];\n"), 0o644); err != nil {
		t.Fatalf("simulate user edit: %v", err)
	}
	// The page has to exist for the route to be unlinked rather than
	// absent — an unlinked route is a page you cannot reach, so without
	// a page there is nothing to reach.
	writeListPage(t, projectDir, "customers")

	warnings := validateGeneratedProject(projectDir, cfg, services, entities)

	var found string
	for _, w := range warnings {
		if strings.Contains(w, "/customers") {
			found = w
		}
	}
	if found == "" {
		t.Fatalf("validateGeneratedProject warnings missing the unlinked /customers route; got: %v", warnings)
	}
	if !strings.Contains(found, "nav.tsx") {
		t.Errorf("warning should name the nav.tsx path; got: %q", found)
	}
}

// TestBaseVersionPseudoVersionSuppressesPinWarning: a dev build in a
// TAGGED repository must not nag about the pin.
//
// Both pseudo-version forms mean the same thing — this commit is named
// by no tag, so no module proxy can serve it — but only the untagged
// form (`v0.0.0-…`) was recognised. Once forge had tags, `task
// install:dev` produced the base-version form (`v0.1.25-0.<ts>-<sha>`),
// which fell through to the warning. Running `forge generate` in the
// forge checkout itself therefore printed:
//
//	⚠️  forge.yaml pins forge_version v0.0.4-…+dirty but this binary is
//	    v0.1.25-0.20260930072646-c80a24a951a5. …
//
// advising `forge project upgrade` to pin a version nobody can fetch.
// That is unactionable by construction, which is exactly the noise
// isUnreleasedBinaryVersion exists to suppress.
func TestBaseVersionPseudoVersionSuppressesPinWarning(t *testing.T) {
	unreleased := []string{
		"v0.0.0-20260101120000-abcdef012345",          // untagged repo
		"v0.1.25-0.20260930072646-c80a24a951a5",       // tagged repo, commit after the tag
		"v0.1.25-0.20260930072646-c80a24a951a5+dirty", // ...with local edits
		"dev", "(devel)", "",
	}
	for _, v := range unreleased {
		if !isUnreleasedBinaryVersion(v) {
			t.Errorf("binary version %q cannot be fetched from a proxy, so the "+
				"pin warning is unactionable and must be suppressed", v)
		}
		if got := forgeVersionMismatchWarning("v0.1.20", v); got != "" {
			t.Errorf("forgeVersionMismatchWarning(pin, %q) = %q, want silence", v, got)
		}
	}

	// The other direction: a real release must still warn, and an
	// ordinary pre-release tag is a real release — it is fetchable.
	for _, v := range []string{"v0.1.25", "v1.2.3-rc1"} {
		if isUnreleasedBinaryVersion(v) {
			t.Errorf("%q is a fetchable release; the pin warning must still fire", v)
		}
		if got := forgeVersionMismatchWarning("v0.1.20", v); got == "" {
			t.Errorf("forgeVersionMismatchWarning(pin, %q) was silent; want a warning", v)
		}
	}
}

// TestValidateGeneratedProject_RouteWithNoPageIsNotUnlinked: a route
// with no page behind it must not be reported as unlinked. There is
// nothing to link to, so "add it to ALL_ROUTES" would produce a 404.
//
// control-plane's internal-console hit this two different ways, and the
// fix has to cover both because the user cannot act on them differently:
//
//   - DELETED. It was told to add /daemons, /plans, /deployments and
//     /llm-keys — four pages it removed on purpose, being owner-scoped
//     customer surfaces that could only render empty against the
//     operator listener. The same run listed all thirteen of those
//     files under "scaffold-once file(s) … left absent on purpose", so
//     forge contradicted itself within one generate.
//   - NEVER WRITTEN. A new org_member_grants entity produced
//     /org-member-grants, for which this frontend has no page at all
//     and the scaffold ledger has no entry.
//
// So the test is existence, not provenance: no page, no link to add.
func TestValidateGeneratedProject_RouteWithNoPageIsNotUnlinked(t *testing.T) {
	projectDir := t.TempDir()
	cfg := &config.ProjectConfig{
		Name:      "demo",
		Frontends: []config.FrontendConfig{{Name: "web", Type: "nextjs"}},
	}
	services := unlinkedRoutesFixtureServices()
	entities := unlinkedRoutesFixtureEntities()

	cs := &checksums.FileChecksums{}
	if err := generateFrontendNav(cfg, services, projectDir, entities, cs); err != nil {
		t.Fatalf("generateFrontendNav: %v", err)
	}

	// The user takes over the nav and drops the /customers link...
	navPath := filepath.Join(projectDir, "frontends", "web", "src", "components", "nav.tsx")
	if err := os.WriteFile(navPath, []byte("// hand-rolled nav, no /customers link\nexport const ALL_ROUTES = [];\n"), 0o644); err != nil {
		t.Fatalf("simulate user edit: %v", err)
	}

	// ...because there is no page behind it. No page is written here at
	// all, which covers BOTH ways a route goes missing: the user deleted
	// it, and forge never scaffolded it in the first place. Those look
	// identical to the person reading the advice, because both make
	// "add it to ALL_ROUTES" produce a 404.
	for _, w := range validateGeneratedProject(projectDir, cfg, services, entities) {
		if strings.Contains(w, "/customers") {
			t.Errorf("a route with no page must not be reported as unlinked — "+
				"linking to it would be a 404; got: %q", w)
		}
	}
}

// TestValidateGeneratedProject_NeverScaffoldedPageIsNotUnlinked is the
// second half of the case above, isolated: a route the scaffold ledger
// has never heard of. This is what a NEW entity produces in a frontend
// whose pages forge did not write, and it is why the check cannot key
// on the ledger's recorded-and-absent "user deleted it" state — there
// is no ledger entry to consult.
func TestValidateGeneratedProject_NeverScaffoldedPageIsNotUnlinked(t *testing.T) {
	projectDir := t.TempDir()
	cfg := &config.ProjectConfig{
		Name:      "demo",
		Frontends: []config.FrontendConfig{{Name: "web", Type: "nextjs"}},
	}
	services := unlinkedRoutesFixtureServices()
	entities := unlinkedRoutesFixtureEntities()

	cs := &checksums.FileChecksums{}
	if err := generateFrontendNav(cfg, services, projectDir, entities, cs); err != nil {
		t.Fatalf("generateFrontendNav: %v", err)
	}
	navPath := filepath.Join(projectDir, "frontends", "web", "src", "components", "nav.tsx")
	if err := os.WriteFile(navPath, []byte("// hand-rolled nav\nexport const ALL_ROUTES = [];\n"), 0o644); err != nil {
		t.Fatalf("simulate user edit: %v", err)
	}

	if checksums.ScaffoldRecorded(projectDir,
		filepath.Join("frontends", "web", "src", "app", "customers", "page.tsx")) {
		t.Fatal("fixture precondition: the page must NOT be in the scaffold ledger")
	}

	for _, w := range validateGeneratedProject(projectDir, cfg, services, entities) {
		if strings.Contains(w, "/customers") {
			t.Errorf("a route forge never scaffolded a page for must not be "+
				"reported as unlinked; got: %q", w)
		}
	}
}

// writeListPage creates the list page for a slug, which is what makes a
// route real enough to be worth linking.
func writeListPage(t *testing.T, projectDir, slug string) {
	t.Helper()
	page := filepath.Join(projectDir, "frontends", "web", "src", "app", slug, "page.tsx")
	if err := os.MkdirAll(filepath.Dir(page), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", slug, err)
	}
	if err := os.WriteFile(page, []byte("export default function P() { return null }\n"), 0o644); err != nil {
		t.Fatalf("write %s page: %v", slug, err)
	}
}

// TestValidateGeneratedProject_PristineNavStaysSilent is the negative
// control the task requires: a freshly scaffolded, never-touched nav.tsx
// must NOT produce a warning, because forge is still keeping it current —
// it is about to add the route itself, so warning would be wrong on every
// greenfield project. This is the exact suppression reportUnlinkedRoutes
// already had; the promotion to the warnings block must not weaken it.
func TestValidateGeneratedProject_PristineNavStaysSilent(t *testing.T) {
	projectDir := t.TempDir()
	cfg := &config.ProjectConfig{
		Name: "demo",
		Frontends: []config.FrontendConfig{
			{Name: "web", Type: "nextjs"},
		},
	}
	services := unlinkedRoutesFixtureServices()
	entities := unlinkedRoutesFixtureEntities()

	cs := &checksums.FileChecksums{}
	if err := generateFrontendNav(cfg, services, projectDir, entities, cs); err != nil {
		t.Fatalf("generateFrontendNav: %v", err)
	}
	// nav.tsx is untouched since the scaffold wrote it.

	warnings := validateGeneratedProject(projectDir, cfg, services, entities)
	for _, w := range warnings {
		if strings.Contains(w, "nav.tsx") {
			t.Errorf("pristine nav must stay silent; got warning: %q (full: %v)", w, warnings)
		}
	}
}
