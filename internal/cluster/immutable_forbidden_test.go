package cluster

import (
	"errors"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/release"
)

// prodStorageClassStderr is the stderr `forge env deploy prod v1.7.32` captured
// from GKE, byte for byte: the apiserver's StorageClass validation rejects a
// parameters change as Forbidden, never as `field is immutable`, and the line
// arrives interleaved with the per-object results of the same batch.
const prodStorageClassStderr = `customresourcedefinition.apiextensions.k8s.io/subscriptions.postgresql.cnpg.io serverside-applied
mutatingwebhookconfiguration.admissionregistration.k8s.io/cnpg-mutating-webhook-configuration serverside-applied
The StorageClass "workspace-ssd" is invalid: parameters: Forbidden: updates to parameters are forbidden.
validatingwebhookconfiguration.admissionregistration.k8s.io/cnpg-validating-webhook-configuration serverside-applied
`

const workspaceSSDManifest = `apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: workspace-ssd
parameters:
  type: pd-balanced
  labels: reliant-workspace-disk=true,managed-by=reliant
`

func TestImmutableResources_MatchesStorageClassForbiddenFromProd(t *testing.T) {
	got := immutableResources(prodStorageClassStderr, workspaceSSDManifest)
	if len(got) != 1 {
		t.Fatalf("the prod StorageClass rejection must be recoverable, got %+v", got)
	}
	if got[0].Kind != "StorageClass" || got[0].Name != "workspace-ssd" || got[0].Namespace != "" {
		t.Errorf("target = %+v, want the cluster-scoped StorageClass workspace-ssd", got[0])
	}
}

func TestApplyWithImmutableRecovery_RecreatesStorageClassOnProdForbidden(t *testing.T) {
	applies := 0
	apply := func() (string, string, error) {
		applies++
		if applies == 1 {
			return "", prodStorageClassStderr, errors.New("exit status 1")
		}
		return "storageclass.storage.k8s.io/workspace-ssd serverside-applied\n", "", nil
	}
	var deleted []immutableTarget
	del := func(t immutableTarget) error { deleted = append(deleted, t); return nil }
	if _, err := applyWithImmutableRecovery(workspaceSSDManifest, apply, del, nil); err != nil {
		t.Fatalf("prod's StorageClass rejection must recover, got %v", err)
	}
	if len(deleted) != 1 || deleted[0].Kind != "StorageClass" || deleted[0].Name != "workspace-ssd" {
		t.Fatalf("deleted = %+v, want exactly StorageClass workspace-ssd", deleted)
	}
	if applies != 2 {
		t.Errorf("applies = %d, want the failed apply and one re-apply", applies)
	}
}

// The wider phrase must not widen WHAT may be deleted: every kind outside the
// allowlist is still refused, whichever way the apiserver words the conflict.
func TestImmutableResources_ForbiddenPhraseStaysScopedToTheAllowlist(t *testing.T) {
	for _, kind := range []string{"PersistentVolumeClaim", "PersistentVolume", "StatefulSet", "Namespace", "CustomResourceDefinition"} {
		stderr := `The ` + kind + ` "x" is invalid: spec: Forbidden: updates to spec are forbidden.`
		if got := immutableResources(stderr, ""); len(got) != 0 {
			t.Errorf("%s must never be auto-deleted, got %+v", kind, got)
		}
	}
}

// A Forbidden that is not an immutability statement (RBAC, quota, admission)
// is not recoverable by deleting anything.
func TestImmutableResources_IgnoresUnrelatedForbidden(t *testing.T) {
	for _, stderr := range []string{
		`Error from server (Forbidden): storageclasses.storage.k8s.io "x" is forbidden: User cannot patch`,
		`The StorageClass "x" is invalid: provisioner: Forbidden: provisioner may not be empty`,
	} {
		if got := immutableResources(stderr, ""); len(got) != 0 {
			t.Errorf("not an immutable conflict, got %+v for %q", got, stderr)
		}
	}
}

func TestImmutableRecovery_UsesTheSharedAllowlist(t *testing.T) {
	if !release.RecreatableKind("StorageClass") || release.RecreatableKind("PersistentVolumeClaim") {
		t.Fatal("the allowlist must permit StorageClass and refuse PersistentVolumeClaim")
	}
	if !strings.Contains(prodStorageClassStderr, "workspace-ssd") {
		t.Fatal("fixture lost its resource name")
	}
}
