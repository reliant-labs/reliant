package cluster

import (
	"strings"
	"testing"
)

// preflight_minted_secret_test.go pins the gate's behaviour for Secrets THIS
// deploy provisions after the preflight runs — PreflightOpts.ProvisionedByDeploy.
//
// The defect these pin: `forge env deploy` runs the deployability preflight
// BEFORE the kubeconfig mint phase, so a forge.KubeconfigSecret forge is about
// to MINT was reported as a missing dependency and the deploy stopped before
// creating it. Self-inflicted — forge blocking on its own output. Observed on
// control-plane's first PHASE-2 prod deploy as:
//
//	Secret control-plane-prod/control-plane-hub-kubeconfig missing keys: [(the Secret does not exist)]
//
// while the same command with --skip-preflight minted it correctly.
//
// The fix exempts only what forge actually provisions, so the tests below come
// in pairs: the minted declaration must pass, and every Secret forge does NOT
// create must still BLOCK.

// mintedKubeconfigManifest is a workload mounting a kubeconfig Secret the same
// deploy mints — the control-plane hub-edge shape, reduced to the parts the
// preflight reads.
const mintedKubeconfigManifest = `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: workspace-proxy
spec:
  template:
    spec:
      containers:
        - name: proxy
          image: ghcr.io/acme/proxy:v1
          volumeMounts:
            - name: hub-kubeconfig
              mountPath: /etc/hub
      volumes:
        - name: hub-kubeconfig
          secret:
            secretName: control-plane-hub-kubeconfig
`

// TestPreflight_MintedKubeconfigSecretDoesNotBlock is the regression test for
// the defect. The Secret does NOT exist on the live target — the getter's table
// is empty — and the deploy must proceed anyway, because this deploy mints it.
//
// Before the fix this failed with the whole-Secret marker.
func TestPreflight_MintedKubeconfigSecretDoesNotBlock(t *testing.T) {
	result, err := runPreflightChecks(t.Context(), PreflightOpts{
		Manifests: mintedKubeconfigManifest,
		Namespace: "control-plane-prod",
		Context:   "gke_p_us-central1_prod",
		// Nothing exists on the target yet — a first deploy.
		Secrets: fakeSecretGetter{secrets: map[string]map[string]struct{}{}},
		ProvisionedByDeploy: []ProvisionedSecret{{
			Name:      "control-plane-hub-kubeconfig",
			Namespace: "control-plane-prod",
			By:        SupplyKubeconfigSecret,
		}},
	})
	if err != nil {
		t.Fatalf("runPreflightChecks: %v", err)
	}
	if got, blocked := result.MissingSecretKeys["control-plane-prod/control-plane-hub-kubeconfig"]; blocked {
		t.Fatalf("a Secret this deploy MINTS was reported missing: %v\n"+
			"the preflight is blocking on its own output — the deploy it refuses is the one that creates it", got)
	}
	if !result.OK() {
		t.Errorf("expected a clean preflight, got %s", FormatPreflightReport(result))
	}
}

// TestPreflight_UnmintedKubeconfigSecretStillBlocks is the negative case that
// keeps the exemption honest. A forge.KubeconfigSecret WITHOUT a
// service_account COPIES an existing kubeconfig on the `env up` path — the
// deploy does NOT mint it — so its absence is a real missing dependency and
// must still block.
//
// The projection layer is what decides this (a declaration with no
// service_account never enters ProvisionedByDeploy); here we pin that an
// un-exempted Secret is still checked exactly as before.
func TestPreflight_UnmintedKubeconfigSecretStillBlocks(t *testing.T) {
	result, err := runPreflightChecks(t.Context(), PreflightOpts{
		Manifests: mintedKubeconfigManifest,
		Namespace: "control-plane-prod",
		Context:   "gke_p_us-central1_prod",
		Secrets:   fakeSecretGetter{secrets: map[string]map[string]struct{}{}},
		// Not minted by this deploy => not exempt.
		ProvisionedByDeploy: nil,
	})
	if err != nil {
		t.Fatalf("runPreflightChecks: %v", err)
	}
	missing := result.MissingSecretKeys["control-plane-prod/control-plane-hub-kubeconfig"]
	if len(missing) == 0 {
		t.Fatal("a Secret forge does NOT create must still block when absent — " +
			"the exemption must not widen to every kubeconfig Secret")
	}
	if missing[0] != wholeSecretMarker {
		t.Errorf("expected the whole-Secret marker, got %v", missing)
	}
}

// TestPreflight_ExemptionIsScopedToTheNamedSecret pins that exempting one
// Secret does not exempt the others in the same bundle. A deploy that mints
// the hub kubeconfig must still be refused for an unrelated Secret nobody
// provisions — otherwise the fix trades one silent rollout failure for
// another.
func TestPreflight_ExemptionIsScopedToTheNamedSecret(t *testing.T) {
	manifests := mintedKubeconfigManifest + `
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: admin-server
spec:
  template:
    spec:
      containers:
        - name: api
          image: ghcr.io/acme/admin:v1
          env:
            - name: STRIPE_SECRET_KEY
              valueFrom:
                secretKeyRef:
                  name: admin-server-secrets
                  key: stripe_secret_key
`
	result, err := runPreflightChecks(t.Context(), PreflightOpts{
		Manifests: manifests,
		Namespace: "control-plane-prod",
		Context:   "gke_p_us-central1_prod",
		Secrets:   fakeSecretGetter{secrets: map[string]map[string]struct{}{}},
		ProvisionedByDeploy: []ProvisionedSecret{{
			Name: "control-plane-hub-kubeconfig", By: SupplyKubeconfigSecret,
		}},
	})
	if err != nil {
		t.Fatalf("runPreflightChecks: %v", err)
	}
	if _, blocked := result.MissingSecretKeys["control-plane-prod/control-plane-hub-kubeconfig"]; blocked {
		t.Error("the minted Secret should be exempt")
	}
	if len(result.MissingSecretKeys["control-plane-prod/admin-server-secrets"]) == 0 {
		t.Fatal("an unrelated absent Secret must still block — the exemption leaked beyond the Secret it names")
	}
	if report := FormatPreflightReport(result); !strings.Contains(report, "admin-server-secrets") {
		t.Errorf("report should name the genuinely-missing Secret, got:\n%s", report)
	}
}

// TestPreflight_MintedSecretMissingKeyIsNotCheckedEither pins the whole-Secret
// scope of the exemption. forge mints the Secret AND its key in one step, so a
// key-level check against a Secret that does not exist yet can only report the
// absence the exemption already covers. Checking it would re-introduce the
// block through a different field.
func TestPreflight_MintedSecretMissingKeyIsNotCheckedEither(t *testing.T) {
	manifests := `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: proxy
spec:
  template:
    spec:
      containers:
        - name: proxy
          image: ghcr.io/acme/proxy:v1
          env:
            - name: KUBECONFIG_BLOB
              valueFrom:
                secretKeyRef:
                  name: control-plane-hub-kubeconfig
                  key: kubeconfig
`
	result, err := runPreflightChecks(t.Context(), PreflightOpts{
		Manifests: manifests,
		Namespace: "control-plane-prod",
		Context:   "gke_p_us-central1_prod",
		// Present but WITHOUT the referenced key — the shape a key-level check
		// would otherwise flag.
		Secrets: fakeSecretGetter{secrets: map[string]map[string]struct{}{
			"control-plane-hub-kubeconfig": {"stale": struct{}{}},
		}},
		ProvisionedByDeploy: []ProvisionedSecret{{
			Name: "control-plane-hub-kubeconfig", By: SupplyKubeconfigSecret,
		}},
	})
	if err != nil {
		t.Fatalf("runPreflightChecks: %v", err)
	}
	if got, blocked := result.MissingSecretKeys["control-plane-prod/control-plane-hub-kubeconfig"]; blocked {
		t.Fatalf("the mint writes the Secret and its key together; the deploy must not be "+
			"blocked on the key it is about to write, got %v", got)
	}
}

// TestWithoutProvisionedSecrets_EmptyExemptionIsIdentity pins that the common
// case — an env that mints nothing — neither filters nor reallocates, so the
// gate's behaviour is bit-for-bit what it was before this seam existed.
func TestWithoutProvisionedSecrets_EmptyExemptionIsIdentity(t *testing.T) {
	refs := map[ResourceRef]map[string]struct{}{
		{Namespace: "app", Name: "a"}: {"k": struct{}{}},
		{Namespace: "app", Name: "b"}: {},
	}
	for _, provisioned := range [][]ProvisionedSecret{nil, {}, {{Name: "   "}}} {
		got := withoutProvisionedSecrets(refs, provisioned, "app")
		if len(got) != len(refs) {
			t.Errorf("provisioned=%v: exempted something, want identity: %v", provisioned, got)
		}
	}
}
