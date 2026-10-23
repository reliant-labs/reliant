// Regression tests for the test-helper naming defect: ComputeTestHelperName
// keyed off DIRECTORY EXISTENCE while the helpers generator keyed off WIRED
// COMPONENTS, so the two disagreed on the same identifier.
//
// THE BUG. `app.NewTest<X>` gets a "Svc" prefix when a handler service's
// package name collides with a wired internal component of the same name
// (service `billing` + component `internal/billing`). Two generators derive
// that name, and they derived it from different sources:
//
//	helpers generator      the WIRED component set — a package is in the
//	                       namespace only if it is a real component
//	                       (contract.go, not excluded, not external)
//	ComputeTestHelperName  os.Stat(internal/<pkg>) — ANY directory
//
// A types-only package is a perfectly ordinary shape (internal/db,
// internal/deploy): it carries `//forge:exclude-contract` because it has no
// component to construct. The helpers generator correctly ignored it; the
// scaffold-test generator saw the bare directory and inferred a collision.
// Result in control-plane: scaffold tests referenced NewTestSvcDeploy while
// testing.go declared NewTestDeploy — sixteen generated files against an
// undefined symbol, and a package that would not compile.
//
// The nastiest part is that hand-fixing does not hold: ReconcileScaffoldTestHelperName
// re-derives the name from ComputeTestHelperName on every generate, so a
// renamed file is silently rewritten back. TestReconcile_DoesNotRewriteBack
// pins that.
//
// THE FIX. Ask "is this a wired component", via the shared
// IsWiredComponentDir predicate that the helpers generator's own filters now
// route through — one implementation, so the two cannot disagree again.
package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/naming"
)

// wiredComponentSrc is the canonical component shape: the Service/Deps/New
// triple. A package with this contract.go IS a wired component.
const wiredComponentSrc = `package %s

type Service interface{ Do() error }

type Deps struct{}

func New(d Deps) Service { return nil }
`

// writeTypesOnlyPkg lays down the shape at the heart of the defect: a
// directory under internal/ with types but no component to construct, opted
// out of contract codegen exactly as forge documents.
func writeTypesOnlyPkg(t *testing.T, projectDir, name string) {
	t.Helper()
	dir := filepath.Join(projectDir, "internal", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", name, err)
	}
	src := "//forge:exclude-contract\npackage " + name + `

// Target is a plain value type. There is no Service, no Deps and no New —
// nothing for forge to wire.
type Target struct {
	Name string
}
`
	if err := os.WriteFile(filepath.Join(dir, "types.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("write %s/types.go: %v", name, err)
	}
}

func writeWiredPkg(t *testing.T, projectDir, name string) {
	t.Helper()
	dir := filepath.Join(projectDir, "internal", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", name, err)
	}
	src := strings.Replace(wiredComponentSrc, "%s", name, 1)
	if err := os.WriteFile(filepath.Join(dir, "contract.go"), []byte(src), 0o644); err != nil {
		t.Fatalf("write %s/contract.go: %v", name, err)
	}
}

// TestComputeTestHelperName_TypesOnlyPackageIsNotACollision is the core
// reproduction. internal/deploy is types-only, so it is NOT in the helpers
// generator's factory namespace and must NOT drive a Svc prefix.
func TestComputeTestHelperName_TypesOnlyPackageIsNotACollision(t *testing.T) {
	projectDir := t.TempDir()
	writeTypesOnlyPkg(t, projectDir, "deploy")

	if got := ComputeTestHelperName("deploy", projectDir); got != "Deploy" {
		t.Errorf("ComputeTestHelperName(\"deploy\") = %q, want %q — a types-only "+
			"package has no NewTest<Pkg> factory, so it cannot collide with one", got, "Deploy")
	}
}

// TestComputeTestHelperName_BareDirectoryIsNotACollision covers the same rule
// for a directory with no Go component at all (internal/db-shaped: helpers,
// SQL, embedded assets). Existence alone is not evidence of a component.
func TestComputeTestHelperName_BareDirectoryIsNotACollision(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectDir, "internal", "assets"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if got := ComputeTestHelperName("assets", projectDir); got != "Assets" {
		t.Errorf("ComputeTestHelperName(\"assets\") = %q, want %q — an empty "+
			"directory is not a wired component", got, "Assets")
	}
}

// TestComputeTestHelperName_WiredComponentStillCollides is the guard against
// over-correcting: a genuine wired component must STILL produce the Svc
// prefix, or the disambiguation this rule exists for is lost.
func TestComputeTestHelperName_WiredComponentStillCollides(t *testing.T) {
	projectDir := t.TempDir()
	writeWiredPkg(t, projectDir, "billing")

	if got := ComputeTestHelperName("billing", projectDir); got != "SvcBilling" {
		t.Errorf("ComputeTestHelperName(\"billing\") = %q, want %q — a wired "+
			"component DOES collide and must keep the Svc prefix", got, "SvcBilling")
	}
}

// TestComputeTestHelperName_AgreesWithHelpersNamespace is the durable half of
// the fix: assert the two generators derive the same answer from the same
// predicate, across every package shape, rather than pinning two constants
// that could drift apart again.
func TestComputeTestHelperName_AgreesWithHelpersNamespace(t *testing.T) {
	projectDir := t.TempDir()
	writeWiredPkg(t, projectDir, "billing")    // wired      -> in namespace
	writeTypesOnlyPkg(t, projectDir, "deploy") // types-only -> not in namespace
	writeWiredPkg(t, projectDir, "user")
	// internal/user is wired but hand-constructed in providers.go.
	if err := os.WriteFile(filepath.Join(projectDir, "internal", "user", "contract.go"),
		[]byte("//forge:external-component\n"+strings.Replace(wiredComponentSrc, "%s", "user", 1)), 0o644); err != nil {
		t.Fatalf("write user contract: %v", err)
	}

	for _, pkg := range []string{"billing", "deploy", "user", "absent"} {
		// The helpers generator's namespace, derived the way it derives it.
		inNamespace := len(filterWiredComponentPackages(projectDir, []BootstrapPackageData{
			{Name: pkg, Package: pkg, ImportPath: pkg},
		})) == 1 && IsWiredComponentDir(filepath.Join(projectDir, "internal", pkg))

		want := naming.ToPascalCase(pkg)
		if inNamespace {
			want = "Svc" + want
		}
		if got := ComputeTestHelperName(pkg, projectDir); got != want {
			t.Errorf("ComputeTestHelperName(%q) = %q, but the helpers namespace implies %q "+
				"(in namespace: %v) — the two generators disagree, which is the defect",
				pkg, got, want, inNamespace)
		}
	}
}

// TestReconcile_DoesNotRewriteBackCorrectName pins the silent-revert behavior
// that made this defect so expensive: an agent renamed sixteen files by hand
// and the next `forge generate` rewrote them back. With the naming rule fixed,
// reconciliation must be a NO-OP on a correctly-named scaffold test.
func TestReconcile_DoesNotRewriteBackCorrectName(t *testing.T) {
	projectDir := t.TempDir()
	writeTypesOnlyPkg(t, projectDir, "deploy")

	handlerDir := filepath.Join(projectDir, "internal", "handlers", "deploy")
	if err := os.MkdirAll(handlerDir, 0o755); err != nil {
		t.Fatalf("mkdir handler: %v", err)
	}
	// The scaffold test as the helpers generator's factory name implies it.
	scaffold := `package deploy

// FORGE_SCAFFOLD: deploy
import "testing"

func TestDeployScaffold(t *testing.T) {
	_ = NewTestDeploy(t)
}
`
	testPath := filepath.Join(handlerDir, "service_scaffold_test.go")
	if err := os.WriteFile(testPath, []byte(scaffold), 0o644); err != nil {
		t.Fatalf("write scaffold test: %v", err)
	}

	rewrote, err := ReconcileScaffoldTestHelperName(projectDir, "deploy", handlerDir)
	if err != nil {
		t.Fatalf("ReconcileScaffoldTestHelperName: %v", err)
	}
	if rewrote {
		t.Error("reconcile rewrote a correctly-named scaffold test — this is the " +
			"silent-revert that undid a hand-fix on the next generate")
	}

	got, err := os.ReadFile(testPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "NewTestSvcDeploy") {
		t.Errorf("reconcile rewrote NewTestDeploy -> NewTestSvcDeploy, an undefined symbol:\n%s", got)
	}
}
