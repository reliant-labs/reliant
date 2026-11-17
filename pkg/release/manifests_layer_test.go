package release

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// layerEntry is one tar entry, built in-test. A zero Type is a regular file.
type layerEntry struct {
	Name string
	Body string
	Type byte
	Link string
}

func buildLayer(t *testing.T, entries ...layerEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		typ := e.Type
		if typ == 0 {
			typ = tar.TypeReg
		}
		hdr := &tar.Header{Name: e.Name, Typeflag: typ, Mode: 0o644, Linkname: e.Link}
		if typ == tar.TypeReg {
			hdr.Size = int64(len(e.Body))
		}
		if typ == tar.TypeDir {
			hdr.Mode = 0o755
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write header %q: %v", e.Name, err)
		}
		if typ == tar.TypeReg {
			if _, err := tw.Write([]byte(e.Body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func manifestYAML(kind, name string) string {
	return fmt.Sprintf("apiVersion: v1\nkind: %s\nmetadata:\n  name: %s\n", kind, name)
}

func dir(name string) layerEntry { return layerEntry{Name: name, Type: tar.TypeDir} }

func validate(t *testing.T, layer []byte) (ManifestsLayerSummary, error) {
	t.Helper()
	return ValidateManifestsLayer(bytes.NewReader(layer))
}

func TestValidateManifestsLayerAcceptsTheV1Layout(t *testing.T) {
	sum, err := validate(t, buildLayer(t,
		dir("manifests"), dir("manifests/gke_p_r_c"),
		layerEntry{Name: "manifests/gke_p_r_c/000-namespace-app.yaml", Body: manifestYAML("Namespace", "app")},
		layerEntry{Name: "manifests/gke_p_r_c/001-configmap-cfg.yaml", Body: "---\n" + manifestYAML("ConfigMap", "cfg") + "data:\n  a: \"1\"\n"},
		dir("manifests/_"),
		layerEntry{Name: "manifests/_/000-namespace-x.yaml", Body: manifestYAML("Namespace", "x")},
	))
	if err != nil {
		t.Fatalf("v1 layout refused: %v", err)
	}
	if len(sum.Objects) != 3 || sum.Files != 3 {
		t.Fatalf("summary = %+v", sum)
	}
	o := sum.Objects[0]
	if o.Cluster != "_" || o.Unit != "" || o.Phase != "" || o.Kind != "Namespace" || o.Name != "x" {
		t.Errorf("first object = %+v", o)
	}
}

func TestValidateManifestsLayerAcceptsTheV2Layout(t *testing.T) {
	sum, err := validate(t, buildLayer(t,
		layerEntry{Name: "manifests/hub/base/pre/000-ns.yaml", Body: manifestYAML("Namespace", "app")},
		layerEntry{Name: "manifests/hub/infra/pre/000-job.yaml", Body: "apiVersion: batch/v1\nkind: Job\nmetadata:\n  name: migrate-abc\n  namespace: app\n"},
		layerEntry{Name: "manifests/hub/infra/rollout/000-dep.yaml", Body: "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: api\n  namespace: app\n"},
		layerEntry{Name: "manifests/hub/infra/post/000-job.yaml", Body: "apiVersion: batch/v1\nkind: Job\nmetadata:\n  name: seed\n"},
		layerEntry{Name: "manifests/daemon/substrate/rollout/000-ds.yaml", Body: "apiVersion: apps/v1\nkind: DaemonSet\nmetadata:\n  name: prepull\n"},
	))
	if err != nil {
		t.Fatalf("v2 layout refused: %v", err)
	}
	var got []string
	for _, o := range sum.Objects {
		got = append(got, o.Cluster+"/"+o.Unit+"/"+o.Phase)
	}
	want := "daemon/substrate/rollout hub/base/pre hub/infra/post hub/infra/pre hub/infra/rollout"
	if strings.Join(got, " ") != want {
		t.Errorf("objects = %v, want %s", got, want)
	}
	for _, o := range sum.Objects {
		if o.Path == "manifests/hub/infra/pre/000-job.yaml" && o.Namespace != "app" {
			t.Errorf("namespace not read: %+v", o)
		}
	}
}

func TestValidateManifestsLayerAcceptsTheObservabilityArtifacts(t *testing.T) {
	sum, err := validate(t, buildLayer(t,
		layerEntry{Name: "manifests/hub/000-ns.yaml", Body: manifestYAML("Namespace", "app")},
		dir("observability"), dir("observability/dashboards"),
		layerEntry{Name: "observability/dashboards/shop-overview_gen.json", Body: `{"id":"overview","panels":[]}`},
		layerEntry{Name: "observability/dashboards/checkout.json", Body: `{"id":"checkout"}`},
		layerEntry{Name: "observability/metrics-schema.json", Body: `{"schema_version":"forge.metrics/v1","metrics":[]}`},
	))
	if err != nil {
		t.Fatalf("artifacts refused: %v", err)
	}
	if len(sum.Objects) != 1 || len(sum.Artifacts) != 3 {
		t.Fatalf("artifacts must be data, never manifests: %+v", sum)
	}
}

func TestValidateManifestsLayerRefusals(t *testing.T) {
	good := layerEntry{Name: "manifests/hub/000-ns.yaml", Body: manifestYAML("Namespace", "app")}
	cases := []struct {
		name    string
		entries []layerEntry
		file    string // must appear in the error
		reason  string // substring of the reason
	}{
		{"path traversal", []layerEntry{good, {Name: "manifests/hub/../../etc/x.yaml", Body: manifestYAML("Namespace", "x")}}, "manifests/hub/../../etc/x.yaml", "`..`"},
		{"leading traversal", []layerEntry{{Name: "../escape.yaml", Body: manifestYAML("Namespace", "x")}}, "../escape.yaml", "`..`"},
		{"absolute path", []layerEntry{{Name: "/etc/cron.d/x.yaml", Body: manifestYAML("Namespace", "x")}}, "/etc/cron.d/x.yaml", "absolute"},
		{"dot segment", []layerEntry{{Name: "manifests/./hub/000-a.yaml", Body: manifestYAML("Namespace", "x")}}, "manifests/./hub/000-a.yaml", "`.` segment"},
		{"backslash", []layerEntry{{Name: `manifests\hub\000-a.yaml`, Body: manifestYAML("Namespace", "x")}}, `manifests\hub\000-a.yaml`, "backslash"},
		{"symlink", []layerEntry{good, {Name: "manifests/hub/001-link.yaml", Type: tar.TypeSymlink, Link: "/etc/passwd"}}, "manifests/hub/001-link.yaml", "symlink"},
		{"hardlink", []layerEntry{good, {Name: "manifests/hub/001-link.yaml", Type: tar.TypeLink, Link: "manifests/hub/000-ns.yaml"}}, "manifests/hub/001-link.yaml", "hardlink"},
		{"device", []layerEntry{good, {Name: "manifests/hub/001-dev.yaml", Type: tar.TypeChar}}, "manifests/hub/001-dev.yaml", "character device"},
		{"fifo", []layerEntry{good, {Name: "manifests/hub/001-fifo.yaml", Type: tar.TypeFifo}}, "manifests/hub/001-fifo.yaml", "fifo"},
		{"kustomization at the root of a tree", []layerEntry{good, {Name: "manifests/hub/kustomization.yaml", Body: "resources: []\n"}}, "manifests/hub/kustomization.yaml", "kustomization"},
		{"nested kustomization", []layerEntry{good, {Name: "manifests/hub/infra/rollout/kustomization.yml", Body: "resources: []\n"}}, "manifests/hub/infra/rollout/kustomization.yml", "kustomization"},
		{"extensionless Kustomization", []layerEntry{good, {Name: "manifests/hub/infra/rollout/Kustomization", Body: "resources: []\n"}}, "manifests/hub/infra/rollout/Kustomization", "kustomization"},
		{"mixed-case kustomization", []layerEntry{good, {Name: "manifests/hub/KUSTOMIZATION.YAML", Body: "resources: []\n"}}, "manifests/hub/KUSTOMIZATION.YAML", "kustomization"},
		{"kustomization hiding in observability", []layerEntry{good, {Name: "observability/dashboards/kustomization.json", Body: "{}"}}, "observability/dashboards/kustomization.json", "kustomization"},
		{"kustomize object under a normal name", []layerEntry{{Name: "manifests/hub/000-k.yaml", Body: "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nmetadata:\n  name: k\n"}}, "manifests/hub/000-k.yaml", "kustomize configuration"},
		{"oversize file", []layerEntry{good, {Name: "manifests/hub/001-big.yaml", Body: manifestYAML("ConfigMap", "big") + "data:\n  x: " + strings.Repeat("a", ManifestsLayerMaxFileBytes) + "\n"}}, "manifests/hub/001-big.yaml", "over the"},
		{"duplicate key", []layerEntry{{Name: "manifests/hub/000-d.yaml", Body: "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: a\n  name: b\n"}}, "manifests/hub/000-d.yaml", `duplicate key "name"`},
		{"nested duplicate key", []layerEntry{{Name: "manifests/hub/000-d.yaml", Body: "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: a\ndata:\n  k: 1\n  k: 2\n"}}, "manifests/hub/000-d.yaml", `duplicate key "k"`},
		{"anchor", []layerEntry{{Name: "manifests/hub/000-a.yaml", Body: "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: a\ndata:\n  x: &x v\n"}}, "manifests/hub/000-a.yaml", "anchors"},
		{"alias", []layerEntry{{Name: "manifests/hub/000-a.yaml", Body: "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: a\ndata:\n  x: &x v\n  y: *x\n"}}, "manifests/hub/000-a.yaml", "anchors"},
		{"merge key", []layerEntry{{Name: "manifests/hub/000-a.yaml", Body: "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: a\ndata:\n  <<: {x: y}\n"}}, "manifests/hub/000-a.yaml", "merge"},
		{"custom tag", []layerEntry{{Name: "manifests/hub/000-a.yaml", Body: "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: a\ndata:\n  x: !!binary aGk=\n"}}, "manifests/hub/000-a.yaml", "tag"},
		{"multi-doc file", []layerEntry{{Name: "manifests/hub/000-m.yaml", Body: manifestYAML("Namespace", "a") + "---\n" + manifestYAML("Namespace", "b")}}, "manifests/hub/000-m.yaml", "more than one"},
		{"non-YAML manifest", []layerEntry{good, {Name: "manifests/hub/001-readme.txt", Body: "hello"}}, "manifests/hub/001-readme.txt", "not YAML"},
		{".yml manifest", []layerEntry{{Name: "manifests/hub/001-a.yml", Body: manifestYAML("Namespace", "a")}}, "manifests/hub/001-a.yml", ".yml"},
		{"JSON outside observability", []layerEntry{{Name: "manifests/hub/001-a.json", Body: `{"apiVersion":"v1"}`}}, "manifests/hub/001-a.json", "not YAML"},
		{"script", []layerEntry{good, {Name: "manifests/hub/run.sh", Body: "#!/bin/sh\n"}}, "manifests/hub/run.sh", "not YAML"},
		{"unknown observability file", []layerEntry{good, {Name: "observability/notes.json", Body: "{}"}}, "observability/notes.json", "not in the bundle layout"},
		{"yaml in observability", []layerEntry{good, {Name: "observability/dashboards/d.yaml", Body: manifestYAML("Namespace", "x")}}, "observability/dashboards/d.yaml", "not in the bundle layout"},
		{"invalid JSON artifact", []layerEntry{good, {Name: "observability/metrics-schema.json", Body: `{"a":`}}, "observability/metrics-schema.json", "JSON"},
		{"JSON artifact not an object", []layerEntry{good, {Name: "observability/metrics-schema.json", Body: `[1]`}}, "observability/metrics-schema.json", "object"},
		{"unknown top-level directory", []layerEntry{good, {Name: "extra/000-a.yaml", Body: manifestYAML("Namespace", "a")}}, "extra/000-a.yaml", "top-level"},
		{"file directly under manifests", []layerEntry{{Name: "manifests/000-a.yaml", Body: manifestYAML("Namespace", "a")}}, "manifests/000-a.yaml", "not in the bundle layout"},
		{"unknown phase", []layerEntry{{Name: "manifests/hub/infra/later/000-a.yaml", Body: manifestYAML("Namespace", "a")}}, "manifests/hub/infra/later/000-a.yaml", "pre, rollout, post"},
		{"too deep", []layerEntry{{Name: "manifests/hub/infra/rollout/extra/000-a.yaml", Body: manifestYAML("Namespace", "a")}}, "manifests/hub/infra/rollout/extra/000-a.yaml", "not in the bundle layout"},
		{"v1 and v2 in one cluster", []layerEntry{good, {Name: "manifests/hub/infra/pre/000-a.yaml", Body: manifestYAML("Namespace", "a")}}, "manifests/hub/infra/pre/000-a.yaml", "mixes"},
		{"duplicate entry", []layerEntry{good, good}, "manifests/hub/000-ns.yaml", "more than once"},
		{"weird characters", []layerEntry{{Name: "manifests/hub/000 a.yaml", Body: manifestYAML("Namespace", "a")}}, "manifests/hub/000 a.yaml", "characters outside"},
		{"empty file", []layerEntry{{Name: "manifests/hub/000-a.yaml"}}, "manifests/hub/000-a.yaml", "empty"},
		{"no apiVersion", []layerEntry{{Name: "manifests/hub/000-a.yaml", Body: "kind: Namespace\nmetadata:\n  name: a\n"}}, "manifests/hub/000-a.yaml", "apiVersion"},
		{"no kind", []layerEntry{{Name: "manifests/hub/000-a.yaml", Body: "apiVersion: v1\nmetadata:\n  name: a\n"}}, "manifests/hub/000-a.yaml", "kind"},
		{"no metadata", []layerEntry{{Name: "manifests/hub/000-a.yaml", Body: "apiVersion: v1\nkind: Namespace\n"}}, "manifests/hub/000-a.yaml", "metadata.name"},
		{"no name", []layerEntry{{Name: "manifests/hub/000-a.yaml", Body: "apiVersion: v1\nkind: Namespace\nmetadata:\n  labels: {a: b}\n"}}, "manifests/hub/000-a.yaml", "metadata.name"},
		{"numeric name", []layerEntry{{Name: "manifests/hub/000-a.yaml", Body: "apiVersion: v1\nkind: Namespace\nmetadata:\n  name: 123\n"}}, "manifests/hub/000-a.yaml", "metadata.name"},
		{"not a mapping", []layerEntry{{Name: "manifests/hub/000-a.yaml", Body: "- a\n- b\n"}}, "manifests/hub/000-a.yaml", "not a mapping"},
		{"syntax error", []layerEntry{{Name: "manifests/hub/000-a.yaml", Body: "apiVersion: [v1\n"}}, "manifests/hub/000-a.yaml", "invalid YAML"},
		{"too deep YAML", []layerEntry{{Name: "manifests/hub/000-a.yaml", Body: manifestYAML("ConfigMap", "a") + "data:\n  x: " + strings.Repeat("[", 150) + strings.Repeat("]", 150) + "\n"}}, "manifests/hub/000-a.yaml", "nests deeper"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := validate(t, buildLayer(t, tc.entries...))
			if err == nil {
				t.Fatal("hostile layer accepted")
			}
			if !errors.Is(err, ErrInvalidManifestsLayer) {
				t.Errorf("error does not wrap ErrInvalidManifestsLayer: %v", err)
			}
			var lerr *ManifestsLayerError
			if !errors.As(err, &lerr) {
				t.Fatalf("error is %T, want *ManifestsLayerError", err)
			}
			found := false
			for _, v := range lerr.Violations {
				if v.File == tc.file && strings.Contains(v.Reason, tc.reason) {
					found = true
				}
			}
			if !found {
				t.Errorf("no violation naming %q with %q:\n%v", tc.file, tc.reason, err)
			}
		})
	}
}

func TestValidateManifestsLayerListsEveryOffendingFile(t *testing.T) {
	_, err := validate(t, buildLayer(t,
		layerEntry{Name: "manifests/hub/kustomization.yaml", Body: "resources: []\n"},
		layerEntry{Name: "manifests/hub/000-a.yaml", Body: "kind: Namespace\n"},
		layerEntry{Name: "manifests/hub/001-b.txt", Body: "x"},
	))
	var lerr *ManifestsLayerError
	if !errors.As(err, &lerr) || len(lerr.Violations) != 3 {
		t.Fatalf("want 3 violations, got %v", err)
	}
	for _, f := range []string{"manifests/hub/kustomization.yaml", "manifests/hub/000-a.yaml", "manifests/hub/001-b.txt"} {
		if !strings.Contains(err.Error(), f) {
			t.Errorf("error does not name %s: %v", f, err)
		}
	}
}

func TestValidateManifestsLayerRefusesAnArchiveThatIsNotAGzippedTar(t *testing.T) {
	for name, data := range map[string][]byte{
		"empty":     nil,
		"plain tar": plainTar(t),
		"garbage":   []byte("not a gzip stream at all"),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ValidateManifestsLayer(bytes.NewReader(data))
			if !errors.Is(err, ErrInvalidManifestsLayer) {
				t.Fatalf("got %v", err)
			}
		})
	}
	var trunc []byte
	trunc = buildLayer(t, layerEntry{Name: "manifests/hub/000-a.yaml", Body: manifestYAML("Namespace", "a")})
	if _, err := ValidateManifestsLayer(bytes.NewReader(trunc[:len(trunc)/2])); !errors.Is(err, ErrInvalidManifestsLayer) {
		t.Fatalf("truncated gzip accepted: %v", err)
	}
}

func plainTar(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: "manifests/hub/000-a.yaml", Typeflag: tar.TypeReg, Size: 1, Mode: 0o644}); err != nil {
		t.Fatal(err)
	}
	_, _ = tw.Write([]byte("x"))
	_ = tw.Close()
	return buf.Bytes()
}

func TestValidateManifestsLayerBoundsTheTotalSize(t *testing.T) {
	// Each file is under the per-file bound; only the total stops it.
	limits := defaultLayerLimits
	limits.totalBytes = 4 << 10
	var entries []layerEntry
	for i := 0; i < 8; i++ {
		entries = append(entries, layerEntry{
			Name: fmt.Sprintf("observability/dashboards/d%d.json", i),
			Body: `{"pad":"` + strings.Repeat("a", 1<<10) + `"}`,
		})
	}
	_, err := validateManifestsLayer(bytes.NewReader(buildLayer(t, entries...)), limits)
	if err == nil || !strings.Contains(err.Error(), "layer exceeds") {
		t.Fatalf("total bound not enforced: %v", err)
	}
}

func TestValidateManifestsLayerBoundsTheDecompressedStream(t *testing.T) {
	// A few hundred bytes of gzip that decompress far past the stream ceiling.
	// The bound is counted on the read side, not taken from a header.
	limits := defaultLayerLimits
	limits.streamBytes = 8 << 10
	layer := buildLayer(t, layerEntry{
		Name: "observability/dashboards/big.json",
		Body: `{"pad":"` + strings.Repeat("a", 256<<10) + `"}`,
	})
	if len(layer) > 2<<10 {
		t.Fatalf("fixture is not a bomb: %d compressed bytes", len(layer))
	}
	_, err := validateManifestsLayer(bytes.NewReader(layer), limits)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("stream bound not enforced: %v", err)
	}
	// And at the real limits the same archive is fine, so the bound is what
	// refused it.
	if _, err := validate(t, layer); err != nil {
		t.Fatalf("fixture invalid at the real limits: %v", err)
	}
}

func TestValidateManifestsLayerBoundsTheFileCount(t *testing.T) {
	limits := defaultLayerLimits
	limits.files = 10
	var entries []layerEntry
	for i := 0; i <= limits.files; i++ {
		entries = append(entries, layerEntry{Name: fmt.Sprintf("manifests/hub/%05d-cm-x.yaml", i), Body: manifestYAML("ConfigMap", fmt.Sprintf("c%d", i))})
	}
	_, err := validateManifestsLayer(bytes.NewReader(buildLayer(t, entries...)), limits)
	if err == nil || !strings.Contains(err.Error(), "more than 10 files") {
		t.Fatalf("file-count bound not enforced: %v", err)
	}
}

func TestValidateManifestsLayerBoundsTheEntryCount(t *testing.T) {
	limits := defaultLayerLimits
	limits.entries = 20
	var entries []layerEntry
	for i := 0; i < 30; i++ {
		entries = append(entries, dir(fmt.Sprintf("manifests/c%d", i)))
	}
	_, err := validateManifestsLayer(bytes.NewReader(buildLayer(t, entries...)), limits)
	if err == nil || !strings.Contains(err.Error(), "more than 20 entries") {
		t.Fatalf("entry bound not enforced: %v", err)
	}
}

func TestValidateManifestsLayerCapsTheViolationsItReports(t *testing.T) {
	var entries []layerEntry
	for i := 0; i < 100; i++ {
		entries = append(entries, layerEntry{Name: fmt.Sprintf("manifests/hub/%03d.txt", i), Body: "x"})
	}
	_, err := validate(t, buildLayer(t, entries...))
	var lerr *ManifestsLayerError
	if !errors.As(err, &lerr) || len(lerr.Violations) != manifestsLayerMaxViolation || !lerr.Truncated {
		t.Fatalf("violations not capped: %v", err)
	}
}

func TestIsKustomizationName(t *testing.T) {
	for name, want := range map[string]bool{
		"kustomization.yaml": true, "kustomization.yml": true, "Kustomization": true,
		"KUSTOMIZATION.YAML": true, "kustomization": true, "kustomization.json": true,
		"000-kustomization-x.yaml": false, "my-kustomization.yaml": false, "000-namespace-app.yaml": false,
	} {
		if got := isKustomizationName(name); got != want {
			t.Errorf("isKustomizationName(%q) = %v, want %v", name, got, want)
		}
	}
}
