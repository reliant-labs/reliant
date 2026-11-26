package codegen

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectFallibleConstructor_Infallible(t *testing.T) {
	dir := t.TempDir()
	src := `package foo

func New(deps Deps) *Service {
	return &Service{}
}
`
	if err := os.WriteFile(filepath.Join(dir, "service.go"), []byte(src), 0644); err != nil {
		t.Fatal(err)
	}

	fallible, err := DetectFallibleConstructor(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fallible {
		t.Error("expected infallible constructor, got fallible")
	}
}

func TestDetectFallibleConstructor_Fallible(t *testing.T) {
	dir := t.TempDir()
	src := `package foo

func New(deps Deps) (*Service, error) {
	return &Service{}, nil
}
`
	if err := os.WriteFile(filepath.Join(dir, "service.go"), []byte(src), 0644); err != nil {
		t.Fatal(err)
	}

	fallible, err := DetectFallibleConstructor(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !fallible {
		t.Error("expected fallible constructor, got infallible")
	}
}

func TestDetectFallibleConstructor_NoNewFunction(t *testing.T) {
	dir := t.TempDir()
	src := `package foo

func Create(deps Deps) *Service {
	return &Service{}
}
`
	if err := os.WriteFile(filepath.Join(dir, "service.go"), []byte(src), 0644); err != nil {
		t.Fatal(err)
	}

	fallible, err := DetectFallibleConstructor(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fallible {
		t.Error("expected false when no New function exists")
	}
}

func TestDetectFallibleConstructor_NewInDifferentFile(t *testing.T) {
	dir := t.TempDir()

	// New is not in service.go but in another file
	other := `package foo

type Service struct{}
`
	if err := os.WriteFile(filepath.Join(dir, "types.go"), []byte(other), 0644); err != nil {
		t.Fatal(err)
	}

	main := `package foo

func New(deps Deps) (*Service, error) {
	return nil, nil
}
`
	if err := os.WriteFile(filepath.Join(dir, "constructor.go"), []byte(main), 0644); err != nil {
		t.Fatal(err)
	}

	fallible, err := DetectFallibleConstructor(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !fallible {
		t.Error("expected fallible constructor found in constructor.go")
	}
}

func TestDetectFallibleConstructor_IgnoresTestFiles(t *testing.T) {
	dir := t.TempDir()
	src := `package foo

func New(deps Deps) (*Service, error) {
	return nil, nil
}
`
	// Only in a test file — should not count
	if err := os.WriteFile(filepath.Join(dir, "service_test.go"), []byte(src), 0644); err != nil {
		t.Fatal(err)
	}

	fallible, err := DetectFallibleConstructor(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fallible {
		t.Error("expected false when New is only in test file")
	}
}

func TestDetectFallibleConstructor_IgnoresMethods(t *testing.T) {
	dir := t.TempDir()
	src := `package foo

func (f *Factory) New(deps Deps) (*Service, error) {
	return nil, nil
}
`
	if err := os.WriteFile(filepath.Join(dir, "factory.go"), []byte(src), 0644); err != nil {
		t.Fatal(err)
	}

	fallible, err := DetectFallibleConstructor(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fallible {
		t.Error("expected false when New is a method, not a function")
	}
}

func TestDetectFallibleConstructor_NonexistentDir(t *testing.T) {
	fallible, err := DetectFallibleConstructor("/nonexistent/path/that/does/not/exist")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fallible {
		t.Error("expected false for nonexistent directory")
	}
}

func TestDetectDepsDBField_HasDB(t *testing.T) {
	dir := t.TempDir()
	src := `package api

import "github.com/reliant-labs/forge/pkg/orm"

type Deps struct {
	Logger interface{}
	Config interface{}
	DB     orm.Context
}
`
	if err := os.WriteFile(filepath.Join(dir, "service.go"), []byte(src), 0644); err != nil {
		t.Fatal(err)
	}

	hasDB, err := DetectDepsDBField(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !hasDB {
		t.Error("expected true for Deps with DB orm.Context field")
	}
}

func TestDetectDepsDBField_NoDB(t *testing.T) {
	dir := t.TempDir()
	src := `package api

type Deps struct {
	Logger interface{}
	Config interface{}
}
`
	if err := os.WriteFile(filepath.Join(dir, "service.go"), []byte(src), 0644); err != nil {
		t.Fatal(err)
	}

	hasDB, err := DetectDepsDBField(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hasDB {
		t.Error("expected false for Deps without DB field")
	}
}

func TestDetectDepsDBField_NoDeps(t *testing.T) {
	dir := t.TempDir()
	src := `package api

type Service struct {
	DB interface{}
}
`
	if err := os.WriteFile(filepath.Join(dir, "service.go"), []byte(src), 0644); err != nil {
		t.Fatal(err)
	}

	hasDB, err := DetectDepsDBField(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hasDB {
		t.Error("expected false when struct is not named Deps")
	}
}

func TestDetectDepsDBField_NonexistentDir(t *testing.T) {
	hasDB, err := DetectDepsDBField("/nonexistent/path/that/does/not/exist")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hasDB {
		t.Error("expected false for nonexistent directory")
	}
}

func TestDetectDepsDBField_IgnoresTestFiles(t *testing.T) {
	dir := t.TempDir()
	src := `package api

import "github.com/reliant-labs/forge/pkg/orm"

type Deps struct {
	DB orm.Context
}
`
	// Only in a test file — should not count
	if err := os.WriteFile(filepath.Join(dir, "service_test.go"), []byte(src), 0644); err != nil {
		t.Fatal(err)
	}

	hasDB, err := DetectDepsDBField(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hasDB {
		t.Error("expected false when DB is only in test file")
	}
}

func TestDetectFallibleConstructor_NoReturnValues(t *testing.T) {
	dir := t.TempDir()
	src := `package foo

func New() {
}
`
	if err := os.WriteFile(filepath.Join(dir, "service.go"), []byte(src), 0644); err != nil {
		t.Fatal(err)
	}

	fallible, err := DetectFallibleConstructor(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fallible {
		t.Error("expected false when New has no return values")
	}
}

// A `//forge:constructor` func with another name returning a non-Service
// contract interface: the node type is that interface, never a pointer to it
// (*connectorgrant.Store does not compile).
func TestDetectConstructorType_MarkedConstructorReturningInterface(t *testing.T) {
	dir := t.TempDir()
	src := "package connectorgrant\n\n//forge:contract\ntype Store interface{ Get() }\n\ntype Deps struct{}\n\n//forge:constructor\nfunc NewSQLStore(deps Deps) Store { return nil }\n"
	if err := os.WriteFile(filepath.Join(dir, "contract.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := DetectConstructorType(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != "Store" {
		t.Fatalf("DetectConstructorType = %q, want Store", got)
	}
	if ft := qualifyConstructorType(got, "connectorgrant", "Store"); ft != "connectorgrant.Store" {
		t.Fatalf("node type = %q, want connectorgrant.Store", ft)
	}
}

func TestDetectFallibleConstructor_MarkedConstructor(t *testing.T) {
	dir := t.TempDir()
	src := "package foo\n\ntype Deps struct{}\ntype Service interface{}\n\n//forge:constructor\nfunc Open(deps Deps) (Service, error) { return nil, nil }\n"
	if err := os.WriteFile(filepath.Join(dir, "service.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := DetectFallibleConstructor(dir); err != nil || !got {
		t.Fatalf("DetectFallibleConstructor = %v, %v; want true", got, err)
	}
}
