package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
)

// TestGenerateCmdGroups_CountsDiscoveredWorkers pins the wiring, not the
// naming: generateCmdGroups must hand the workers it discovers on disk to the
// cmd-group generator. codegen's own test proves the naming once the input
// carries them; this one fails if the caller stops passing them, which is
// how the legacy mount surface and cmd groups came to disagree with compose.go.
func TestGenerateCmdGroups_CountsDiscoveredWorkers(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/proj\n\ngo 1.24\n")
	write("forge.yaml", "name: proj\nmodule_path: example.com/proj\n")         // the binary is cmd/proj
	write("internal/app/providers.go", "package app\n\ntype Infra struct{}\n") // legacy composition
	write("cmd/proj/cmd/serve.go", "package cmd\n")
	write("internal/handlers/billing/service.go", "package billing\n\ntype Service struct{}\n")
	write("internal/workers/billing/worker.go", "package billing\n\ntype Worker struct{}\n")

	if err := generateCmdGroups([]codegen.ServiceDef{{Name: "BillingService"}}, dir, nil); err != nil {
		t.Fatalf("generateCmdGroups: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "cmd", "proj", "cmd", "services", "billing_mount_gen.go"))
	if err != nil {
		t.Fatal(err)
	}
	// The method expression, not the bare name: the file's header comment
	// names MountSvcBilling as an example whatever the input was.
	if mount := string(data); !strings.Contains(mount, "= (*app.Components).MountSvcBilling\n") {
		t.Errorf("internal/workers/billing shares the service's package, so compose.go names it SvcBilling; "+
			"the cmd group must call MountSvcBilling:\n%s", mount)
	}
}
