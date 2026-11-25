package bundle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/release"
	"github.com/reliant-labs/forge/pkg/servicecatalog"
)

func withObservability(files ...ObservabilityFile) BuildInput {
	in := buildFixture()
	in.Observability = files
	return in
}

var sampleObservability = []ObservabilityFile{
	{Path: "observability/dashboards/shop-overview_gen.json", Data: []byte(`{"id":"overview","title":"o","panels":[]}` + "\n")},
	{Path: "observability/dashboards/checkout.json", Data: []byte(`{"id":"checkout","title":"c","panels":[]}` + "\n")},
	{Path: release.BundleMetricsSchemaPath, Data: []byte(`{"schema_version":"forge.metrics/v1","service":"shop","version":"v1","metrics":[]}` + "\n")},
}

func TestObservabilityFilesRideInTheManifestsLayerOutsideEveryClusterTree(t *testing.T) {
	built := mustBuild(t, withObservability(sampleObservability...))
	layer, ok := built.Layer(release.BundleManifestsLayer)
	if !ok {
		t.Fatal("no manifests layer")
	}
	files := unpackLayer(t, layer)
	for _, f := range sampleObservability {
		if got, ok := files[f.Path]; !ok || got != string(f.Data) {
			t.Errorf("%s not carried byte for byte (present=%v)", f.Path, ok)
		}
	}

	// Nothing a Kustomization path can reach: every cluster tree is under
	// manifests/, and the observability files are not.
	for _, tree := range built.Doc.ClusterPaths {
		for name := range files {
			if strings.HasPrefix(name, tree.Path+"/") && strings.HasPrefix(name, release.BundleObservabilityPrefix+"/") {
				t.Errorf("%s is inside the applied tree %s", name, tree.Path)
			}
		}
	}
	// And they are not documents: no cluster tree counts them.
	plain := mustBuild(t, buildFixture())
	if len(plain.Doc.ClusterPaths) != len(built.Doc.ClusterPaths) {
		t.Fatalf("cluster trees changed: %v vs %v", plain.Doc.ClusterPaths, built.Doc.ClusterPaths)
	}
	for i := range plain.Doc.ClusterPaths {
		if plain.Doc.ClusterPaths[i] != built.Doc.ClusterPaths[i] {
			t.Errorf("cluster tree %d: %+v vs %+v", i, plain.Doc.ClusterPaths[i], built.Doc.ClusterPaths[i])
		}
	}
}

func TestObservabilityFilesLeaveTheShapeAndConfigDigestAlone(t *testing.T) {
	plain := mustBuild(t, buildFixture())
	with := mustBuild(t, withObservability(sampleObservability...))
	if plain.Doc.ConfigDigest != with.Doc.ConfigDigest {
		t.Error("the config digest moved with the dashboards; it is a digest of what is deployed")
	}
	if len(plain.Doc.Shape.Objects) != len(with.Doc.Shape.Objects) {
		t.Error("the shape's objects changed")
	}
	// A bundle with different dashboards is a different artifact: its digest
	// is what a promotion names.
	if plain.Digest == with.Digest {
		t.Error("the bundle digest ignores its dashboards, so a promotion could not tell two apart")
	}
}

func TestObservabilityFilesBuildDeterministically(t *testing.T) {
	a := mustBuild(t, withObservability(sampleObservability...))
	reversed := append([]ObservabilityFile(nil), sampleObservability...)
	sort.Slice(reversed, func(i, j int) bool { return reversed[i].Path > reversed[j].Path })
	b := mustBuild(t, withObservability(reversed...))
	if a.Digest != b.Digest {
		t.Errorf("the digest depends on the order the files were handed in:\n %s\n %s", a.Digest, b.Digest)
	}
	la, _ := a.Layer(release.BundleManifestsLayer)
	lb, _ := b.Layer(release.BundleManifestsLayer)
	if !bytes.Equal(la, lb) {
		t.Error("the layers differ")
	}
}

func TestBundleStillWritesExactlyOneLayerWithObservabilityFiles(t *testing.T) {
	built := mustBuild(t, withObservability(sampleObservability...))
	var m struct {
		Layers []struct {
			MediaType string `json:"mediaType"`
		} `json:"layers"`
	}
	if err := jsonUnmarshal(built.Manifest, &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Layers) != 1 || m.Layers[0].MediaType != release.BundleManifestsLayer {
		t.Fatalf("layers = %+v, want the one manifests layer: a second one is invisible to Flux and refused by Fetch", m.Layers)
	}
}

func TestObservabilityPathsAreConfined(t *testing.T) {
	cases := map[string]string{
		"outside the directory": "manifests/prod/001-evil.yaml",
		"traversal":             "observability/../manifests/x.yaml",
		"absolute":              "/observability/x.json",
		"unclean":               "observability//x.json",
		"the directory itself":  "observability",
	}
	for name, p := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Build(t.Context(), withObservability(ObservabilityFile{Path: p, Data: []byte("{}")}))
			if !errors.Is(err, release.ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
		})
	}
	_, err := Build(t.Context(), withObservability(
		ObservabilityFile{Path: "observability/a.json", Data: []byte("{}")},
		ObservabilityFile{Path: "observability/a.json", Data: []byte("{}")},
	))
	if !errors.Is(err, release.ErrInvalid) {
		t.Errorf("a repeated path: err = %v", err)
	}
}

func TestObservabilityFilesAreBounded(t *testing.T) {
	half := bytes.Repeat([]byte("a"), maxObservabilityBytes/2+1)
	two := []ObservabilityFile{{Path: "observability/dashboards/a.json", Data: half}, {Path: "observability/dashboards/b.json", Data: half}}
	if _, err := Build(t.Context(), withObservability(two...)); !errors.Is(err, release.ErrInvalid) {
		t.Errorf("the total over the bound: err = %v", err)
	}
}

// A service catalog grows with its project (reliant's is 2.1 MB compact): one
// over the manifest bound, and one over the old 16 MiB unpack bound, builds,
// validates as a layer, and unpacks — every party agrees on one contract.
func TestALargeServiceCatalogBuildsValidatesAndUnpacks(t *testing.T) {
	for _, components := range []int{4000, 40000} {
		if components > 4000 && testing.Short() {
			t.Skip("an ~19 MB catalog takes seconds to build and scan; runs in task test")
		}
		c := servicecatalog.Catalog{Schema: servicecatalog.Schema, Module: "example.com/big"}
		for len(c.Components) < components {
			c.Components = append(c.Components, servicecatalog.Component{ID: "c", Name: "c", Kind: "service", Type: strings.Repeat("t", 400)})
		}
		raw, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		if len(raw) <= release.ManifestsLayerMaxFileBytes {
			t.Fatalf("fixture is %d bytes; it must exceed the %d-byte manifest bound", len(raw), release.ManifestsLayerMaxFileBytes)
		}
		built, err := Build(t.Context(), withObservability(ObservabilityFile{Path: servicecatalog.BundlePath, Data: raw}))
		if err != nil {
			t.Fatalf("a %d-byte catalog must build: %v", len(raw), err)
		}
		layer, ok := built.Layer(release.BundleManifestsLayer)
		if !ok {
			t.Fatal("no manifests layer")
		}
		if _, err := release.ValidateManifestsLayer(bytes.NewReader(layer)); err != nil {
			t.Fatalf("its layer must validate: %v", err)
		}
		if _, err := Unpack(bytes.NewReader(layer), t.TempDir()); err != nil {
			t.Fatalf("a %d-byte catalog must unpack: %v", len(raw), err)
		}
	}
}

func TestObservabilityFilesPassTheSecretScan(t *testing.T) {
	// The F-13 scan reads every file of the layer as YAML. A dashboard is JSON,
	// which YAML reads; a file that is not either is refused rather than
	// shipped unscanned.
	if _, err := Build(t.Context(), withObservability(ObservabilityFile{Path: "observability/dashboards/x.json", Data: []byte("{not json or yaml")})); err == nil {
		t.Error("an unreadable observability file was packed")
	}
}

// THE INGEST GATE RUNS AT BUILD. forge refuses to cut a bundle the control
// plane would refuse to record, and names the file.
func TestBuildRefusesALayerTheIngestValidatorRefuses(t *testing.T) {
	cases := map[string]struct {
		in   func() BuildInput
		file string
	}{
		"kustomization smuggled in as an observability file": {
			in: func() BuildInput {
				return withObservability(ObservabilityFile{Path: "observability/dashboards/kustomization.json", Data: []byte(`{}`)})
			},
			file: "observability/dashboards/kustomization.json",
		},
		"observability artifact that is not JSON": {
			in: func() BuildInput {
				return withObservability(ObservabilityFile{Path: release.BundleMetricsSchemaPath, Data: []byte(`{"a":`)})
			},
			file: release.BundleMetricsSchemaPath,
		},
		"observability file outside the allowed names": {
			in: func() BuildInput {
				return withObservability(ObservabilityFile{Path: "observability/run.json", Data: []byte(`{}`)})
			},
			file: "observability/run.json",
		},
		"a kustomize configuration rendered as a manifest": {
			in: func() BuildInput {
				in := buildFixture()
				in.Shape.Manifests += "---\n# cluster: gke-prod\napiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nmetadata:\n  name: sneaky\n"
				return in
			},
			file: "-kustomization-sneaky.yaml",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Build(context.Background(), tc.in())
			if !errors.Is(err, release.ErrInvalidManifestsLayer) {
				t.Fatalf("Build accepted an invalid layer, or failed differently: %v", err)
			}
			if !strings.Contains(err.Error(), tc.file) {
				t.Errorf("error does not name %q: %v", tc.file, err)
			}
		})
	}
}

// What forge builds is always a layer the validator accepts: the producer and
// the gate cannot disagree.
func TestBuiltLayerPassesIngestValidation(t *testing.T) {
	built := mustBuild(t, withObservability(sampleObservability...))
	layer, _ := built.Layer(release.BundleManifestsLayer)
	sum, err := release.ValidateManifestsLayer(bytes.NewReader(layer))
	if err != nil {
		t.Fatalf("forge's own bundle fails ingest validation: %v", err)
	}
	if len(sum.Artifacts) != len(sampleObservability) || len(sum.Objects) == 0 {
		t.Errorf("summary = %d objects, %d artifacts", len(sum.Objects), len(sum.Artifacts))
	}
}
