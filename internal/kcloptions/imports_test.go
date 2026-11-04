package kcloptions

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/internal/kclvendor"
)

// TestDiscoverSuppliesForgeProjectImports: an env that imports another forge
// project's KCL ([forge_projects] in kcl.mod) must be as discoverable as the
// render, which supplies that package. Without it ListOptions bails on the
// unresolved import and reports nothing, which callers read as "discovery
// failed".
func TestDiscoverSuppliesForgeProjectImports(t *testing.T) {
	t.Cleanup(kclvendor.SetCacheDirForTest(t.TempDir()))
	root := t.TempDir()
	const mod = "[package]\nname = \"p\"\nversion = \"0.1.0\"\n\n[dependencies]\n"
	files := map[string]string{
		"producer/go.mod":                 "module example.com/producer\n",
		"producer/forge.yaml":             "forge_version: v0.1.47\n",
		"producer/deploy/kcl/kcl.mod":     mod,
		"producer/deploy/kcl/workloads.k": "import forge\nname = \"api\"\n",
		"consumer/go.mod":                 "module example.com/consumer\n\nrequire example.com/producer v1.0.0\n\nreplace example.com/producer => ../producer\n",
		"consumer/forge.yaml":             "forge_version: v0.1.47\n",
		"consumer/deploy/kcl/kcl.mod":     mod + "\n[forge_projects]\nproducer = { module = \"example.com/producer\" }\n",
		"consumer/deploy/kcl/prod/main.k": "import forge\nimport producer.workloads as pwl\n\nregion = option(\"region\", default=\"us\")\nname = pwl.name\n",
	}
	for rel, body := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	opts, discoverable, err := Discover(filepath.Join(root, "consumer"), "prod")
	if err != nil || !discoverable {
		t.Fatalf("Discover = %+v, %v, %v", opts, discoverable, err)
	}
	found := false
	for _, o := range opts {
		found = found || o.Name == "region"
	}
	if !found {
		t.Fatalf("Discover() = %+v, want the env's `region` option", opts)
	}
}
