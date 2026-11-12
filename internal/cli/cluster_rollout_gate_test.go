package cli

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/cluster"
	"github.com/reliant-labs/forge/internal/deploytarget"
	"github.com/reliant-labs/forge/pkg/release"
)

// Tests for the cluster-rollout gate (cluster_rollout_gate.go): what a mixed
// env's local stage reports on its promotion, and that the report lands before
// the hosted stage that waits on a control plane holding for it.

func TestClusterRolloutGate_StatusFollowsTheRolloutPolicy(t *testing.T) {
	started := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	finished := started.Add(3 * time.Minute)
	for _, tc := range []struct {
		name       string
		policy     cluster.RolloutPolicy
		err        error
		want       release.GateStatus
		wantWaited bool
		wantMode   string
	}{
		{"the default policy waits and passes", cluster.RolloutPolicy{}, nil, release.GateStatusPassed, true, "wait"},
		{"wait passes", cluster.RolloutPolicy{Mode: cluster.RolloutWait}, nil, release.GateStatusPassed, true, "wait"},
		// --no-wait applied and waited for nothing: it saw nothing roll out.
		{"no-wait is skipped, never passed", cluster.RolloutPolicy{Mode: cluster.RolloutSkip}, nil, release.GateStatusSkipped, false, "skip"},
		// warn waits but reports a workload that never came up without failing.
		{"warn is skipped, never passed", cluster.RolloutPolicy{Mode: cluster.RolloutWarn}, nil, release.GateStatusSkipped, true, "warn"},
		{"a failed stage fails", cluster.RolloutPolicy{}, errors.New("rollout failed: api did not become ready"), release.GateStatusFailed, true, "wait"},
		{"a failed no-wait apply fails", cluster.RolloutPolicy{Mode: cluster.RolloutSkip}, errors.New("kubectl apply failed"), release.GateStatusFailed, false, "skip"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := clusterRolloutGate(started, finished, tc.err, tc.policy, "sha256:bundle")
			if g.Name != clusterRolloutGateName || g.Status != tc.want {
				t.Fatalf("gate %s/%s, want %s/%s", g.Name, g.Status, clusterRolloutGateName, tc.want)
			}
			if err := g.Validate(); err != nil {
				t.Fatalf("the gate could not be recorded: %v", err)
			}
			if g.Details[clusterRolloutWaited] != tc.wantWaited || g.Details[clusterRolloutMode] != tc.wantMode {
				t.Errorf("details %v: want waited=%v rollout_mode=%s", g.Details, tc.wantWaited, tc.wantMode)
			}
			if g.Details[applyGateBundleDigest] != "sha256:bundle" {
				t.Errorf("details %v do not bind the gate to the bundle it applied", g.Details)
			}
			if !reflect.DeepEqual(g.Details[clusterRolloutKinds], cluster.AwaitedKinds) {
				t.Errorf("kinds = %v, want what the rollout wait judges: %v", g.Details[clusterRolloutKinds], cluster.AwaitedKinds)
			}
			if tc.err != nil && g.Summary != tc.err.Error() {
				t.Errorf("summary %q does not say what failed (%v)", g.Summary, tc.err)
			}
			if g.StartedAt == nil || !g.StartedAt.Equal(started) || g.FinishedAt == nil || !g.FinishedAt.Equal(finished) {
				t.Errorf("window %v..%v, want %v..%v", g.StartedAt, g.FinishedAt, started, finished)
			}
		})
	}
}

// A scoped apply shipped part of the bundle, so its gate names none: a control
// plane that binds activation to the digest keeps holding.
func TestClusterRolloutGate_ScopedApplyNamesNoBundle(t *testing.T) {
	at := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	g := clusterRolloutGate(at, at, nil, cluster.RolloutPolicy{}, "")
	if _, ok := g.Details[applyGateBundleDigest]; ok {
		t.Fatalf("details %v name a bundle for an apply that shipped none whole", g.Details)
	}
	if got := appliedBundleDigest("prod", "v2", deployOptions{targets: []string{"api"}}); got != "" {
		t.Fatalf("a --target apply's digest = %q, want none", got)
	}
	if got := appliedBundleDigest("prod", "v2", deployOptions{frontendsOnly: true}); got != "" {
		t.Fatalf("a --frontends-only apply's digest = %q, want none", got)
	}
}

// THE ORDER. The local stage's verdict is handed over before the next stage
// runs: the hosted stage waits on a convergence the control plane holds until
// it reads the gate, so a gate recorded after it would hold that wait out.
func TestRunDeployStage_LocalVerdictLandsBeforeTheNextStage(t *testing.T) {
	var events []string
	opts := deployOptions{
		rollout: cluster.RolloutPolicy{Mode: cluster.RolloutSkip},
		onLocalStage: func(p cluster.RolloutPolicy, err error) {
			events = append(events, fmt.Sprintf("gate(%s,%v)", p.Mode, err))
		},
	}
	run := map[deployStage]func() error{
		stageLocal:     func() error { events = append(events, "local"); return nil },
		stageHosted:    func() error { events = append(events, "hosted"); return nil },
		stageFrontends: func() error { events = append(events, "frontends"); return nil },
	}
	for _, s := range defaultStageOrder {
		if err := runDeployStage(s, run[s], opts, false); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := strings.Join(events, " "), "local gate(skip,<nil>) hosted frontends"; got != want {
		t.Fatalf("events %q, want %q", got, want)
	}

	// A failed local stage reports its failure, and a dry run reports nothing.
	events = nil
	boom := errors.New("boom")
	if err := runDeployStage(stageLocal, func() error { return boom }, opts, false); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the stage's", err)
	}
	if err := runDeployStage(stageLocal, func() error { return nil }, opts, true); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(events, " "); got != "gate(skip,boom)" {
		t.Fatalf("events %q: want the failure reported once and the dry run not at all", got)
	}
}

// stagedClientDeploy stands in for runDeploy on a mixed env: it runs the local
// stage (handing its verdict to the hook, as runDeployStage does), then the
// hosted stage, which records what the control plane's gate store holds at
// that moment.
type stagedClientDeploy struct {
	localErr  error
	beforeErr error // a failure before the local stage runs
	hostedErr error
	// atHosted is the promotion's cluster-rollout gates as the hosted stage
	// would see them.
	atHosted []release.Gate
	opts     deployOptions
}

func (s *stagedClientDeploy) install(t *testing.T, gates *gateStore, store *hostedStore) {
	t.Helper()
	prev := runPromoteClientDeploy
	promoteClientDeployStubbed = true
	t.Cleanup(func() { promoteClientDeployStubbed = false; runPromoteClientDeploy = prev })
	runPromoteClientDeploy = func(ctx context.Context, env string, o deployOptions) error {
		if o.preflightOnly {
			return nil
		}
		s.opts = o
		if s.beforeErr != nil {
			return s.beforeErr
		}
		if err := runDeployStage(stageLocal, func() error { return s.localErr }, o, o.dryRun); err != nil {
			return err
		}
		cur, _, err := store.Current(ctx, env)
		if err != nil {
			return err
		}
		all, err := gates.listGates(ctx, cur.ID)
		if err != nil {
			return err
		}
		s.atHosted = clusterRolloutGates(all)
		return s.hostedErr
	}
}

func clusterRolloutGates(gates []release.Gate) []release.Gate {
	var out []release.Gate
	for _, g := range gates {
		if g.Name == clusterRolloutGateName {
			out = append(out, g)
		}
	}
	return out
}

// mixedDeploy runs `forge env deploy prod v2` against a mixed ledger with the
// staged apply, and returns the promotion's cluster-rollout gates afterwards.
func mixedDeploy(t *testing.T, apply *stagedClientDeploy, ledger func(*hostedStore) envLedger, follow *promoteFollowOptions) []release.Gate {
	t.Helper()
	var wait capturedWait
	wait.install(t)
	fake, store := hostedPromoteFixture(t, "v1")
	gstore := newGateTestStore(t, fake)
	useGateBackend(t, gateBackend{hosted: true, store: gstore, bindings: store})
	apply.install(t, gstore, store)
	_, _ = runHostedPromote(t, store, "v2", promoteOptions{Ledger: ledger(store), Follow: follow})
	cur, _, err := store.Current(context.Background(), "prod")
	if err != nil {
		t.Fatal(err)
	}
	all, err := gstore.listGates(context.Background(), cur.ID)
	if err != nil {
		t.Fatalf("list gates: %v", err)
	}
	return clusterRolloutGates(all)
}

// THE HEADLINE: on a mixed env the cluster-rollout gate is on the promotion by
// the time the hosted stage runs, and says passed for a waited rollout.
func TestDeployRelease_MixedEnvRecordsClusterRolloutBeforeTheHostedStage(t *testing.T) {
	var apply stagedClientDeploy
	after := mixedDeploy(t, &apply, mixedLedger, waitByDefault())
	if len(apply.atHosted) != 1 || apply.atHosted[0].Status != release.GateStatusPassed {
		t.Fatalf("the hosted stage saw cluster-rollout gates %+v; want one passed, recorded before it", apply.atHosted)
	}
	if len(after) != 1 {
		t.Fatalf("the promotion carries %d cluster-rollout gates after the deploy, want exactly 1: %+v", len(after), after)
	}
	g := after[0]
	if g.RunID == "" {
		t.Error("the gate carries no run id: a re-apply of this promotion would be handed this verdict back")
	}
	if want := appliedBundleDigest("prod", "v2", apply.opts); gateDetail(g, applyGateBundleDigest) != want {
		t.Errorf("bundle_digest = %q, want the apply's %q", gateDetail(g, applyGateBundleDigest), want)
	}
}

// --no-wait applies without waiting: the gate says skipped, so a control plane
// waiting for passed keeps the hosted part held.
func TestDeployRelease_MixedEnvNoWaitRecordsSkipped(t *testing.T) {
	var apply stagedClientDeploy
	after := mixedDeploy(t, &apply, mixedLedger, &promoteFollowOptions{NoWait: true})
	if len(after) != 1 || after[0].Status != release.GateStatusSkipped {
		t.Fatalf("cluster-rollout gates %+v; want one skipped for --no-wait", after)
	}
}

func TestDeployRelease_MixedEnvFailedRolloutRecordsFailed(t *testing.T) {
	for _, tc := range []struct {
		name  string
		apply stagedClientDeploy
	}{
		{"the local stage failed", stagedClientDeploy{localErr: errors.New("rollout failed: api did not become ready")}},
		{"the deploy failed before the local stage", stagedClientDeploy{beforeErr: errors.New("render deploy/kcl/prod/main.k: boom")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			apply := tc.apply
			after := mixedDeploy(t, &apply, mixedLedger, waitByDefault())
			if len(after) != 1 || after[0].Status != release.GateStatusFailed {
				t.Fatalf("cluster-rollout gates %+v; want one failed", after)
			}
		})
	}
}

// A failure AFTER the local stage reported (the hosted stage itself) does not
// overwrite or duplicate the local verdict: the cluster half did roll out.
func TestDeployRelease_MixedEnvHostedFailureKeepsTheClusterVerdict(t *testing.T) {
	apply := stagedClientDeploy{hostedErr: errors.New("hosted publish failed")}
	after := mixedDeploy(t, &apply, mixedLedger, waitByDefault())
	if len(after) != 1 || after[0].Status != release.GateStatusPassed {
		t.Fatalf("cluster-rollout gates %+v; want the one passed local verdict", after)
	}
}

// A publish still applying is not a failed rollout: it records nothing extra.
func TestDeployRelease_StillApplyingRecordsNoFailedClusterRollout(t *testing.T) {
	pending := &deploytarget.ApplyPendingError{Env: "prod", Bundle: "sha256:new", Waited: 10 * time.Minute}
	apply := stagedClientDeploy{beforeErr: fmt.Errorf("deploy hosted: %w", pending)}
	if after := mixedDeploy(t, &apply, mixedLedger, waitByDefault()); len(after) != 0 {
		t.Fatalf("a still-applying deploy recorded cluster-rollout gates %+v; want none", after)
	}
}

// Only a mixed env's control plane holds on the gate. A ledger-only env (no
// hosted tier) records none.
func TestDeployRelease_LedgerOnlyEnvRecordsNoClusterRollout(t *testing.T) {
	var apply stagedClientDeploy
	ledgerOnly := func(store *hostedStore) envLedger {
		return envLedger{Bindings: store, Releases: store, Hosted: true, Mixed: true}
	}
	if after := mixedDeploy(t, &apply, ledgerOnly, waitByDefault()); len(after) != 0 {
		t.Fatalf("a ledger-only env recorded cluster-rollout gates %+v; nothing reads them", after)
	}
}
