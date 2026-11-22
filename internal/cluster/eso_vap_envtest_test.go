//go:build !windows

// envtest's process package does not compile for windows (controller-runtime
// v0.25.2 declares signalProcess twice there), so neither does this test; the
// same tag as pkg/controller/controllertest. deadcodeguard loads every package
// with GOOS=windows and refuses an unsound load.

package cluster

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/yaml"

	"github.com/reliant-labs/forge/internal/kclplugin"
	"github.com/reliant-labs/forge/internal/kclrender"
	"github.com/reliant-labs/forge/pkg/deploy/eso"
)

// The three ESO admission policies, run by a REAL kube-apiserver. A CEL
// expression that parses can still be wrong (a field path that does not exist
// in the CRD fails closed and denies everything, or a guard that never
// matches), so the generated policies are applied to an API server holding the
// embedded CRDs and probed with objects that must be admitted and objects that
// must be refused.
//
// Needs the envtest binaries (KUBEBUILDER_ASSETS): skipped without them and
// under -short.

const vapMain = `import forge

_hub = forge.ClusterTarget {cluster = "envtest", namespace = "acme"}

output = forge.render(forge.Bundle {
    project = "fixture"
    env = "prod"
    cluster_target = _hub
    external_secrets_operators = [forge.ExternalSecretsOperator {
        cluster = _hub
        admission = forge.ESOAdmission {
            writers = ["alice"]
            servers = ["https://openbao.openbao.svc:8200"]
            jwt_mounts = ["jwt-hub"]
            audiences = ["https://secrets.example.com/c/hub"]
            gcp_projects = ["acme-prod"]
        }
    }]
})
`

// renderESOBundle renders main.k (a Bundle with one ExternalSecretsOperator)
// and returns the admission objects riding the ESO chart's extraObjects and
// every other manifest the render emitted.
func renderESOBundle(t *testing.T, main string) (vaps, manifests []*unstructured.Unstructured) {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{
		"kcl.mod": "[package]\nname = \"esovap\"\nedition = \"v0.11.0\"\nversion = \"0.0.1\"\n",
		"main.k":  main,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	kclplugin.Register()
	out, err := kclrender.Run(dir, dir, nil)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	var contract struct {
		Output struct {
			Manifests  []map[string]any `json:"manifests"`
			HelmCharts []struct {
				Values struct {
					ExtraObjects []map[string]any `json:"extraObjects"`
				} `json:"values"`
			} `json:"helm_charts"`
		} `json:"output"`
	}
	if err := json.Unmarshal(out, &contract); err != nil || len(contract.Output.HelmCharts) != 1 {
		t.Fatalf("contract: %v", err)
	}
	for _, o := range contract.Output.HelmCharts[0].Values.ExtraObjects {
		vaps = append(vaps, &unstructured.Unstructured{Object: o})
	}
	if len(vaps) != 6 {
		t.Fatalf("rendered %d admission objects, want 3 policies + 3 bindings", len(vaps))
	}
	for _, o := range contract.Output.Manifests {
		manifests = append(manifests, &unstructured.Unstructured{Object: o})
	}
	return vaps, manifests
}

func renderVAPObjects(t *testing.T) []*unstructured.Unstructured {
	t.Helper()
	vaps, _ := renderESOBundle(t, vapMain)
	return vaps
}

// startESOEnvtest starts a kube-apiserver holding the embedded ESO CRDs and
// returns an admin client (system:masters), skipping when the binaries are
// absent.
func startESOEnvtest(t *testing.T) (*rest.Config, client.Client) {
	t.Helper()
	if testing.Short() {
		t.Skip("starts kube-apiserver and etcd; runs in task test")
	}
	assets := os.Getenv("KUBEBUILDER_ASSETS")
	if assets == "" {
		t.Skip("KUBEBUILDER_ASSETS not set (setup-envtest use); the policies are also pinned by the KCL fixtures")
	}
	crdFile := filepath.Join(t.TempDir(), "eso-crds.yaml")
	crds, err := eso.CRDs(eso.ChartVersion, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(crdFile, []byte(crds), 0o644); err != nil {
		t.Fatal(err)
	}
	env := &envtest.Environment{CRDDirectoryPaths: []string{crdFile}, ErrorIfCRDPathMissing: true, BinaryAssetsDirectory: assets}
	cfg, err := env.Start()
	if err != nil {
		t.Fatalf("start envtest: %v", err)
	}
	t.Cleanup(func() { _ = env.Stop() })
	admin, err := client.New(cfg, client.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return cfg, admin
}

var (
	gvkSS = schema.GroupVersionKind{Group: "external-secrets.io", Version: "v1", Kind: "SecretStore"}
	gvkES = schema.GroupVersionKind{Group: "external-secrets.io", Version: "v1", Kind: "ExternalSecret"}
)

func esObj(gvk schema.GroupVersionKind, ns, name, specYAML string) *unstructured.Unstructured {
	var spec map[string]any
	if err := yaml.Unmarshal([]byte(specYAML), &spec); err != nil {
		panic(err)
	}
	u := &unstructured.Unstructured{Object: map[string]any{"spec": spec}}
	u.SetGroupVersionKind(gvk)
	u.SetNamespace(ns)
	u.SetName(name)
	return u
}

func vaultStore(server, mount, sa string, audiences ...string) string {
	quoted := make([]string, len(audiences))
	for i, s := range audiences {
		quoted[i] = `"` + s + `"`
	}
	return `
provider:
  vault:
    server: "` + server + `"
    path: secret
    version: v2
    auth:
      jwt:
        path: ` + mount + `
        role: r
        kubernetesServiceAccountToken:
          serviceAccountRef: {name: ` + sa + `}
          audiences: [` + strings.Join(quoted, ",") + `]`
}

func esSpec(target, extra string) string {
	return `
secretStoreRef: {kind: SecretStore, name: s}
` + target + `
data:
  - secretKey: k
    remoteRef: {key: some/path}
` + extra
}

const goodTarget = `target: {creationPolicy: Orphan, deletionPolicy: Retain}`

// waitPoliciesActive blocks until each of the three policies refuses an object
// only it refuses. The API server activates ValidatingAdmissionPolicies
// asynchronously and independently, so waiting on one says nothing about the
// others. nonWriter, when set, is a client RBAC lets through but eso-writers
// must refuse.
func waitPoliciesActive(t *testing.T, admin, nonWriter client.Client) {
	t.Helper()
	ctx := context.Background()
	probes := map[string]func() *unstructured.Unstructured{
		"eso-store-pin": func() *unstructured.Unstructured {
			return esObj(gvkSS, "app", "warmup-pin", `provider: {fake: {data: [{key: a, value: b}]}}`)
		},
		"eso-es-policy": func() *unstructured.Unstructured {
			return esObj(gvkES, "app", "warmup-es", esSpec(goodTarget, "dataFrom: [{extract: {key: some/path}}]"))
		},
	}
	for name, mk := range probes {
		waitRefused(t, name, func() error { return admin.Create(ctx, mk()) }, func() { _ = admin.Delete(ctx, mk()) })
	}
	if nonWriter != nil {
		mk := func() *unstructured.Unstructured {
			return esObj(gvkSS, "app", "warmup-writer", vaultStore("https://openbao.openbao.svc:8200", "jwt-hub", "reliant-secrets", "https://secrets.example.com/c/hub"))
		}
		waitRefused(t, "eso-writers", func() error { return nonWriter.Create(ctx, mk()) }, func() { _ = admin.Delete(ctx, mk()) })
	}
}

func waitRefused(t *testing.T, policy string, try func() error, cleanup func()) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		if err := try(); err != nil {
			return
		}
		cleanup()
		if time.Now().After(deadline) {
			t.Fatalf("%s never became active", policy)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func TestESOAdmissionPolicies(t *testing.T) {
	cfg, admin := startESOEnvtest(t)
	vaps := renderVAPObjects(t)

	as := func(user string, groups ...string) client.Client {
		c := rest.CopyConfig(cfg)
		c.Impersonate = rest.ImpersonationConfig{UserName: user, Groups: groups}
		cl, err := client.New(c, client.Options{})
		if err != nil {
			t.Fatal(err)
		}
		return cl
	}
	ctx := context.Background()
	mustCreate := func(c client.Client, o *unstructured.Unstructured) {
		t.Helper()
		if err := c.Create(ctx, o); err != nil {
			t.Fatalf("create %s/%s: %v", o.GetKind(), o.GetName(), err)
		}
	}
	for _, ns := range []string{"app", "other"} {
		n := &unstructured.Unstructured{Object: map[string]any{}}
		n.SetAPIVersion("v1")
		n.SetKind("Namespace")
		n.SetName(ns)
		mustCreate(admin, n)
	}
	// RBAC so that alice and bob reach the admission step at all: the policy
	// under test is the second gate, not the first.
	for _, y := range []string{`
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata: {name: eso-test}
rules:
- {apiGroups: [external-secrets.io], resources: [externalsecrets, secretstores], verbs: ["*"]}
`, `
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata: {name: eso-test}
roleRef: {apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: eso-test}
subjects:
- {apiGroup: rbac.authorization.k8s.io, kind: User, name: alice}
- {apiGroup: rbac.authorization.k8s.io, kind: User, name: bob}
`} {
		u := &unstructured.Unstructured{}
		if err := yaml.Unmarshal([]byte(y), &u.Object); err != nil {
			t.Fatal(err)
		}
		mustCreate(admin, u)
	}

	// A store that predates the policies: a webhook provider, the exfiltration
	// channel eso-store-pin exists to close.
	legacy := esObj(gvkSS, "app", "legacy", `
provider:
  webhook: {url: "https://evil.example/x", result: {jsonPath: "$"}}
`)
	mustCreate(admin, legacy)

	for _, o := range vaps {
		mustCreate(admin, o)
	}

	good := vaultStore("https://openbao.openbao.svc:8200", "jwt-hub", "reliant-secrets", "https://secrets.example.com/c/hub")

	// Policies take a moment to become active, each on its own clock: probe
	// until every one refuses what it must.
	waitPoliciesActive(t, admin, as("bob"))

	type probe struct {
		name   string
		c      client.Client
		o      *unstructured.Unstructured
		deny   bool
		reason string // substring of the refusal
	}
	check := func(t *testing.T, p probe) {
		t.Helper()
		err := p.c.Create(ctx, p.o)
		switch {
		case p.deny && err == nil:
			_ = p.c.Delete(ctx, p.o)
			t.Fatalf("%s was admitted, want a refusal containing %q", p.name, p.reason)
		case p.deny && !strings.Contains(err.Error(), p.reason):
			t.Fatalf("%s refused for the wrong reason:\n got: %v\nwant: %q", p.name, err, p.reason)
		case !p.deny && err != nil:
			t.Fatalf("%s was refused: %v", p.name, err)
		}
	}

	t.Run("eso-writers", func(t *testing.T) {
		alice, bob := as("alice"), as("bob")
		check(t, probe{"declared writer", alice, esObj(gvkSS, "app", "w-alice", good), false, ""})
		check(t, probe{"cluster admin group", admin, esObj(gvkSS, "app", "w-admin", good), false, ""})
		check(t, probe{"RBAC-permitted non-writer", bob, esObj(gvkSS, "app", "w-bob", good), true, "only the declared writers"})
		check(t, probe{"non-writer ExternalSecret", bob, esObj(gvkES, "app", "w-bob-es", esSpec(goodTarget, "")), true, "only the declared writers"})
		if err := bob.Delete(ctx, esObj(gvkSS, "app", "w-alice", "{}")); err == nil || !strings.Contains(err.Error(), "only the declared writers") {
			t.Fatalf("non-writer delete: %v", err)
		}
		if err := alice.Delete(ctx, esObj(gvkSS, "app", "w-alice", "{}")); err != nil {
			t.Fatalf("writer delete: %v", err)
		}
	})

	t.Run("eso-store-pin", func(t *testing.T) {
		const why = "may only dial a declared server"
		cases := []probe{
			{"pinned vault JWT store", admin, esObj(gvkSS, "app", "p-good", good), false, ""},
			{"gcpsm declared project, workload identity", admin, esObj(gvkSS, "app", "p-gcp", `
provider:
  gcpsm:
    projectID: acme-prod
    auth:
      workloadIdentity: {clusterLocation: l, clusterName: c, serviceAccountRef: {name: reliant-secrets}}
`), false, ""},
			{"foreign server", admin, esObj(gvkSS, "app", "p-server", vaultStore("https://evil.example:8200", "jwt-hub", "reliant-secrets", "https://secrets.example.com/c/hub")), true, why},
			{"foreign mount", admin, esObj(gvkSS, "app", "p-mount", vaultStore("https://openbao.openbao.svc:8200", "jwt-evil", "reliant-secrets", "https://secrets.example.com/c/hub")), true, why},
			{"foreign service account", admin, esObj(gvkSS, "app", "p-sa", vaultStore("https://openbao.openbao.svc:8200", "jwt-hub", "default", "https://secrets.example.com/c/hub")), true, why},
			{"foreign audience", admin, esObj(gvkSS, "app", "p-aud", vaultStore("https://openbao.openbao.svc:8200", "jwt-hub", "reliant-secrets", "https://evil.example")), true, why},
			{"two audiences", admin, esObj(gvkSS, "app", "p-aud2", vaultStore("https://openbao.openbao.svc:8200", "jwt-hub", "reliant-secrets", "https://secrets.example.com/c/hub", "https://evil.example")), true, why},
			{"no audience (the provider would default to 'vault')", admin, esObj(gvkSS, "app", "p-noaud", `
provider:
  vault:
    server: "https://openbao.openbao.svc:8200"
    path: secret
    auth:
      jwt:
        path: jwt-hub
        role: r
        kubernetesServiceAccountToken: {serviceAccountRef: {name: reliant-secrets}}
`), true, why},
			{"a stored JWT instead of a projected token", admin, esObj(gvkSS, "app", "p-secretref", `
provider:
  vault:
    server: "https://openbao.openbao.svc:8200"
    path: secret
    auth:
      jwt: {path: jwt-hub, role: r, secretRef: {name: tok, key: t}}
`), true, why},
			{"token auth", admin, esObj(gvkSS, "app", "p-token", `
provider:
  vault:
    server: "https://openbao.openbao.svc:8200"
    path: secret
    auth:
      tokenSecretRef: {name: tok, key: t}
`), true, why},
			{"second auth method beside jwt", admin, esObj(gvkSS, "app", "p-two", `
provider:
  vault:
    server: "https://openbao.openbao.svc:8200"
    path: secret
    auth:
      jwt:
        path: jwt-hub
        role: r
        kubernetesServiceAccountToken: {serviceAccountRef: {name: reliant-secrets}, audiences: ["https://secrets.example.com/c/hub"]}
      userPass: {path: up, username: u, secretRef: {name: tok, key: t}}
`), true, why},
			{"a service account in another namespace", admin, esObj(gvkSS, "app", "p-ns", `
provider:
  vault:
    server: "https://openbao.openbao.svc:8200"
    path: secret
    auth:
      jwt:
        path: jwt-hub
        role: r
        kubernetesServiceAccountToken: {serviceAccountRef: {name: reliant-secrets, namespace: other}, audiences: ["https://secrets.example.com/c/hub"]}
`), true, why},
			{"gcpsm undeclared project", admin, esObj(gvkSS, "app", "p-gcp2", `
provider: {gcpsm: {projectID: evil-project, auth: {workloadIdentity: {clusterLocation: l, clusterName: c, serviceAccountRef: {name: reliant-secrets}}}}}
`), true, why},
			{"gcpsm with a stored key", admin, esObj(gvkSS, "app", "p-gcp3", `
provider: {gcpsm: {projectID: acme-prod, auth: {secretRef: {secretAccessKeySecretRef: {name: k, key: k}}}}}
`), true, why},
			{"webhook provider", admin, esObj(gvkSS, "app", "p-webhook", `provider: {webhook: {url: "https://evil.example/x", result: {jsonPath: "$"}}}`), true, why},
			{"fake provider", admin, esObj(gvkSS, "app", "p-fake", `provider: {fake: {data: [{key: a, value: b}]}}`), true, why},
		}
		for _, c := range cases {
			check(t, c)
		}

		// A policy tightened over an existing store never wedges it: a change
		// that leaves spec alone (finalizers, annotations) is admitted, a change
		// to spec is judged.
		l := legacy.DeepCopy()
		if err := admin.Get(ctx, client.ObjectKeyFromObject(l), l); err != nil {
			t.Fatal(err)
		}
		l.SetAnnotations(map[string]string{"touched": "yes"})
		if err := admin.Update(ctx, l); err != nil {
			t.Fatalf("a spec-neutral update of a pre-existing store was refused: %v", err)
		}
		l.Object["spec"] = map[string]any{"provider": map[string]any{"webhook": map[string]any{"url": "https://evil.example/y", "result": map[string]any{"jsonPath": "$"}}}}
		if err := admin.Update(ctx, l); err == nil || !strings.Contains(err.Error(), why) {
			t.Fatalf("a spec change to a non-conforming store: %v", err)
		}
		if err := admin.Delete(ctx, l); err != nil {
			t.Fatalf("deleting a non-conforming store must stay possible: %v", err)
		}
	})

	t.Run("eso-es-policy", func(t *testing.T) {
		cases := []probe{
			{"conforming ExternalSecret", admin, esObj(gvkES, "app", "e-good", esSpec(goodTarget, "refreshInterval: 5m")), false, ""},
			{"merge adoption", admin, esObj(gvkES, "app", "e-merge", esSpec(`target: {creationPolicy: Merge, deletionPolicy: Retain}`, "")), false, ""},
			{"its own name as target", admin, esObj(gvkES, "app", "e-self", esSpec(`target: {name: e-self, creationPolicy: Orphan, deletionPolicy: Retain}`, "")), false, ""},
			{"dataFrom", admin, esObj(gvkES, "app", "e-datafrom", esSpec(goodTarget, "dataFrom: [{extract: {key: some/path}}]")), true, "may not use dataFrom"},
			{"default creationPolicy (Owner)", admin, esObj(gvkES, "app", "e-owner", esSpec(`target: {name: e-owner}`, "")), true, "creationPolicy must be Orphan or Merge"},
			{"no target at all (defaults to Owner)", admin, esObj(gvkES, "app", "e-notarget", esSpec("", "")), true, "creationPolicy must be Orphan or Merge"},
			{"creationPolicy None", admin, esObj(gvkES, "app", "e-none", esSpec(`target: {creationPolicy: None, deletionPolicy: Retain}`, "")), true, "creationPolicy must be Orphan or Merge"},
			{"deletionPolicy Merge with no store value (a template-only Secret)", admin, esObj(gvkES, "app", "e-tplonly", `
secretStoreRef: {kind: SecretStore, name: s}
target: {creationPolicy: Orphan, deletionPolicy: Merge, template: {data: {k: v}}}
`), false, ""},
			{"deletionPolicy Merge while reading a store value", admin, esObj(gvkES, "app", "e-mergedata", esSpec(`target: {creationPolicy: Orphan, deletionPolicy: Merge}`, "data: [{secretKey: k, remoteRef: {key: some/path}}]")), true, "deletionPolicy must be Retain"},
			{"deletionPolicy Delete", admin, esObj(gvkES, "app", "e-delete", esSpec(`target: {creationPolicy: Orphan, deletionPolicy: Delete}`, "")), true, "deletionPolicy must be Retain"},
			{"another Secret's name", admin, esObj(gvkES, "app", "e-other", esSpec(`target: {name: victim, creationPolicy: Orphan, deletionPolicy: Retain}`, "")), true, "named like itself"},
			{"refresh under five minutes", admin, esObj(gvkES, "app", "e-fast", esSpec(goodTarget, "refreshInterval: 1m")), true, "at least 5m"},
			{"a ClusterSecretStore reference", admin, esObj(gvkES, "app", "e-css", `
secretStoreRef: {kind: ClusterSecretStore, name: s}
target: {creationPolicy: Orphan, deletionPolicy: Retain}
data: [{secretKey: k, remoteRef: {key: some/path}}]
`), true, "only a namespaced SecretStore"},
			{"a generic target", admin, esObj(gvkES, "app", "e-manifest", esSpec(`target: {creationPolicy: Orphan, deletionPolicy: Retain, manifest: {apiVersion: v1, kind: ConfigMap}}`, "")), true, "generic target"},
			{"a per-entry sourceRef", admin, esObj(gvkES, "app", "e-sourceref", `
secretStoreRef: {kind: SecretStore, name: s}
target: {creationPolicy: Orphan, deletionPolicy: Retain}
data: [{secretKey: k, remoteRef: {key: p}, sourceRef: {storeRef: {kind: ClusterSecretStore, name: x}}}]
`), true, "sourceRef"},
		}
		for _, c := range cases {
			check(t, c)
		}
	})
}

// A Bundle with OpenBao, an External Secrets Operator and one store-backed
// ExternalSecret, and NO hand-filled `admission`, must pass its own policies:
// the server, JWT mount and audience lists are derived from the SecretStores
// the same render emits and from the OpenBao component. An empty list is not
// a wildcard, so before the derivation these objects were all refused.
const derivedAdmissionMain = `import forge

_hub = forge.ClusterTarget {cluster = "envtest", namespace = "acme", platform = "amd64", connected_cluster = "hub"}

output = forge.render(forge.Bundle {
    project = "fixture"
    env = "prod"
    cluster_target = _hub
    openbao = forge.OpenBao {}
    external_secrets_operators = [forge.ExternalSecretsOperator {cluster = _hub}]
    required_secrets = [forge.ExternalSecret {
        name = "stripe"
        namespace = "app"
        keys = ["key"]
        source = forge.StoreSource {
            store = forge.OpenBaoStore {}
            data = {"key" = "STRIPE_KEY"}
        }
    }]
})
`

func TestESODerivedAdmissionAdmitsTheBundlesOwnObjects(t *testing.T) {
	cfg, admin := startESOEnvtest(t)
	_ = cfg
	vaps, manifests := renderESOBundle(t, derivedAdmissionMain)
	ctx := context.Background()

	var store, es *unstructured.Unstructured
	for _, m := range manifests {
		switch m.GetKind() {
		case "SecretStore":
			store = m
		case "ExternalSecret":
			es = m
		}
	}
	if store == nil || es == nil {
		t.Fatalf("the render emitted no SecretStore/ExternalSecret (store=%v es=%v)", store != nil, es != nil)
	}

	ns := &unstructured.Unstructured{Object: map[string]any{}}
	ns.SetAPIVersion("v1")
	ns.SetKind("Namespace")
	ns.SetName("app")
	if err := admin.Create(ctx, ns); err != nil {
		t.Fatal(err)
	}
	for _, o := range vaps {
		if err := admin.Create(ctx, o); err != nil {
			t.Fatalf("create %s/%s: %v", o.GetKind(), o.GetName(), err)
		}
	}

	waitPoliciesActive(t, admin, nil)

	// Strip server-populated fields the render does not carry; Create takes
	// the object as rendered.
	if err := admin.Create(ctx, store); err != nil {
		t.Fatalf("the Bundle's own SecretStore was refused by its own policies: %v", err)
	}
	if err := admin.Create(ctx, es); err != nil {
		t.Fatalf("the Bundle's own ExternalSecret was refused by its own policies: %v", err)
	}

	// The derivation widens nothing past the Bundle: a foreign server is still
	// refused, so the policies are live, not vacuously open.
	bad := esObj(gvkSS, "app", "foreign", vaultStore("https://evil.example:8200", "jwt-hub", "reliant-secrets", "https://openbao.openbao.svc:8200/c/hub"))
	if err := admin.Create(ctx, bad); err == nil || !strings.Contains(err.Error(), "may only dial a declared server") {
		t.Fatalf("a foreign server was not refused by the derived policy: %v", err)
	}
}

// The OpenBao component's bootstrap delivery: a SecretStore over Secret Manager
// that logs in as the component's reader service account, and the ExternalSecrets
// that read the seal key and the Postgres password through it. The store-pin
// policy must admit exactly those, and nothing else over Google.
const bootstrapAdmissionMain = `import forge

_hub = forge.ClusterTarget {cluster = "gke_acme_us-central1_prod", namespace = "acme-prod", platform = "amd64", connected_cluster = "hub"}

output = forge.render(forge.Bundle {
    project = "fixture"
    env = "prod"
    cluster_target = _hub
    openbao = forge.OpenBao {
        namespace = "acme-prod"
        replicas = 2
        storage = forge.OpenBaoPostgres {
            cloudsql_instance = "acme:us-central1:pg"
            gcp_service_account = "openbao@acme.iam.gserviceaccount.com"
            password = forge.BootstrapSecret {name = "openbao-pg-password-prod"}
        }
    }
    external_secrets_operators = [forge.ExternalSecretsOperator {cluster = _hub, namespace = "eso"}]
    bootstrap_source = forge.GCPSecretManager {project = "acme-prod"}
})
`

func TestESOBootstrapStoreIsAdmittedByTheDerivedPolicy(t *testing.T) {
	_, admin := startESOEnvtest(t)
	vaps, manifests := renderESOBundle(t, bootstrapAdmissionMain)
	ctx := context.Background()

	items := make([]any, len(manifests))
	for i, m := range manifests {
		items[i] = m.Object
	}
	expanded, err := expandTierDeclarations(items, nil, nil)
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	var stores, ess []*unstructured.Unstructured
	for _, it := range expanded {
		u := &unstructured.Unstructured{Object: it.(map[string]any)}
		switch u.GetKind() {
		case "SecretStore":
			stores = append(stores, u)
		case "ExternalSecret":
			ess = append(ess, u)
		}
	}
	if len(stores) != 1 || len(ess) != 2 {
		t.Fatalf("the OpenBao bootstrap rendered %d SecretStores and %d ExternalSecrets, want 1 and 2", len(stores), len(ess))
	}

	ns := &unstructured.Unstructured{Object: map[string]any{}}
	ns.SetAPIVersion("v1")
	ns.SetKind("Namespace")
	ns.SetName("acme-prod")
	if err := admin.Create(ctx, ns); err != nil {
		t.Fatal(err)
	}
	for _, o := range vaps {
		if err := admin.Create(ctx, o); err != nil {
			t.Fatalf("create %s/%s: %v", o.GetKind(), o.GetName(), err)
		}
	}
	waitPoliciesActive(t, admin, nil)

	for _, o := range append(stores, ess...) {
		if err := admin.Create(ctx, o); err != nil {
			t.Fatalf("the bundle's own %s %s was refused by its own policies: %v", o.GetKind(), o.GetName(), err)
		}
	}

	gcp := func(project, sa string) string {
		return `
provider:
  gcpsm:
    projectID: ` + project + `
    auth:
      workloadIdentity:
        clusterLocation: us-central1
        clusterName: prod
        serviceAccountRef: {name: ` + sa + `}`
	}
	for name, spec := range map[string]string{
		"foreign-project": gcp("evil-project", "openbao-bootstrap-reader"),
		"foreign-sa":      gcp("acme-prod", "some-other-sa"),
	} {
		if err := admin.Create(ctx, esObj(gvkSS, "acme-prod", name, spec)); err == nil || !strings.Contains(err.Error(), "may only dial a declared server") {
			t.Errorf("%s: a store outside the declared project/service account was not refused: %v", name, err)
		}
	}
}
