package cli

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cluster"
)

// kubeconfig_preflight_test.go pins the projection that keeps the deployability
// preflight from blocking on a Secret the same deploy MINTS, and — the part
// that matters more — pins that it exempts nothing else.
//
// See provisionedByDeployForPreflight. The gate and the mint phase read one
// filter (deployMintedKubeconfigSecrets) precisely so they cannot drift: a
// declaration forge stops minting must stop being exempt in the same commit.

// hubKubeconfigEntities is control-plane's prod hub edge: a KubeconfigSecret
// forge MINTS (service_account set), reachable in-cluster.
func hubKubeconfigEntities() *KCLEntities {
	return &KCLEntities{KubeconfigSecrets: []KubeconfigSecretEntity{{
		Name:          "control-plane-hub-kubeconfig",
		InCluster:     "gke_p_us-central1_prod",
		TargetCluster: "control-plane-cluster",
		TargetContext: "gke_p_us-central1_prod",
		ContextName:   "hub",
		Namespace:     "control-plane-prod",
		Key:           "kubeconfig",
		Reachability:  "in-cluster",
		ServiceAccount: &KubeconfigServiceAccountEntity{
			Name:      "flux-hub-applier",
			Namespace: "control-plane-prod",
		},
	}}}
}

// TestProvisionedByDeployForPreflight_ExemptsTheMintedSecret is the projection
// half of the F1c regression: the Secret forge is about to mint must reach the
// preflight as provisioned-by-this-deploy, or the gate blocks on forge's own
// output.
func TestProvisionedByDeployForPreflight_ExemptsTheMintedSecret(t *testing.T) {
	got := provisionedByDeployForPreflight(hubKubeconfigEntities())
	if len(got) != 1 {
		t.Fatalf("want exactly the minted declaration, got %+v", got)
	}
	if got[0].Name != "control-plane-hub-kubeconfig" {
		t.Errorf("name: %+v", got[0])
	}
	if got[0].Namespace != "control-plane-prod" {
		t.Errorf("namespace should carry the declaration's own: %+v", got[0])
	}
	if got[0].By != cluster.SupplyKubeconfigSecret {
		t.Errorf("By should label the provisioning step: %+v", got[0])
	}
}

// TestProvisionedByDeployForPreflight_DoesNotExemptACopiedKubeconfig is the
// negative case the fix must not swallow. A KubeconfigSecret WITHOUT a
// service_account COPIES a k3d kubeconfig on the `env up` path — the deploy
// mints nothing — so its absence is a genuine missing dependency and the
// preflight must still block on it.
//
// Exempting every declared KubeconfigSecret (rather than every MINTED one)
// would pass this deploy and fail at rollout with FailedMount, which is the
// class of late, opaque failure the preflight exists to prevent.
func TestProvisionedByDeployForPreflight_DoesNotExemptACopiedKubeconfig(t *testing.T) {
	entities := &KCLEntities{KubeconfigSecrets: []KubeconfigSecretEntity{{
		Name:          "cp-daemon-kubeconfig",
		TargetCluster: "cp-daemon",
		Reachability:  "in-network",
		// No ServiceAccount => forge does not mint this one.
	}}}
	if got := provisionedByDeployForPreflight(entities); len(got) != 0 {
		t.Fatalf("a copied (unminted) kubeconfig must stay gated by the preflight, got %+v", got)
	}
}

// TestProvisionedByDeployForPreflight_Empty pins the inert cases: an env with
// no declarations, and nil entities, exempt nothing.
func TestProvisionedByDeployForPreflight_Empty(t *testing.T) {
	if got := provisionedByDeployForPreflight(nil); got != nil {
		t.Errorf("nil entities: %+v", got)
	}
	if got := provisionedByDeployForPreflight(&KCLEntities{}); got != nil {
		t.Errorf("no declarations: %+v", got)
	}
}

// TestDeployMintedKubeconfigSecrets_IsTheSharedFilter pins the property the
// whole design rests on: the preflight's exemption set and the mint phase's
// work list are the SAME selection. If these ever disagree, either forge
// blocks on a Secret it creates (the F1c defect) or it waves through a Secret
// nobody creates (a silent FailedMount).
func TestDeployMintedKubeconfigSecrets_IsTheSharedFilter(t *testing.T) {
	entities := hubKubeconfigEntities()
	entities.KubeconfigSecrets = append(entities.KubeconfigSecrets, KubeconfigSecretEntity{
		Name: "cp-daemon-kubeconfig", TargetCluster: "cp-daemon", Reachability: "in-network",
	})

	minted := deployMintedKubeconfigSecrets(entities)
	exempt := provisionedByDeployForPreflight(entities)
	if len(minted) != len(exempt) {
		t.Fatalf("the mint list (%d) and the preflight exemption (%d) must be the same selection", len(minted), len(exempt))
	}
	for i := range minted {
		if minted[i].Name != exempt[i].Name {
			t.Errorf("entry %d: mints %q but exempts %q", i, minted[i].Name, exempt[i].Name)
		}
	}
}

// TestMintDeployKubeconfigSecrets_DryRunDescribesTheMint pins that --dry-run
// still PREVIEWS the mint. This is what the exemption buys: before the fix,
// reviewing the credential forge would grant required --skip-preflight,
// because the preflight refused the dry run over the very Secret the dry run
// was printing. A dry run contacts no cluster, so nothing here is granted.
func TestMintDeployKubeconfigSecrets_DryRunDescribesTheMint(t *testing.T) {
	out := captureStdout(t, func() {
		if err := mintDeployKubeconfigSecrets(t.Context(), hubKubeconfigEntities(), "control-plane-prod", true); err != nil {
			t.Fatalf("dry-run mint: %v", err)
		}
	})
	for _, want := range []string{
		"would mint",
		"control-plane-hub-kubeconfig",
		"ServiceAccount control-plane-prod/flux-hub-applier",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("dry-run should describe the mint (missing %q), got:\n%s", want, out)
		}
	}
}
