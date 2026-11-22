package bundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/reliant-labs/forge/pkg/release"
)

// The render these tests build a bundle from: one Deployment that must stay
// prunable, and the objects a prune must never touch.
//
// The `# cluster:` headers are the routing the deploy layer stamps, which is
// what puts each document under a per-cluster path in the layer.
const pruneGuardRender = `# cluster: k3d-a
apiVersion: v1
kind: Namespace
metadata:
  name: app
---
# cluster: k3d-a
apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
  namespace: app
  labels:
    forge.dev/workload: api
spec:
  replicas: 1
---
# cluster: k3d-a
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: api-data
  namespace: app
spec:
  resources:
    requests:
      storage: 1Gi
---
# cluster: k3d-a
apiVersion: v1
kind: Secret
metadata:
  name: api-env
  namespace: app
stringData:
  TOKEN: s3cret
---
# cluster: k3d-a
apiVersion: postgresql.cnpg.io/v1
kind: Cluster
metadata:
  name: maindb
  namespace: app
  labels:
    forge.dev/workload: maindb
spec:
  instances: 1
`

func buildPruneGuardBundle(t *testing.T) Bundle {
	t.Helper()
	b, err := Build(context.Background(), BuildInput{
		Project: "fixture", Env: "dev-k8s", CreatedAt: time.Unix(1, 0).UTC(),
		Shape: ShapeInput{
			Kind:      release.EnvSelfManaged,
			Manifests: pruneGuardRender,
			Clusters:  []string{"k3d-a"},
			// `maindb` is a declared database: its objects hold data
			// even though `Cluster` is just a CR to Kubernetes.
			StatefulWorkloads: []string{"maindb"},
		},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return b
}

// TestBuild_GuardsStatefulObjectsAgainstPrune is the one that matters.
//
// A Kustomization with `prune: true` deletes whatever its path no longer
// carries, and the trigger is not a human deciding to delete anything — it is
// a render that came out one document short (a `--target` subset, a KCL
// conditional that went false, a workload renamed in one place). Without the
// guard, that render deletes a PersistentVolumeClaim.
//
// The assertion reads the annotation out of the PACKED LAYER rather than off
// the in-memory documents, because the layer is what Flux applies: a stamp
// that existed only in forge's memory would protect nothing.
func TestBuild_GuardsStatefulObjectsAgainstPrune(t *testing.T) {
	t.Parallel()
	b := buildPruneGuardBundle(t)
	byKind := unpackByKind(t, b)

	// Every object whose removal destroys data or everything under it.
	for _, kind := range []string{"PersistentVolumeClaim", "Namespace"} {
		doc, ok := byKind[kind]
		if !ok {
			t.Fatalf("the layer carries no %s; the fixture is wrong", kind)
		}
		if got := annotationOf(doc, annotationPrune); got != pruneDisabled {
			t.Errorf("%s: prune annotation = %q, want %q — a render that merely failed to select it "+
				"would otherwise delete it", kind, got, pruneDisabled)
		}
	}

	// A declared database's CR: its KIND says nothing, so this half comes
	// from ShapeInput.StatefulWorkloads. It is the case a kind-only rule
	// would miss, and the data loss would be a whole Postgres cluster.
	if got := annotationOf(byKind["Cluster"], annotationPrune); got != pruneDisabled {
		t.Errorf("the declared database's CR has prune = %q, want %q — a stateful workload's objects "+
			"hold data even when their kind does not say so", got, pruneDisabled)
	}

	// AND THE GUARD IS NOT BLANKET. A Deployment must stay prunable, or
	// removing a workload would leave it running forever and the bundle
	// would stop being authoritative.
	if got := annotationOf(byKind["Deployment"], annotationPrune); got != "" {
		t.Errorf("Deployment carries prune = %q; it must stay prunable so a removed workload "+
			"actually goes away", got)
	}

	if b.GuardedObjects != 3 {
		t.Errorf("GuardedObjects = %d, want 3 (Namespace, PVC, the database CR)", b.GuardedObjects)
	}
}

// TestBuild_GuardedObjectsHashMatchesTheLayer pins the ORDERING of the stamp
// against the projection, which is the subtle half.
//
// The shape's per-object hash is what drift detection compares the live object
// against. Stamping AFTER the projection would leave every guarded object's
// recorded hash describing bytes the layer does not carry — so each one would
// read as permanently drifted, on every env, forever. This re-hashes the
// document as PACKED and requires the shape to agree.
func TestBuild_GuardedObjectsHashMatchesTheLayer(t *testing.T) {
	t.Parallel()
	b := buildPruneGuardBundle(t)
	byKind := unpackByKind(t, b)

	for _, kind := range []string{"PersistentVolumeClaim", "Deployment"} {
		var body any
		if err := yaml.Unmarshal(byKind[kind], &body); err != nil {
			t.Fatalf("%s: parse packed document: %v", kind, err)
		}
		packedHash, _, err := release.ObjectHashes(body, nil)
		if err != nil {
			t.Fatalf("%s: hash packed document: %v", kind, err)
		}
		var recorded string
		for _, o := range b.Doc.Shape.Objects {
			if o.Kind == kind {
				recorded = o.Hash
				break
			}
		}
		if recorded == "" {
			t.Fatalf("the shape names no %s", kind)
		}
		if recorded != packedHash {
			t.Errorf("%s: shape records hash %s but the layer carries %s — the shape must describe the "+
				"bytes that were packaged, or every guarded object reads as permanently drifted",
				kind, recorded, packedHash)
		}
	}
}

// TestBuild_StaysDeterministicWithTheGuard pins that the stamp did not break
// the property the whole bundle model rests on: the same render must produce
// the same digest, or an unchanged render cuts a new record on every build and
// "has this already been deployed" always answers no.
func TestBuild_StaysDeterministicWithTheGuard(t *testing.T) {
	t.Parallel()
	first, second := buildPruneGuardBundle(t), buildPruneGuardBundle(t)
	if first.Digest != second.Digest {
		t.Errorf("two builds of one render produced %s and %s", first.Digest, second.Digest)
	}
}

// TestBuild_GuardDoesNotLeakSecretValues pins that stamping an annotation onto
// a Secret did not reintroduce its value. The guard mutates the same decoded
// body redaction produced, so a mistake here would put a plaintext secret in
// an artifact that is kept forever and shared.
func TestBuild_GuardDoesNotLeakSecretValues(t *testing.T) {
	t.Parallel()
	b := buildPruneGuardBundle(t)
	layer, ok := b.Layer(release.BundleManifestsLayer)
	if !ok {
		t.Fatal("no manifest layer")
	}
	if bytes.Contains(layer, []byte("s3cret")) {
		t.Fatal("the manifest layer carries a plaintext secret value")
	}
	// And the Secret is not in the layer at all, so Flux can neither write
	// nor prune it.
	if _, present := unpackByKind(t, b)["Secret"]; present {
		t.Error("a Secret rode the manifest layer")
	}
}

// TestSetAnnotation_DoesNotOverruleADeclaration pins that an existing value is
// left alone. A document that already carries the annotation said something
// deliberate, and the guard is not the place to overrule a declaration.
func TestSetAnnotation_DoesNotOverruleADeclaration(t *testing.T) {
	t.Parallel()
	body := map[string]any{
		"kind": "PersistentVolumeClaim",
		"metadata": map[string]any{
			"name":        "data",
			"annotations": map[string]any{annotationPrune: "enabled"},
		},
	}
	if setAnnotation(body, annotationPrune, pruneDisabled) {
		t.Error("setAnnotation reported a write over an existing value")
	}
	meta := body["metadata"].(map[string]any)
	if got := meta["annotations"].(map[string]any)[annotationPrune]; got != "enabled" {
		t.Errorf("annotation = %v; an explicit declaration must survive", got)
	}
}

// TestSetAnnotation_CreatesTheMetadataPath pins the ordinary case: a document
// with no annotations map at all gets one.
func TestSetAnnotation_CreatesTheMetadataPath(t *testing.T) {
	t.Parallel()
	body := map[string]any{"kind": "Secret", "metadata": map[string]any{"name": "s"}}
	if !setAnnotation(body, annotationPrune, pruneDisabled) {
		t.Fatal("setAnnotation reported no write")
	}
	meta := body["metadata"].(map[string]any)
	if got := meta["annotations"].(map[string]any)[annotationPrune]; got != pruneDisabled {
		t.Errorf("annotation = %v, want %q", got, pruneDisabled)
	}
}

// ─── helpers ────────────────────────────────────────────────────────────────

// unpackByKind reads the manifest layer and returns each document's bytes by
// kind. It reads the LAYER deliberately: that is what a reconciler applies.
func unpackByKind(t *testing.T, b Bundle) map[string][]byte {
	t.Helper()
	layer, ok := b.Layer(release.BundleManifestsLayer)
	if !ok {
		t.Fatal("bundle has no manifest layer")
	}
	gz, err := gzip.NewReader(bytes.NewReader(layer))
	if err != nil {
		t.Fatalf("gunzip layer: %v", err)
	}
	defer func() { _ = gz.Close() }()
	out := map[string][]byte{}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read tar: %v", err)
		}
		if hdr.Typeflag != tar.TypeReg || !strings.HasSuffix(hdr.Name, ".yaml") {
			continue
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("read %s: %v", hdr.Name, err)
		}
		var probe struct {
			Kind string `yaml:"kind"`
		}
		if err := yaml.Unmarshal(data, &probe); err != nil {
			t.Fatalf("parse %s: %v", hdr.Name, err)
		}
		out[probe.Kind] = data
	}
	return out
}

// annotationOf reads one annotation out of a packed document, or "" when it
// carries none.
func annotationOf(doc []byte, key string) string {
	var parsed struct {
		Metadata struct {
			Annotations map[string]string `yaml:"annotations"`
		} `yaml:"metadata"`
	}
	if err := yaml.Unmarshal(doc, &parsed); err != nil {
		return ""
	}
	return parsed.Metadata.Annotations[key]
}

// A PDB holds no data, even inside a stateful workload: guarding it would keep
// a stale one (the workload dropped to one replica) alive under Flux forever.
func TestHoldsData_PDBStaysPrunableInStatefulWorkload(t *testing.T) {
	t.Parallel()
	doc := parsedDoc{meta: docMeta{kind: "PodDisruptionBudget", workload: "maindb"}}
	if holdsData(doc, map[string]bool{"maindb": true}) {
		t.Fatal("a PodDisruptionBudget must stay prunable even when its workload is stateful")
	}
}

const jobRenderRelease = `# cluster: k3d-a
apiVersion: v1
kind: Namespace
metadata:
  name: app
---
# cluster: k3d-a
apiVersion: batch/v1
kind: Job
metadata:
  name: app-migrate-%s
  namespace: app
  labels:
    app.kubernetes.io/managed-by: forge
    forge.dev/job-name: app-migrate
    forge.dev/spec-hash: %s
  annotations:
    forge.dev/deploy-phase: pre-rollout
spec:
  template:
    spec:
      restartPolicy: OnFailure
      containers:
        - name: main
          image: example/app:%s
`

func buildJobRelease(t *testing.T, hash string) Bundle {
	t.Helper()
	b, err := Build(context.Background(), BuildInput{
		Project: "fixture", Env: "prod", CreatedAt: time.Unix(1, 0).UTC(), Reconciled: true,
		Shape: ShapeInput{
			Kind:      release.EnvSelfManaged,
			Manifests: fmt.Sprintf(jobRenderRelease, hash, hash, hash),
			Clusters:  []string{"k3d-a"},
		},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return b
}

// TestBuild_HashNamedJobsStayPrunable pins the Flux half of the Job-cleanup
// story. A hash-named Job is superseded by a NEW name, so under a reconciler the
// previous Job leaves the render and Flux's inventory prune deletes it: nothing
// else is needed, PROVIDED the bundle never stamps a Job prune-exempt. A Job that
// held `kustomize.toolkit.fluxcd.io/prune: disabled` would be skipped by that
// prune and pile up exactly as under direct apply. (Direct apply cannot rely on
// an inventory, so it keeps the newest few itself: internal/cluster/job_prune.go.)
func TestBuild_HashNamedJobsStayPrunable(t *testing.T) {
	t.Parallel()
	one, two := buildJobRelease(t, "aaaaaaaaaa"), buildJobRelease(t, "bbbbbbbbbb")

	jobIn := func(b Bundle) []byte {
		t.Helper()
		doc, ok := unpackByKind(t, b)["Job"]
		if !ok {
			t.Fatal("the layer carries no Job; the fixture is wrong")
		}
		return doc
	}
	for name, b := range map[string]Bundle{"release one": one, "release two": two} {
		if got := annotationOf(jobIn(b), annotationPrune); got != "" {
			t.Errorf("%s: a Job carries prune=%q; it must stay inside Flux's inventory so a superseded Job is deleted", name, got)
		}
	}
	// And the Job set the layer carries moves with the hash: release two does not
	// carry release one's Job, so a Kustomization that applied one and now applies
	// two prunes it. Read from the shape, which is what the plan diffs.
	has := func(b Bundle, name string) bool {
		for _, o := range b.Doc.Shape.Objects {
			if o.Kind == "Job" && o.Name == name {
				return true
			}
		}
		return false
	}
	if !has(one, "app-migrate-aaaaaaaaaa") || has(one, "app-migrate-bbbbbbbbbb") {
		t.Errorf("release one should carry only its own Job: %+v", one.Doc.Shape.Objects)
	}
	if !has(two, "app-migrate-bbbbbbbbbb") || has(two, "app-migrate-aaaaaaaaaa") {
		t.Errorf("release two should carry only its own Job, so release one's leaves the render: %+v", two.Doc.Shape.Objects)
	}
	if one.GuardedObjects != 1 || two.GuardedObjects != 1 {
		t.Errorf("guarded objects = %d / %d, want 1 each (the Namespace only): a Job is never guarded", one.GuardedObjects, two.GuardedObjects)
	}
}
