package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/internal/kclrender"
)

// TestResolvePortThroughRender proves the kcl_plugin.forge.resolve_port
// plugin dispatches through kclrender.Run (the shared render seam, which
// calls kclplugin.Register()) AND that KCL composes the resolved port into
// a string declaratively — the whole point of the plugin approach.
func TestResolvePortThroughRender(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.k"), []byte(
		`import kcl_plugin.forge
port = forge.resolve_port("reliant-web", 3000)
admin_url = "http://localhost:${port}/admin"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := kclrender.Run(dir, dir, nil)
	if err != nil {
		t.Fatalf("render with resolve_port: %v", err)
	}

	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal %s: %v", out, err)
	}
	t.Logf("rendered: %s", out)

	if _, ok := m["port"]; !ok {
		t.Fatalf("no resolved port in render output: %s", out)
	}
	url, _ := m["admin_url"].(string)
	if url == "" || url == "http://localhost:/admin" {
		t.Fatalf("resolved port did not compose into admin_url: %q", url)
	}
}
