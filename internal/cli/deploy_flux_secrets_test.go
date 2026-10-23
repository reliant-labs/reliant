package cli

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/release"
)

const secretStream = `# cluster: k3d-a
apiVersion: v1
kind: Secret
metadata:
  name: raw-creds
  namespace: app
stringData:
  token: hunter2-canary
---
# cluster: k3d-a
apiVersion: v1
kind: ConfigMap
metadata:
  name: cfg
  namespace: app
`

// The names a bundle records are what the deploy syncs, and no value is among
// them.
func TestEnvSyncedSecretRefsNamesTheRenderedSecrets(t *testing.T) {
	refs, err := envSyncedSecretRefs(&KCLEntities{}, nil, "app", secretStream)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 || refs[0] != (release.BundleSecretRef{Cluster: "k3d-a", Namespace: "app", Name: "raw-creds"}) {
		t.Fatalf("refs = %+v", refs)
	}
}

// A hub-reconciled env that needs a forge-supplied value is refused, naming
// the command that fills the gap; one with nothing to sync is untouched.
func TestRefuseUnsyncableHostedSecrets(t *testing.T) {
	if err := refuseUnsyncableHostedSecrets("prod", nil); err != nil {
		t.Fatalf("no secrets, no refusal: %v", err)
	}
	err := refuseUnsyncableHostedSecrets("prod", []release.BundleSecretRef{{Namespace: "app", Name: "creds"}})
	if err == nil || !strings.Contains(err.Error(), "forge env secrets sync prod") || !strings.Contains(err.Error(), "app/creds") {
		t.Fatalf("want a refusal naming the secret and `forge env secrets sync prod`, got %v", err)
	}
}
