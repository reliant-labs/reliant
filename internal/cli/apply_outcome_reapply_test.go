package cli

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/release"
)

// A RE-APPLY'S VERDICT IS RECORDED. control-plane prod's v1.7.89-rc.2 was
// applied three times on 2026-11-03; the first failed and the control plane
// kept that verdict through the other two, because every apply gate was
// recorded under the same idempotency key (promotion, "apply", "") and the
// server handed the old row back. A promotion whose apply later succeeded
// read "NOT APPLIED" forever.
func TestRecordApplyOutcome_AReapplyRecordsItsOwnVerdict(t *testing.T) {
	fake, store := hostedPromoteFixture(t, "v1")
	useGateBackend(t, gateBackend{hosted: true, store: newGateTestStore(t, fake), bindings: store})
	ctx := context.Background()
	current, _, err := store.Current(ctx, "prod")
	if err != nil {
		t.Fatal(err)
	}
	plan := promotePlan{Recorded: &current}
	ledger := envLedger{Bindings: store, Releases: store, Hosted: true, Mixed: true}

	recordApplyOutcome(ctx, "prod", plan, ledger, time.Now(), errors.New("rollout failed: delivery-signer did not become ready"), promoteFollowOptions{})
	recordApplyOutcome(ctx, "prod", plan, ledger, time.Now(), nil, promoteFollowOptions{})

	gates, err := mustGates(t, current.ID)
	if err != nil {
		t.Fatal(err)
	}
	var applies int
	for _, g := range gates {
		if g.Name == applyGateName {
			applies++
		}
	}
	if applies != 2 {
		t.Fatalf("two applies recorded %d apply gate(s), want one each: %+v", applies, gates)
	}
	got := applyOf(t.TempDir(), "prod", release.Promotion{ID: current.ID, RecordedGates: gates})
	if got.Status() != applyStatusPassed {
		t.Fatalf("the latest apply succeeded, yet the promotion reads %s (%q)", got.Status(), got.Summary)
	}
}

// One apply's gate is keyed by the deploy attempt it ran under, so a retry of
// the same record call dedupes while a second apply does not.
func TestApplyGateRunID_IsTheAttemptOrFresh(t *testing.T) {
	a := &deployAttempt{id: "att_0123", opened: true}
	ctx := context.WithValue(context.Background(), deployAttemptCtxKey{}, a)
	if got := applyGateRunID(ctx); got != "att_0123" {
		t.Errorf("an opened attempt's apply gate is keyed %q, want the attempt id", got)
	}
	first, second := applyGateRunID(context.Background()), applyGateRunID(context.Background())
	if first == "" || first == second || !strings.HasPrefix(first, "apply_") {
		t.Errorf("with no attempt each apply needs its own key, got %q and %q", first, second)
	}
	unopened := context.WithValue(context.Background(), deployAttemptCtxKey{}, &deployAttempt{id: "att_x"})
	if got := applyGateRunID(unopened); got == "att_x" {
		t.Error("an attempt the control plane never opened must not key the gate")
	}
}

// A PARTLY APPLIED RECORD NAMES WHAT FAILED. The recorded summary is bounded,
// and a cluster stage label lists every workload it shipped: with the stages
// ahead of the error, prod's records were cut inside that list and never said
// which object failed.
func TestApplyOutcomeGate_PartlyAppliedNamesTheFailingObject(t *testing.T) {
	l := &shipLog{}
	workloads := strings.Repeat("admin-server, control-plane-workers, workspace-proxy, reliant-api-server, ", 6)
	_ = l.run([]string{"[k8s-cluster] cluster=gke_reliant-labs-475814_us-central1-a_prod-daemon-v2 ns=observability: "}, func() error { return nil })
	boom := errors.New("rollout failed: delivery-signer (gke_prod/control-plane-prod) did not become ready")
	_ = l.run([]string{"[k8s-cluster] cluster=gke_reliant-labs-475814_us-central1_prod ns=control-plane-prod: " + workloads}, func() error { return boom })

	evidence := l.annotate(boom)
	if !errors.Is(evidence, boom) {
		t.Fatal("the annotation must wrap the apply's error")
	}
	g := applyOutcomeGate(time.Now(), time.Now(), evidence, "", "")
	if !strings.HasPrefix(g.Summary, "PARTLY APPLIED") {
		t.Errorf("summary %q lost its PARTLY APPLIED marker", g.Summary)
	}
	// What every reader shows of it (env status, the next plan) is shorter
	// still than what the ledger keeps.
	if shown := oneLine(g.Summary, 200); !strings.Contains(shown, "delivery-signer") {
		t.Errorf("the recorded failure does not name the object that failed:\n%s", shown)
	}
}
