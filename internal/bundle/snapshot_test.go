package bundle

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/releasecompose"
	"github.com/reliant-labs/forge/pkg/release"
)

func snapDigest(c string) string { return "sha256:" + strings.Repeat(c, 64) }
func snapshotFixture(t *testing.T) (BuildInput, release.Snapshot) {
	t.Helper()
	in := buildFixture()
	in.Shape.Manifests = ""
	in.Shape.Images = nil
	in.Pins = release.BundlePins{}
	src := release.Source{Repo: "github.com/acme/shop", Ref: "main", Commit: strings.Repeat("a", 40)}
	a := release.SnapshotArtifact{Kind: release.KindOCI, Location: "registry.test/reliant", Content: snapDigest("a"), Platforms: []string{"linux/amd64"}, Origin: release.ArtifactOrigin{Release: "v1", Source: &src}}
	id, e := release.ArtifactIdentity(a)
	if e != nil {
		t.Fatal(e)
	}
	s := release.Snapshot{Schema: release.SnapshotSchema, Project: in.Project, Environment: in.Env, Source: src, PolicyDigest: snapDigest("b"), Artifacts: map[release.ArtifactID]release.SnapshotArtifact{id: a}}
	for _, name := range []string{"api", "worker"} {
		key := release.ConsumerKey{Cluster: "cluster", Workload: name}
		s.Consumers = append(s.Consumers, release.Consumer{DependenciesDeclared: true, Key: key, Slots: []string{"container:" + name}})
		s.Selected = append(s.Selected, key)
		s.Closure = append(s.Closure, key)
		s.Bindings = append(s.Bindings, release.ConsumerBinding{Consumer: key, Slot: "container:" + name, Artifact: id})
		s.Decisions = append(s.Decisions, release.CompositionDecision{Consumer: key, Action: "build", Reason: "bootstrap"})
		in.Shape.Manifests += "# cluster: cluster\napiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: " + name + "\n  namespace: shop\n  annotations:\n    forge.dev/release-owners: '[\"" + name + "\"]'\nspec:\n  template:\n    spec:\n      containers:\n        - name: " + name + "\n          image: registry.test/reliant@" + snapDigest("a") + "\n---\n"
	}
	return in, s
}

func TestSnapshotInventoryCompositionBundleRoundtrip(t *testing.T) {
	in, decl := snapshotFixture(t)
	base, err := SnapshotInventory(context.Background(), in, decl)
	if err != nil {
		t.Fatal(err)
	}
	// Isolated candidate has the normal safety transforms before composition.
	var worker release.ConsumerKey
	for _, c := range decl.Consumers {
		if c.Key.Workload == "worker" {
			worker = c.Key
		}
	}
	old := decl.Bindings[1].Artifact
	a := decl.Artifacts[old]
	a.Content = snapDigest("c")
	a.Origin.Release = "v2"
	id, _ := release.ArtifactIdentity(a)
	decl.Artifacts[id] = a
	decl.Bindings[1].Artifact = id
	in.Shape.Manifests = strings.Replace(in.Shape.Manifests, "name: worker\n          image: registry.test/reliant@"+snapDigest("a"), "name: worker\n          image: registry.test/reliant@"+snapDigest("c"), 1)
	candidate, err := SnapshotInventory(context.Background(), in, decl)
	if err != nil {
		t.Fatal(err)
	}
	hash, _ := base.Digest()
	composed, err := releasecompose.Compose(releasecompose.Input{Base: &base, Candidate: candidate, BaseRef: &release.SnapshotBase{PromotionID: "p1", SnapshotDigest: hash, BundleDigest: snapDigest("d")}, Selected: []release.ConsumerKey{worker}})
	if err != nil {
		t.Fatal(err)
	}
	in.Snapshot = &composed
	// Prove the caller's current render cannot overwrite carried approved bytes.
	in.Shape.Manifests = "invalid checkout render"
	in.Shape.Domains = []string{"unapproved-current-checkout.example"}
	built, err := Build(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if len(built.Doc.Shape.Domains) != len(composed.Declaration.Domains) {
		t.Fatal("current checkout declaration leaked into sealed shape")
	}
	if built.Doc.Schema != release.SnapshotBundleSchema || built.Doc.Snapshot == nil {
		t.Fatal("snapshot not embedded")
	}
	layer, _ := built.Layer(release.BundleManifestsLayer)
	if err := release.ValidateSnapshotLayer(composed, layer); err != nil {
		t.Fatal(err)
	}
	resources, err := release.ReadSnapshotResources(layer)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range resources {
		for _, want := range composed.Resources {
			if r.Key.Name == want.Key.Name && !bytes.Equal(r.Spec, want.Spec) {
				t.Fatal("layer changed approved object bytes")
			}
		}
	}
	// Same-repository pins stay independent all the way through OCI packaging.
	if err := release.ValidateSnapshotTransition(&base, composed); err != nil {
		t.Fatal(err)
	}
	malicious := composed
	malicious.Resources = append([]release.SnapshotResource(nil), composed.Resources...)
	malicious.Resources[0].Spec = bytes.ReplaceAll(malicious.Resources[0].Spec, []byte("name: api"), []byte("name: intruder"))
	malicious.Resources[0].Digest = release.SpecDigest(malicious.Resources[0].Spec)
	if err := release.ValidateSnapshotTransition(&base, malicious); err == nil {
		t.Fatal("server transition allowed malicious unselected resource")
	}
	if composed.Bindings[0].Artifact == composed.Bindings[1].Artifact {
		t.Fatal("repository flattened independent bindings")
	}
	corrupt := composed
	corrupt.Resources = append([]release.SnapshotResource(nil), composed.Resources...)
	corrupt.Resources[0].Spec = append([]byte("# corrupt\n"), corrupt.Resources[0].Spec...)
	corrupt.Resources[0].Digest = release.SpecDigest(corrupt.Resources[0].Spec)
	if err := release.ValidateSnapshotLayer(corrupt, layer); !errors.Is(err, release.ErrInvalid) {
		t.Fatalf("layer disagreement accepted: %v", err)
	}
	bad, _ := writeTarGz([]tarEntry{{name: "../../escape.yaml", data: []byte("bad")}})
	if err := release.ValidateSnapshotLayer(composed, bad); err == nil {
		t.Fatal("archive traversal accepted")
	}
}

// A bootstrap snapshot bundle survives its own wire decode: what the control
// plane's RecordBundle runs on the bytes. An env with no per-unit trees encodes
// none (omitempty) and must still validate against the snapshot's derived
// delivery, which has none either.
func TestSnapshotBundleRoundTripsThroughTheStrictDecode(t *testing.T) {
	for _, cluster := range []string{"cluster", release.BundleHostedCluster} {
		t.Run(cluster, func(t *testing.T) { snapshotBundleRoundTrip(t, cluster) })
	}
}

func snapshotBundleRoundTrip(t *testing.T, cluster string) {
	in, decl := snapshotFixture(t)
	in.Shape.Manifests = strings.ReplaceAll(in.Shape.Manifests, "# cluster: cluster\n", "# cluster: "+cluster+"\n")
	for i := range decl.Consumers {
		decl.Consumers[i].Key.Cluster = cluster
		decl.Selected[i].Cluster, decl.Closure[i].Cluster = cluster, cluster
		decl.Bindings[i].Consumer.Cluster = cluster
		decl.Decisions[i].Consumer.Cluster = cluster
	}
	s, err := SnapshotInventory(context.Background(), in, decl)
	if err != nil {
		t.Fatal(err)
	}
	resources, err := SnapshotResources(context.Background(), in, decl.Consumers)
	if err != nil {
		t.Fatal(err)
	}
	if len(resources) != len(s.Resources) {
		t.Fatalf("SnapshotResources = %d resources, SnapshotInventory %d", len(resources), len(s.Resources))
	}
	for i := range resources {
		if resources[i].Key != s.Resources[i].Key || !bytes.Equal(resources[i].Spec, s.Resources[i].Spec) || len(resources[i].Owners) != len(s.Resources[i].Owners) {
			t.Fatalf("SnapshotResources[%d] = %+v, SnapshotInventory's %+v", i, resources[i].Key, s.Resources[i].Key)
		}
	}
	in.Snapshot = &s
	built, err := Build(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := release.DecodeBundleDoc(built.ConfigBlob())
	if err != nil {
		t.Fatal(err)
	}
	if err := doc.Validate(); err != nil {
		t.Fatalf("a snapshot bundle forge built does not validate after its own decode (trees %d): %v", len(built.Doc.Trees), err)
	}
}
