package bundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/reliant-labs/forge/pkg/release"
)

// buildFixture is the fixture the shape tests use, as a BuildInput. Reusing
// that stream is deliberate: a bundle's layer and its shape describe the same
// render, and a second fixture would let the two drift in the tests exactly
// the way parse.go exists to stop them drifting in the code.
func buildFixture() BuildInput {
	return BuildInput{
		Project: "shop",
		Env:     "prod",
		Release: "v1.7.13",
		Pins: release.BundlePins{Images: map[string]string{
			"api": "sha256:1111111111111111111111111111111111111111111111111111111111111111",
		}},
		Provenance: release.Provenance{
			Repo:         "github.com/acme/shop",
			Commit:       "0123456789abcdef0123456789abcdef01234567",
			Branch:       "main",
			Tree:         "fedcba9876543210fedcba9876543210fedcba98",
			ForgeVersion: "v0.1.43",
			Worktree:     release.Worktree{Key: "feat-x"},
		},
		Shape:     fixtureInput(),
		CreatedAt: time.Date(2026, 10, 2, 17, 4, 5, 0, time.UTC),
	}
}

func mustBuild(t *testing.T, in BuildInput) Bundle {
	t.Helper()
	b, err := Build(context.Background(), in)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return b
}

// THE DETERMINISM PROPERTY. If the same render did not produce the same
// digest, a bundle's identity would be a function of when it was built: every
// build would cut a new record of an unchanged deploy, "has this already been
// deployed" would always answer no, and a re-push would never be idempotent.
func TestBuildIsDeterministic(t *testing.T) {
	first := mustBuild(t, buildFixture())
	for i := 0; i < 5; i++ {
		again := mustBuild(t, buildFixture())
		if again.Digest != first.Digest {
			t.Fatalf("build %d produced a different digest:\n first %s\n again %s", i, first.Digest, again.Digest)
		}
		if !bytes.Equal(again.Manifest, first.Manifest) {
			t.Fatalf("build %d produced different manifest bytes", i)
		}
		layerA, _ := first.Layer(release.BundleManifestsLayer)
		layerB, _ := again.Layer(release.BundleManifestsLayer)
		if !bytes.Equal(layerA, layerB) {
			t.Fatalf("build %d produced a different manifest layer", i)
		}
	}
	if !release.ValidDigest(first.Digest) {
		t.Fatalf("digest %q is not canonical", first.Digest)
	}
}

// Determinism must not be achieved by ignoring the input.
func TestBuildDigestMovesWithTheRender(t *testing.T) {
	base := mustBuild(t, buildFixture())

	changedManifests := buildFixture()
	changedManifests.Shape.Manifests = strings.Replace(changedManifests.Shape.Manifests, "replicas: 3", "replicas: 4", 1)
	if got := mustBuild(t, changedManifests); got.Digest == base.Digest {
		t.Error("the digest did not move when the render did")
	}

	changedRelease := buildFixture()
	changedRelease.Release = "v1.7.14"
	if got := mustBuild(t, changedRelease); got.Digest == base.Digest {
		t.Error("the digest did not move when the pinned release did")
	}

	changedProvenance := buildFixture()
	changedProvenance.Provenance.Commit = "9999999999999999999999999999999999999999"
	if got := mustBuild(t, changedProvenance); got.Digest == base.Digest {
		t.Error("the digest did not move when the source commit did")
	}

	changedTime := buildFixture()
	changedTime.CreatedAt = changedTime.CreatedAt.Add(time.Hour)
	if got := mustBuild(t, changedTime); got.Digest == base.Digest {
		t.Error("the digest did not move when created_at did; the document is inside the digest")
	}
}

// Build reads no clock, so the caller must supply one. A Build that defaulted
// to time.Now could not be determinism-tested at all.
func TestBuildRequiresACreationTime(t *testing.T) {
	in := buildFixture()
	in.CreatedAt = time.Time{}
	if _, err := Build(context.Background(), in); !errors.Is(err, release.ErrInvalid) {
		t.Fatalf("a zero created_at must be refused; got %v", err)
	}
}

func TestBuildRequiresProjectAndEnv(t *testing.T) {
	for name, mutate := range map[string]func(*BuildInput){
		"no project": func(in *BuildInput) { in.Project = "" },
		"no env":     func(in *BuildInput) { in.Env = " " },
		"bad kind":   func(in *BuildInput) { in.Shape.Kind = "something-else" },
	} {
		in := buildFixture()
		mutate(&in)
		if _, err := Build(context.Background(), in); !errors.Is(err, release.ErrInvalid) {
			t.Errorf("%s: want release.ErrInvalid, got %v", name, err)
		}
	}
}

// ─── F-13 ───────────────────────────────────────────────────────────────────

// TestBuildRefusesAnUnredactedSecret is the F-13 canary (doc §13).
//
// It defeats the structural redaction on purpose — the canary is injected
// into the BYTES Build is about to write, past parseStream — so what is under
// test is the final gate rather than the redaction that should have made it
// unnecessary. A test that only checked the happy path would pass against a
// Build whose gate had been deleted.
func TestBuildRefusesAnUnredactedSecret(t *testing.T) {
	leaky := []byte(`apiVersion: v1
kind: Secret
metadata:
  name: db
data:
  DATABASE_URL: ` + canaryValue + `
`)
	layer, err := writeTarGz([]tarEntry{{name: "manifests/gke-prod/000-secret-db.yaml", data: leaky}})
	if err != nil {
		t.Fatal(err)
	}
	err = refuseSecretValues(layer)
	if !errors.Is(err, ErrSecretValue) {
		t.Fatalf("the F-13 gate passed a layer carrying a Secret value; got %v", err)
	}
	if !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Errorf("the refusal must name the offending key, so an operator knows what leaked: %v", err)
	}
}

// And the whole way through: a render carrying a Secret value builds, because
// the value never survives the parse.
func TestBuildRedactsEverySecretValue(t *testing.T) {
	built := mustBuild(t, buildFixture())
	if !strings.Contains(buildFixture().Shape.Manifests, canaryValue) {
		t.Fatal("the fixture no longer carries a Secret value; this test would prove nothing")
	}

	layer, ok := built.Layer(release.BundleManifestsLayer)
	if !ok {
		t.Fatal("no manifest layer")
	}
	for name, data := range unpackLayer(t, layer) {
		if strings.Contains(data, canaryValue) {
			t.Fatalf("the canary survived into %s:\n%s", name, data)
		}
	}
	if strings.Contains(string(built.ConfigBlob()), canaryValue) {
		t.Fatal("the canary survived into the bundle document")
	}
	if strings.Contains(string(built.Manifest), canaryValue) {
		t.Fatal("the canary survived into the OCI manifest")
	}

	// The Secret is NOT in the layer at all — a reconciler would apply a
	// redaction marker over the cluster's real value and own the object's
	// lifetime. It is named in the document instead, so the deploy syncs it.
	for name, data := range unpackLayer(t, layer) {
		if strings.Contains(data, "kind: Secret") {
			t.Errorf("%s: a Secret rode the manifest layer:\n%s", name, data)
		}
	}
	var named bool
	for _, ref := range built.Doc.Secrets {
		if ref.Name == "orders-superuser" {
			named = true
		}
	}
	if !named {
		t.Errorf("the Secret is not named in the bundle document: %+v", built.Doc.Secrets)
	}
}

// ─── The manifest layer ─────────────────────────────────────────────────────

func TestBuildManifestLayerLayout(t *testing.T) {
	built := mustBuild(t, buildFixture())
	layer, ok := built.Layer(release.BundleManifestsLayer)
	if !ok {
		t.Fatal("no manifest layer")
	}
	files := unpackLayer(t, layer)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) == 0 {
		t.Fatal("the manifest layer is empty")
	}
	for _, name := range names {
		if !strings.HasPrefix(name, manifestsPrefix+"/") {
			t.Errorf("%s is not under %s/", name, manifestsPrefix)
		}
		if !strings.HasSuffix(name, ".yaml") {
			t.Errorf("%s is not a .yaml entry", name)
		}
		if parts := strings.Split(name, "/"); len(parts) != 5 {
			t.Errorf("%s is not manifests/<cluster>/<unit>/<phase>/<NNN>-<kind>-<name>.yaml", name)
		}
	}
	// The apply order is checked over a render big enough to catch it —
	// see TestBuildManifestOrderSurvivesTenDocuments.

	// EVERY SHAPE OBJECT MUST BE IN THE LAYER. A shape describing objects
	// its own layer does not carry is a record of a deploy that never
	// happened, and both halves are under the same digest so nothing
	// downstream could notice.
	for _, o := range built.Doc.Shape.Objects {
		want := strings.ToLower(o.Kind) + "-" + o.Name + ".yaml"
		var hit bool
		for _, name := range names {
			if strings.HasSuffix(name, want) && strings.Contains(name, pathSegment(o.Cluster)) {
				hit = true
				break
			}
		}
		if !hit {
			t.Errorf("shape names %s but no layer entry matches *%s", o.Key(), want)
		}
	}
}

// A kubectl context is free-form and routinely holds characters that are not
// legal in a path component. An unsanitized one would place entries at an
// unexpected depth.
func TestBuildSanitizesClusterSegments(t *testing.T) {
	in := buildFixture()
	in.Shape.Manifests = strings.ReplaceAll(in.Shape.Manifests, "# cluster: gke-prod",
		"# cluster: gke_proj/us-central1..prod")
	in.Shape.Clusters = []string{"gke_proj/us-central1..prod"}
	built := mustBuild(t, in)
	layer, _ := built.Layer(release.BundleManifestsLayer)
	for name := range unpackLayer(t, layer) {
		if parts := strings.Split(name, "/"); len(parts) != 5 {
			t.Errorf("a cluster name with a separator escaped its segment: %s", name)
		}
	}
}

// ─── The document and the OCI manifest ──────────────────────────────────────

func TestBuildDocumentAndAnnotations(t *testing.T) {
	built := mustBuild(t, buildFixture())

	// The doc must round-trip through the STRICT decoder: that is how a
	// backend reads it, and a field forge writes that the decoder refuses
	// would make every RecordBundle fail at the server.
	decoded, err := release.DecodeBundleDoc(built.ConfigBlob())
	if err != nil {
		t.Fatalf("the config blob does not strictly decode: %v", err)
	}
	if decoded.Schema != release.BundleSchema {
		t.Errorf("schema = %q", decoded.Schema)
	}
	if decoded.Project != "shop" || decoded.Env != "prod" || decoded.Release != "v1.7.13" {
		t.Errorf("document identity is %s/%s/%s", decoded.Project, decoded.Env, decoded.Release)
	}
	if !release.ValidDigest(decoded.ConfigDigest) {
		t.Errorf("config_digest %q is not canonical", decoded.ConfigDigest)
	}

	var manifest ocispec.Manifest
	if err := jsonUnmarshal(built.Manifest, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.ArtifactType != release.BundleArtifactType {
		t.Errorf("artifactType = %q, want %q", manifest.ArtifactType, release.BundleArtifactType)
	}
	if manifest.Config.MediaType != release.BundleConfigMediaType {
		t.Errorf("config mediaType = %q", manifest.Config.MediaType)
	}
	// The config descriptor's digest must be over the config BYTES: it is
	// what a backend re-derives to decide whether the document it decoded
	// is the one the manifest named.
	if manifest.Config.Digest.String() != mustHash(t, built.ConfigBlob()) {
		t.Error("the config descriptor's digest is not the digest of the config blob")
	}
	if len(manifest.Layers) != 1 || manifest.Layers[0].MediaType != release.BundleManifestsLayer {
		t.Errorf("layers = %+v", manifest.Layers)
	}
	for key, want := range map[string]string{
		AnnotationEnv:              "prod",
		AnnotationProject:          "shop",
		AnnotationRelease:          "v1.7.13",
		AnnotationConfigDigest:     decoded.ConfigDigest,
		AnnotationDirty:            "false",
		AnnotationWorktreeKey:      "feat-x",
		ocispec.AnnotationRevision: "0123456789abcdef0123456789abcdef01234567",
		ocispec.AnnotationSource:   "github.com/acme/shop",
	} {
		if got := manifest.Annotations[key]; got != want {
			t.Errorf("annotation %s = %q, want %q", key, got, want)
		}
	}
	// `created` is pinned, or the digest would move every second.
	if got := manifest.Annotations[ocispec.AnnotationCreated]; got != fixedCreated {
		t.Errorf("created annotation = %q, want the pinned %q", got, fixedCreated)
	}
}

// An unreleased bundle (dev, Preview) pins no version, and the annotation is
// then ABSENT rather than present-and-empty: an empty annotation reads as
// "recorded as nothing" in a registry UI.
func TestBuildUnreleasedBundleOmitsTheReleaseAnnotation(t *testing.T) {
	in := buildFixture()
	in.Release = ""
	built := mustBuild(t, in)
	var manifest ocispec.Manifest
	if err := jsonUnmarshal(built.Manifest, &manifest); err != nil {
		t.Fatal(err)
	}
	if _, ok := manifest.Annotations[AnnotationRelease]; ok {
		t.Error("an unreleased bundle must not carry a release annotation")
	}
	if built.Doc.Release != "" {
		t.Errorf("document release = %q", built.Doc.Release)
	}
}

// config_digest is the SHAPE's identity: an images-only change must not move
// it, which is what makes "promote" and "the KCL moved" one comparison apart
// (doc §2.1).
func TestBuildConfigDigestIsBlindToImagesAndTheBundleDigestIsNot(t *testing.T) {
	const next = "sha256:9999999999999999999999999999999999999999999999999999999999999999"
	base := mustBuild(t, buildFixture())

	repinned := buildFixture()
	old := "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	repinned.Shape.Manifests = strings.ReplaceAll(repinned.Shape.Manifests, old, next)
	repinned.Shape.Images = map[string]string{"api": next}
	repinned.Pins.Images = map[string]string{"api": next}
	moved := mustBuild(t, repinned)

	if moved.Doc.ConfigDigest != base.Doc.ConfigDigest {
		t.Errorf("config_digest moved on an images-only change:\n %s\n %s", base.Doc.ConfigDigest, moved.Doc.ConfigDigest)
	}
	if moved.Digest == base.Digest {
		t.Error("the bundle digest did NOT move on an images-only change; it covers the pins")
	}
}

// TestBuildWritesExactlyOneLayer pins the property Flux depends on.
//
// An OCIRepository's layerSelector selects ONE layer, so a bundle with two
// would have a layer nobody applies — and the bundle would still be RECORDED
// as what shipped, which is the worst available outcome: a record saying the
// platform install went out, over a cluster that never received it. That is
// why the charts layer was removed rather than reordered, and why this asserts
// the count rather than the absence of one media type.
func TestBuildWritesExactlyOneLayer(t *testing.T) {
	built := mustBuild(t, buildFixture())

	var manifest ocispec.Manifest
	if err := jsonUnmarshal(built.Manifest, &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Layers) != 1 {
		t.Fatalf("a bundle carries exactly one layer (Flux selects one); got %d: %v",
			len(manifest.Layers), layerMediaTypes(manifest))
	}
	if got := manifest.Layers[0].MediaType; got != release.BundleManifestsLayer {
		t.Errorf("the one layer is %q, want the manifests layer %q", got, release.BundleManifestsLayer)
	}
}

func layerMediaTypes(m ocispec.Manifest) []string {
	out := make([]string, 0, len(m.Layers))
	for _, l := range m.Layers {
		out = append(out, l.MediaType)
	}
	return out
}

// ─── helpers ────────────────────────────────────────────────────────────────

func unpackLayer(t *testing.T, layer []byte) map[string]string {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(layer))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = gz.Close() }()
	out := map[string]string{}
	tr := tar.NewReader(gz)
	for {
		hdr, nerr := tr.Next()
		if errors.Is(nerr, io.EOF) {
			return out
		}
		if nerr != nil {
			t.Fatal(nerr)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		data, rerr := io.ReadAll(tr)
		if rerr != nil {
			t.Fatal(rerr)
		}
		out[hdr.Name] = string(data)
	}
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func jsonUnmarshal(data []byte, v any) error { return json.Unmarshal(data, v) }

func mustHash(t *testing.T, data []byte) string {
	t.Helper()
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// TestBuildManifestOrderSurvivesTenDocuments pins the apply order.
//
// A cluster's entries are applied in directory order, which is
// lexicographic, so the render index must be zero-padded: unpadded, "10"
// sorts before "9" and a bundle applies a CRD after the custom resources that
// need it, or a Namespace after the objects in it. The render therefore has
// to cross ten documents for the test to bite at all — the five-object
// fixture would pass against an unpadded implementation.
func TestBuildManifestOrderSurvivesTenDocuments(t *testing.T) {
	var stream strings.Builder
	const count = 12
	for i := 0; i < count; i++ {
		if i > 0 {
			stream.WriteString("---\n")
		}
		fmt.Fprintf(&stream, "# cluster: gke-prod\napiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: cm-%02d\n  namespace: shop-prod\ndata:\n  order: \"%d\"\n", i, i)
	}
	in := buildFixture()
	in.Shape = ShapeInput{Kind: release.EnvSelfManaged, Clusters: []string{"gke-prod"}, Manifests: stream.String()}

	layer, ok := mustBuild(t, in).Layer(release.BundleManifestsLayer)
	if !ok {
		t.Fatal("no manifest layer")
	}
	names := keysOf(unpackLayer(t, layer))
	if len(names) != count {
		t.Fatalf("layer holds %d entries, want %d", len(names), count)
	}
	// names is sorted, i.e. in the order an apply would walk the
	// directory. Entry k must be render document k.
	for k, name := range names {
		idx, err := strconv.Atoi(strings.SplitN(path.Base(name), "-", 2)[0])
		if err != nil {
			t.Fatalf("%s carries no render index: %v", name, err)
		}
		if idx != k {
			t.Fatalf("directory position %d holds render document %d (%s); the apply order does not survive",
				k, idx, name)
		}
	}
}

// D4/G11: the document describes every object it hashes, one summary per
// shape object and joined by key, so a release can be diffed against the
// previous one without either's manifests — and that description survives the
// strict decode a backend reads it with.
func TestBuildSummarizesEveryObject(t *testing.T) {
	built := mustBuild(t, buildFixture())
	decoded, err := release.DecodeBundleDoc(built.ConfigBlob())
	if err != nil {
		t.Fatalf("the config blob does not strictly decode: %v", err)
	}
	if len(decoded.Objects) != len(decoded.Shape.Objects) || len(decoded.Objects) == 0 {
		t.Fatalf("%d summaries for %d objects", len(decoded.Objects), len(decoded.Shape.Objects))
	}
	parts := 0
	for i, o := range decoded.Shape.Objects {
		s := decoded.Objects[i]
		if s.Key != o.Key() {
			t.Fatalf("summary %d is for %s, object %d is %s: they must be in shape order", i, s.Key, i, o.Key())
		}
		parts += len(s.Parts)
		if o.Kind == "Deployment" && !strings.Contains(s.Summary, "containers") {
			t.Errorf("deployment %s summary = %q", o.Name, s.Summary)
		}
	}
	if parts == 0 {
		t.Fatal("no object recorded any part digest")
	}
}

// A bundle recorded on a control plane that predates forge#850 must carry
// none of #850's document fields: that control plane strict-decodes the
// config blob and refuses the whole bundle for one unknown key. The reader
// is what the recorder said it decodes; the document is built for it.
func TestBuildWritesOnlyTheDocumentFeaturesItsReaderDecodes(t *testing.T) {
	in := buildFixture()
	in.Provenance.CommitSubject = "fix: something"
	in.Provenance.CommitAuthor = "Reliant"

	full := mustBuild(t, in)
	if full.Doc.Provenance.CommitSubject == "" || len(full.Doc.Objects) == 0 {
		t.Fatalf("the fixture must exercise the optional parts: subject %q, %d summaries", full.Doc.Provenance.CommitSubject, len(full.Doc.Objects))
	}

	in.Reader = &release.BundleReader{} // a control plane that names no feature
	old := mustBuild(t, in)
	var top struct {
		Objects    *json.RawMessage           `json:"objects"`
		Provenance map[string]json.RawMessage `json:"provenance"`
	}
	if err := json.Unmarshal(old.ConfigBlob(), &top); err != nil {
		t.Fatal(err)
	}
	_, subject := top.Provenance["commit_subject"]
	_, author := top.Provenance["commit_author"]
	if top.Objects != nil || subject || author {
		t.Fatalf("a bundle for a reader with no features carries forge#850 keys (objects=%v subject=%v author=%v)", top.Objects != nil, subject, author)
	}
	if old.Doc.Provenance.Commit != full.Doc.Provenance.Commit || old.Doc.ConfigDigest != full.Doc.ConfigDigest {
		t.Fatal("limiting the document changed what it describes")
	}
	if old.Digest == full.Digest {
		t.Fatal("the two documents differ, so the bundles must too: the digest covers the config blob")
	}

	in.Reader = &release.BundleReader{Features: release.BundleDocFeatures()}
	if again := mustBuild(t, in); again.Digest != full.Digest {
		t.Fatalf("a reader of every feature must get the same bundle as the nil reader: %s vs %s", again.Digest, full.Digest)
	}
}
