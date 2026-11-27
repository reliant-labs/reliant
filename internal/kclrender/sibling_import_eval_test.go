package kclrender_test

// sibling_import_eval_test.go pins the idiom kcl/ml.k documents and
// `forge ml discover` writes: an env's ML stack in its own file beside
// main.k (deploy/kcl/<env>/ml.k), imported with `import .ml`. forge renders
// an env both ways — some callers pass the main.k file, others the env
// directory, which compiles every .k in it as one package — so the idiom must
// hold under both. (Under the directory form ml.k's public `ml_stacks` also
// appears beside `output`, as every public top-level var of the env's files
// does; every reader takes `output`.)

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/internal/kclrender"
	"github.com/reliant-labs/forge/internal/kclvendor"
)

func TestRunImportsASiblingFileOfTheEnv(t *testing.T) {
	if testing.Short() {
		t.Skip("evaluates KCL; runs in task test")
	}
	t.Cleanup(kclvendor.SetCacheDirForTest(t.TempDir()))
	dir := t.TempDir()
	for rel, body := range map[string]string{
		"deploy/kcl/kcl.mod": "[package]\nname = \"sibling_probe\"\nedition = \"v0.11.0\"\nversion = \"0.0.1\"\n\n[dependencies]\n",
		"deploy/kcl/prod/ml.k": `import forge

ml_stacks = [forge.MLStack {
    cluster = forge.ClusterTarget {cluster = "gke_acme_gpu", namespace = "forge-ml"}
    queue = forge.Adopt {kind = "kueue", namespace = "kueue-system"}
}]
`,
		"deploy/kcl/prod/main.k": `import forge
import .ml

output = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    ml_stacks = ml.ml_stacks
})
`,
	} {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	envDir := filepath.Join(dir, "deploy", "kcl", "prod")
	for name, source := range map[string]string{"main.k": filepath.Join(envDir, "main.k"), "env dir": envDir} {
		t.Run(name, func(t *testing.T) {
			out, err := kclrender.Run(dir, source, []string{"env=prod"})
			if err != nil {
				t.Fatalf("render: %v", err)
			}
			var doc struct {
				Output struct {
					MLStacks []struct {
						Cluster string `json:"cluster"`
					} `json:"ml_stacks"`
				} `json:"output"`
			}
			if err := json.Unmarshal(out, &doc); err != nil {
				t.Fatalf("decode: %v\n%s", err, out)
			}
			if len(doc.Output.MLStacks) != 1 || doc.Output.MLStacks[0].Cluster != "gke_acme_gpu" {
				t.Errorf("the sibling ml.k did not reach the render:\n%s", out)
			}
		})
	}
}
