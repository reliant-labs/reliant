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

// TestGenerateFrontendPages_NoUnusedVars pins lint-cleanliness of the
// scaffolded CRUD pages (journey fr-cb84c64912: pristine generated
// pages shipped @typescript-eslint/no-unused-vars warnings).
//
// The two offenders, fixed at the template source:
//
//   - detail page: `const router = useRouter()` is only consumed by the
//     delete flow's onSuccess redirect, but was declared (and imported)
//     unconditionally — every entity without a Delete RPC warned.
//   - create/edit pages: `register` and `formState.errors` are only
//     consumed by the per-field inputs, but were destructured even for
//     zero-field forms.
//
// The fixture is the most common pristine shape: Get/List/Create (the
// scaffold example's RPC set — no update, no delete) with an entity that
// has no projected form fields.
func TestGenerateFrontendPages_NoUnusedVars(t *testing.T) {
	projectDir := t.TempDir()

	cfg := &config.ProjectConfig{
		Name: "demo",
		Frontends: []config.FrontendConfig{
			{Name: "dashboard", Type: "nextjs"},
		},
	}
	services := []codegen.ServiceDef{
		{
			Name:    "ClinicService",
			Package: "demo.v1",
			Methods: []codegen.Method{
				{Name: "ListPatients", InputType: "ListPatientsRequest", OutputType: "ListPatientsResponse"},
				{Name: "GetPatient", InputType: "GetPatientRequest", OutputType: "GetPatientResponse"},
				{Name: "CreatePatient", InputType: "CreatePatientRequest", OutputType: "CreatePatientResponse"},
			},
		},
	}
	entities := []codegen.EntityDef{{Name: "Patient"}}
	cs := &checksums.FileChecksums{}

	if err := generateFrontendPages(cfg, services, projectDir, entities, cs); err != nil {
		t.Fatalf("generateFrontendPages: %v", err)
	}

	appDir := filepath.Join(projectDir, "frontends", "dashboard", "src", "app")

	// Detail page: no delete RPC → no router at all (decl OR import).
	detail := readPageFile(t, filepath.Join(appDir, "patients", "view", "page.tsx"))
	if strings.Contains(detail, "useRouter") {
		t.Errorf("detail page without a Delete RPC must not import/declare useRouter (eslint no-unused-vars on the pristine scaffold); got:\n%s", detail)
	}
	if !strings.Contains(detail, "useEntityIdParam()") {
		t.Errorf("detail page must still read the id, via useEntityIdParam; got:\n%s", detail)
	}

	// Create page: zero form fields → no register/errors destructure.
	create := readPageFile(t, filepath.Join(appDir, "patients", "new", "page.tsx"))
	for _, sym := range []string{"register", "formState"} {
		if strings.Contains(create, sym) {
			t.Errorf("create page with zero form fields must not destructure %q (eslint no-unused-vars on the pristine scaffold); got:\n%s", sym, create)
		}
	}
	if !strings.Contains(create, "handleSubmit") {
		t.Errorf("create page must still wire handleSubmit; got:\n%s", create)
	}
}

// TestGenerateFrontendPages_DeleteFlowKeepsRouter is the inverse guard:
// when the service HAS a Delete RPC, the detail page must keep the
// router wiring its post-delete redirect — the unused-vars fix must not
// have over-pruned the delete flow.
func TestGenerateFrontendPages_DeleteFlowKeepsRouter(t *testing.T) {
	projectDir := t.TempDir()

	cfg := &config.ProjectConfig{
		Name: "demo",
		Frontends: []config.FrontendConfig{
			{Name: "dashboard", Type: "nextjs"},
		},
	}
	services := []codegen.ServiceDef{
		{
			Name:    "ClinicService",
			Package: "demo.v1",
			Methods: []codegen.Method{
				{Name: "ListPatients", InputType: "ListPatientsRequest", OutputType: "ListPatientsResponse"},
				{Name: "GetPatient", InputType: "GetPatientRequest", OutputType: "GetPatientResponse"},
				{Name: "DeletePatient", InputType: "DeletePatientRequest", OutputType: "DeletePatientResponse"},
			},
		},
	}
	entities := []codegen.EntityDef{{Name: "Patient"}}
	cs := &checksums.FileChecksums{}

	if err := generateFrontendPages(cfg, services, projectDir, entities, cs); err != nil {
		t.Fatalf("generateFrontendPages: %v", err)
	}

	detail := readPageFile(t, filepath.Join(projectDir, "frontends", "dashboard", "src", "app", "patients", "view", "page.tsx"))
	for _, want := range []string{"useRouter", "const router = useRouter();", `router.push("/patients")`} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail page WITH a Delete RPC must keep the post-delete redirect; missing %q in:\n%s", want, detail)
		}
	}
}

func readPageFile(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(body)
}
