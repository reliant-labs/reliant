package bundle

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/release"
)

// canaryValue is a string that appears nowhere in forge, so finding it
// anywhere in a projected shape proves a Secret value survived the
// projection. See TestProjectShapeRedactsEverySecretValue (F-13).
const canaryValue = "ZmFrZS1kYi1wYXNzd29yZC1DQU5BUlk="

// fixtureStream is the shape of a real render reduced to the five cases the
// projection distinguishes: a release-bound Deployment, a LoadBalancer
// Service with a pinned address, a volume, a Secret with a value, and the
// objects a declared ManagedDatabase expands to.
//
// It carries the `# cluster:` header `forge env render` prints, because that
// header IS the input contract — reading routing off the stream is what keeps
// this package from modelling the router a second time.
const fixtureStream = `# cluster: gke-prod
apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
  namespace: shop-prod
  labels:
    app.kubernetes.io/name: api
    forge.dev/workload: api
spec:
  replicas: 3
  template:
    spec:
      containers:
        - name: api
          image: ghcr.io/acme/api@sha256:1111111111111111111111111111111111111111111111111111111111111111
---
# cluster: gke-prod
apiVersion: v1
kind: Service
metadata:
  name: api
  namespace: shop-prod
  labels:
    forge.dev/workload: api
spec:
  type: LoadBalancer
  loadBalancerIP: 34.1.2.3
  ports:
    - port: 443
---
# cluster: gke-prod
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: orders-data
  namespace: shop-prod
  labels:
    forge.dev/workload: orders
spec:
  resources:
    requests:
      storage: 10Gi
---
# cluster: gke-prod
apiVersion: v1
kind: Secret
metadata:
  name: orders-superuser
  namespace: shop-prod
  labels:
    forge.dev/workload: orders
type: kubernetes.io/basic-auth
data:
  password: ` + canaryValue + `
  username: cG9zdGdyZXM=
---
# cluster: gke-prod
apiVersion: postgresql.cnpg.io/v1
kind: Cluster
metadata:
  name: orders
  namespace: shop-prod
  labels:
    forge.dev/workload: orders
spec:
  instances: 1
---
# cluster: gke-prod, gke-daemon
apiVersion: v1
kind: Namespace
metadata:
  name: shop-prod
`

const apiDigest = "sha256:1111111111111111111111111111111111111111111111111111111111111111"

func fixtureInput() ShapeInput {
	return ShapeInput{
		Kind: release.EnvPersistent,
		Workloads: []release.ShapeWorkload{
			{Name: "api", Runtime: "cluster", Cluster: "gke-prod", Artifact: "api"},
		},
		Secrets: []release.ShapeSecret{
			{Name: "DATABASE_URL", Provider: "external", DeclaredBy: []string{"api"}},
		},
		Domains:           []string{"api.acme.test"},
		Clusters:          []string{"gke-daemon", "gke-prod"},
		Manifests:         fixtureStream,
		Images:            map[string]string{"api": apiDigest},
		StatefulWorkloads: []string{"orders"},
	}
}

func projectFixture(t *testing.T, in ShapeInput) release.Shape {
	t.Helper()
	shape, err := ProjectShape(in)
	if err != nil {
		t.Fatalf("ProjectShape: %v", err)
	}
	return shape
}

func objectNamed(t *testing.T, shape release.Shape, kind, name string) release.ShapeObject {
	t.Helper()
	for _, o := range shape.Objects {
		if o.Kind == kind && o.Name == name {
			return o
		}
	}
	t.Fatalf("no %s named %q among %d object(s)", kind, name, len(shape.Objects))
	return release.ShapeObject{}
}

// TestProjectShapeOmitsSecrets is F-13 at its strongest: a Secret is not in the
// shape at all, so no hash of one exists to leak. A shape is stored forever
// and shown to every member of an org; a Secret's own value is synced by
// forge and never recorded.
func TestProjectShapeOmitsSecrets(t *testing.T) {
	shape := projectFixture(t, fixtureInput())
	for _, o := range shape.Objects {
		if o.Kind == "Secret" {
			t.Errorf("the shape names Secret %q", o.Name)
		}
	}
	raw, err := json.Marshal(shape)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), canaryValue) {
		t.Fatal("the canary survived into the shape")
	}
}

// TestProjectShapeHashIsStableAcrossDocumentOrder: the hash has to mean "this
// object", so re-ordering the render's documents must not change a single
// one. Without it every cosmetic render change would read as drift on every
// object, which is the signal the hash exists to carry.
func TestProjectShapeHashIsStableAcrossDocumentOrder(t *testing.T) {
	forward := projectFixture(t, fixtureInput())

	reversed := fixtureInput()
	docs := strings.Split(reversed.Manifests, "\n---\n")
	for i, j := 0, len(docs)-1; i < j; i, j = i+1, j-1 {
		docs[i], docs[j] = docs[j], docs[i]
	}
	reversed.Manifests = strings.Join(docs, "\n---\n")

	a, err := forward.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	b, err := projectFixture(t, reversed).Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if string(a) != string(b) {
		t.Errorf("the shape depends on document order:\n forward: %s\nreversed: %s", a, b)
	}
}

// TestProjectShapeConfigHashIgnoresTheImageDigest is what makes "promote a
// new release" and "the KCL moved" one comparison: a bundle whose images
// changed and nothing else has the same ConfigHash per object and a different
// Hash.
func TestProjectShapeConfigHashIgnoresTheImageDigest(t *testing.T) {
	const nextDigest = "sha256:2222222222222222222222222222222222222222222222222222222222222222"

	before := objectNamed(t, projectFixture(t, fixtureInput()), "Deployment", "api")

	promoted := fixtureInput()
	promoted.Manifests = strings.ReplaceAll(promoted.Manifests, apiDigest, nextDigest)
	promoted.Images = map[string]string{"api": nextDigest}
	after := objectNamed(t, projectFixture(t, promoted), "Deployment", "api")

	if before.ConfigHash != after.ConfigHash {
		t.Errorf("ConfigHash moved with the image digest: %s → %s", before.ConfigHash, after.ConfigHash)
	}
	if before.Hash == after.Hash {
		t.Error("Hash did NOT move with the image digest: a new release would be indistinguishable from a no-op")
	}
	if got := before.Images["api"]; got != apiDigest {
		t.Errorf("Deployment images: got %q for api, want %q", got, apiDigest)
	}
	// An object that runs no release-bound image carries no Images at all;
	// an empty map and a nil map must not read alike.
	if ns := objectNamed(t, projectFixture(t, fixtureInput()), "Namespace", "shop-prod"); ns.Images != nil {
		t.Errorf("Namespace carries images %v, want none", ns.Images)
	}
}

// TestProjectShapeRecordsIdentityAndStateful: both drive STOP-class findings
// in the deploy plan, so an object that holds data or an address the world
// points at has to be marked as one.
func TestProjectShapeRecordsIdentityAndStateful(t *testing.T) {
	shape := projectFixture(t, fixtureInput())

	svc := objectNamed(t, shape, "Service", "api")
	if got := svc.Identity[release.IdentityType]; got != "LoadBalancer" {
		t.Errorf("Service identity type: got %q, want LoadBalancer", got)
	}
	if got := svc.Identity[release.IdentityLoadBalancerIP]; got != "34.1.2.3" {
		t.Errorf("Service load_balancer_ip: got %q, want 34.1.2.3", got)
	}

	// The database's objects hold data their KIND does not announce: a
	// CNPG Cluster is a CustomResource like any other, and deleting it
	// takes the volumes with it.
	if cnpg := objectNamed(t, shape, "Cluster", "orders"); !cnpg.Stateful {
		t.Error("the declared database's Cluster is not marked stateful")
	}
	// A workload with no data and no address carries neither mark.
	dep := objectNamed(t, shape, "Deployment", "api")
	if dep.Stateful || dep.Identity != nil {
		t.Errorf("Deployment api: stateful=%v identity=%v, want neither", dep.Stateful, dep.Identity)
	}
}

// TestProjectShapeAttributesEachObjectToEveryClusterItLandsOn: drift is a
// per-cluster fact — the same Namespace can be correct on one cluster and
// missing from another — so a replicated document becomes one object per
// cluster. A shape that named it once could not say which.
func TestProjectShapeAttributesEachObjectToEveryClusterItLandsOn(t *testing.T) {
	shape := projectFixture(t, fixtureInput())

	var namespaceClusters []string
	for _, o := range shape.Objects {
		if o.Kind == "Namespace" {
			namespaceClusters = append(namespaceClusters, o.Cluster)
		}
	}
	if got := strings.Join(namespaceClusters, ","); got != "gke-daemon,gke-prod" {
		t.Errorf("the replicated Namespace lands on %q, want gke-daemon,gke-prod", got)
	}
	for _, o := range shape.Objects {
		if o.Kind != "Namespace" && o.Cluster != "gke-prod" {
			t.Errorf("%s %s: cluster %q, want gke-prod", o.Kind, o.Name, o.Cluster)
		}
	}
}

// TestProjectShapeKeepsAnUnroutedObject: a host-only env renders objects the
// deploy layer routes nowhere ("# cluster: (none declared)"). They are part
// of what the env declares, and dropping them would make the shape disagree
// with the render's own object count.
func TestProjectShapeKeepsAnUnroutedObject(t *testing.T) {
	shape := projectFixture(t, ShapeInput{
		Kind: release.EnvLocal,
		Manifests: `# cluster: (none declared)
apiVersion: v1
kind: ConfigMap
metadata:
  name: app-config
`,
	})
	if len(shape.Objects) != 1 {
		t.Fatalf("objects: got %d, want 1", len(shape.Objects))
	}
	if cm := shape.Objects[0]; cm.Cluster != "" || cm.Name != "app-config" {
		t.Errorf("got cluster %q name %q, want \"\" app-config", cm.Cluster, cm.Name)
	}
}

// TestProjectShapeRefusesAnUnusableRender: a document with no kind or name
// has no identity, so it cannot be hashed into a set that is compared by
// identity. Refusing beats recording an object nothing can ever match.
func TestProjectShapeRefusesAnUnusableRender(t *testing.T) {
	for name, in := range map[string]ShapeInput{
		"no kind": {Kind: release.EnvLocal, Manifests: "apiVersion: v1\nmetadata:\n  name: x\n"},
		"no name": {Kind: release.EnvLocal, Manifests: "apiVersion: v1\nkind: ConfigMap\n"},
		"no env kind": {Kind: "", Manifests: `apiVersion: v1
kind: ConfigMap
metadata:
  name: x
`},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ProjectShape(in); err == nil {
				t.Fatal("ProjectShape accepted an unusable render")
			}
		})
	}
}

// TestProjectShapeEncodesEmptyListsAsLists pins the thing a reader cannot
// recover from: "no secrets declared" and "not recorded" must not both arrive
// as null.
func TestProjectShapeEncodesEmptyListsAsLists(t *testing.T) {
	shape := projectFixture(t, ShapeInput{
		Kind:      release.EnvLocal,
		Manifests: "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: x\n",
	})
	encoded, err := shape.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, field := range []string{"workloads", "secrets", "domains", "clusters"} {
		if got := string(decoded[field]); got != "[]" {
			t.Errorf("%s encoded as %s, want []", field, got)
		}
	}
}
