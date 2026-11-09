package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeHandlerFiles writes files (name → body) into a fresh handler dir and
// returns the project dir and the handler dir.
func writeHandlerFiles(t *testing.T, files map[string]string) (string, string) {
	t.Helper()
	projectDir := t.TempDir()
	dir := filepath.Join(projectDir, "internal", "handlers", "orders")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return projectDir, dir
}

// A handler dir someone started by hand — an rpc method on *Service and
// nothing else — gets the service.go that declares Service, Deps and New, in
// the package its files already use; the hand-written file is untouched.
func TestEnsureServiceFileCompletesAHandStartedPackage(t *testing.T) {
	rpc := "package ordersvc\n\n// Ping is hand-written.\nfunc (s *Service) Ping() {}\n"
	projectDir, dir := writeHandlerFiles(t, map[string]string{"rpc_ping.go": rpc})

	wrote, found, err := EnsureServiceFile(threeCustomRPCs(), projectDir, dir)
	if err != nil || !wrote || len(found) != 0 {
		t.Fatalf("EnsureServiceFile = wrote %v, found %v, err %v; want it to write service.go", wrote, found, err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "service.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"package ordersvc", "type Deps struct", "type Service struct", "func New(deps Deps)"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("service.go lacks %q:\n%s", want, got)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "rpc_ping.go")); string(b) != rpc {
		t.Errorf("the hand-written rpc_ping.go changed:\n%s", b)
	}
}

// It never writes over a service.go, and never one that would redeclare a
// Service, Deps or New the package declares in another file.
func TestEnsureServiceFileNeverOverwritesOrRedeclares(t *testing.T) {
	t.Run("service.go exists", func(t *testing.T) {
		mine := "package orders\n\n// mine\ntype Service struct{}\n"
		projectDir, dir := writeHandlerFiles(t, map[string]string{"service.go": mine})
		if wrote, _, err := EnsureServiceFile(threeCustomRPCs(), projectDir, dir); err != nil || wrote {
			t.Fatalf("wrote %v err %v, want no write", wrote, err)
		}
		if b, _ := os.ReadFile(filepath.Join(dir, "service.go")); string(b) != mine {
			t.Errorf("service.go was rewritten:\n%s", b)
		}
	})
	t.Run("Service declared elsewhere", func(t *testing.T) {
		projectDir, dir := writeHandlerFiles(t, map[string]string{
			"handler.go": "package orders\n\ntype Service struct{}\n\nfunc New() *Service { return &Service{} }\n",
		})
		wrote, found, err := EnsureServiceFile(threeCustomRPCs(), projectDir, dir)
		if err != nil || wrote {
			t.Fatalf("wrote %v err %v, want no write", wrote, err)
		}
		if found["Service"] != "handler.go" || found["New"] != "handler.go" || DescribeDecls(found) != "New in handler.go, Service in handler.go" {
			t.Errorf("found = %v, want Service and New in handler.go", found)
		}
		if _, err := os.Stat(filepath.Join(dir, "service.go")); !os.IsNotExist(err) {
			t.Errorf("service.go was written beside a hand-written Service")
		}
	})
}
