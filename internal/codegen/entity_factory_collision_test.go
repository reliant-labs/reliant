package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A handler package that already declares a factory's name by hand — here a
// domain type called NewDeployment, as control-plane's deploy handler has —
// must not get a generated New<Entity> of the same name: the package's tests
// stop compiling ("NewDeployment redeclared in this block") and `forge
// generate` fails its compile check. The factory is left out, with a note, and
// every other factory of the package is still emitted.
//
// Mutation that fails it: drop the dropCollidingFactories call in
// GenerateEntityFactories (or make handWrittenDecls return nothing).
func TestEntityFactory_SkipsNamesThePackageDeclaresByHand(t *testing.T) {
	dir := t.TempDir()
	write := func(name, src string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("service.go", "package deploy\n\n// NewDeployment is a domain type, not a test factory.\ntype NewDeployment struct{ OrgID string }\n\nfunc (NewDeployment) Validate() error { return nil }\n")
	// A method name and an external test package's name are not the package's.
	write("helpers.go", "package deploy\n\ntype store struct{}\n\nfunc (store) NewEnvironment() {}\n")
	write("ext_test.go", "package deploy_test\n\nfunc NewWidget() {}\n")
	// forge's own previous output is not a hand-written declaration.
	write(entityFactoryFile, "package deploy\n\nfunc NewRelease() {}\n")

	taken := handWrittenDecls(dir, "deploy")
	if !taken["NewDeployment"] || !taken["store"] {
		t.Fatalf("handWrittenDecls = %v, want NewDeployment and store", taken)
	}
	for _, n := range []string{"NewEnvironment", "NewWidget", "NewRelease"} {
		if taken[n] {
			t.Errorf("handWrittenDecls claims %s, which the package does not declare by hand", n)
		}
	}

	g := factoryGroup{
		pkg: "deploy", pbImport: "example.com/test/gen/deploy/v1",
		specs: []entityFactorySpec{
			{goName: "Deployment", lower: "deployment", rootSQL: "INSERT 1"},
			{goName: "Environment", lower: "environment", rootSQL: "INSERT 2"},
			{goName: "Widget", lower: "widget", rootSQL: "INSERT 3"},
			{goName: "Release", lower: "release", rootSQL: "INSERT 4"},
		},
		creates: []createRequestSpec{{funcName: "NewCreateDeploymentRequest", inputType: "CreateDeploymentRequest", entity: "Deployment"}},
	}
	g, notes := dropCollidingFactories(g, taken)
	var kept []string
	for _, s := range g.specs {
		kept = append(kept, s.goName)
	}
	if strings.Join(kept, ",") != "Environment,Widget,Release" {
		t.Errorf("kept row factories %v, want Environment,Widget,Release", kept)
	}
	if len(g.creates) != 1 {
		t.Errorf("the request factory names nothing the package declares; it must be kept: %+v", g.creates)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "NewDeployment is not generated") || !strings.Contains(notes[0], "by hand") {
		t.Errorf("notes = %q, want one naming NewDeployment", notes)
	}

	out := string(renderEntityFactoryFile("example.com/test/internal/db", g))
	if strings.Contains(out, "func NewDeployment(") || strings.Contains(out, "type DeploymentOverride") {
		t.Errorf("the factories file still declares the package's NewDeployment:\n%s", out)
	}
	if !strings.Contains(out, "func NewEnvironment(") || !strings.Contains(out, "func NewCreateDeploymentRequest(") {
		t.Errorf("the other factories were dropped too:\n%s", out)
	}

	// A request factory whose name the package already declares goes too.
	g2, notes2 := dropCollidingFactories(factoryGroup{creates: []createRequestSpec{{funcName: "NewCreateDeploymentRequest"}}},
		map[string]bool{"NewCreateDeploymentRequest": true})
	if len(g2.creates) != 0 || len(notes2) != 1 {
		t.Errorf("a colliding request factory must be dropped with a note: creates=%v notes=%q", g2.creates, notes2)
	}
}

// An owner-aware request factory that forge could not build a request for
// (its body only fails the test) keeps the owner-aware signature, so the file
// must still declare FactoryOption. It did not: the declaration was gated on a
// factory that had succeeded, and control-plane's deploy handler failed
// `forge generate` with "undefined: FactoryOption".
//
// Mutation that fails it: restore `s.failure == "" &&` in createSpecsOwnerAware.
func TestCreateRequestFactory_FailingOwnerAwareFactoryDeclaresItsOption(t *testing.T) {
	spec := createRequestSpec{
		funcName: "NewCreateDeploymentRequest", inputType: "CreateDeploymentRequest", entity: "Deployment",
		ownerAware: true, failure: "seeding the foreign-key parents failed: a CHECK forge cannot place",
	}
	out := string(renderEntityFactoryFile("example.com/test/internal/db", factoryGroup{
		pkg: "deploy", pbImport: "example.com/test/gen/deploy/v1", creates: []createRequestSpec{spec},
	}))
	if !strings.Contains(out, "opts ...FactoryOption) *pb.CreateDeploymentRequest {") {
		t.Fatalf("the failing owner-aware factory lost its signature:\n%s", out)
	}
	if !strings.Contains(out, "type FactoryOption func(*factoryOptions)") || !strings.Contains(out, "\t\"strings\"\n") {
		t.Errorf("a file whose factory takes FactoryOption must declare it (and import what its helpers use):\n%s", out)
	}
}
