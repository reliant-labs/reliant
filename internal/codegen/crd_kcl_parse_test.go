package codegen

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/goexec"
)

// The CRD loader drives controller-tools over REAL Go packages, so these tests
// write a small module to disk and load it. That costs a couple of seconds, so
// they are skipped under -short.
//
// The point of loading real packages (rather than hand-building CRD values, as
// crd_kcl_gen_test.go does) is that these tests pin the PROPERTY THE WHOLE
// GENERATOR EXISTS FOR: the Go type is the single source, so a field's presence
// in the schema must follow from its presence in the struct, with no second
// place to update. A test that hand-built the CRD could not observe that.

// The fixture is a real Go module, because controller-tools type-checks the
// package and needs metav1 to genuinely resolve — without a resolvable
// apimachinery it reports `invalid package name: ""` and finds ZERO kube
// kinds, which would make every assertion below vacuously "pass" while
// proving nothing.
//
// Resolution is borrowed from forge's own module cache rather than the
// network: fixtureRequires below is filled in from forge's go.mod at runtime,
// and the fixture gets a go.sum copied from forge's.
const crdTestModuleTemplate = `module crdfixture

go 1.25

require k8s.io/apimachinery %s
`

// crdTestTypes is a minimal but complete CRD package. %s is the injection
// point the sabotage tests use to add a field.
const crdTestTypes = `// +kubebuilder:object:generate=true
// +groupName=fixture.test
package v1alpha1

import (
	"fmt"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var GroupVersion = schema.GroupVersion{Group: "fixture.test", Version: "v1alpha1"}

type GadgetSpec struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Name string ` + "`json:\"name\"`" + `

	// +kubebuilder:validation:Enum=public;private
	Network string ` + "`json:\"network,omitempty\"`" + `
%s
}

type GadgetStatus struct {
	// +kubebuilder:validation:Enum=Pending;Ready
	Phase string ` + "`json:\"phase,omitempty\"`" + `
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=gd
type Gadget struct {
	metav1.TypeMeta   ` + "`json:\",inline\"`" + `
	metav1.ObjectMeta ` + "`json:\"metadata,omitempty\"`" + `

	Spec   GadgetSpec   ` + "`json:\"spec\"`" + `
	Status GadgetStatus ` + "`json:\"status,omitempty\"`" + `
}

// +kubebuilder:object:root=true
type GadgetList struct {
	metav1.TypeMeta ` + "`json:\",inline\"`" + `
	metav1.ListMeta ` + "`json:\"metadata,omitempty\"`" + `
	Items           []Gadget ` + "`json:\"items\"`" + `
}
`

// apimachineryVersion reads the apimachinery version forge itself depends on,
// so the fixture pins the same one already present in the module cache.
func apimachineryVersion(t *testing.T, forgeRoot string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(forgeRoot, "go.mod"))
	if err != nil {
		t.Fatalf("read forge go.mod: %v", err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) >= 2 && fields[0] == "k8s.io/apimachinery" {
			return fields[1]
		}
	}
	t.Fatal("k8s.io/apimachinery not found in forge's go.mod")
	return ""
}

// writeCRDFixture materialises the fixture project and returns its root.
// extraFields is spliced into GadgetSpec.
func writeCRDFixture(t *testing.T, extraFields string) string {
	t.Helper()
	root := t.TempDir()

	apiDir := filepath.Join(root, "api", "v1alpha1")
	if err := os.MkdirAll(apiDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	write := func(path string, content []byte) {
		if err := os.WriteFile(path, content, 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	forgeRoot := forgeRepoRoot(t)
	write(filepath.Join(root, "go.mod"),
		[]byte(fmt.Sprintf(crdTestModuleTemplate, apimachineryVersion(t, forgeRoot))))

	// Borrow forge's go.sum so the fixture verifies against the same module
	// cache entries that are already downloaded. Without it the load fails
	// with `invalid package name: ""` and finds zero kinds.
	sum, err := os.ReadFile(filepath.Join(forgeRoot, "go.sum"))
	if err != nil {
		t.Fatalf("read forge go.sum: %v", err)
	}
	write(filepath.Join(root, "go.sum"), sum)

	write(filepath.Join(apiDir, "gadget_types.go"),
		[]byte(strings.Replace(crdTestTypes, "%s", extraFields, 1)))

	// apimachinery pulls indirect requirements that the hand-written go.mod
	// above does not name, and the package loader refuses to run against an
	// incomplete module graph. Tidy resolves them entirely from the module
	// cache forge has already populated (GOFLAGS=-mod=mod, no network needed
	// because go.sum came from forge).
	tidy := exec.Command("go", "mod", "tidy")
	tidy.Dir = root
	tidy.Env = goexec.WithTrimpath(append(os.Environ(), "GOFLAGS=-mod=mod"))
	if out, err := tidy.CombinedOutput(); err != nil {
		// FATAL, NOT SKIP. The fixture's go.sum is copied from forge's own a
		// few lines above, so every module this tidy needs is already in the
		// cache forge populated — there is no offline case for it to be
		// inapplicable in. A skip here would make the whole CRD-parse suite
		// report green on the day the fixture template stops matching the
		// module graph, which is exactly the failure this suite exists to
		// catch. See internal/vacuousguard.
		t.Fatalf("resolving the fixture module (go mod tidy: %v)\n%s", err, out)
	}

	return root
}

// generateFixture loads and renders the fixture, returning the KCL module.
func generateFixture(t *testing.T, root string) string {
	t.Helper()
	docs, err := LoadCRDsFromGoTypes(root)
	if err != nil {
		t.Fatalf("LoadCRDsFromGoTypes: %v", err)
	}
	if len(docs) == 0 {
		t.Fatal("no CRDs projected from the fixture package")
	}
	out, err := GenerateCRDKCL(docs, ControllerToolsVersion)
	if err != nil {
		t.Fatalf("GenerateCRDKCL: %v", err)
	}
	return out
}

// TestLoadCRDsFromGoTypes_ProjectsTheDeclaredKind is the baseline: the loader
// finds the CRD and carries its markers through to the schema.
func TestLoadCRDsFromGoTypes_ProjectsTheDeclaredKind(t *testing.T) {
	if testing.Short() {
		t.Skip("loads real Go packages; skipped under -short")
	}
	out := generateFixture(t, writeCRDFixture(t, ""))

	for _, want := range []string{
		"gadget_crd = lambda -> any {",
		`name = "gadgets.fixture.test"`,
		`minLength = 1`,
		`maxLength = 63`,
		`"public"`,
		`"private"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("generated schema missing %q\n--- output ---\n%s", want, out)
		}
	}
}

// TestLoadCRDsFromGoTypes_NewGoFieldAppearsInSchema is the sabotage test for
// the PRUNE HAZARD, in its original direction.
//
// Under the old two-declaration design, adding a field to the Go struct and
// forgetting the KCL meant the API server silently pruned that field from
// every CR and the controller read a zero value forever — with no error in any
// build, lint or deploy. This test adds a field to the Go type ONLY, and
// asserts it reaches the schema with its validation intact.
func TestLoadCRDsFromGoTypes_NewGoFieldAppearsInSchema(t *testing.T) {
	if testing.Short() {
		t.Skip("loads real Go packages; skipped under -short")
	}

	// Before: the field does not exist anywhere.
	before := generateFixture(t, writeCRDFixture(t, ""))
	if strings.Contains(before, "storageGib") {
		t.Fatal("fixture already declares storageGib; the sabotage would prove nothing")
	}

	const added = `
	// +kubebuilder:validation:Minimum=0
	StorageGib int32 ` + "`json:\"storageGib,omitempty\"`" + `
`
	after := generateFixture(t, writeCRDFixture(t, added))

	if !strings.Contains(after, "storageGib") {
		t.Errorf("a field added to the Go type did NOT appear in the generated schema — "+
			"the API server would prune it from every CR silently.\n--- output ---\n%s", after)
	}
	if !strings.Contains(after, `format = "int32"`) {
		t.Errorf("the added field lost its int32 format:\n%s", after)
	}
}

// TestLoadCRDsFromGoTypes_RemovedGoFieldLeavesSchema is the prune hazard
// INVERTED: a field deleted from the Go type must vanish from the schema.
//
// Left behind, it is "dead schema" — the API server keeps accepting a field no
// controller reads, so a CR can be written with a value that silently does
// nothing. Deriving the schema makes the removal automatic.
func TestLoadCRDsFromGoTypes_RemovedGoFieldLeavesSchema(t *testing.T) {
	if testing.Short() {
		t.Skip("loads real Go packages; skipped under -short")
	}

	const present = `
	// +kubebuilder:validation:Minimum=0
	StorageGib int32 ` + "`json:\"storageGib,omitempty\"`" + `
`
	with := generateFixture(t, writeCRDFixture(t, present))
	if !strings.Contains(with, "storageGib") {
		t.Fatal("precondition failed: storageGib absent while declared in Go")
	}

	// Remove it from the Go type and regenerate.
	without := generateFixture(t, writeCRDFixture(t, ""))
	if strings.Contains(without, "storageGib") {
		t.Errorf("a field REMOVED from the Go type still appears in the schema (dead schema) — "+
			"CRs could still set a value nothing reads.\n--- output ---\n%s", without)
	}
}

// TestLoadCRDsFromGoTypes_Idempotent pins that two loads of the same unchanged
// source produce identical bytes. This is the property the generated-file
// checksum guard depends on; without it every `forge generate` would report
// spurious drift.
func TestLoadCRDsFromGoTypes_Idempotent(t *testing.T) {
	if testing.Short() {
		t.Skip("loads real Go packages; skipped under -short")
	}
	root := writeCRDFixture(t, "")
	first := generateFixture(t, root)
	second := generateFixture(t, root)
	if first != second {
		t.Error("two generations of unchanged source produced different output")
	}
}

// TestLoadCRDsFromGoTypes_NoAPIDirIsNotAnError: most forge projects have no
// operator. They must generate cleanly, emitting nothing.
func TestLoadCRDsFromGoTypes_NoAPIDirIsNotAnError(t *testing.T) {
	docs, err := LoadCRDsFromGoTypes(t.TempDir())
	if err != nil {
		t.Fatalf("a project with no api/ directory must not error, got: %v", err)
	}
	if len(docs) != 0 {
		t.Errorf("expected no CRDs, got %d", len(docs))
	}
}
