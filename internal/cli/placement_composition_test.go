package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/bundle"
	"github.com/reliant-labs/forge/pkg/release"
)

// Prod's post-migration shape, composed the real way: a cluster API, a
// hosted SPA (its only hosted part) and the native OpenBao provider. The
// consumers and placement declarations come from compositionConsumers and
// bundlePlacementDeclarations, and the sealed hosted record and the
// frontend's consumer agree on the hosted destination, so it seals; OpenBao
// precedes the SPA under the absent-release_dependencies default.
func TestPlacementCompositionHostedSPAOnlyWithOpenBaoSeals(t *testing.T) {
	const owner = "openbao-secrets-store"
	digest := "sha256:" + strings.Repeat("a", 64)
	ref := "registry.test/reliant@" + digest
	e := &KCLEntities{
		Workloads: []WorkloadEntity{{Name: "api", Runtime: RuntimeEntity{Type: RuntimeCluster, Cluster: &ClusterRuntime{Cluster: "prod"}}}},
		Frontends: []FrontendEntity{orderFrontend("web", FrontendRuntimeHosted, nil)},
		OpenBao:   &OpenBaoEntity{Name: "store", Namespace: "secrets", Cluster: "prod", ReleaseOwner: owner, ReleaseInputs: map[string]string{"container:openbao": ref, "container:converge": ref}},
	}
	consumers, err := compositionConsumers(e)
	if err != nil {
		t.Fatal(err)
	}
	placements, err := bundlePlacementDeclarations(e)
	if err != nil {
		t.Fatal(err)
	}
	src := release.Source{Repo: "github.com/acme/shop", Ref: "main", Commit: strings.Repeat("a", 40)}
	artifact := release.SnapshotArtifact{Kind: release.KindOCI, Location: "registry.test/reliant", Content: digest, Platforms: []string{"linux/amd64"}, Origin: release.ArtifactOrigin{Release: "v1", Source: &src}}
	id, err := release.ArtifactIdentity(artifact)
	if err != nil {
		t.Fatal(err)
	}
	s := release.Snapshot{Schema: release.SnapshotSchema, Project: "shop", Environment: "prod", Source: src, PolicyDigest: "sha256:" + strings.Repeat("b", 64),
		Artifacts: map[release.ArtifactID]release.SnapshotArtifact{id: artifact}, Consumers: consumers}
	for _, c := range consumers {
		s.Selected, s.Closure = append(s.Selected, c.Key), append(s.Closure, c.Key)
		s.Decisions = append(s.Decisions, release.CompositionDecision{Consumer: c.Key, Action: "build", Reason: "bootstrap"})
		for _, slot := range c.Slots {
			s.Bindings = append(s.Bindings, release.ConsumerBinding{Consumer: c.Key, Slot: slot, Artifact: id})
		}
	}
	container := func(name string) string {
		return "spec:\n  template:\n    spec:\n      containers:\n        - name: " + name + "\n          image: " + ref + "\n"
	}
	bao := func(apiVersion, kind, name, body string) string {
		return "---\n# cluster: prod\napiVersion: " + apiVersion + "\nkind: " + kind + "\nmetadata:\n  name: " + name +
			"\n  namespace: secrets\n  annotations:\n    forge.dev/release-owners: '[\"" + owner + "\"]'\n" + body
	}
	stream := "# cluster: prod\napiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: api\n  namespace: shop\n  labels:\n    forge.dev/workload: api\n" + container("api") +
		bao("apps/v1", "Deployment", "store", container("openbao")) +
		bao("batch/v1", "Job", "store-converge-abcd1234", strings.Replace(container("converge"), "    spec:\n", "    spec:\n      restartPolicy: Never\n", 1)) +
		bao("v1", "ConfigMap", "store-config", "data: {config.hcl: x}\n") +
		// The record hostedBundleObjects seals for a hosted frontend: it
		// serves exactly the site release the snapshot binds web to.
		"---\n# cluster: hosted\napiVersion: forge.dev/v1alpha1\nkind: StaticSite\nmetadata:\n  name: web\n  labels:\n    app.kubernetes.io/name: web\nspec:\n  releaseRepository: registry.test/reliant\n  liveDigest: " + digest + "\n"
	in := bundle.BuildInput{Project: "shop", Env: "prod", CreatedAt: time.Unix(1700000000, 0).UTC(), Placements: placements,
		Provenance: release.Provenance{Repo: "r", Commit: strings.Repeat("c", 40), Tree: strings.Repeat("0", 40), ForgeVersion: "v"},
		Shape:      bundle.ShapeInput{Kind: release.EnvPersistent, Clusters: []string{"prod", release.BundleHostedCluster}, Manifests: stream}}
	sealed, err := bundle.SnapshotInventory(t.Context(), in, s)
	if err != nil {
		t.Fatalf("inventory of the composition: %v", err)
	}
	in.Snapshot = &sealed
	built, err := bundle.Build(t.Context(), in)
	if err != nil {
		t.Fatalf("a cluster + hosted-SPA + OpenBao composition must seal: %v", err)
	}
	var store, web release.PlacementKey
	for _, item := range built.Doc.PlacementPrerequisites.Items {
		switch {
		case item.Workload == owner && item.Key.Kind == "Deployment":
			store = item.Key
		case item.Workload == "web":
			web = item.Key
		}
	}
	if store.Name != "store" || web.Kind != "StaticSite" || web.Cluster != release.BundleHostedCluster {
		t.Fatalf("items = %+v", built.Doc.PlacementPrerequisites.Items)
	}
	found := false
	for _, edge := range built.Doc.PlacementPrerequisites.Edges {
		found = found || (edge.Predecessor == store && edge.Dependent == web)
	}
	if !found {
		t.Fatalf("OpenBao does not precede the hosted SPA: %+v", built.Doc.PlacementPrerequisites.Edges)
	}
}

// A frontend's composition key is its sealed destination: hosted frontends
// are keyed at the hosted tree, frontends forge publishes itself are not.
func TestCompositionFrontendKeyIsItsSealedDestination(t *testing.T) {
	if k := compositionFrontendKey(orderFrontend("web", FrontendRuntimeHosted, nil)); k.Cluster != release.BundleHostedCluster {
		t.Errorf("hosted frontend key = %+v", k)
	}
	if k := compositionFrontendKey(orderFrontend("web", FrontendRuntimeBucket, nil)); k.Cluster != snapshotFrontendCluster {
		t.Errorf("bucket frontend key = %+v", k)
	}
	key := release.ConsumerKey{Cluster: release.BundleHostedCluster, Workload: "web"}
	s := release.Snapshot{Consumers: []release.Consumer{{Key: key, Slots: []string{"frontend"}}}, Bindings: []release.ConsumerBinding{{Consumer: key, Slot: "frontend"}}}
	if _, err := snapshotFrontendEntries(s); err != nil {
		t.Errorf("a platform-published frontend needs no frozen forge recipe: %v", err)
	}
}
