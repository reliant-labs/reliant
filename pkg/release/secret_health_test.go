package release

import (
	"reflect"
	"testing"
)

func TestExternalSecretTreeHealth(t *testing.T) {
	for _, tc := range []struct {
		name, api, sync string
		await           bool
	}{
		{"awaited", "external-secrets.io/v1", "", true},
		{"shadow", "external-secrets.io/v1", SecretSyncShadow, false},
		{"deferred", "external-secrets.io/v1", SecretSyncDeferred, false},
		{"unrelated kind", "example.com/v1", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			secret := treeObj("hub", "base", PhasePre, "ExternalSecret", "credentials")
			secret.APIVersion = tc.api
			secret.SecretSync = tc.sync
			trees, err := PlanTrees([]TreeObject{
				secret, treeObj("hub", "base", PhaseRollout, "Deployment", "api"),
			}, nil)
			if err != nil {
				t.Fatal(err)
			}
			var pre, rollout BundleTree
			for _, tree := range trees {
				if tree.Phase == PhasePre {
					pre = tree
				} else {
					rollout = tree
				}
			}
			wantBudget := MinBudgetSeconds
			var wantHealth []BundleHealthCheck
			if tc.await {
				wantHealth = []BundleHealthCheck{{Kind: "ExternalSecret", Namespace: secret.Namespace, Name: secret.Name}}
				wantBudget = DefaultJobBudgetSeconds + BudgetMarginSeconds
			}
			if !reflect.DeepEqual(pre.Health, wantHealth) || pre.BudgetSeconds != wantBudget {
				t.Fatalf("pre health=%+v budget=%d; want %+v / %d", pre.Health, pre.BudgetSeconds, wantHealth, wantBudget)
			}
			if !reflect.DeepEqual(rollout.DependsOn, []string{pre.Path}) {
				t.Fatalf("consumer rollout lost pre gate: %+v", rollout)
			}
		})
	}
}
