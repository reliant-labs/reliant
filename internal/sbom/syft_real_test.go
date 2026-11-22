package sbom

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/internal/toolfetch"
	"github.com/reliant-labs/forge/pkg/release"
	"github.com/reliant-labs/forge/pkg/testkit"
)

// The real pinned syft, on a lockfile and on this test's own binary: the
// arguments forge passes are ones syft accepts, and what it writes is the
// CycloneDX 1.6 document the ref records. Runs only where forge's syft is
// already installed (`forge tools install` or any SBOM-generating cut); it
// never downloads one.
func TestGenerate_RealSyft(t *testing.T) {
	testkit.Slow(t, "execs the pinned syft binary")
	installed, ok := toolfetch.Find(toolfetch.Syft)
	if !ok {
		t.Skip("forge's pinned syft is not installed on this machine; this test never downloads it")
	}
	dir := t.TempDir()
	lock := filepath.Join(dir, "package-lock.json")
	if err := os.WriteFile(lock, []byte(`{"name":"site","lockfileVersion":3,"requires":true,"packages":{
		"":{"name":"site","dependencies":{"left-pad":"1.3.0"},"devDependencies":{"vite":"5.0.0"}},
		"node_modules/left-pad":{"version":"1.3.0","resolved":"https://registry.npmjs.org/left-pad/-/left-pad-1.3.0.tgz"},
		"node_modules/vite":{"version":"5.0.0","dev":true,"resolved":"https://registry.npmjs.org/vite/-/vite-5.0.0.tgz"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	g := &Generator{Tool: func(context.Context) (string, string, error) { return installed.Path, installed.Version, nil }}
	results := g.Generate(context.Background(), []Target{
		{Name: "site", Kind: KindLockfile, Ref: lock},
		{Name: "sbom.test", Kind: KindBinary, Ref: self},
	})
	for _, r := range results {
		if !r.Ref.HasSBOM() {
			t.Fatalf("%s: %+v", r.Target.Name, r.Ref)
		}
		var doc struct {
			SpecVersion string `json:"specVersion"`
			Metadata    struct {
				Component struct {
					Name string `json:"name"`
				} `json:"component"`
			} `json:"metadata"`
			Components []struct {
				Name string `json:"name"`
			} `json:"components"`
		}
		if err := json.Unmarshal(r.Bytes, &doc); err != nil {
			t.Fatal(err)
		}
		if doc.SpecVersion != "1.6" || doc.Metadata.Component.Name != r.Target.Name || r.Ref.Format != release.SBOMFormatCycloneDXJSON {
			t.Errorf("%s: spec %q, subject %q, format %q", r.Target.Name, doc.SpecVersion, doc.Metadata.Component.Name, r.Ref.Format)
		}
		if r.Target.Kind == KindLockfile {
			names := map[string]bool{}
			for _, c := range doc.Components {
				names[c.Name] = true
			}
			if !names["left-pad"] || names["vite"] {
				t.Errorf("lockfile SBOM components %v: want left-pad and no dev dependency", names)
			}
		}
	}
}
