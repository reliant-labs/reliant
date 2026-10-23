package cli

import (
	"context"
	"strings"
	"testing"
)

// prod on 2026-10-07: the hub created the OCIRepository, and both
// control-plane Kustomizations were Ready=False: Forbidden. A hub-converged
// deploy must not record a promotion nothing can apply; it refuses with the
// hub's own words, and records nothing.
func TestDeployRelease_HubConvergedRefusesWhileTheHubIsFailing(t *testing.T) {
	var wait capturedWait
	wait.install(t)
	fake, store := hostedPromoteFixture(t, "v1")
	fake.convergences = map[string][]map[string]any{"env-prod-uuid": {{
		"cluster": "gke_reliant-labs-475814_us-central1_prod", "state": "failed",
		"reason":  "ReconciliationFailed",
		"message": `Deployment/control-plane-prod/admin-api dry-run failed: deployments.apps "admin-api" is forbidden: User "system:serviceaccount:org-acme:reconciler" cannot patch resource "deployments"`,
	}}}

	_, err := runHostedPromote(t, store, "v2", promoteOptions{
		Ledger: envLedger{Bindings: store, Releases: store, Hosted: true, Units: allHubUnits()},
		Follow: waitByDefault(),
	})
	if err == nil || !strings.Contains(err.Error(), "FAILING") || !strings.Contains(err.Error(), "is forbidden") {
		t.Fatalf("a hub-converged deploy against a failing hub must be refused with the hub's error, got %v", err)
	}
	if cur, _, _ := store.Current(context.Background(), "prod"); cur.Release != "v1" {
		t.Fatalf("the refused deploy recorded a promotion: prod is on %s", cur.Release)
	}
	if len(wait.calls) != 0 {
		t.Errorf("a refused deploy waited %d time(s)", len(wait.calls))
	}
}

// The readings that must NOT refuse: a Kustomization that has never had an
// artifact (this promotion is what gives it one), a converged hub, and the
// skip flag.
func TestDeployRelease_HubConvergedRecordsWhenTheHubCanApply(t *testing.T) {
	for _, tc := range []struct {
		name string
		rows []map[string]any
		skip bool
	}{
		{"artifact not yet pulled", []map[string]any{{"cluster": "c", "state": "failed", "reason": "ArtifactFailed", "message": "no artifact"}}, false},
		{"converged", []map[string]any{{"cluster": "c", "state": "converged", "reason": "ReconciliationSucceeded"}}, false},
		{"failing, --skip-hub-check", []map[string]any{{"cluster": "c", "state": "failed", "reason": "ReconciliationFailed", "message": "forbidden"}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var wait capturedWait
			wait.install(t)
			fake, store := hostedPromoteFixture(t, "v1")
			fake.convergences = map[string][]map[string]any{"env-prod-uuid": tc.rows}
			follow := waitByDefault()
			follow.skipHubCheck = tc.skip
			if _, err := runHostedPromote(t, store, "v2", promoteOptions{
				Ledger: envLedger{Bindings: store, Releases: store, Hosted: true, Units: allHubUnits()},
				Follow: follow,
			}); err != nil {
				t.Fatalf("deploy: %v", err)
			}
			if cur, _, _ := store.Current(context.Background(), "prod"); cur.Release != "v2" {
				t.Fatalf("prod is on %s, want v2", cur.Release)
			}
		})
	}
}

// --explain names the path, so nobody has to infer it from a verdict line.
func TestDeployApplyPath(t *testing.T) {
	for _, tc := range []struct {
		ledger envLedger
		want   string
	}{
		{envLedger{Hosted: true, Units: allHubUnits()}, "hub converge"},
		{envLedger{Hosted: true, Mixed: true, HostedTiers: []hostedTier{{Name: "api", Kind: hostedTierWorkload}}}, "mixed"},
		{envLedger{Hosted: true, Mixed: true}, "ledger only"},
		{envLedger{Hosted: true}, "hosted"},
		{envLedger{}, "client apply"},
	} {
		if got := deployApplyPath(tc.ledger); !strings.HasPrefix(got, tc.want) {
			t.Errorf("deployApplyPath(%+v) = %q, want %q…", tc.ledger, got, tc.want)
		}
	}
}
