package kclrender_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/forgecompat"
	"github.com/reliant-labs/forge/internal/kclrender"
	"github.com/reliant-labs/forge/internal/kclvendor"
)

const importsKclMod = "[package]\nname = \"%s\"\nedition = \"v0.11.0\"\nversion = \"0.0.1\"\n\n[dependencies]\n"

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// importsFixture is a cp-shaped consumer importing a reliant-shaped
// producer's deploy/kcl through a go.mod directory replace. Both keep a
// root-level config_gen.k, as every forge project does, so the render proves
// the producer's own `import config_gen` resolves against ITS package root.
func importsFixture(t *testing.T, producerPin, consumerPin string) (consumer string) {
	t.Helper()
	t.Cleanup(kclvendor.SetCacheDirForTest(t.TempDir()))
	root := t.TempDir()
	writeTree(t, filepath.Join(root, "producer"), map[string]string{
		"go.mod":     "module example.com/producer\n\ngo 1.22\n",
		"forge.yaml": "name: producer\nforge_version: " + producerPin + "\n",
		// A dash in the package name, like reliant's "reliant-deploy": the
		// import name is the [forge_projects] key, not this.
		"deploy/kcl/kcl.mod":      strings.Replace(importsKclMod, "%s", "producer-deploy", 1),
		"deploy/kcl/config_gen.k": "image = \"producer-api\"\n",
		"deploy/kcl/workloads.k":  "import forge\nimport config_gen\n\napi_image = config_gen.image\nhas_forge = forge.Bundle is not None\n",
		"deploy/kcl/dev/main.k":   "x = 1\n",
	})
	consumer = filepath.Join(root, "consumer")
	writeTree(t, consumer, map[string]string{
		"go.mod":     "module example.com/consumer\n\ngo 1.22\n\nrequire example.com/producer v1.2.3\n\nreplace example.com/producer => ../producer\n",
		"forge.yaml": "name: consumer\nforge_version: " + consumerPin + "\n",
		"deploy/kcl/kcl.mod": strings.Replace(importsKclMod, "%s", "consumer-deploy", 1) +
			"\n[forge_projects]\nproducer = { module = \"example.com/producer\" }\n",
		"deploy/kcl/config_gen.k": "image = \"consumer-api\"\n",
		"deploy/kcl/prod/main.k": "import producer.workloads as pwl\nimport config_gen\n\n" +
			"theirs = pwl.api_image\nmine = config_gen.image\nforge_ok = pwl.has_forge\n",
	})
	return consumer
}

// TestRunImportsAForgeProjectAtItsGoModLocation is the render half of F6: a
// project's KCL imports another forge project's package, which forge supplies
// from where go.mod resolves that module — read-only, the way the module
// cache holds it — with the imported package's `import forge` bound to this
// binary's module.
func TestRunImportsAForgeProjectAtItsGoModLocation(t *testing.T) {
	const pin = "v0.1.47-0.20261023173404-df09b730b6b6"
	consumer := importsFixture(t, pin, pin)
	producerKCL := filepath.Join(filepath.Dir(consumer), "producer", "deploy", "kcl")
	if err := os.Chmod(producerKCL, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(producerKCL, 0o755) })

	out, err := kclrender.Run(consumer, filepath.Join(consumer, "deploy", "kcl", "prod"), nil)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decode %s: %v", out, err)
	}
	if got["theirs"] != "producer-api" || got["mine"] != "consumer-api" || got["forge_ok"] != true {
		t.Fatalf("each package's config_gen must be its own, and the import's forge must resolve; got %s", out)
	}
	entries, _ := os.ReadDir(producerKCL)
	if len(entries) != 4 { // kcl.mod, config_gen.k, workloads.k, dev/
		t.Errorf("the render wrote into the imported package: %v", entries)
	}
}

func TestRunRefusesAnImportPinnedToAnotherForge(t *testing.T) {
	t.Setenv(forgecompat.SkewOverrideEnv, "")
	consumer := importsFixture(t, "v0.1.40", "v0.1.47")
	_, err := kclrender.Run(consumer, filepath.Join(consumer, "deploy", "kcl", "prod"), nil)
	if err == nil || !strings.Contains(err.Error(), "version skew") || !strings.Contains(err.Error(), "v0.1.40") {
		t.Fatalf("want the skew refusal, got %v", err)
	}
}
