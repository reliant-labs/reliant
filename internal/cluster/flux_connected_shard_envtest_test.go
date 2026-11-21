//go:build !windows

// envtest's process package does not compile for windows (see
// eso_vap_envtest_test.go); the same tag applies here.

package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/yaml"

	"github.com/reliant-labs/forge/internal/kclplugin"
	"github.com/reliant-labs/forge/internal/kclrender"
	"github.com/reliant-labs/forge/pkg/flux"
)

// The connected-shard admission policy, run by a REAL kube-apiserver. The owner's
// rule is "don't let it apply with a service account": a CEL expression that
// parses can still be wrong (a field path that does not exist fails closed and
// denies everything, or a guard that never matches), so the policy forge
// renders is applied to an API server and probed in both directions.
//
// The Kustomization CRD is a minimal schema-preserving one: the policy reads
// only metadata.labels and a few spec paths, and a hermetic CRD keeps the test
// off the network. Needs the envtest binaries (KUBEBUILDER_ASSETS); skipped
// without them and under -short.

const fluxShardMain = `import forge

output = forge.flux_chart(connected_shard = True, egress = {
    kustomize = []
    source = []
    kustomize_connected = []
    operator = []
})
`

const kustomizationCRD = `
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata: {name: kustomizations.kustomize.toolkit.fluxcd.io}
spec:
  group: kustomize.toolkit.fluxcd.io
  scope: Namespaced
  names: {kind: Kustomization, listKind: KustomizationList, plural: kustomizations, singular: kustomization}
  versions:
  - name: v1
    served: true
    storage: true
    schema:
      openAPIV3Schema:
        type: object
        x-kubernetes-preserve-unknown-fields: true
`

var gvkKustomization = schema.GroupVersionKind{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Kind: "Kustomization"}

func renderConnectedShardObjects(t *testing.T) []*unstructured.Unstructured {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{
		"kcl.mod": "[package]\nname = \"fluxshard\"\nedition = \"v0.11.0\"\nversion = \"0.0.1\"\n",
		"main.k":  fluxShardMain,
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
			Manifests []map[string]any `json:"manifests"`
		} `json:"output"`
	}
	if err := json.Unmarshal(out, &contract); err != nil {
		t.Fatalf("contract: %v", err)
	}
	var vap []*unstructured.Unstructured
	for _, o := range contract.Output.Manifests {
		u := &unstructured.Unstructured{Object: o}
		if strings.HasPrefix(u.GetKind(), "ValidatingAdmissionPolicy") {
			vap = append(vap, u)
		}
	}
	// The shard's Kustomization policy, its identity policy and the
	// controller binding guard, each with its binding.
	if len(vap) != 6 {
		t.Fatalf("rendered %d admission objects, want three policies and their bindings", len(vap))
	}
	return vap
}

func kustomization(name string, labels map[string]string, specYAML string) *unstructured.Unstructured {
	var spec map[string]any
	if err := yaml.Unmarshal([]byte(specYAML), &spec); err != nil {
		panic(err)
	}
	u := &unstructured.Unstructured{Object: map[string]any{"spec": spec}}
	u.SetGroupVersionKind(gvkKustomization)
	u.SetNamespace("flux-acme")
	u.SetName(name)
	if labels != nil {
		u.SetLabels(labels)
	}
	return u
}

func TestFluxConnectedShardAdmission(t *testing.T) {
	if testing.Short() {
		t.Skip("starts kube-apiserver and etcd; runs in task test")
	}
	assets := os.Getenv("KUBEBUILDER_ASSETS")
	if assets == "" {
		t.Skip("KUBEBUILDER_ASSETS not set (setup-envtest use); the policy is also pinned by the KCL fixtures")
	}
	crdFile := filepath.Join(t.TempDir(), "kustomization-crd.yaml")
	if err := os.WriteFile(crdFile, []byte(kustomizationCRD), 0o644); err != nil {
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
	ctx := context.Background()

	ns := &unstructured.Unstructured{Object: map[string]any{}}
	ns.SetAPIVersion("v1")
	ns.SetKind("Namespace")
	ns.SetName("flux-acme")
	if err := admin.Create(ctx, ns); err != nil {
		t.Fatal(err)
	}
	for _, o := range renderConnectedShardObjects(t) {
		if err := admin.Create(ctx, o); err != nil {
			t.Fatalf("create %s/%s: %v", o.GetKind(), o.GetName(), err)
		}
	}

	shard := map[string]string{flux.ShardLabel: flux.ConnectedShard}
	const (
		kubeCfgSecret = "kubeConfig: {secretRef: {name: kc}}\n"
		base          = "interval: 5m\nsourceRef: {kind: OCIRepository, name: s}\npath: ./\nprune: true\n"
	)

	// The API server activates a policy asynchronously: probe with the shape
	// that must be refused until it is.
	waitRefused(t, flux.ConnectedController+" policy", func() error {
		return admin.Create(ctx, kustomization("warmup", shard, base+"serviceAccountName: x\n"+kubeCfgSecret))
	}, func() { _ = admin.Delete(ctx, kustomization("warmup", shard, base)) })

	i := 0
	for _, tc := range []struct {
		name   string
		labels map[string]string
		spec   string
		deny   bool
		reason string
	}{
		// What the shard exists for.
		{"shard + kubeConfig secret, no service account", shard, base + kubeCfgSecret, false, ""},
		// THE BAN.
		{"shard + serviceAccountName", shard, base + "serviceAccountName: " + flux.DeployServiceAccount + "\n" + kubeCfgSecret, true, "must not set spec.serviceAccountName"},
		{"shard + decryption.serviceAccountName", shard, base + "decryption: {provider: sops, serviceAccountName: kms}\n" + kubeCfgSecret, true, "spec.decryption.serviceAccountName"},
		{"shard, no kubeConfig at all", shard, base, true, "must set spec.kubeConfig.secretRef"},
		{"shard + kubeConfig configMapRef (cloud-identity provider)", shard, base + "kubeConfig: {configMapRef: {name: kc}}\n", true, "must set spec.kubeConfig.secretRef"},
		// The other direction: a kubeConfig Kustomization must not land on the
		// main controller, whose --default-service-account would then apply
		// on the remote cluster.
		{"kubeConfig, no shard label", nil, base + kubeCfgSecret, true, "must carry the label"},
		{"kubeConfig, wrong shard label", map[string]string{flux.ShardLabel: "shard0"}, base + kubeCfgSecret, true, "must carry the label"},
		// The main controller's in-cluster Kustomizations are untouched.
		{"in-cluster, service account, no shard label", map[string]string{flux.ShardLabel: "shard0"}, base + "serviceAccountName: " + flux.DeployServiceAccount + "\n", false, ""},
		{"in-cluster, no labels at all", nil, base, false, ""},
		{"shard + empty serviceAccountName", shard, base + "serviceAccountName: \"\"\n" + kubeCfgSecret, false, ""},
	} {
		i++
		t.Run(tc.name, func(t *testing.T) {
			o := kustomization(fmt.Sprintf("case-%d", i), tc.labels, tc.spec)
			err := admin.Create(ctx, o)
			switch {
			case tc.deny && err == nil:
				t.Fatalf("admitted; want a refusal containing %q", tc.reason)
			case tc.deny && !strings.Contains(err.Error(), tc.reason):
				t.Fatalf("refused for the wrong reason:\n got: %v\nwant: %q", err, tc.reason)
			case !tc.deny && err != nil:
				t.Fatalf("refused: %v", err)
			}
		})
	}

	// THE IDENTITY. The operator renders the shard Deployment; one that runs as
	// anything but the shard's own ServiceAccount, or impersonates by
	// default, is refused, so a patch that stopped matching fails loudly.
	deploy := func(name, sa string, args ...string) *unstructured.Unstructured {
		var a []any
		for _, s := range args {
			a = append(a, s)
		}
		u := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{
			"selector": map[string]any{"matchLabels": map[string]any{"app": name}},
			"template": map[string]any{
				"metadata": map[string]any{"labels": map[string]any{"app": name}},
				"spec":     map[string]any{"serviceAccountName": sa, "containers": []any{map[string]any{"name": "manager", "image": "x", "args": a}}},
			},
		}}}
		u.SetAPIVersion("apps/v1")
		u.SetKind("Deployment")
		u.SetNamespace("flux-system")
		u.SetName(name)
		return u
	}
	fluxSystem := &unstructured.Unstructured{Object: map[string]any{}}
	fluxSystem.SetAPIVersion("v1")
	fluxSystem.SetKind("Namespace")
	fluxSystem.SetName("flux-system")
	if err := admin.Create(ctx, fluxSystem); err != nil {
		t.Fatal(err)
	}
	waitRefused(t, "shard identity policy", func() error {
		return admin.Create(ctx, deploy(flux.ConnectedController, "kustomize-controller"))
	}, func() { _ = admin.Delete(ctx, deploy(flux.ConnectedController, "")) })
	if err := admin.Create(ctx, deploy(flux.ConnectedController, flux.ConnectedController, "--default-service-account=x")); err == nil || !strings.Contains(err.Error(), "--default-service-account") {
		t.Errorf("a shard passing --default-service-account: err = %v, want a refusal", err)
	}
	if err := admin.Create(ctx, deploy(flux.ConnectedController, flux.ConnectedController, "--watch-label-selector=sharding.fluxcd.io/key=connected")); err != nil {
		t.Errorf("the shard as rendered must be admitted: %v", err)
	}
	if err := admin.Create(ctx, deploy("kustomize-controller", "kustomize-controller", "--default-service-account=reliant-deploy-tenant")); err != nil {
		t.Errorf("the main controller is not the shard policy's business: %v", err)
	}

	// THE BINDING GUARD. A binding of a controller ServiceAccount to anything
	// but forge's roles is refused — cluster-admin above all.
	binding := func(name, role string, subjects ...map[string]any) *unstructured.Unstructured {
		var subs []any
		for _, s := range subjects {
			subs = append(subs, s)
		}
		u := &unstructured.Unstructured{Object: map[string]any{
			"roleRef":  map[string]any{"apiGroup": "rbac.authorization.k8s.io", "kind": "ClusterRole", "name": role},
			"subjects": subs,
		}}
		u.SetAPIVersion("rbac.authorization.k8s.io/v1")
		u.SetKind("ClusterRoleBinding")
		u.SetName(name)
		return u
	}
	kc := map[string]any{"kind": "ServiceAccount", "name": "kustomize-controller", "namespace": "flux-system"}
	waitRefused(t, "binding guard", func() error {
		return admin.Create(ctx, binding("cluster-reconciler-flux-system", "cluster-admin", kc))
	}, func() { _ = admin.Delete(ctx, binding("cluster-reconciler-flux-system", "cluster-admin")) })
	for _, tc := range []struct {
		name, role string
		subject    map[string]any
		deny       bool
	}{
		{"controller to crd-controller", "crd-controller", kc, false},
		{"controller to the distribution's crd-controller", "crd-controller-flux-system", kc, true},
		{"group of flux-system service accounts to cluster-admin", "cluster-admin", map[string]any{"kind": "Group", "name": "system:serviceaccounts:flux-system", "apiGroup": "rbac.authorization.k8s.io"}, true},
		{"unrelated subject to cluster-admin", "cluster-admin", map[string]any{"kind": "ServiceAccount", "name": "someone", "namespace": "default"}, false},
	} {
		i++
		t.Run("binding: "+tc.name, func(t *testing.T) {
			err := admin.Create(ctx, binding(fmt.Sprintf("guard-%d", i), tc.role, tc.subject))
			if tc.deny && (err == nil || !strings.Contains(err.Error(), "may be bound only to")) {
				t.Fatalf("err = %v, want the guard's refusal", err)
			}
			if !tc.deny && err != nil {
				t.Fatalf("refused: %v", err)
			}
		})
	}

	// UPDATE is covered too: adding a service account to an admitted shard
	// Kustomization is refused.
	ok := kustomization("update-me", shard, base+kubeCfgSecret)
	if err := admin.Create(ctx, ok); err != nil {
		t.Fatal(err)
	}
	if err := unstructured.SetNestedField(ok.Object, flux.DeployServiceAccount, "spec", "serviceAccountName"); err != nil {
		t.Fatal(err)
	}
	if err := admin.Update(ctx, ok); err == nil || !strings.Contains(err.Error(), "must not set spec.serviceAccountName") {
		t.Fatalf("UPDATE adding serviceAccountName: err = %v, want a refusal", err)
	}
}
