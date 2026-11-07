package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestFrontendEnvVarsRoundTrip exercises the whole #2 path at once: the
// forge.Frontend.env_vars schema field, _render_frontend emitting it,
// FrontendEntity.EnvVars parsing it — AND that a value composed
// declaratively from forge.resolve_port(...) flows through intact. It
// renders a minimal bundle against the in-tree forge KCL module via the
// real render seam (kpm + plugin).
func TestFrontendEnvVarsRoundTrip(t *testing.T) {
	forgeKcl, err := filepath.Abs("../../kcl")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(forgeKcl, "schema.k")); err != nil {
		t.Skipf("forge kcl module not found at %s: %v", forgeKcl, err)
	}

	root := t.TempDir()
	kclParent := filepath.Join(root, "deploy", "kcl")
	devDir := filepath.Join(kclParent, "dev")
	if err := os.MkdirAll(devDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mod := "[package]\nname = \"t\"\nedition = \"v0.11.4\"\n\n[dependencies]\n"
	if err := os.WriteFile(filepath.Join(kclParent, "kcl.mod"), []byte(mod), 0o644); err != nil {
		t.Fatal(err)
	}
	main := `import forge
import kcl_plugin.forge as fp
_port = fp.resolve_port("reliant-web", 3000)
_bundle = forge.Bundle {
    project = "acme"
    frontends = [forge.Frontend {
        name = "reliant-web"
        type = "vite"
        path = "web"
        port = _port
        env_vars = [forge.EnvVar { name = "VITE_ADMIN_URL", value = "http://localhost:${_port}/admin" }]
        runtime = forge.OnHost {}
    }]
}
output = forge.render(_bundle)
`
	if err := os.WriteFile(filepath.Join(devDir, "main.k"), []byte(main), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := renderKCLRaw(context.Background(), root, "dev")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	ents, err := parseKCLEntities(out)
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, out)
	}
	if len(ents.Frontends) != 1 {
		t.Fatalf("want 1 frontend, got %d: %s", len(ents.Frontends), out)
	}
	fe := ents.Frontends[0]
	if len(fe.EnvVars) != 1 {
		t.Fatalf("want 1 env_var on frontend, got %d: %s", len(fe.EnvVars), out)
	}
	ev := fe.EnvVars[0]
	if ev.Name != "VITE_ADMIN_URL" {
		t.Errorf("env var name = %q, want VITE_ADMIN_URL", ev.Name)
	}
	want := fmt.Sprintf("http://localhost:%d/admin", fe.Port)
	if ev.Value != want {
		t.Errorf("env var value = %q, want %q (resolved port %d)", ev.Value, want, fe.Port)
	}
	t.Logf("frontend %q port=%d env=%s=%s", fe.Name, fe.Port, ev.Name, ev.Value)
}
