package contract

import (
	"bytes"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/goexec"
)

var updateGolden = flag.Bool("update", false, "rewrite testdata/*/mock_gen.go.golden from the current render")

// TestRenderMock_CallRecordsGolden pins the typed call records' rendered
// shape: the record types, the accessors, and every name the generator
// chose to dodge a clash (testdata/callrecords/contract.go lists the cases).
// Regenerate with `go test ./internal/generator/contract -run
// CallRecordsGolden -update`.
func TestRenderMock_CallRecordsGolden(t *testing.T) {
	cf, err := ParseContract("testdata/callrecords/contract.go")
	if err != nil {
		t.Fatalf("ParseContract: %v", err)
	}
	got, err := renderMock(cf, Options{})
	if err != nil {
		t.Fatalf("renderMock: %v", err)
	}
	golden := filepath.Join("testdata", "callrecords", "mock_gen.go.golden")
	if *updateGolden {
		if err := os.WriteFile(golden, got, 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read golden (regenerate with -update): %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("mock_gen.go render differs from %s (regenerate with -update if intended)\n---- got ----\n%s", golden, got)
	}
}

// TestGenerate_TypedCallRecords is the regression test for asserting a mock
// call's arguments through positional type assertions
// (`calls[0].Args[1].(store.Lease)`), which keep compiling — and assert the
// wrong argument — after a parameter is added. The generated package is
// built and its tagged test (testdata/callrecords/records_test.go) reads
// every argument back through the typed accessors by field name.
func TestGenerate_TypedCallRecords(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: builds and runs the generated package with go test; runs in task test")
	}
	dir := copyTestdata(t, "testdata/callrecords")
	if err := Generate(filepath.Join(dir, "contract.go")); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(buildSandboxGoMod("callrecordstest")), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	writeSandboxGoSum(t, dir)

	cmd := exec.Command("go", "test", "-tags", "callrecords", "./...")
	cmd.Dir = dir
	cmd.Env = goexec.WithTrimpath(append(os.Environ(), "GOFLAGS=-mod=mod"))
	if out, err := cmd.CombinedOutput(); err != nil {
		mock, _ := os.ReadFile(filepath.Join(dir, "mock_gen.go"))
		t.Fatalf("go test of the generated package failed: %v\n%s\n---- mock_gen.go ----\n%s", err, out, mock)
	}
}

func TestGenerateMock_RejectsIncompletePackageScope(t *testing.T) {
	dir := t.TempDir()
	contract := filepath.Join(dir, "contract.go")
	if err := os.WriteFile(contract, []byte("package sample\ntype Service interface { Run() }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Even an inactive platform file can declare a colliding name. Its
	// malformed syntax must not be silently omitted from the scope scan.
	broken := filepath.Join(dir, "platform.go")
	if err := os.WriteFile(broken, []byte("//go:build otherplatform\n\npackage sample\ntype MockServiceRunCall struct{}\nfunc broken(\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mock := filepath.Join(dir, "mock_gen.go")
	original := []byte("package sample\n// existing mock must survive a failed scan\n")
	if err := os.WriteFile(mock, original, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := GenerateMock(contract, Options{}); err == nil || !strings.Contains(err.Error(), broken) {
		t.Fatalf("GenerateMock() error = %v, want parse failure naming %s", err, broken)
	}
	got, err := os.ReadFile(mock)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, original) {
		t.Fatal("failed package scope scan overwrote the existing mock")
	}
}

func TestCollectPackageScopeNames(t *testing.T) {
	dir := t.TempDir()
	for name, source := range map[string]string{
		"platform.go":      "//go:build otherplatform\n\npackage sample\ntype MockServiceRunCall struct{}\n",
		"scope_test.go":    "package sample\nvar reserved int\nconst constant = 1\nfunc helper() {}\n",
		"external_test.go": "package sample_test\ntype External struct{}\n",
		"mock_gen.go":      "package sample\ntype OldRecord struct{}\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(source), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := collectPackageScopeNames(dir, "sample")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"MockServiceRunCall": true, "reserved": true, "constant": true, "helper": true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("package names = %v, want %v", got, want)
	}
	if _, err := collectPackageScopeNames(filepath.Join(dir, "missing"), "sample"); err == nil {
		t.Fatal("missing directory must fail rather than return an incomplete scope")
	}
}

// TestCallFields pins the field-naming rule: a named parameter exports its
// own name, unnamed and blank ones take P<index> after every named field has
// claimed its name, and a variadic is recorded as its slice.
func TestCallFields(t *testing.T) {
	fields := callFields([]ParamDef{
		{Name: "ctx", TypeExpr: "context.Context"},
		{Name: "", TypeExpr: "string"},
		{Name: "_", TypeExpr: "int"},
		{Name: "p1", TypeExpr: "bool"},
		{Name: "jobID", TypeExpr: "string"},
		{Name: "id", TypeExpr: "string"},
		{Name: "ID", TypeExpr: "string"},
		{Name: "_x", TypeExpr: "int"},
		{Name: "rest", TypeExpr: "...any", Variadic: true},
	})
	var got []string
	for _, f := range fields {
		got = append(got, f.Name+" "+f.Type)
	}
	want := []string{
		"Ctx context.Context",
		"P1_ string", // its positional name is the named p1's field
		"P2 int",
		"P1 bool",
		"JobID string",
		"ID string",
		"ID_ string",
		"P7 int",
		"Rest []any",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("callFields() =\n  %q\nwant\n  %q", got, want)
	}
}
