package sbom

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/release"
)

func TestIdentify_NamesTheFormatOfEachKindOfDocument(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, doc, format, generator string
	}{
		{"cyclonedx 1.6 tools object",
			`{"bomFormat":"CycloneDX","specVersion":"1.6","metadata":{"tools":{"components":[{"type":"application","author":"anchore","name":"syft","version":"1.57.2"}]}}}`,
			"cyclonedx-json@1.6", "syft@1.57.2"},
		{"cyclonedx 1.4 tools list",
			`{"bomFormat":"CycloneDX","specVersion":"1.4","metadata":{"tools":[{"vendor":"aquasecurity","name":"trivy","version":"0.50.0"}]}}`,
			"cyclonedx-json@1.4", "trivy@0.50.0"},
		{"cyclonedx without tools", `{"bomFormat":"CycloneDX","specVersion":"1.5"}`, "cyclonedx-json@1.5", ""},
		{"spdx 2.3",
			`{"spdxVersion":"SPDX-2.3","SPDXID":"SPDXRef-DOCUMENT","creationInfo":{"creators":["Organization: Anchore, Inc","Tool: syft-1.57.2"]}}`,
			"spdx-json@2.3", "syft-1.57.2"},
		{"spdx 2.2 no tool", `{"spdxVersion":"SPDX-2.2","creationInfo":{"creators":["Person: ci"]}}`, "spdx-json@2.2", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			doc, err := Identify([]byte("\n" + tc.doc + "\n"))
			if err != nil {
				t.Fatalf("Identify: %v", err)
			}
			if doc.Format != tc.format || doc.Generator != tc.generator {
				t.Errorf("got %+v, want format %q generator %q", doc, tc.format, tc.generator)
			}
		})
	}
}

func TestIdentify_RefusesWhatIsNotAnSBOMItCanRecord(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct{ doc, want string }{
		"empty":         {"  ", "empty"},
		"xml":           {`<bom xmlns="http://cyclonedx.org/schema/bom/1.6"/>`, "not a JSON document"},
		"other json":    {`{"name":"app","version":"1.0.0"}`, "neither a CycloneDX"},
		"cyclonedx 2.0": {`{"bomFormat":"CycloneDX","specVersion":"2.0"}`, "not a CycloneDX 1.x"},
		"spdx 3":        {`{"spdxVersion":"SPDX-3.0"}`, "SPDX 2.x"},
	} {
		if _, err := Identify([]byte(tc.doc)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err %v, want one saying %q", name, err, tc.want)
		}
	}
	big := make([]byte, MaxProvidedSize+1)
	for i := range big {
		big[i] = 'x'
	}
	if _, err := Identify(big); err == nil || !strings.Contains(err.Error(), "MiB") {
		t.Errorf("oversized: %v", err)
	}
}

// A source tree is read with syft's directory scan, recorded as lockfile
// evidence, and never cached: it has no one digest to key it by.
func TestGenerate_DirScansTheTreeAndIsNotCached(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fake := &fakeSyft{}
	g := &Generator{Tool: pinnedTool, Runner: fake, Cache: &Cache{Dir: t.TempDir()}}
	for i := 0; i < 2; i++ {
		r := g.Generate(context.Background(), []Target{{Name: "shop", Kind: KindDir, Ref: dir, Version: "v1"}})[0]
		if r.Ref.Status != release.SBOMStatusOK || r.Ref.Source != release.SBOMSourceLockfile || r.Cached {
			t.Fatalf("run %d: %+v", i, r)
		}
	}
	if got := fake.sources(); len(got) != 2 || got[0] != "dir:"+dir {
		t.Errorf("syft sources %v, want two dir scans of %s", got, dir)
	}

	missing := g.Generate(context.Background(), []Target{{Name: "shop", Kind: KindDir, Ref: filepath.Join(dir, "nope")}})[0]
	if missing.Ref.Status != release.SBOMStatusUnavailable || !strings.Contains(missing.Ref.Reason, "not a directory") {
		t.Errorf("missing dir: %+v", missing.Ref)
	}
}
