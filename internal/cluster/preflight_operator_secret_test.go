package cluster

import (
	"context"
	"strings"
	"testing"
)

// operatorSecretStream is control-plane prod's hosted-actions shape: a
// cert-manager Certificate issues the Secret a Deployment in the same release
// mounts, and an ExternalSecret delivers another. Neither Secret exists before
// the apply that creates its provider.
const operatorSecretStream = `apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: hosted-actions-controller
  namespace: control-plane-prod
spec:
  secretName: hosted-actions-controller-tls
---
apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata:
  name: worker-dsn
  namespace: control-plane-prod
spec:
  target:
    name: worker-database
---
apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata:
  name: seal-key
  namespace: control-plane-prod
---
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: elsewhere
  namespace: other
spec:
  secretName: other-namespace-tls
---
apiVersion: example.com/v1
kind: Certificate
metadata:
  name: not-cert-manager
  namespace: control-plane-prod
spec:
  secretName: lookalike-tls
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: reliant-temporal-worker
  namespace: control-plane-prod
spec:
  template:
    spec:
      containers:
        - name: worker
          env:
            - name: DSN
              valueFrom:
                secretKeyRef:
                  name: worker-database
                  key: dsn
            - name: SEAL
              valueFrom:
                secretKeyRef:
                  name: seal-key
                  key: key
      volumes:
        - name: hosted-actions-tls
          secret:
            secretName: hosted-actions-controller-tls
            items:
              - key: ca.crt
                path: ca.crt
        - name: other
          secret:
            secretName: other-namespace-tls
        - name: lookalike
          secret:
            secretName: lookalike-tls
`

// The live preflight's regression: control-plane v1.7.57's plan was refused
// with "Secret control-plane-prod/hosted-actions-controller-tls missing keys:
// [(the Secret does not exist)]" for a Secret the release's own Certificate
// issues. A Certificate's secretName and an ExternalSecret's target are
// provided by the release, in the provider's namespace only; a lookalike kind
// from another API group provides nothing.
//
// Mutation that fails it: drop the operatorProvidedSecret branch in
// dropSelfProvided.
func TestPreflight_OperatorIssuedSecretsAreProvidedByTheRelease(t *testing.T) {
	const hub = "gke_prod"
	secrets, asked := newPlacedSecretGetter(map[string]map[string]map[string]struct{}{hub: {}})
	// The two Secrets nothing provides fail the report; that error is the
	// second half of this test, asserted below through the result.
	res, _ := PreflightReport(context.Background(), PreflightOpts{
		Manifests: operatorSecretStream,
		Context:   hub,
		Namespace: "control-plane-prod",
		Secrets:   secrets,
	})
	for _, provided := range []string{"hosted-actions-controller-tls", "worker-database", "seal-key"} {
		if _, ok := res.MissingSecretKeys["control-plane-prod/"+provided]; ok {
			t.Errorf("%s is issued by an operator object in this release and must not be required; missing=%v", provided, res.MissingSecretKeys)
		}
		for _, a := range asked() {
			if strings.HasSuffix(a, "control-plane-prod/"+provided) {
				t.Errorf("%s is provided in-stream and must not be looked up; asked %v", provided, asked())
			}
		}
	}
	// What the release does NOT provide is still checked.
	for _, missing := range []string{"other-namespace-tls", "lookalike-tls"} {
		if _, ok := res.MissingSecretKeys["control-plane-prod/"+missing]; !ok {
			t.Errorf("%s is not provided in control-plane-prod and must still be reported missing; missing=%v", missing, res.MissingSecretKeys)
		}
	}
}
