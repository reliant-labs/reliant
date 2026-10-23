package cluster

import (
	"context"
	"strings"
	"testing"
)

// clusterSecretGetter resolves keys per (kubectl context, namespace/name), so a
// Secret can exist in one cluster and be absent — or incomplete — in another.
type clusterSecretGetter struct {
	byCluster map[string]map[string]map[string]struct{} // kctx -> "ns/name" -> keys
}

func (g clusterSecretGetter) GetSecretKeys(_ context.Context, kctx, ns, name string) (map[string]struct{}, bool, error) {
	inCluster, ok := g.byCluster[kctx]
	if !ok {
		return nil, false, nil
	}
	keys, ok := inCluster[ns+"/"+name]
	if !ok {
		return nil, false, nil
	}
	return keys, true, nil
}

// TestRequiredSecretsCheckedInEveryCluster is the regression for a deploy that
// reached a rollout and died with CreateContainerConfigError. The env deploys
// one render to two clusters; the Secret existed in the primary with every key,
// and in the secondary was missing one. Checking only the primary passed the
// preflight and left the failure to surface minutes later as an unschedulable
// pod naming a key, in a namespace, in a cluster the operator had to work out.
func TestRequiredSecretsCheckedInEveryCluster(t *testing.T) {
	full := map[string]struct{}{"proxy_session_secret": {}, "zitadel_broker_token": {}}
	stale := map[string]struct{}{"proxy_session_secret": {}} // secondary lags a key

	opts := PreflightOpts{
		Namespace: "control-plane-dev",
		Context:   "k3d-control-plane",
		Secrets: clusterSecretGetter{byCluster: map[string]map[string]map[string]struct{}{
			"k3d-control-plane": {"control-plane-dev/control-plane-secrets": full},
			"k3d-cp-daemon":     {"control-plane-dev/control-plane-secrets": stale},
		}},
		RequiredSecrets: []RequiredSecret{{
			Name: "control-plane-secrets", Namespace: "control-plane-dev",
			Keys: []string{"proxy_session_secret", "zitadel_broker_token"},
		}},
		RequiredSecretContexts: []string{"k3d-control-plane", "k3d-cp-daemon"},
	}
	res, err := runPreflightChecks(context.Background(), opts)
	if err != nil {
		t.Fatalf("runPreflightChecks: %v", err)
	}
	if len(res.MissingRequiredSecretKeys) == 0 {
		t.Fatal("preflight passed while a required key was missing from the secondary cluster")
	}
	key := "k3d-cp-daemon:control-plane-dev/control-plane-secrets"
	missing, ok := res.MissingRequiredSecretKeys[key]
	if !ok {
		t.Fatalf("secondary-cluster miss not reported under %q; got %v", key, res.MissingRequiredSecretKeys)
	}
	if len(missing) != 1 || missing[0] != "zitadel_broker_token" {
		t.Fatalf("missing = %v; want [zitadel_broker_token]", missing)
	}
	// The primary is complete and must NOT be reported.
	for k := range res.MissingRequiredSecretKeys {
		if strings.HasPrefix(k, "k3d-control-plane:") {
			t.Errorf("primary cluster reported despite having every key: %v", res.MissingRequiredSecretKeys)
		}
	}
}

// TestRequiredSecretsSingleClusterKeepsUnqualifiedKey keeps the common case's
// message unchanged: the cluster name only qualifies the key when more than one
// cluster is actually checked.
func TestRequiredSecretsSingleClusterKeepsUnqualifiedKey(t *testing.T) {
	opts := PreflightOpts{
		Namespace: "app",
		Context:   "prod",
		Secrets: clusterSecretGetter{byCluster: map[string]map[string]map[string]struct{}{
			"prod": {"cert-manager/cloudflare-api-token": {}},
		}},
		RequiredSecrets: []RequiredSecret{{
			Name: "cloudflare-api-token", Namespace: "cert-manager", Keys: []string{"api-token"},
		}},
	}
	res, err := runPreflightChecks(context.Background(), opts)
	if err != nil {
		t.Fatalf("runPreflightChecks: %v", err)
	}
	if _, ok := res.MissingRequiredSecretKeys["cert-manager/cloudflare-api-token"]; !ok {
		t.Fatalf("single-cluster key should stay unqualified; got %v", res.MissingRequiredSecretKeys)
	}
}

// TestRequiredSecretContextsFallsBackToContext pins the compatibility path.
func TestRequiredSecretContextsFallsBackToContext(t *testing.T) {
	got := requiredSecretContexts(PreflightOpts{Context: "prod"})
	if len(got) != 1 || got[0] != "prod" {
		t.Fatalf("fallback = %v; want [prod]", got)
	}
	got = requiredSecretContexts(PreflightOpts{Context: "prod", RequiredSecretContexts: []string{"a", "", "a", "b"}})
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("dedup/trim = %v; want [a b]", got)
	}
	if n := len(requiredSecretContexts(PreflightOpts{})); n != 0 {
		t.Fatalf("no context configured should yield none, got %d", n)
	}
}

// TestRequiredSecretKeysUnionsManifestReferences is the regression for a key
// the ExternalSecret never declared. A workload started reading
// `zitadel_broker_token`; nothing required the author to also add it to the
// prerequisite's hand-written key list, so the preflight passed and the pod
// failed at rollout with CreateContainerConfigError naming a key the check had
// never been told to look for.
func TestRequiredSecretKeysUnionsManifestReferences(t *testing.T) {
	rs := RequiredSecret{Name: "control-plane-secrets", Namespace: "control-plane-dev", Keys: []string{"proxy_session_secret"}}
	at := ResourceRef{Context: "k3d-control-plane", Namespace: "control-plane-dev", Name: "control-plane-secrets"}
	refs := ManifestRefs{Secrets: map[ResourceRef]map[string]struct{}{
		at: {"zitadel_broker_token": {}, "proxy_session_secret": {}, "": {}},
	}}

	got := requiredSecretKeys(rs, refs, "k3d-control-plane")
	if len(got) != 2 || got[0] != "proxy_session_secret" || got[1] != "zitadel_broker_token" {
		t.Fatalf("keys = %v; want the declared key unioned with the referenced one", got)
	}

	// A whole-Secret reference ("") asserts existence only and adds no key.
	for _, k := range got {
		if k == "" {
			t.Fatal("whole-Secret marker leaked into the required key set")
		}
	}

	// A prerequisite in ANOTHER namespace is resolved there, so deploy-namespace
	// references must not be folded in.
	other := RequiredSecret{Name: "control-plane-secrets", Namespace: "cert-manager", Keys: []string{"api-token"}}
	got = requiredSecretKeys(other, refs, "k3d-control-plane")
	if len(got) != 1 || got[0] != "api-token" {
		t.Fatalf("cross-namespace keys = %v; want only the declared [api-token]", got)
	}

	// A reference from a pod on ANOTHER cluster resolves to that cluster's
	// copy, so it adds nothing to this one's requirement.
	got = requiredSecretKeys(rs, refs, "k3d-cp-daemon")
	if len(got) != 1 || got[0] != "proxy_session_secret" {
		t.Fatalf("cross-cluster keys = %v; want only the declared [proxy_session_secret]", got)
	}

	// A reference collected with no cluster (the deploy target was never
	// named) is placed nowhere in particular, so it counts on every cluster.
	unplaced := ManifestRefs{Secrets: map[ResourceRef]map[string]struct{}{
		{Namespace: "control-plane-dev", Name: "control-plane-secrets"}: {"zitadel_broker_token": {}},
	}}
	got = requiredSecretKeys(rs, unplaced, "k3d-cp-daemon")
	if len(got) != 2 || got[1] != "zitadel_broker_token" {
		t.Fatalf("unplaced keys = %v; want the referenced key folded in on any cluster", got)
	}
}
