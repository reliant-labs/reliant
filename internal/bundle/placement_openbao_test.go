package bundle

import (
	"context"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/release"
)

// openbaoOwner is the release owner render.k gives Bundle.openbao
// ("openbao-<namespace>-<name>"); every object pkg/deploy/openbao renders
// carries it in forge.dev/release-owners and none carries forge.dev/workload.
const openbaoOwner = "openbao-secrets-store"

func openbaoDoc(apiVersion, kind, name, extra string) string {
	return "# cluster: prod\napiVersion: " + apiVersion + "\nkind: " + kind + "\nmetadata:\n  name: " + name +
		"\n  namespace: secrets\n  labels:\n    app.kubernetes.io/name: store\n    app.kubernetes.io/managed-by: forge\n" +
		"  annotations:\n    forge.dev/release-owners: '[\"" + openbaoOwner + "\"]'\n" + extra + "---\n"
}

// The web consumer is keyed at the hosted tree, as compositionFrontendKey
// keys a hosted frontend. A cluster + hosted + OpenBao composition seals, and OpenBao's real
// controller is an ordinary item that precedes the hosted frontend under
// the absent-release_dependencies default (every server, providers included).
func TestPlacementPrerequisitesOpenBaoCompositionSeals(t *testing.T) {
	in, s := snapshotFixture(t)
	in.Shape.Clusters = []string{"cluster", release.BundleHostedCluster}
	in.Shape.Manifests += openbaoDoc("apps/v1", "Deployment", "store", "spec:\n  template:\n    spec:\n      containers:\n        - name: openbao\n          image: registry.test/reliant@"+snapDigest("a")+"\n") +
		openbaoDoc("batch/v1", "Job", "store-converge-abcd1234", "spec:\n  template:\n    spec:\n      restartPolicy: Never\n      containers:\n        - name: converge\n          image: registry.test/reliant@"+snapDigest("a")+"\n") +
		openbaoDoc("v1", "ConfigMap", "store-config", "data: {config.hcl: x}\n") +
		openbaoDoc("cert-manager.io/v1", "Issuer", "store-ca", "spec: {}\n") +
		openbaoDoc("external-secrets.io/v1", "ExternalSecret", "store-bootstrap", "spec: {}\n") +
		// The record serves exactly the site release the snapshot binds web to.
		"# cluster: hosted\napiVersion: forge.dev/v1alpha1\nkind: StaticSite\nmetadata:\n  name: web\nspec:\n  releaseRepository: registry.test/reliant\n  liveDigest: " + snapDigest("a") + "\n"
	in.Shape.Manifests = strings.ReplaceAll(in.Shape.Manifests, "# cluster: prod\n", "# cluster: cluster\n")
	provider := release.ConsumerKey{Cluster: "cluster", Workload: openbaoOwner}
	web := release.ConsumerKey{Cluster: release.BundleHostedCluster, Workload: "web"}
	for _, c := range []release.Consumer{
		{DependenciesDeclared: true, Key: provider, Slots: []string{"container:openbao", "container:converge"}},
		// A frontend whose release_dependencies is absent: unknown, so it
		// waits for every server consumer.
		{Key: web, Slots: []string{"frontend"}},
	} {
		s.Consumers = append(s.Consumers, c)
		s.Selected = append(s.Selected, c.Key)
		s.Closure = append(s.Closure, c.Key)
		s.Decisions = append(s.Decisions, release.CompositionDecision{Consumer: c.Key, Action: "build", Reason: "bootstrap"})
	}
	s.Bindings = append(s.Bindings,
		release.ConsumerBinding{Consumer: provider, Slot: "container:openbao", Artifact: s.Bindings[0].Artifact},
		release.ConsumerBinding{Consumer: provider, Slot: "container:converge", Artifact: s.Bindings[0].Artifact},
		release.ConsumerBinding{Consumer: web, Slot: "frontend", Artifact: s.Bindings[0].Artifact})
	in.Placements = []PlacementDeclaration{
		{Workload: "api", Runtime: "cluster", Actuator: "forge"},
		{Workload: "worker", Runtime: "cluster", Actuator: "forge"},
		{Workload: openbaoOwner, Runtime: "cluster", Actuator: "forge"},
		{Workload: "web", Runtime: "hosted", Actuator: "platform"},
	}
	sealed, err := SnapshotInventory(context.Background(), in, s)
	if err != nil {
		t.Fatalf("inventory: %v", err)
	}
	in.Snapshot = &sealed
	built, err := Build(context.Background(), in)
	if err != nil {
		t.Fatalf("a cluster + hosted + OpenBao composition must seal: %v", err)
	}
	p := built.Doc.PlacementPrerequisites
	var store *release.PlacementItem
	for i := range p.Items {
		if p.Items[i].Key.Kind == "Deployment" && p.Items[i].Key.Name == "store" {
			store = &p.Items[i]
		}
	}
	if store == nil || store.Workload != openbaoOwner || store.Key.Namespace != "secrets" || store.Key.Cluster != "cluster" || store.Key.Actuator != "forge" {
		t.Fatalf("OpenBao controller item = %+v in %+v", store, p.Items)
	}
	preceded := false
	for _, e := range p.Edges {
		preceded = preceded || (e.Predecessor == store.Key && e.Dependent.Name == "web")
	}
	if !preceded {
		t.Fatalf("OpenBao does not precede the hosted frontend: %+v", p.Edges)
	}
	bound := map[string]string{}
	for _, b := range p.Bindings {
		if b.Item == store.Key {
			bound[b.Object.Kind] = b.JobContract
		}
	}
	if _, ok := bound["ConfigMap"]; !ok {
		t.Errorf("OpenBao config not bound to its item: %+v", bound)
	}
	if bound["Job"] != release.KubernetesJobCompleteV1 {
		t.Errorf("OpenBao converge Job not bound with the completion contract: %+v", bound)
	}
	if _, ok := bound["ExternalSecret"]; ok {
		t.Error("an ESO object is applied inventory, never a readiness binding")
	}
}

// An object claiming a release owner nothing declares is refused, not
// broadcast or dropped.
func TestPlacementPrerequisitesUndeclaredReleaseOwner(t *testing.T) {
	in := placementBuildInput()
	in.Shape.Manifests = openbaoDoc("apps/v1", "Deployment", "store", "spec: {}\n") + in.Shape.Manifests
	if _, err := Build(t.Context(), in); err == nil || !strings.Contains(err.Error(), openbaoOwner) {
		t.Fatalf("undeclared release owner: %v", err)
	}
}
