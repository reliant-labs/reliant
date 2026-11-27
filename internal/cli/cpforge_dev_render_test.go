package cli

// TEMPORARY: verifies cp-forge's real dev/main.k renders through the new
// runtime — resolve_port allocates host ports and the frontend env_vars
// compose from them. Delete after the cp-forge migration is committed.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCpForgeDevRendersWithResolvePort(t *testing.T) {
	// Dev-machine path to the local cp-forge checkout; defaults to the standard
	// ~/src/reliant-labs/cp-forge layout, overridable via CPFORGE_PROJECT_DIR.
	projectDir := os.Getenv("CPFORGE_PROJECT_DIR")
	if projectDir == "" {
		projectDir = filepath.Join(os.Getenv("HOME"), "src/reliant-labs/cp-forge")
	}
	if _, err := os.Stat(projectDir + "/deploy/kcl/dev"); err != nil {
		t.Skipf("cp-forge not present: %v", err)
	}

	out, err := renderKCLRaw(context.Background(), projectDir, "dev")
	if err != nil {
		t.Fatalf("render cp-forge dev: %v", err)
	}
	ents, err := parseKCLEntities(out)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	want := map[string]bool{"reliant-web": false, "admin-web": false}
	for _, fe := range ents.Frontends {
		t.Logf("frontend %s: port=%d env_vars=%d", fe.Name, fe.Port, len(fe.EnvVars))
		for _, ev := range fe.EnvVars {
			t.Logf("    %s=%s", ev.Name, ev.Value)
		}
		if _, ok := want[fe.Name]; ok {
			want[fe.Name] = true
			if fe.Port < 1024 {
				t.Errorf("%s: expected a resolved port, got %d", fe.Name, fe.Port)
			}
		}
		if fe.Name == "reliant-web" {
			var adminURL string
			for _, ev := range fe.EnvVars {
				if ev.Name == "VITE_ADMIN_URL" {
					adminURL = ev.Value
				}
			}
			if !strings.HasPrefix(adminURL, "http://localhost:") || !strings.HasSuffix(adminURL, "/admin") {
				t.Errorf("reliant-web VITE_ADMIN_URL not composed from resolved port: %q", adminURL)
			}
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("frontend %q missing from render", name)
		}
	}
}
