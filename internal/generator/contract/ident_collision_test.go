package contract

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/reliant-labs/forge/internal/goexec"
)

// TestGenerate_ParamsNamedLikeBodyIdentifiers is the regression test for a
// contract parameter named after something a generated method body uses —
// reliant's httpaction had to rename an `m` parameter because the mock's
// receiver is `m`, and the decorator had the same defect for its receiver,
// its capture locals, the context package and every package qualifier.
//
// The fixture names its parameters after every one of those. The assertion is
// that the package — contract.go, mock_gen.go, middleware_gen.go — builds and
// that each argument reaches the mock unchanged (testdata's forward_test.go):
// a shadowed parameter can compile and forward the wrong value.
func TestGenerate_ParamsNamedLikeBodyIdentifiers(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: builds and runs the generated package with go test; runs in task test")
	}
	dir := copyTestdata(t, "testdata/identcollision")
	contractPath := filepath.Join(dir, "contract.go")

	if err := Generate(contractPath); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	cf, err := ParseContract(contractPath)
	if err != nil {
		t.Fatalf("ParseContract: %v", err)
	}
	written, err := WriteObservedDecorator(cf, dir, "Service", "NewServiceWithForgeMiddleware",
		"forgeMiddlewareService", "identcollision", nil, Options{})
	if err != nil || !written {
		t.Fatalf("WriteObservedDecorator: written=%v err=%v", written, err)
	}
	// The owned seam the decorator calls. Its scaffolded body has no
	// per-method code, so an empty chain stands in for it here.
	seam := "package identcollision\n\nimport \"github.com/reliant-labs/forge/pkg/observe\"\n\n" +
		"func newObserveChain() *observe.ComponentChain { return observe.NewComponentChain() }\n"
	if err := os.WriteFile(filepath.Join(dir, "observe_chain.go"), []byte(seam), 0o644); err != nil {
		t.Fatalf("write observe_chain.go: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(buildSandboxGoMod("identcollisiontest")), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	writeSandboxGoSum(t, dir)

	cmd := exec.Command("go", "test", "./...")
	cmd.Dir = dir
	cmd.Env = goexec.WithTrimpath(append(os.Environ(), "GOFLAGS=-mod=mod"))
	if out, err := cmd.CombinedOutput(); err != nil {
		mock, _ := os.ReadFile(filepath.Join(dir, "mock_gen.go"))
		decorator, _ := os.ReadFile(filepath.Join(dir, "middleware_gen.go"))
		t.Fatalf("go test of the generated package failed: %v\n%s\n---- mock_gen.go ----\n%s\n---- middleware_gen.go ----\n%s",
			err, out, mock, decorator)
	}
}

// TestWithBodyScopedNames pins the renaming rule itself: a parameter keeps
// its contract name unless the body needs that name, and then (like an
// unnamed or blank one) takes the first positional pN nothing else holds.
func TestWithBodyScopedNames(t *testing.T) {
	m := MethodDef{
		Name: "Do",
		Params: []ParamDef{
			{Name: "ctx", TypeExpr: "context.Context"},
			{Name: "m", TypeExpr: "string"},                    // the receiver
			{Name: "user", TypeExpr: "user.User"},              // its own type's qualifier
			{Name: "_", TypeExpr: "int"},                       // blank
			{Name: "p3", TypeExpr: "int"},                      // the blank's positional name
			{Name: "new", TypeExpr: "bool"},                    // predeclared
			{Name: "id", TypeExpr: "string"},                   // free
			{Name: "rows", TypeExpr: "...any", Variadic: true}, // free, variadic
		},
		Results: []ParamDef{{Name: "u", TypeExpr: "user.User"}, {Name: "err", TypeExpr: "error"}},
	}

	got := m.withBodyScopedNames("m", "contractkit")

	var names []string
	for _, p := range got.Params {
		names = append(names, p.Name)
	}
	want := []string{"ctx", "p1", "p2", "p3_", "p3", "p5", "id", "rows"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("parameter names = %v, want %v", names, want)
	}
	if sig := got.ParamSignature(); sig != "ctx context.Context, p1 string, p2 user.User, p3_ int, p3 int, p5 bool, id string, rows ...any" {
		t.Errorf("ParamSignature() = %q", sig)
	}
	if sig := got.ResultSignature(); sig != "(user.User, error)" {
		t.Errorf("ResultSignature() = %q, want the result names dropped", sig)
	}
	// The contract's own MethodDef is untouched: the copy is the generator's.
	if m.Params[1].Name != "m" || m.Results[0].Name != "u" {
		t.Errorf("withBodyScopedNames mutated its receiver: %+v", m)
	}
}
