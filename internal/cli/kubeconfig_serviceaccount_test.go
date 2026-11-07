package cli

import (
	"strings"
	"testing"

	"k8s.io/client-go/tools/clientcmd"
)

// TestMintOne_NonK3dTargetWithoutServiceAccount_IsRefused is the RED test
// for forge defect F1b at the Go seam.
//
// Before the change there was no way to say "this target is not a k3d
// cluster": the mint ran `k3d kubeconfig get <target_cluster>`
// unconditionally, so a declaration for a GKE cluster either failed with an
// opaque k3d error or — worse, had it been given a k3d-shaped name — copied
// the operator's exec-plugin credential into a Secret and produced a
// kubeconfig no pod can authenticate with. Nothing told the operator which
// of those had happened.
//
// Now naming a target_context without a service_account is refused up front,
// with the fix in the message. This test contacts no cluster: the refusal
// must come BEFORE any subprocess, which is itself the property under test
// (a k3d shell-out against a GKE context is not a diagnosis).
//
// MUTATION VERIFIED RED: deleting the target_context guard makes this fail
// with a `k3d kubeconfig get` error instead.
func TestMintOne_NonK3dTargetWithoutServiceAccount_IsRefused(t *testing.T) {
	k := KubeconfigSecretEntity{
		Name:          "control-plane-hub-kubeconfig",
		InCluster:     "gke_p_us-central1_prod",
		TargetCluster: "prod",
		TargetContext: "gke_p_us-central1_prod",
		ContextName:   "hub",
		Reachability:  "endpoint",
	}
	err := mintOneKubeconfigSecret(t.Context(), k, "", "control-plane-prod")
	if err == nil {
		t.Fatal("a non-k3d target with no service_account must be refused: copying the operator's " +
			"exec-plugin credential yields a kubeconfig no pod can use")
	}
	for _, want := range []string{"service_account", "target_context"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal should name %q so the operator knows the fix; got: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "k3d kubeconfig get") {
		t.Errorf("the refusal shelled out to k3d against a non-k3d target: %v", err)
	}
}

// TestAssembleServiceAccountKubeconfig_IsPodUsable pins the property the
// whole defect is about: the minted kubeconfig authenticates with an INLINE
// bearer token and nothing else.
//
// A GKE kubeconfig's credential is an `exec` block invoking
// gke-gcloud-auth-plugin with the operator's ADC. Mounted into a pod there
// is no plugin binary and no credentials, so it cannot authenticate — the
// reason the hub kubeconfig was hand-made. Every negative assertion below is
// a shape that would reintroduce that.
//
// MUTATION VERIFIED RED: setting AuthInfo.TokenFile instead of Token trips
// the tokenFile assertion; dropping CAData trips the CA assertion.
func TestAssembleServiceAccountKubeconfig_IsPodUsable(t *testing.T) {
	out, err := assembleServiceAccountKubeconfig(serviceAccountKubeconfig{
		ContextName: "hub",
		Server:      inClusterAPIServer,
		CAData:      []byte("-----BEGIN CERTIFICATE-----\nca\n-----END CERTIFICATE-----\n"),
		Token:       "eyJhbGciOi.minted.token",
		Namespace:   "control-plane-prod",
	})
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	text := string(out)
	// Nothing that needs a binary, a file, or the operator's machine.
	for _, forbidden := range []string{"exec:", "tokenFile", "client-certificate", "insecure-skip-tls-verify"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("minted kubeconfig contains %q — it is not pod-usable:\n%s", forbidden, text)
		}
	}

	cfg, err := clientcmd.Load(out)
	if err != nil {
		t.Fatalf("load minted kubeconfig: %v", err)
	}
	if cfg.CurrentContext != "hub" {
		t.Errorf("current-context = %q want hub", cfg.CurrentContext)
	}
	auth := cfg.AuthInfos["hub"]
	if auth == nil || auth.Token != "eyJhbGciOi.minted.token" {
		t.Fatalf("the credential is not an inline token: %+v", auth)
	}
	if auth.Exec != nil {
		t.Error("the minted kubeconfig carries an exec plugin, which no pod can run")
	}
	c := cfg.Clusters["hub"]
	if c.Server != inClusterAPIServer {
		t.Errorf("server = %q want %q", c.Server, inClusterAPIServer)
	}
	if len(c.CertificateAuthorityData) == 0 {
		t.Error("the minted kubeconfig has no inline CA, so a pod cannot verify the API server")
	}
	if c.CertificateAuthority != "" {
		t.Errorf("CA is a FILE path %q — it does not exist in the consuming pod", c.CertificateAuthority)
	}
	if cfg.Contexts["hub"].Namespace != "control-plane-prod" {
		t.Errorf("context namespace = %q", cfg.Contexts["hub"].Namespace)
	}
}

// TestAssembleServiceAccountKubeconfig_RefusesWithoutCA pins that the mint
// never degrades to an unverified connection. Flux's kustomize-controller
// ignores insecure-skip-tls-verify by design, so a CA-less kubeconfig fails
// at apply time with an x509 error far from its cause; failing here, where
// the declaration is, is the actionable place.
func TestAssembleServiceAccountKubeconfig_RefusesWithoutCA(t *testing.T) {
	_, err := assembleServiceAccountKubeconfig(serviceAccountKubeconfig{
		ContextName: "hub", Server: inClusterAPIServer, Token: "t",
	})
	if err == nil || !strings.Contains(err.Error(), "CA") {
		t.Fatalf("a kubeconfig with no CA must be refused, got %v", err)
	}
}

// TestReaderServer_InClusterNeedsNoDiscovery pins that an in-cluster reader
// gets the Service address, resolved with no cluster contact at all. That is
// what makes the hub edge work on any cluster and survive an endpoint
// rotation.
func TestReaderServer_InClusterNeedsNoDiscovery(t *testing.T) {
	got, err := readerServer(t.Context(), KubeconfigSecretEntity{
		Reachability: "in-cluster",
	}, "gke_p_us-central1_prod")
	if err != nil {
		t.Fatalf("readerServer: %v", err)
	}
	if got != "https://kubernetes.default.svc" {
		t.Fatalf("in-cluster server = %q", got)
	}
}

// TestServiceAccountRBACManifests_ClusterWide pins the objects forge
// converges on the target for a cluster-wide credential — the hub edge's
// exact shape.
func TestServiceAccountRBACManifests_ClusterWide(t *testing.T) {
	sa := &KubeconfigServiceAccountEntity{
		Name:      "flux-hub-applier",
		Namespace: "control-plane-prod",
		Rules: []KubeconfigPolicyRuleEntity{
			{APIGroups: []string{"forge.dev"}, Resources: []string{"*"}, Verbs: []string{"*"}},
			{Resources: []string{"namespaces"}, Verbs: []string{"get", "create"}},
		},
	}
	got := serviceAccountRBACManifests(sa, "flux-hub-applier-forge-token")

	for _, want := range []string{
		"kind: ServiceAccount",
		"name: flux-hub-applier",
		"namespace: control-plane-prod",
		"kind: ClusterRole",
		"kind: ClusterRoleBinding",
		// Namespaced so two envs minting the same SA name do not fight over
		// one cluster-scoped object.
		"name: forge-control-plane-prod-flux-hub-applier",
		`apiGroups: ["forge.dev"]`,
		// The core group is the empty string and MUST stay quoted, or YAML
		// reads it as null and the rule silently covers nothing.
		`apiGroups: [""]`,
		`verbs: ["get", "create"]`,
		"type: kubernetes.io/service-account-token",
		"kubernetes.io/service-account.name: flux-hub-applier",
		// The adoption gate's marker.
		"app.kubernetes.io/managed-by: forge",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered permissions missing %q:\n%s", want, got)
		}
	}
	// Cluster-wide means no Role/RoleBinding at all.
	if strings.Contains(got, "kind: Role\n") || strings.Contains(got, "kind: RoleBinding\n") {
		t.Errorf("cluster-wide mint rendered a namespaced Role:\n%s", got)
	}
}

// TestServiceAccountRBACManifests_NamespaceScoped pins that declaring
// namespaces swaps the ClusterRole for a Role per namespace, while the
// binding's subject stays the SA in its OWN namespace (a Role in namespace N
// may be granted to a ServiceAccount from anywhere).
func TestServiceAccountRBACManifests_NamespaceScoped(t *testing.T) {
	sa := &KubeconfigServiceAccountEntity{
		Name:       "workload-reader",
		Namespace:  "forge-system",
		Namespaces: []string{"app-a", "app-b"},
		Rules: []KubeconfigPolicyRuleEntity{
			{Resources: []string{"pods"}, Verbs: []string{"get", "list"}},
		},
	}
	got := serviceAccountRBACManifests(sa, "workload-reader-forge-token")

	if strings.Contains(got, "ClusterRole") {
		t.Errorf("a namespace-scoped mint must not grant cluster-wide access:\n%s", got)
	}
	for _, ns := range []string{"app-a", "app-b"} {
		if !strings.Contains(got, "  namespace: "+ns+"\n") {
			t.Errorf("no Role/RoleBinding in namespace %q:\n%s", ns, got)
		}
	}
	// Count the top-level object headers, not `roleRef.kind`, which also
	// spells "kind: Role".
	if strings.Count(got, "\nkind: Role\n") != 2 || strings.Count(got, "\nkind: RoleBinding\n") != 2 {
		t.Errorf("want one Role + RoleBinding per declared namespace:\n%s", got)
	}
	// The subject is the SA where it actually lives.
	if !strings.Contains(got, "- kind: ServiceAccount\n  name: workload-reader\n  namespace: forge-system\n") {
		t.Errorf("binding subject does not name the SA in its own namespace:\n%s", got)
	}
}

// TestDescribeServiceAccountMint_ContactsNothing pins the --dry-run
// rendering: the full object set and the chosen server, with no cluster
// contact, so a credential grant can be reviewed before it is made.
func TestDescribeServiceAccountMint_ContactsNothing(t *testing.T) {
	got := describeServiceAccountMint(KubeconfigSecretEntity{
		Name:          "control-plane-hub-kubeconfig",
		InCluster:     "gke_p_us-central1_prod",
		TargetCluster: "prod",
		TargetContext: "gke_p_us-central1_prod",
		ContextName:   "hub",
		Reachability:  "in-cluster",
		ServiceAccount: &KubeconfigServiceAccountEntity{
			Name:      "flux-hub-applier",
			Namespace: "control-plane-prod",
			Rules: []KubeconfigPolicyRuleEntity{
				{APIGroups: []string{"forge.dev"}, Resources: []string{"*"}, Verbs: []string{"*"}},
			},
		},
	}, "control-plane-prod")

	for _, want := range []string{
		"control-plane-hub-kubeconfig",
		"ServiceAccount control-plane-prod/flux-hub-applier",
		"https://kubernetes.default.svc",
		"kind: ClusterRole",
		"type: kubernetes.io/service-account-token",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("dry-run description missing %q:\n%s", want, got)
		}
	}
}

// TestTargetKubectlContext covers the two ways a target is addressed: an
// explicit context (any cluster) and the k3d derivation (the default).
func TestTargetKubectlContext(t *testing.T) {
	got, err := targetKubectlContext(KubeconfigSecretEntity{TargetCluster: "workload"})
	if err != nil || got != "k3d-workload" {
		t.Errorf("k3d derivation = %q, %v", got, err)
	}
	got, err = targetKubectlContext(KubeconfigSecretEntity{
		TargetCluster: "prod", TargetContext: "gke_p_us-central1_prod",
	})
	if err != nil || got != "gke_p_us-central1_prod" {
		t.Errorf("explicit context = %q, %v", got, err)
	}
}

// TestParseKCLEntities_ServiceAccountMint pins that the renderer's
// service_account projection parses into the entity the mint reads.
func TestParseKCLEntities_ServiceAccountMint(t *testing.T) {
	const js = `{
  "output": {
    "kubeconfig_secrets": [
      {
        "name": "control-plane-hub-kubeconfig",
        "in_cluster": "gke_p_us-central1_prod",
        "target_cluster": "prod",
        "target_context": "gke_p_us-central1_prod",
        "context_name": "hub",
        "key": "kubeconfig",
        "reachability": "in-cluster",
        "service_account": {
          "name": "flux-hub-applier",
          "namespace": "control-plane-prod",
          "namespaces": [],
          "rules": [
            {"api_groups": ["forge.dev"], "resources": ["*"], "verbs": ["*"]}
          ]
        }
      }
    ],
    "workloads": []
  }
}`
	entities, err := parseKCLEntities([]byte(js))
	if err != nil {
		t.Fatalf("parseKCLEntities: %v", err)
	}
	k := entities.KubeconfigSecrets[0]
	if k.TargetContext != "gke_p_us-central1_prod" || k.Reachability != "in-cluster" {
		t.Errorf("target/reachability wrong: %+v", k)
	}
	if k.ServiceAccount == nil {
		t.Fatal("service_account did not parse — the mint would fall back to the k3d copy path")
	}
	if k.ServiceAccount.Name != "flux-hub-applier" || len(k.ServiceAccount.Rules) != 1 {
		t.Errorf("service_account wrong: %+v", k.ServiceAccount)
	}
	if k.ServiceAccount.Rules[0].APIGroups[0] != "forge.dev" {
		t.Errorf("rules wrong: %+v", k.ServiceAccount.Rules)
	}
}

// TestMintDeployKubeconfigSecrets_OnlyMintingDeclarations pins that the
// DEPLOY path handles only the declarations that mint their own credential.
// A k3d in-network declaration resolves a docker container address on the
// operator's machine, which is an `env up` concern — running it during a
// cloud deploy would fail, or resolve something unrelated.
func TestMintDeployKubeconfigSecrets_OnlyMintingDeclarations(t *testing.T) {
	entities := &KCLEntities{KubeconfigSecrets: []KubeconfigSecretEntity{
		{Name: "k3d-one", TargetCluster: "workload", Reachability: "in-network"},
	}}
	// No service_account => nothing for the deploy path, and no shell-out.
	if err := mintDeployKubeconfigSecrets(t.Context(), entities, "dev", false); err != nil {
		t.Fatalf("in-network declaration must be skipped on the deploy path, got %v", err)
	}
	if err := mintDeployKubeconfigSecrets(t.Context(), nil, "dev", false); err != nil {
		t.Fatalf("nil entities: %v", err)
	}
}

// TestServiceAccountRBACManifests_ResourceNames pins that a rule limited to
// named objects renders `resourceNames`, and that a rule without any renders
// none (an empty list would be harmless, but the diff against live objects
// forge converges should stay minimal).
func TestServiceAccountRBACManifests_ResourceNames(t *testing.T) {
	sa := &KubeconfigServiceAccountEntity{
		Name:      "flux-hub-applier",
		Namespace: "control-plane-prod",
		Rules: []KubeconfigPolicyRuleEntity{
			{
				APIGroups:     []string{"rbac.authorization.k8s.io"},
				Resources:     []string{"clusterroles"},
				Verbs:         []string{"bind"},
				ResourceNames: []string{"hub-reconciler", "hub-source"},
			},
			{Resources: []string{"namespaces"}, Verbs: []string{"get"}},
		},
	}
	got := serviceAccountRBACManifests(sa, "flux-hub-applier-forge-token")
	want := `resourceNames: ["hub-reconciler", "hub-source"]`
	if !strings.Contains(got, want) {
		t.Errorf("rendered rules missing %q:\n%s", want, got)
	}
	if n := strings.Count(got, "resourceNames:"); n != 1 {
		t.Errorf("resourceNames rendered %d times, want 1 (only the named rule):\n%s", n, got)
	}
}
