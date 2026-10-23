package cluster

import (
	"testing"
)

// preflight_control_plane_secret_test.go pins that a forge.ControlPlaneSecret
// is never a precondition of a deploy.
//
// The control plane writes the Secret AFTER the deploy that declares it: it
// reads the declaration from the shape the deploy records. So a first deploy
// finds no Secret, by construction, and must proceed. The tests come in pairs,
// as the minted-kubeconfig ones do: the delivered Secret passes, and a Secret
// nothing delivers still blocks.

const controlPlaneSecretManifest = `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
spec:
  template:
    spec:
      containers:
        - name: api
          image: ghcr.io/acme/api:v1
          volumeMounts:
            - name: forge-otlp-auth
              mountPath: /var/run/secrets/forge/otlp
      volumes:
        - name: forge-otlp-auth
          secret:
            secretName: reliant-otel-auth
            optional: true
`

func TestPreflight_ControlPlaneSecretDoesNotBlockAFirstDeploy(t *testing.T) {
	// The Secret is volume-referenced like any other, and the target has
	// nothing: a first deploy. optional: true already keeps the reference
	// out of the live check; this pins that even a reference the collector
	// does see (a non-optional one) is exempt once it is declared delivered.
	nonOptional := `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
spec:
  template:
    spec:
      containers:
        - name: api
          image: ghcr.io/acme/api:v1
          env:
            - name: TOKEN
              valueFrom:
                secretKeyRef:
                  name: reliant-otel-auth
                  key: token
`
	for name, manifests := range map[string]string{"optional volume": controlPlaneSecretManifest, "required reference": nonOptional} {
		t.Run(name, func(t *testing.T) {
			result, err := runPreflightChecks(t.Context(), PreflightOpts{
				Manifests: manifests,
				Namespace: "control-plane-prod",
				Context:   "gke_p_prod",
				Secrets:   fakeSecretGetter{secrets: map[string]map[string]struct{}{}},
				ProvisionedByDeploy: []ProvisionedSecret{{
					Name: "reliant-otel-auth", Namespace: "control-plane-prod", By: SupplyControlPlaneSecret,
				}},
			})
			if err != nil {
				t.Fatalf("runPreflightChecks: %v", err)
			}
			if got, blocked := result.MissingSecretKeys["control-plane-prod/reliant-otel-auth"]; blocked {
				t.Fatalf("a Secret the control plane delivers blocked a first deploy: %v", got)
			}
			if !result.OK() {
				t.Errorf("want a clean preflight, got:\n%s", FormatPreflightReport(result))
			}
		})
	}
}

// The converse: the same absent Secret, referenced non-optionally, with NOTHING
// declaring it delivered, still blocks. The exemption is the declaration, not
// a general softening of the Secret check.
func TestPreflight_UndeclaredAbsentSecretStillBlocks(t *testing.T) {
	manifests := `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
spec:
  template:
    spec:
      containers:
        - name: api
          image: ghcr.io/acme/api:v1
          env:
            - name: TOKEN
              valueFrom:
                secretKeyRef:
                  name: reliant-otel-auth
                  key: token
`
	result, err := runPreflightChecks(t.Context(), PreflightOpts{
		Manifests: manifests,
		Namespace: "control-plane-prod",
		Context:   "gke_p_prod",
		Secrets:   fakeSecretGetter{secrets: map[string]map[string]struct{}{}},
	})
	if err != nil {
		t.Fatalf("runPreflightChecks: %v", err)
	}
	if _, blocked := result.MissingSecretKeys["control-plane-prod/reliant-otel-auth"]; !blocked {
		t.Fatalf("an absent Secret nothing delivers must still block: %+v", result)
	}
}

// A ControlPlaneSecret is supply for the render-time gate: a workload mounting
// one is not an "undeclared" mount, with no cluster to ask.
func TestPreflight_ControlPlaneSecretIsSupply(t *testing.T) {
	// Non-optional on purpose: the optional volume is already out of the demand set.
	manifests := `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
spec:
  template:
    spec:
      containers:
        - name: api
          image: ghcr.io/acme/api:v1
      volumes:
        - name: otlp
          secret:
            secretName: reliant-otel-auth
`
	supplied := CheckSecretSupply(manifests, []SecretSupply{{Name: "reliant-otel-auth", Namespace: "control-plane-prod", Kind: SupplyControlPlaneSecret}})
	if len(supplied) != 0 {
		t.Errorf("a delivered Secret reported as an undeclared mount: %+v", supplied)
	}
	unsupplied := CheckSecretSupply(manifests, nil)
	if len(unsupplied) != 1 {
		t.Errorf("control: with no supply the mount must be reported, got %+v", unsupplied)
	}
}

// An optional Secret volume is not a schedule-time dependency (kubelet starts
// the pod without it), so it is not collected as one.
func TestCollectManifestRefs_OptionalSecretVolumeIsNotADependency(t *testing.T) {
	refs := CollectManifestRefs(controlPlaneSecretManifest, RefPlacement{})
	if _, ok := byName(refs.Secrets)["reliant-otel-auth"]; ok {
		t.Errorf("an optional Secret volume was collected as a demand: %+v", refs.Secrets)
	}
}
