package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withDeleteProjectRoot builds a synthetic project with one service:
// forge.yaml, a handlers/<svc>/ scaffold dir, and a pkg/app/services.go
// registering the service. The service is DISCOVERED from these real sources
// (handler impl + registry) — nothing else declares it. Chdirs in and returns
// root.
func withDeleteProjectRoot(t *testing.T, svc string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "forge.yaml"), []byte("name: x\nmodule_path: example.com/x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Handler scaffold dir with a file inside.
	hdir := filepath.Join(root, "internal", "handlers", svc)
	if err := os.MkdirAll(hdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hdir, "service.go"), []byte("package "+svc+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// services.go registering the service via its serviceRow line.
	appDir := filepath.Join(root, "pkg", "app")
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatal(err)
	}
	servicesGo := "package app\n\nfunc RegisteredServices(app *App) []any {\n\treturn []any{\n\t\tserviceRow" + pascal(svc) + "(app),\n\t}\n}\n"
	if err := os.WriteFile(filepath.Join(appDir, "services.go"), []byte(servicesGo), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	return root
}

// pascal mirrors the ServiceRowFuncName suffix for simple lowercase names.
func pascal(s string) string {
	if s == "" {
		return ""
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// TestDeleteService_RemovesDirAndTombstones verifies the full default
// path: handler dir gone and the services.go serviceRow line replaced by a
// types-only tombstone comment that the registry classifies as TOMBSTONED.
// The inventory is introspected from the real sources, so delete removes the
// CODE; the service simply stops being discovered on the next load.
func TestDeleteService_RemovesDirAndTombstones(t *testing.T) {
	root := withDeleteProjectRoot(t, "reporting")

	if err := runDeleteService("reporting", false, true, true, strings.NewReader("")); err != nil {
		t.Fatalf("runDeleteService: %v", err)
	}

	// The removed handler dir + services.go tombstone (below) are what make
	// the service stop being discovered — there is nothing else to rewrite.

	// handler dir removed.
	if _, err := os.Stat(filepath.Join(root, "internal", "handlers", "reporting")); !os.IsNotExist(err) {
		t.Errorf("handlers/reporting should be removed")
	}

	// services.go: serviceRow line gone, tombstone comment present.
	sg, err := os.ReadFile(filepath.Join(root, "pkg", "app", "services.go"))
	if err != nil {
		t.Fatalf("read services.go: %v", err)
	}
	if strings.Contains(string(sg), "serviceRowReporting(") {
		t.Errorf("serviceRow line should be removed:\n%s", sg)
	}
	if !strings.Contains(string(sg), "reporting") {
		t.Errorf("tombstone comment mentioning reporting should remain:\n%s", sg)
	}

	// Registry must now read the service as TOMBSTONED (types-only).
	reg, rerr := loadServiceRegistry(root)
	if rerr != nil {
		t.Fatalf("loadServiceRegistry: %v", rerr)
	}
	if got := reg.state("reporting"); got != registrationTombstoned {
		t.Errorf("registry state = %v, want tombstoned", got)
	}
}

// TestDeleteService_DryRunChangesNothing verifies --dry-run leaves every
// artifact in place.
func TestDeleteService_DryRunChangesNothing(t *testing.T) {
	root := withDeleteProjectRoot(t, "reporting")

	if err := runDeleteService("reporting", true, true, true, strings.NewReader("")); err != nil {
		t.Fatalf("runDeleteService dry-run: %v", err)
	}

	if _, err := os.Stat(filepath.Join(root, "internal", "handlers", "reporting")); err != nil {
		t.Errorf("dry-run must NOT remove handlers/reporting: %v", err)
	}
	// dry-run must leave the service registration intact: services.go still
	// carries the serviceRow line (the source the service is discovered from).
	sg, err := os.ReadFile(filepath.Join(root, "pkg", "app", "services.go"))
	if err != nil {
		t.Fatalf("read services.go: %v", err)
	}
	if !strings.Contains(string(sg), "serviceRowReporting(") {
		t.Errorf("dry-run must NOT touch services.go registration:\n%s", sg)
	}
}

// TestDeleteService_KeepTypesFalseUnlists verifies --keep-types=false
// removes the serviceRow line WITHOUT leaving a tombstone, so the service
// reverts to unlisted.
func TestDeleteService_KeepTypesFalseUnlists(t *testing.T) {
	root := withDeleteProjectRoot(t, "reporting")

	if err := runDeleteService("reporting", false, true, false, strings.NewReader("")); err != nil {
		t.Fatalf("runDeleteService: %v", err)
	}

	sg, _ := os.ReadFile(filepath.Join(root, "pkg", "app", "services.go"))
	if strings.Contains(string(sg), "serviceRowReporting(") {
		t.Errorf("serviceRow line should be removed:\n%s", sg)
	}
	reg, _ := loadServiceRegistry(root)
	if got := reg.state("reporting"); got != registrationUnlisted {
		t.Errorf("registry state = %v, want unlisted (no tombstone)", got)
	}
}

// TestDeleteService_NotFound errors cleanly when the service is unknown.
func TestDeleteService_NotFound(t *testing.T) {
	withDeleteProjectRoot(t, "reporting")
	err := runDeleteService("nonexistent", false, true, true, strings.NewReader(""))
	if err == nil {
		t.Fatal("expected error for unknown service, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should say not found, got: %v", err)
	}
}

// TestDeleteService_ConfirmAbort verifies a "no" answer aborts without
// changing anything.
func TestDeleteService_ConfirmAbort(t *testing.T) {
	root := withDeleteProjectRoot(t, "reporting")

	if err := runDeleteService("reporting", false, false, true, strings.NewReader("n\n")); err != nil {
		t.Fatalf("runDeleteService: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "internal", "handlers", "reporting")); err != nil {
		t.Errorf("abort must NOT remove handlers/reporting: %v", err)
	}
}
