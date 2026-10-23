package cluster

import (
	"context"
	"sort"
	"strings"
	"sync"
	"testing"
)

// The manifest-driven Secret / ConfigMap check resolves each reference where
// kubelet resolves it: in the REFERENCING object's own namespace. It used to
// look every reference up in the env's deploy namespace, so a workload the
// bundle renders into another namespace was refused for a Secret that existed
// right where it is used — control-plane's prod release was blocked on
// observability-data/obs-credentials and observability/otel-agent-token,
// reported as control-plane-prod/obs-credentials and
// control-plane-prod/otel-agent-token. The fixtures below are cut from that
// render.

const observabilityManifests = `apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: clickhouse
  namespace: observability-data
spec:
  template:
    spec:
      containers:
        - name: clickhouse
          image: clickhouse/clickhouse-server:24.8
          env:
            - name: CH_ADMIN_PASSWORD
              valueFrom:
                secretKeyRef:
                  name: obs-credentials
                  key: CH_ADMIN_PASSWORD
            - name: CH_SHARDS
              valueFrom:
                configMapKeyRef:
                  name: obs-topology
                  key: shards
---
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: otel-agent
  namespace: observability
spec:
  template:
    spec:
      containers:
        - name: otel-agent
          image: otel/opentelemetry-collector-contrib:0.111.0
          env:
            - name: OTEL_TOKEN
              valueFrom:
                secretKeyRef:
                  name: otel-agent-token
                  key: token
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: admin-server
spec:
  template:
    spec:
      containers:
        - name: admin-server
          image: ghcr.io/acme/admin-server:abc
          envFrom:
            - secretRef:
                name: admin-server-secrets
`

// placedSecretGetter resolves Secret keys per (kubectl context, namespace/name),
// and records every lookup so a test can assert WHERE the gate looked.
type placedSecretGetter struct {
	byCluster map[string]map[string]map[string]struct{} // kctx -> "ns/name" -> keys
	mu        *sync.Mutex
	asked     *[]string // "kctx:ns/name"
}

func (g placedSecretGetter) GetSecretKeys(_ context.Context, kctx, ns, name string) (map[string]struct{}, bool, error) {
	if g.asked != nil {
		g.mu.Lock()
		*g.asked = append(*g.asked, kctx+":"+ns+"/"+name)
		g.mu.Unlock()
	}
	keys, ok := g.byCluster[kctx][ns+"/"+name]
	return keys, ok, nil
}

type placedConfigMapGetter struct {
	byCluster map[string]map[string]map[string]struct{} // kctx -> "ns/name" -> keys
}

func (g placedConfigMapGetter) GetConfigMapKeys(_ context.Context, kctx, ns, name string) (map[string]struct{}, bool, error) {
	keys, ok := g.byCluster[kctx][ns+"/"+name]
	return keys, ok, nil
}

func newPlacedSecretGetter(byCluster map[string]map[string]map[string]struct{}) (placedSecretGetter, func() []string) {
	var asked []string
	g := placedSecretGetter{byCluster: byCluster, mu: &sync.Mutex{}, asked: &asked}
	return g, func() []string {
		g.mu.Lock()
		defer g.mu.Unlock()
		out := append([]string(nil), asked...)
		sort.Strings(out)
		return out
	}
}

// A workload rendered into another namespace is satisfied by the Secret and
// ConfigMap in THAT namespace — the shape of the blocked prod release.
func TestPreflight_ReferenceResolvesInReferencingObjectsNamespace(t *testing.T) {
	const hub = "gke_prod"
	secrets, asked := newPlacedSecretGetter(map[string]map[string]map[string]struct{}{
		hub: {
			"observability-data/obs-credentials":      keySet("CH_ADMIN_PASSWORD"),
			"observability/otel-agent-token":          keySet("token"),
			"control-plane-prod/admin-server-secrets": keySet("x"),
		},
	})
	res, err := PreflightReport(context.Background(), PreflightOpts{
		Manifests: observabilityManifests,
		Context:   hub,
		Namespace: "control-plane-prod",
		Secrets:   secrets,
		ConfigMaps: placedConfigMapGetter{byCluster: map[string]map[string]map[string]struct{}{
			hub: {"observability-data/obs-topology": keySet("shards")},
		}},
	})
	if err != nil {
		t.Fatalf("every referenced Secret and ConfigMap exists in its referencing object's namespace; got:\n%v", err)
	}
	if !res.OK() {
		t.Fatalf("result not OK: secrets=%v configmaps=%v", res.MissingSecretKeys, res.MissingConfigMapKeys)
	}
	want := []string{
		hub + ":control-plane-prod/admin-server-secrets",
		hub + ":observability-data/obs-credentials",
		hub + ":observability/otel-agent-token",
	}
	if got := asked(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("Secret lookups = %v; want %v (each in its referencing object's namespace)", got, want)
	}
}

// A Secret that is missing from the referencing namespace is reported under
// THAT namespace — even when a same-named Secret sits in the deploy namespace,
// which the old name-only lookup would have accepted.
func TestPreflight_MissingReferenceNamesTheReferencingNamespace(t *testing.T) {
	const hub = "gke_prod"
	secrets, _ := newPlacedSecretGetter(map[string]map[string]map[string]struct{}{
		hub: {
			// Decoys: right names, wrong namespace.
			"control-plane-prod/obs-credentials":  keySet("CH_ADMIN_PASSWORD"),
			"control-plane-prod/otel-agent-token": keySet("token"),
			// Present, but lacking the referenced key.
			"observability/otel-agent-token":          keySet("other"),
			"control-plane-prod/admin-server-secrets": keySet("x"),
		},
	})
	res, err := PreflightReport(context.Background(), PreflightOpts{
		Manifests: observabilityManifests,
		Context:   hub,
		Namespace: "control-plane-prod",
		Secrets:   secrets,
		ConfigMaps: placedConfigMapGetter{byCluster: map[string]map[string]map[string]struct{}{
			hub: {"control-plane-prod/obs-topology": keySet("shards")}, // decoy
		}},
	})
	if err == nil {
		t.Fatal("preflight passed while observability-data/obs-credentials does not exist")
	}
	if got := res.MissingSecretKeys["observability-data/obs-credentials"]; len(got) != 1 || got[0] != wholeSecretMarker {
		t.Errorf("observability-data/obs-credentials = %v; want [%s] (all: %v)", got, wholeSecretMarker, res.MissingSecretKeys)
	}
	if got := res.MissingSecretKeys["observability/otel-agent-token"]; len(got) != 1 || got[0] != "token" {
		t.Errorf("observability/otel-agent-token = %v; want [token] (all: %v)", got, res.MissingSecretKeys)
	}
	// An absent ConfigMap is reported as one, not as "(the Secret does not
	// exist)".
	if got := res.MissingConfigMapKeys["observability-data/obs-topology"]; len(got) != 1 || got[0] != "(the ConfigMap does not exist)" {
		t.Errorf("observability-data/obs-topology = %v; want [(the ConfigMap does not exist)] (all: %v)", got, res.MissingConfigMapKeys)
	}
	for k := range res.MissingSecretKeys {
		if strings.HasPrefix(k, "control-plane-prod/") {
			t.Errorf("reported %q, but every control-plane-prod reference is satisfied", k)
		}
	}
	if msg := err.Error(); !strings.Contains(msg, "Secret observability-data/obs-credentials missing keys") {
		t.Errorf("report must name the referencing namespace; got:\n%s", msg)
	}
}

// A Secret the bundle renders satisfies only references in its own namespace.
// Subtracting it by bare name hid a reference from another namespace that
// nothing provides.
func TestCollectManifestRefs_SelfProvidedIsNamespaceLocal(t *testing.T) {
	stream := observabilityManifests + `---
apiVersion: v1
kind: Secret
metadata:
  name: obs-credentials
  namespace: control-plane-prod
stringData:
  CH_ADMIN_PASSWORD: x
---
apiVersion: v1
kind: Secret
metadata:
  name: otel-agent-token
  namespace: observability
stringData:
  token: x
`
	const hub = "gke_prod"
	secrets, asked := newPlacedSecretGetter(map[string]map[string]map[string]struct{}{
		hub: {"control-plane-prod/admin-server-secrets": keySet("x")},
	})
	res, _ := PreflightReport(context.Background(), PreflightOpts{
		Manifests: stream,
		Context:   hub,
		Namespace: "control-plane-prod",
		Secrets:   secrets,
	})
	if _, ok := res.MissingSecretKeys["observability-data/obs-credentials"]; !ok {
		t.Errorf("a Secret rendered into control-plane-prod must not satisfy a reference from observability-data; missing=%v", res.MissingSecretKeys)
	}
	for _, a := range asked() {
		if strings.HasSuffix(a, "observability/otel-agent-token") {
			t.Errorf("observability/otel-agent-token is rendered in-stream and must not be looked up; asked %v", asked())
		}
	}
}

// A Secret this deploy provisions is exempt in the namespace it is provisioned
// into, and only there. An unset namespace means the deploy namespace.
func TestPreflight_ProvisionedExemptionMatchesNamespace(t *testing.T) {
	const hub = "gke_prod"
	secrets, asked := newPlacedSecretGetter(map[string]map[string]map[string]struct{}{
		hub: {"observability-data/obs-credentials": keySet("CH_ADMIN_PASSWORD")},
	})
	res, _ := PreflightReport(context.Background(), PreflightOpts{
		Manifests: observabilityManifests,
		Context:   hub,
		Namespace: "control-plane-prod",
		Secrets:   secrets,
		ProvisionedByDeploy: []ProvisionedSecret{
			{Name: "admin-server-secrets", By: SupplyKubeconfigSecret},                              // deploy namespace
			{Name: "otel-agent-token", Namespace: "control-plane-prod", By: SupplyKubeconfigSecret}, // wrong namespace
		},
	})
	if _, ok := res.MissingSecretKeys["control-plane-prod/admin-server-secrets"]; ok {
		t.Errorf("admin-server-secrets is provisioned into the deploy namespace and must be exempt; missing=%v", res.MissingSecretKeys)
	}
	if _, ok := res.MissingSecretKeys["observability/otel-agent-token"]; !ok {
		t.Errorf("otel-agent-token is provisioned into control-plane-prod, not observability: the reference must still be checked; missing=%v asked=%v",
			res.MissingSecretKeys, asked())
	}
}

// imagePullSecrets are read from the namespace of the pod that names them, so
// the cluster's pull credentials are resolved there.
func TestPreflight_PullCredsResolvedInReferencingNamespace(t *testing.T) {
	const ref = "ghcr.io/acme/private-agent:abc"
	manifest := `apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: otel-agent
  namespace: observability
spec:
  template:
    spec:
      imagePullSecrets:
        - name: ghcr-pull
      containers:
        - name: otel-agent
          image: ` + ref + `
`
	resolver := &recordingPullCreds{}
	res, err := runPreflightChecks(context.Background(), PreflightOpts{
		Manifests: manifest,
		Context:   "gke_prod",
		Namespace: "control-plane-prod",
		Images: credAwareImageChecker{
			fakeImageChecker: fakeImageChecker{authDenied: keySet(ref)},
			credPresent:      keySet(ref),
		},
		PullCreds: resolver,
	})
	if err != nil {
		t.Fatalf("runPreflightChecks: %v", err)
	}
	if got := strings.Join(resolver.asked, ","); got != "gke_prod:observability/ghcr-pull" {
		t.Errorf("pull creds resolved at %q; want gke_prod:observability/ghcr-pull", got)
	}
	if len(res.UnverifiableImages) != 0 {
		t.Errorf("the cluster's pull creds confirm the image; got unverifiable=%v", res.UnverifiableImages)
	}
}

// In an env that spans clusters, each reference is checked on the cluster its
// object is applied to, and findings are qualified "<context>:<ns>/<name>".
// The same DaemonSet renders to both clusters; the token exists only on the
// hub, so the miss is the daemon cluster's alone.
func TestPreflight_MultiClusterChecksEachReferenceWhereItIsApplied(t *testing.T) {
	const (
		hub    = "gke_reliant-labs-475814_us-central1_prod"
		daemon = "gke_reliant-labs-475814_us-central1-a_prod-daemon-v2"
	)
	agent := func(kctx string) string {
		return `apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: otel-agent
  namespace: observability
  labels:
    forge.dev/cluster: ` + kctx + `
spec:
  template:
    spec:
      containers:
        - name: otel-agent
          image: otel/opentelemetry-collector-contrib:0.111.0
          env:
            - name: OTEL_TOKEN
              valueFrom:
                secretKeyRef:
                  name: otel-agent-token
                  key: token
`
	}
	clickhouse := `apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: clickhouse
  namespace: observability-data
  labels:
    forge.dev/cluster: ` + hub + `
spec:
  template:
    spec:
      containers:
        - name: clickhouse
          image: clickhouse/clickhouse-server:24.8
          envFrom:
            - secretRef:
                name: obs-credentials
`
	// A Namespace carries no routing label: it is replicated everywhere and
	// references nothing.
	shared := `apiVersion: v1
kind: Namespace
metadata:
  name: observability
`
	manifests := strings.Join([]string{shared, agent(hub), agent(daemon), clickhouse}, "---\n")
	// Route by the forge.dev/cluster label, as ScopeManifestsToGroup does
	// first; unlabeled documents land on both.
	route := func(doc string) []string {
		for _, c := range []string{hub, daemon} {
			if strings.Contains(doc, "forge.dev/cluster: "+c) {
				return []string{c}
			}
		}
		return []string{hub, daemon}
	}
	secrets, asked := newPlacedSecretGetter(map[string]map[string]map[string]struct{}{
		hub: {
			"observability/otel-agent-token":     keySet("token"),
			"observability-data/obs-credentials": keySet("x"),
		},
		daemon: {},
	})
	res, err := PreflightReport(context.Background(), PreflightOpts{
		Manifests:        manifests,
		Context:          hub,
		Namespace:        "control-plane-prod",
		Secrets:          secrets,
		DocumentContexts: route,
	})
	if err == nil {
		t.Fatal("preflight passed while the daemon cluster lacks observability/otel-agent-token")
	}
	want := []string{
		daemon + ":observability/otel-agent-token",
		hub + ":observability-data/obs-credentials",
		hub + ":observability/otel-agent-token",
	}
	if got := asked(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("lookups = %v; want %v", got, want)
	}
	if len(res.MissingSecretKeys) != 1 {
		t.Fatalf("missing = %v; want exactly the daemon cluster's token", res.MissingSecretKeys)
	}
	if got := res.MissingSecretKeys[daemon+":observability/otel-agent-token"]; len(got) != 1 || got[0] != wholeSecretMarker {
		t.Errorf("missing = %v; want %s:observability/otel-agent-token absent", res.MissingSecretKeys, daemon)
	}
	if msg := err.Error(); !strings.Contains(msg, "Secret "+daemon+":observability/otel-agent-token missing keys") {
		t.Errorf("report must name the cluster, namespace and Secret; got:\n%s", msg)
	}
}

// A document the router applies to no cluster demands nothing, and the
// placement of a document that declares no namespace is the deploy namespace.
func TestCollectManifestRefs_PlacementDefaultsAndUnroutedDocuments(t *testing.T) {
	stream := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
spec:
  template:
    spec:
      containers:
        - name: api
          envFrom:
            - secretRef:
                name: api-secrets
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: nowhere
  namespace: elsewhere
spec:
  template:
    spec:
      containers:
        - name: nowhere
          envFrom:
            - secretRef:
                name: never-applied
`
	refs := CollectManifestRefs(stream, RefPlacement{
		Namespace: "app",
		Context:   "ignored-when-routed",
		Contexts: func(doc string) []string {
			if strings.Contains(doc, "name: nowhere") {
				return nil
			}
			return []string{"hub"}
		},
	})
	want := ResourceRef{Context: "hub", Namespace: "app", Name: "api-secrets"}
	if len(refs.Secrets) != 1 {
		t.Fatalf("refs = %v; want only %+v", refs.Secrets, want)
	}
	if _, ok := refs.Secrets[want]; !ok {
		t.Errorf("refs = %v; want %+v", refs.Secrets, want)
	}
}

// Pull credentials read from several (cluster, namespace) locations are
// merged; the first in sorted order wins a registry configured twice.
func TestMergeDockerConfigs(t *testing.T) {
	a := []byte(`{"auths":{"ghcr.io":{"auth":"YQ=="}}}`)
	b := []byte(`{"auths":{"ghcr.io":{"auth":"Yg=="},"us-docker.pkg.dev":{"auth":"Yw=="}}}`)
	if got := mergeDockerConfigs([][]byte{a}); string(got) != string(a) {
		t.Errorf("a single config must pass through unchanged; got %s", got)
	}
	got := string(mergeDockerConfigs([][]byte{a, []byte("not json"), b}))
	want := `{"auths":{"ghcr.io":{"auth":"YQ=="},"us-docker.pkg.dev":{"auth":"Yw=="}}}`
	if got != want {
		t.Errorf("merged = %s; want %s", got, want)
	}
	if mergeDockerConfigs(nil) != nil {
		t.Error("no configs must merge to nil")
	}
}

// recordingPullCreds returns a usable docker config for any request and
// records each (context, namespace, name) it was asked to read.
type recordingPullCreds struct {
	mu    sync.Mutex
	asked []string
}

func (r *recordingPullCreds) ResolveDockerConfig(_ context.Context, kctx, ns string, names []string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, n := range names {
		r.asked = append(r.asked, kctx+":"+ns+"/"+n)
	}
	sort.Strings(r.asked)
	return []byte(`{"auths":{"ghcr.io":{"auth":"eDp5"}}}`), nil
}

// byName flattens placed references to their names, for tests that assert
// only WHAT a stream references, not where it resolves.
func byName(in map[ResourceRef]map[string]struct{}) map[string]map[string]struct{} {
	out := map[string]map[string]struct{}{}
	for ref, keys := range in {
		if out[ref.Name] == nil {
			out[ref.Name] = map[string]struct{}{}
		}
		for k := range keys {
			out[ref.Name][k] = struct{}{}
		}
	}
	return out
}

// pullSecretNames is byName for ManifestRefs.ImagePullSecrets.
func pullSecretNames(refs ManifestRefs) map[string]struct{} {
	out := map[string]struct{}{}
	for ref := range refs.ImagePullSecrets {
		out[ref.Name] = struct{}{}
	}
	return out
}
