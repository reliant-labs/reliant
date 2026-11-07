package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cluster"
	"github.com/reliant-labs/forge/internal/deploytarget"
	"github.com/reliant-labs/forge/internal/kclplugin"
	deployv1alpha1 "github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
)

// Bundle.rendered_secrets exists for the case neither secret provider can
// express: a Secret whose consumer is a PLAIN manifest, in an env whose
// provider is FileSecrets.
//
// Incident: control-plane dev runs an in-cluster OpenBao from raw manifests
// (a Deployment + a bootstrap Job in additional_manifests). Both mount three
// Secrets. A Bundle has ONE secret_provider — dev's is FileSecrets, for its
// services — and a RenderedSecrets provider lands a Secret only where a
// forge WORKLOAD references it. So the Secrets were hand-written as raw
// `kind: Secret` manifests with literal values: no preflight entry, no
// dev/e2e literal gate, no local-cluster guard, and a `from = "file"` value
// would have had to be pasted into git to be expressed at all.

// openbaoDevEntities is control-plane dev's shape: FileSecrets for services,
// three explicitly-placed rendered Secrets for the plain-manifest OpenBao.
func openbaoDevEntities() *KCLEntities {
	return &KCLEntities{
		SecretProvider: &SecretProviderEntity{Type: "file", Path: "secrets/dev.yaml"},
		RenderedSecrets: []RenderedSecretEntity{
			{Name: "control-plane-openbao-storage", Cluster: "k3d-control-plane-v2", Namespace: "control-plane-dev",
				Keys: map[string]RenderedSecretKeyEntity{"connection-url": {From: "literal", Value: "postgres://postgres:postgres@host.k3d.internal:5434/openbao"}}},
			{Name: "control-plane-openbao-approles", Cluster: "k3d-control-plane-v2", Namespace: "control-plane-dev",
				Keys: map[string]RenderedSecretKeyEntity{"service-secret-id": {From: "literal", Value: "dev-openbao-service-secret-id-32"}}},
			{Name: "control-plane-openbao-unseal", Cluster: "k3d-control-plane-v2", Namespace: "control-plane-dev",
				Keys: map[string]RenderedSecretKeyEntity{"static-key": {From: "file", Key: "OPENBAO_STATIC_KEY"}}},
		},
	}
}

// TestPlaceDeclaredSecrets_ExplicitPlacementBesideFileSecrets is the
// regression test: with a FileSecrets provider and NO service referencing
// them, the declared Secrets are placed exactly where they say.
//
// Mutation that fails it: drop the entities.RenderedSecrets loop from
// placeDeclaredSecrets (the pre-fix behaviour: only a RenderedSecrets
// provider's service-referenced Secrets were ever placed).
func TestPlaceDeclaredSecrets_ExplicitPlacementBesideFileSecrets(t *testing.T) {
	placed := placeDeclaredSecrets(openbaoDevEntities(), nil, "fallback-ns")
	if len(placed) != 1 {
		t.Fatalf("want one placement (k3d-control-plane-v2/control-plane-dev), got %+v", placed)
	}
	p := placed[0]
	if p.cluster != "k3d-control-plane-v2" || p.namespace != "control-plane-dev" {
		t.Errorf("placement = %s/%s", p.cluster, p.namespace)
	}
	if got := declaredSecretNamesList(p.secrets); got != `"control-plane-openbao-approles", "control-plane-openbao-storage", "control-plane-openbao-unseal"` {
		t.Errorf("secrets placed = %s", got)
	}
}

// TestPlaceDeclaredSecrets_ProviderInferenceUnchanged: a RenderedSecrets
// provider entry with no cluster keeps landing where its workloads reference
// it — and ONLY there — while one that names a cluster is placed explicitly.
func TestPlaceDeclaredSecrets_ProviderInferenceUnchanged(t *testing.T) {
	entities := &KCLEntities{
		Workloads: []WorkloadEntity{
			{Name: "cp-api", Runtime: RuntimeEntity{Type: RuntimeCluster}, Spec: deployv1alpha1.WorkloadSpec{Env: []deployv1alpha1.EnvVar{
				{Name: "DB_PASSWORD", SecretRef: &deployv1alpha1.SecretKeyRef{Name: "cp-db", Key: "password"}}}}},
			{Name: "workload-api", Runtime: RuntimeEntity{Type: RuntimeCluster}, Spec: deployv1alpha1.WorkloadSpec{Env: []deployv1alpha1.EnvVar{
				{Name: "TOKEN", SecretRef: &deployv1alpha1.SecretKeyRef{Name: "workload-token", Key: "TOKEN"}}}}},
		},
		SecretProvider: &SecretProviderEntity{Type: "rendered", Secrets: []RenderedSecretEntity{
			{Name: "cp-db", Keys: map[string]RenderedSecretKeyEntity{"password": {From: "literal", Value: "x"}}},
			{Name: "workload-token", Keys: map[string]RenderedSecretKeyEntity{"TOKEN": {From: "literal", Value: "y"}}},
			{Name: "litellm-db", Cluster: "k3d-cp", Keys: map[string]RenderedSecretKeyEntity{"url": {From: "literal", Value: "z"}}},
		}},
	}
	groups := []deploytarget.ServiceGroup{
		{ProviderID: "k8s-cluster", Cluster: "k3d-cp", Namespace: "cp", Services: []deploytarget.ResolvedService{{Name: "cp-api"}}},
		{ProviderID: "k8s-cluster", Cluster: "k3d-workload", Namespace: "wl", Services: []deploytarget.ResolvedService{{Name: "workload-api"}}},
	}
	got := map[string]string{}
	for _, p := range placeDeclaredSecrets(entities, groups, "env-ns") {
		got[p.cluster+"/"+p.namespace] = declaredSecretNamesList(p.secrets)
	}
	want := map[string]string{
		"k3d-cp/cp":       `"cp-db"`,
		"k3d-workload/wl": `"workload-token"`,
		// No group references litellm-db — the carrier-ref hack e2e needed —
		// and it names a cluster, so it lands there, in the env namespace.
		"k3d-cp/env-ns": `"litellm-db"`,
	}
	if len(got) != len(want) {
		t.Fatalf("placements = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: got %s, want %s", k, got[k], v)
		}
	}
}

// TestApplyDeclaredSecrets_RefusesNonLocalCluster keeps the plaintext guard:
// a Secret placed on a remote cluster is refused before anything is applied.
func TestApplyDeclaredSecrets_RefusesNonLocalCluster(t *testing.T) {
	e := &KCLEntities{RenderedSecrets: []RenderedSecretEntity{{
		Name: "leak", Cluster: "gke_p_us-central1_prod", Namespace: "prod",
		Keys: map[string]RenderedSecretKeyEntity{"k": {From: "literal", Value: "v"}},
	}}}
	err := applyDeclaredSecrets(context.Background(), e, nil, "prod", "dev", true)
	if err == nil || !strings.Contains(err.Error(), "not local") {
		t.Fatalf("want a not-local refusal, got %v", err)
	}
}

// TestApplyDeclaredSecrets_DryRunResolvesFileKeysFromTheFileSecretsStore:
// a `from = "file"` key reads the SAME store the FileSecrets provider
// declares (not a hardcoded secrets/<env>.yaml), and a literal is gated to
// dev/e2e by the Go guard too.
func TestApplyDeclaredSecrets_DryRunResolvesFileKeysFromTheFileSecretsStore(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte("name: x\nmodule_path: example.com/x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := filepath.Join(dir, "config", "dev-secrets.yaml")
	if err := os.MkdirAll(filepath.Dir(store), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store, []byte("OPENBAO_STATIC_KEY: from-the-store-0123456789abcdef\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	e := openbaoDevEntities()
	e.SecretProvider.Path = "config/dev-secrets.yaml"

	var streams []string
	collect := secretTarget{sink: func(_ context.Context, p secretPlacement) error {
		streams = append(streams, p.stream)
		return nil
	}}
	if err := applyDeclaredSecretsTo(context.Background(), e, nil, "fallback", "dev", false, collect); err != nil {
		t.Fatalf("apply: %v", err)
	}
	placed := strings.Join(streams, "\n")
	for _, want := range []string{"control-plane-openbao-unseal", "from-the-store-0123456789abcdef", "namespace: control-plane-dev"} {
		if !strings.Contains(placed, want) {
			t.Errorf("placed Secrets missing %q:\n%s", want, placed)
		}
	}
	// The dry run resolves the same keys but prints no value.
	dry := captureStdout(t, func() {
		if err := applyDeclaredSecrets(context.Background(), e, nil, "fallback", "dev", true); err != nil {
			t.Fatalf("dry-run apply: %v", err)
		}
	})
	if strings.Contains(dry, "from-the-store-0123456789abcdef") {
		t.Errorf("a dry run printed a Secret value:\n%s", dry)
	}

	if err := applyDeclaredSecrets(context.Background(), e, nil, "fallback", "prod", true); err == nil ||
		!strings.Contains(err.Error(), "only allowed in dev/e2e") {
		t.Errorf("a literal under env=prod must be refused by the Go guard, got %v", err)
	}
}

// TestSecretSupply_CountsBundleRenderedSecrets: a Bundle-level rendered
// Secret is SUPPLY for the render-time mount gate, so a plain manifest that
// mounts it is not reported as an undeclared mount.
//
// Mutation that fails it: drop RenderedSecrets from declaredSecretEntities.
func TestSecretSupply_CountsBundleRenderedSecrets(t *testing.T) {
	manifests := `apiVersion: apps/v1
kind: Deployment
metadata: {name: openbao, namespace: control-plane-dev}
spec:
  template:
    spec:
      containers:
      - name: openbao
        env:
        - name: BAO_STATIC_KEY
          valueFrom: {secretKeyRef: {name: control-plane-openbao-unseal, key: static-key}}
`
	if misses := cluster.CheckSecretSupply(manifests, secretSupplyForPreflight(openbaoDevEntities())); len(misses) != 0 {
		t.Errorf("a declared rendered Secret must satisfy the mount; undeclared = %+v", misses)
	}
	if misses := cluster.CheckSecretSupply(manifests, secretSupplyForPreflight(&KCLEntities{})); len(misses) == 0 {
		t.Errorf("control: with no declaration the mount must be reported undeclared")
	}
}

// TestSecretDeclarations_IncludeRenderedSecretStoreKeys: a `from = "file"`
// key of a Bundle-level rendered Secret is a declared store key, so
// `forge secret ensure` lists it when it has no value and `forge secret
// list` attributes it to its Secret rather than calling it inert.
func TestSecretDeclarations_IncludeRenderedSecretStoreKeys(t *testing.T) {
	e := openbaoDevEntities()
	if !containsString(declaredSecretNames(e), "OPENBAO_STATIC_KEY") {
		t.Errorf("declaredSecretNames must include the rendered Secret's store key; got %v", declaredSecretNames(e))
	}
	for _, name := range declaredSecretNames(e) {
		if strings.Contains(name, "openbao-storage") || name == "connection-url" {
			t.Errorf("a literal key is not a store declaration: %q", name)
		}
	}
	decl := secretDeclarationsByEnvName(e)["OPENBAO_STATIC_KEY"]
	if len(decl) != 1 || decl[0].Kind != "rendered-secret" || decl[0].SecretName != "control-plane-openbao-unseal" || decl[0].SecretKey != "static-key" {
		t.Errorf("attribution = %+v", decl)
	}
}

// bundleRenderedSecretsMainK is control-plane dev's shape in miniature: a
// FileSecrets provider for services, and rendered_secrets for a plain-manifest
// consumer. Two entries leave namespace/cluster UNSET, which is the case that
// broke.
const bundleRenderedSecretsMainK = `import forge

_target = forge.ClusterTarget {
    cluster = "k3d-rs"
    namespace = "rs-dev"
}

_bundle = forge.Bundle {
    project = "acme"
    cluster_target = _target
    secret_provider = forge.FileSecrets {path = "secrets/dev.yaml"}
    rendered_secrets = [
        forge.RenderedSecret {
            name = "store-storage"
            keys = {"connection-url" = forge.RenderedSecretKey {from = "literal", value = "postgres://localhost/store"}}
        }
        forge.RenderedSecret {
            name = "store-unseal"
            keys = {"static-key" = forge.RenderedSecretKey {key = "STORE_STATIC_KEY"}}
        }
        forge.RenderedSecret {
            name = "store-unseal"
            cluster = "k3d-rs-other"
            namespace = "other"
            keys = {"static-key" = forge.RenderedSecretKey {key = "STORE_STATIC_KEY"}}
        }
    ]
}

output = forge.render(_bundle)
`

// TestBundleRenderedSecrets_RenderThroughForge renders the declaration through
// forge's own KCL runtime (not the kcl CLI) and checks the placements it
// resolves.
//
// It also pins a KCL scoping hazard found while validating against
// control-plane: RenderedSecret's placement attributes were first declared
// OPTIONAL (`namespace?: str`). A schema check that reads an UNSET attribute
// by bare name falls through to package scope, and forge's own kcl/base.k
// defines a package-level `namespace = lambda …` — so under forge's runtime
// the check saw a function, treated it as truthy, and rejected every entry
// that set no namespace ("RenderedSecret.namespace requires cluster"). The
// kcl CLI resolves it differently, which is why the kcl/tests fixtures did not
// catch it. Defaulting both attributes to "" makes the name always resolve to
// the attribute.
//
// Mutation that fails it: declare RenderedSecret.cluster/namespace as `?: str`.
func TestBundleRenderedSecrets_RenderThroughForge(t *testing.T) {
	kclplugin.Register()
	resetDevStackGlobals(t)
	dir := t.TempDir()
	writePortblockProject(t, dir, "dev", bundleRenderedSecretsMainK)
	t.Chdir(dir)

	entities, err := RenderKCL(t.Context(), dir, "dev")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if entities.SecretProvider == nil || entities.SecretProvider.Type != "file" {
		t.Fatalf("the FileSecrets provider must be untouched, got %+v", entities.SecretProvider)
	}
	if len(entities.RenderedSecrets) != 3 {
		t.Fatalf("want 3 rendered secrets, got %+v", entities.RenderedSecrets)
	}
	unseal := entities.RenderedSecrets[1].Keys["static-key"]
	if unseal.From != "file" || unseal.Key != "STORE_STATIC_KEY" || unseal.Value != "" {
		t.Errorf("a store key must carry its KEY and no value, got %+v", unseal)
	}

	got := map[string]string{}
	for _, p := range placeDeclaredSecrets(entities, nil, "fallback") {
		got[p.cluster+"/"+p.namespace] = declaredSecretNamesList(p.secrets)
	}
	want := map[string]string{
		"k3d-rs/rs-dev":      `"store-storage", "store-unseal"`,
		"k3d-rs-other/other": `"store-unseal"`,
	}
	if len(got) != len(want) {
		t.Fatalf("placements = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: got %s, want %s", k, got[k], v)
		}
	}
}
