package scaffold

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A scaffold aimed at a `// forge:unmanaged` service would write into a
// handler package generate never wires (scaffold rpc) or declare a second
// copy of a service that already exists (scaffold service). Every name form
// the commands accept resolves to the same refusal, and the refusal names the
// adoption step rather than just saying no.
func TestRefuseUnmanagedService(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/app\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// No descriptor yet: best-effort, the command proceeds as before.
	if err := refuseUnmanagedService("forge scaffold rpc", dir, "legacy"); err != nil {
		t.Fatalf("no descriptor: want nil, got %v", err)
	}

	if err := os.MkdirAll(filepath.Join(dir, "gen"), 0o755); err != nil {
		t.Fatal(err)
	}
	desc := `{"services":[
	  {"Name":"LegacyService","Package":"services.legacy.v1","ProtoFile":"services/legacy/v1/legacy.proto","Unmanaged":true},
	  {"Name":"BillingService","Package":"services.billing.v1","ProtoFile":"services/billing/v1/billing.proto"}]}`
	if err := os.WriteFile(filepath.Join(dir, "gen", "forge_descriptor.json"), []byte(desc), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"legacy", "LegacyService", "Legacy"} {
		err := refuseUnmanagedService("forge scaffold rpc", dir, name)
		if err == nil {
			t.Errorf("%q: want a refusal for the unmanaged service", name)
			continue
		}
		for _, want := range []string{"LegacyService", "forge:unmanaged", "services/legacy/v1/legacy.proto", "delete the", "internal/handlers/legacy/"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%q: refusal %q does not mention %q", name, err, want)
			}
		}
	}
	if err := refuseUnmanagedService("forge scaffold rpc", dir, "billing"); err != nil {
		t.Errorf("managed service: want nil, got %v", err)
	}
	if err := refuseUnmanagedService("forge scaffold service", dir, "orders"); err != nil {
		t.Errorf("undeclared service: want nil, got %v", err)
	}
}
