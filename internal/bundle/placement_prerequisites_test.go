package bundle

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/release"
)

func placementBuildInput() BuildInput {
	return BuildInput{Project: "p", Env: "prod", CreatedAt: time.Unix(1700000000, 0).UTC(), Provenance: release.Provenance{Repo: "r", Commit: strings.Repeat("c", 40), Tree: strings.Repeat("0", 40), ForgeVersion: "v"}, Placements: []PlacementDeclaration{{Workload: "api", Runtime: "cluster", Actuator: "forge"}, {Workload: "web", Runtime: "hosted", Actuator: "platform", Dependencies: []string{"api"}}}, Shape: ShapeInput{Kind: release.EnvPersistent, Manifests: `# cluster: prod
apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
  namespace: app
  labels:
    forge.dev/workload: api
spec:
  replicas: 1
  selector:
    matchLabels: {app: api}
  template:
    metadata:
      labels: {app: api}
    spec:
      containers:
        - name: api
          image: api:v1
---
# cluster: prod
apiVersion: batch/v1
kind: Job
metadata:
  name: migrate-abcd1234
  namespace: app
spec:
  template:
    spec:
      restartPolicy: Never
      containers:
        - name: migrate
          image: migrator:v1
---
# cluster: prod
apiVersion: v1
kind: ConfigMap
metadata:
  name: config
  namespace: app
data: {mode: production}
---
# cluster: hosted
apiVersion: forge.dev/v1alpha1
kind: StaticSite
metadata:
  name: web
spec: {}
`}}
}

func TestPlacementPrerequisitesSameParseBindings(t *testing.T) {
	in := placementBuildInput()
	built, err := Build(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	p := built.Doc.PlacementPrerequisites
	if p == nil || len(p.Items) != 2 || len(p.Edges) != 1 {
		t.Fatalf("graph %+v", p)
	}
	if len(p.Bindings) != 4 {
		t.Fatalf("bindings %+v", p.Bindings)
	}
	for _, b := range p.Bindings {
		found := false
		for _, o := range built.Doc.Shape.Objects {
			if o.Cluster == b.Object.Cluster && o.Kind == b.Object.Kind && o.Namespace == b.Object.Namespace && o.Name == b.Object.Name {
				found = true
				if o.Hash != b.ManifestHash {
					t.Fatal("binding differs from SAME ShapeObject.Hash")
				}
			}
		}
		if !found {
			t.Fatal("binding missing from sealed inventory")
		}
		if b.Object.Kind == "Job" && b.JobContract != release.KubernetesJobCompleteV1 {
			t.Fatal("missing ordinary Job completion contract")
		}
	}
	again, err := Build(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if built.Digest != again.Digest {
		t.Fatal("nondeterministic graph")
	}
}

func TestPlacementPrerequisitesUnitAfter(t *testing.T) {
	in := placementBuildInput()
	// Same-stage controller dependency through unit.after is retained.
	in.Shape.Manifests = strings.Replace(in.Shape.Manifests, "    forge.dev/workload: api", "    forge.dev/workload: api\n    forge.dev/delivery-unit: server", 1)
	in.Shape.Manifests += `---
# cluster: prod
apiVersion: apps/v1
kind: Deployment
metadata:
  name: worker
  namespace: app
  labels:
    forge.dev/workload: worker
    forge.dev/delivery-unit: worker
spec: {}
`
	in.Placements = append(in.Placements, PlacementDeclaration{Workload: "worker", Runtime: "cluster", Actuator: "forge"})
	in.Units = []release.DeliveryUnit{{Name: "server", After: []string{}}, {Name: "worker", After: []string{"server"}}}
	b, err := Build(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, edge := range b.Doc.PlacementPrerequisites.Edges {
		if edge.Predecessor.Name == "api" && edge.Dependent.Name == "worker" {
			found = true
		}
	}
	if !found {
		t.Fatal("same-stage unit.after edge dropped")
	}
}

// unitDoc is one cluster object in a named delivery unit.
func unitDoc(kind, apiVersion, name, workload, unit string) string {
	labels := "    forge.dev/delivery-unit: " + unit + "\n"
	if workload != "" {
		labels += "    forge.dev/workload: " + workload + "\n"
	}
	return "---\n# cluster: prod\napiVersion: " + apiVersion + "\nkind: " + kind + "\nmetadata:\n  name: " + name +
		"\n  namespace: app\n  labels:\n" + labels + "spec: {}\n"
}

const hostedWebDoc = "---\n# cluster: hosted\napiVersion: forge.dev/v1alpha1\nkind: StaticSite\nmetadata:\n  name: web\nspec: {}\n"

// Delivery-unit order follows release.PlanTrees: a named unit with no
// declared `after` waits for base only when base renders. With no base
// objects at all the bundle builds, as a pure cluster env does.
func TestPlacementPrerequisitesNoBaseUnit(t *testing.T) {
	in := placementBuildInput()
	in.Shape.Manifests = unitDoc("Deployment", "apps/v1", "api", "api", "server") + hostedWebDoc
	in.Units = []release.DeliveryUnit{{Name: "server"}}
	b, err := Build(t.Context(), in)
	if err != nil {
		t.Fatalf("mixed env without base objects: %v", err)
	}
	p := b.Doc.PlacementPrerequisites
	if len(p.Edges) != 1 || p.Edges[0].Predecessor.Name != "api" || p.Edges[0].Dependent.Name != "web" {
		t.Fatalf("edges = %+v, want only api -> web", p.Edges)
	}
}

// A unit holding only support objects is not an item, and ordering passes
// through it: db (controller) <- support (ConfigMap only) <- server.
// Base holds only a support object too.
func TestPlacementPrerequisitesSupportOnlyUnits(t *testing.T) {
	in := placementBuildInput()
	in.Placements = append(in.Placements, PlacementDeclaration{Workload: "db", Runtime: "cluster", Actuator: "forge"})
	in.Shape.Manifests = unitDoc("ServiceAccount", "v1", "base-sa", "", release.DefaultDeliveryUnit) +
		unitDoc("Deployment", "apps/v1", "db", "db", "data") +
		unitDoc("ConfigMap", "v1", "settings", "", "support") +
		unitDoc("Deployment", "apps/v1", "api", "api", "server") + hostedWebDoc
	in.Units = []release.DeliveryUnit{{Name: "data"}, {Name: "support", After: []string{"data"}}, {Name: "server", After: []string{"support"}}}
	b, err := Build(t.Context(), in)
	if err != nil {
		t.Fatalf("support-only units: %v", err)
	}
	want := map[string]bool{"db->api": true, "api->web": false}
	for _, e := range b.Doc.PlacementPrerequisites.Edges {
		key := e.Predecessor.Name + "->" + e.Dependent.Name
		if _, ok := want[key]; ok {
			want[key] = true
		}
		if e.Predecessor.Kind == "ConfigMap" || e.Dependent.Kind == "ConfigMap" || e.Predecessor.Kind == "ServiceAccount" {
			t.Fatalf("support object became an item: %+v", e)
		}
	}
	for key, found := range want {
		if !found {
			t.Errorf("edge %s missing from %+v", key, b.Doc.PlacementPrerequisites.Edges)
		}
	}
}

func TestPlacementPrerequisitesRefusals(t *testing.T) {
	tests := map[string]struct {
		mutate func(*BuildInput)
		want   string
	}{
		"old reader":           {func(in *BuildInput) { in.Reader = &release.BundleReader{} }, release.BundleFeaturePlacementPrerequisites},
		"unknown declarations": {func(in *BuildInput) { in.Placements = nil }, "complete placement"},
		"missing peer":         {func(in *BuildInput) { in.Placements[1].Dependencies = []string{"missing"} }, "missing predecessor"},
		"hub":                  {func(in *BuildInput) { in.Placements[0].Actuator = "hub" }, "unsupported mixed"},
		"hosted to forge": {func(in *BuildInput) {
			in.Placements[1].Dependencies = nil
			in.Placements[0].Dependencies = []string{"web"}
		}, "hosted predecessor"},
		"cycle": {func(in *BuildInput) { in.Placements[0].Dependencies = []string{"web"} }, "cycle"},
		"unit missing peer": {func(in *BuildInput) {
			in.Units = []release.DeliveryUnit{{Name: release.DefaultDeliveryUnit, After: []string{"missing"}}}
		}, "which nothing declares or renders"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			in := placementBuildInput()
			tc.mutate(&in)
			_, err := Build(t.Context(), in)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
		})
	}
}

func TestPlacementPrerequisitesSnapshotDependenciesStayFrozen(t *testing.T) {
	declarations := []PlacementDeclaration{{Workload: "api", Runtime: "cluster", Actuator: "forge"}, {Workload: "web", Runtime: "hosted", Actuator: "platform", Dependencies: []string{}}}
	s := release.Snapshot{Consumers: []release.Consumer{{Key: release.ConsumerKey{Cluster: "prod", Workload: "api"}, Slots: []string{"container:api"}, DependenciesDeclared: true}, {Key: release.ConsumerKey{Cluster: "frontend", Workload: "web"}, Slots: []string{"frontend"}, DependenciesDeclared: false}}}
	got, err := snapshotPlacements(s, declarations)
	if err != nil {
		t.Fatal(err)
	}
	if len(got[1].Dependencies) != 1 || got[1].Dependencies[0] != "api" {
		t.Fatalf("checkout independence replaced frozen default: %+v", got)
	}
	s.Consumers[1].DependenciesDeclared = true
	declarations[1].Dependencies = []string{"api"}
	got, err = snapshotPlacements(s, declarations)
	if err != nil {
		t.Fatal(err)
	}
	if len(got[1].Dependencies) != 0 {
		t.Fatalf("checkout dependency replaced frozen independence: %+v", got)
	}
}

// A build-only consumer is an artifact, not a placement: a sealed mixed
// composition carrying one projects only its runtime consumers.
func TestPlacementPrerequisitesSnapshotSkipsBuildOnly(t *testing.T) {
	declarations := []PlacementDeclaration{{Workload: "api", Runtime: "cluster", Actuator: "forge"}, {Workload: "web", Runtime: "hosted", Actuator: "platform"}}
	s := release.Snapshot{Consumers: []release.Consumer{
		{Key: release.ConsumerKey{Cluster: "prod", Workload: "api"}, Slots: []string{"container:api"}, DependenciesDeclared: true},
		{Key: release.ConsumerKey{Cluster: "build-only", Workload: "cli"}, Slots: []string{"build-only"}, DependenciesDeclared: true},
		{Key: release.ConsumerKey{Cluster: "frontend", Workload: "web"}, Slots: []string{"frontend"}},
	}}
	got, err := snapshotPlacements(s, declarations)
	if err != nil {
		t.Fatalf("build-only consumer blocked sealing: %v", err)
	}
	if len(got) != 2 || got[1].Workload != "web" || !slices.Equal(got[1].Dependencies, []string{"api"}) {
		t.Fatalf("placements = %+v, want api and web (after api only)", got)
	}
}

func TestPlacementPrerequisitesIndependentAndPure(t *testing.T) {
	in := placementBuildInput()
	in.Placements[1].Dependencies = []string{}
	b, err := Build(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	if b.Doc.PlacementPrerequisites == nil || len(b.Doc.PlacementPrerequisites.Edges) != 0 {
		t.Fatal("explicit independent graph lost")
	}
	// Pure historical input gets no new wire field, even with an old reader.
	in.Shape.Manifests = strings.Split(in.Shape.Manifests, "---\n# cluster: hosted")[0]
	in.Placements = nil
	in.Reader = &release.BundleReader{}
	b, err = Build(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	if b.Doc.PlacementPrerequisites != nil || strings.Contains(string(b.ConfigBlob()), "placement_prerequisites") {
		t.Fatal("pure bundle changed")
	}
}

// ownedDoc is a cluster object owned only through forge.dev/release-owners,
// as a raw manifest consumer's and a native provider's (OpenBao's) are.
func ownedDoc(owner, apiVersion, kind, name, body string) string {
	return "---\n# cluster: prod\napiVersion: " + apiVersion + "\nkind: " + kind + "\nmetadata:\n  name: " + name +
		"\n  namespace: app\n  labels:\n    app.kubernetes.io/name: " + name + "\n  annotations:\n    forge.dev/release-owners: '[\"" + owner + "\"]'\n" + body
}

// A raw manifest consumer and a native provider are owned only by the
// release-owners annotation (the rule SnapshotInventory reads). Their
// controllers are ordinary items, their Jobs and config bind to them, the
// passive objects they render are inventory, and the hosted frontend waits
// for both.
func TestPlacementPrerequisitesAnnotationOwnedConsumers(t *testing.T) {
	in := placementBuildInput()
	const bao = "openbao-app-store"
	in.Placements = []PlacementDeclaration{
		{Workload: "api", Runtime: "cluster", Actuator: "forge"},
		{Workload: "nats", Runtime: "cluster", Actuator: "forge"},
		{Workload: bao, Runtime: "cluster", Actuator: "forge"},
		{Workload: "web", Runtime: "hosted", Actuator: "platform", Dependencies: []string{"api", "nats", bao}},
	}
	in.Shape.Manifests += ownedDoc("nats", "apps/v1", "StatefulSet", "nats", "spec: {}\n") +
		ownedDoc(bao, "apps/v1", "Deployment", "store", "spec: {}\n") +
		ownedDoc(bao, "batch/v1", "Job", "store-converge-abcd1234", "spec: {}\n") +
		ownedDoc(bao, "v1", "ConfigMap", "store-config", "data: {config.hcl: x}\n") +
		ownedDoc(bao, "external-secrets.io/v1", "ExternalSecret", "store-bootstrap", "spec: {}\n") +
		ownedDoc(bao, "cert-manager.io/v1", "Issuer", "store-ca", "spec: {}\n")
	b, err := Build(t.Context(), in)
	if err != nil {
		t.Fatalf("annotation-owned consumers must place: %v", err)
	}
	p := b.Doc.PlacementPrerequisites
	item := map[string]release.PlacementKey{}
	for _, it := range p.Items {
		if it.Key.Kind == "StatefulSet" || it.Key.Kind == "Deployment" || it.Key.Kind == "StaticSite" {
			item[it.Workload] = it.Key
		}
	}
	if item["nats"].Name != "nats" || item[bao].Name != "store" {
		t.Fatalf("controller items = %+v", p.Items)
	}
	waits := map[string]bool{}
	for _, e := range p.Edges {
		if e.Dependent == item["web"] {
			waits[e.Predecessor.Name] = true
		}
	}
	if !waits["nats"] || !waits["store"] {
		t.Fatalf("the hosted frontend does not wait for both: %+v", p.Edges)
	}
	bound := map[string]string{}
	for _, bd := range p.Bindings {
		if bd.Item == item[bao] {
			bound[bd.Object.Kind] = bd.JobContract
		}
	}
	if _, ok := bound["ConfigMap"]; !ok || bound["Job"] != release.KubernetesJobCompleteV1 {
		t.Fatalf("provider bindings = %+v", bound)
	}
	if _, ok := bound["ExternalSecret"]; ok {
		t.Fatal("an ESO object is applied inventory, never a readiness binding")
	}
}

// clusterDoc is an unowned cluster object in namespace app (or cluster-scoped
// when namespaced is false), with extra metadata lines verbatim.
func clusterDoc(apiVersion, kind, name string, namespaced bool, meta string) string {
	ns := ""
	if namespaced {
		ns = "\n  namespace: app"
	}
	return "---\n# cluster: prod\napiVersion: " + apiVersion + "\nkind: " + kind + "\nmetadata:\n  name: " + name + ns + "\n" + meta + "spec: {}\n"
}

// What a real two-cluster env renders beside its controllers: scheduled work,
// the cluster's API and scheduling vocabulary, admission, every route and
// policy attachment, and custom resources. No kind is refused for being
// unknown: each is applied inventory, the bundle composes, and none of them
// becomes an item or a binding.
func TestPlacementPrerequisitesPassiveKinds(t *testing.T) {
	in := placementBuildInput()
	for _, d := range []struct {
		apiVersion, kind string
		namespaced       bool
	}{
		{"batch/v1", "CronJob", true},
		{"apiextensions.k8s.io/v1", "CustomResourceDefinition", false},
		{"scheduling.k8s.io/v1", "PriorityClass", false},
		{"storage.k8s.io/v1", "StorageClass", false},
		{"node.k8s.io/v1", "RuntimeClass", false},
		{"admissionregistration.k8s.io/v1", "ValidatingAdmissionPolicy", false},
		{"admissionregistration.k8s.io/v1", "ValidatingAdmissionPolicyBinding", false},
		{"admissionregistration.k8s.io/v1", "MutatingWebhookConfiguration", false},
		{"admissionregistration.k8s.io/v1", "ValidatingWebhookConfiguration", false},
		{"gateway.networking.k8s.io/v1", "GRPCRoute", true},
		{"gateway.envoyproxy.io/v1alpha1", "EnvoyProxy", true},
		{"networking.gke.io/v1", "HealthCheckPolicy", true},
		// A custom resource: its controller is an item of its own.
		{"reliant.dev/v1alpha1", "DaemonRollout", true},
	} {
		in.Shape.Manifests += clusterDoc(d.apiVersion, d.kind, strings.ToLower(d.kind), d.namespaced, "")
	}
	b, err := Build(t.Context(), in)
	if err != nil {
		t.Fatalf("passive inventory must compose: %v", err)
	}
	p := b.Doc.PlacementPrerequisites
	for _, it := range p.Items {
		if !release.PlacementRuntimeKind(it.Key.Kind) && it.Key.Kind != "StaticSite" {
			t.Errorf("applied inventory became an item: %+v", it.Key)
		}
	}
	for _, bd := range p.Bindings {
		if bd.Object.Cluster != release.BundleHostedCluster && release.PlacementPassiveKind(bd.Object.Kind) {
			t.Errorf("applied inventory became a readiness binding: %+v", bd.Object)
		}
	}
}

// A raw manifest consumer that renders only routes and policies (no
// controller) is applied inventory: it is no item, a frontend waiting on
// every server does not wait on it, and its objects are env-level. A hosted
// part with no record is still refused.
func TestPlacementPrerequisitesInventoryOnlyConsumer(t *testing.T) {
	in := placementBuildInput()
	in.Placements = []PlacementDeclaration{
		{Workload: "api", Runtime: "cluster", Actuator: "forge"},
		{Workload: "routes", Runtime: "cluster", Actuator: "forge"},
		{Workload: "web", Runtime: "hosted", Actuator: "platform", Dependencies: []string{"api", "routes"}},
	}
	in.Shape.Manifests += ownedDoc("routes", "gateway.networking.k8s.io/v1", "HTTPRoute", "api-route", "spec: {}\n") +
		ownedDoc("routes", "v1", "ConfigMap", "route-limits", "data: {rps: '10'}\n")
	b, err := Build(t.Context(), in)
	if err != nil {
		t.Fatalf("a controller-less manifest consumer must compose: %v", err)
	}
	p := b.Doc.PlacementPrerequisites
	for _, it := range p.Items {
		if it.Workload == "routes" {
			t.Fatalf("an inventory-only consumer became an item: %+v", it)
		}
	}
	limits := false
	for _, bd := range p.Bindings {
		limits = limits || (bd.Object.Name == "route-limits" && bd.Item.Name == "api")
	}
	if !limits {
		t.Fatalf("an inventory-only consumer's ConfigMap is env-level and binds to its namespace's items: %+v", p.Bindings)
	}

	in.Placements[1].Dependencies = []string{"absent"}
	if _, err := Build(t.Context(), in); err == nil || !strings.Contains(err.Error(), "missing predecessor") {
		t.Fatalf("a typo'd peer is still refused, got %v", err)
	}
	in = placementBuildInput()
	in.Placements = append(in.Placements, PlacementDeclaration{Workload: "docs", Runtime: "hosted", Actuator: "platform"})
	if _, err := Build(t.Context(), in); err == nil || !strings.Contains(err.Error(), "no sealed controller/record") {
		t.Fatalf("a hosted part with no record, got %v", err)
	}
}

// A ConfigMap whose only owner runs in another namespace would be bound to
// nothing, which a control plane's reader refuses; the producer refuses it
// first, naming it.
func TestPlacementPrerequisitesRefusesAnUnboundConfigMap(t *testing.T) {
	in := placementBuildInput()
	in.Shape.Manifests += strings.Replace(ownedDoc("api", "v1", "ConfigMap", "api-elsewhere", "data: {k: v}\n"), "namespace: app", "namespace: other", 1)
	if _, err := Build(t.Context(), in); err == nil || !strings.Contains(err.Error(), "bound to no placement item") {
		t.Fatalf("got %v, want the unbound ConfigMap refused", err)
	}
}

// web -> routes (no controller) -> api: the dependency resolves THROUGH the
// inventory-only component, so web still waits for api.
func TestPlacementPrerequisitesDependencyThroughInventoryOnly(t *testing.T) {
	in := placementBuildInput()
	in.Placements = []PlacementDeclaration{
		{Workload: "api", Runtime: "cluster", Actuator: "forge"},
		{Workload: "routes", Runtime: "cluster", Actuator: "forge", Dependencies: []string{"api"}},
		{Workload: "web", Runtime: "hosted", Actuator: "platform", Dependencies: []string{"routes"}},
	}
	in.Shape.Manifests += ownedDoc("routes", "gateway.networking.k8s.io/v1", "HTTPRoute", "api-route", "spec: {}\n")
	b, err := Build(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	waits := false
	for _, e := range b.Doc.PlacementPrerequisites.Edges {
		waits = waits || (e.Predecessor.Name == "api" && e.Dependent.Name == "web")
	}
	if !waits {
		t.Fatalf("web no longer waits for api through routes: %+v", b.Doc.PlacementPrerequisites.Edges)
	}

	// A cycle that closes on an item through the inventory-only component is
	// refused, never dropped.
	in.Placements[0].Dependencies = []string{"routes"}
	if _, err := Build(t.Context(), in); err == nil {
		t.Fatal("api -> routes -> api accepted")
	}
}

// A control plane that advertises only the retired placement-prerequisites-v1
// (its decoder predates the shared passive set and Passive) is refused before
// composition: it is never handed a graph it would reject or under-check.
func TestPlacementPrerequisitesRefusesAV1OnlyReader(t *testing.T) {
	in := placementBuildInput()
	in.Reader = &release.BundleReader{Features: []string{"placement-prerequisites-v1"}}
	if _, err := Build(t.Context(), in); err == nil || !strings.Contains(err.Error(), release.BundleFeaturePlacementPrerequisites) {
		t.Fatalf("a v1-only reader: got %v, want a refusal naming %s", err, release.BundleFeaturePlacementPrerequisites)
	}
}
