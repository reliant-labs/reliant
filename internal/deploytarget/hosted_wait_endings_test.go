package deploytarget

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/cluster"
)

// The two ways a hosted wait ends without success, and the apply-progress
// lines it prints on the way, were extracted from HostedProvider.wait. The
// end-to-end deploy tests cover their common paths; these pin each branch
// directly: the observations every workload gets, the error's wording, and
// --rollout warn downgrading the error to a printed warning.

type recordedObservation struct {
	state cluster.RolloutState
	err   error
}

func observingProvider(seen map[string]recordedObservation) HostedProvider {
	return HostedProvider{OnRollout: func(o cluster.RolloutObservation) {
		seen[o.Name] = recordedObservation{o.State, o.Err}
	}}
}

var waitEndingsPlan = []hostedPlanItem{{Name: "api"}, {Name: "web"}}

func TestApplyNotLanded(t *testing.T) {
	t.Run("not applied yet is still applying, not a timeout or a failure", func(t *testing.T) {
		seen := map[string]recordedObservation{}
		err := observingProvider(seen).applyNotLanded("prod", "promo-1", newBundle, bundleApplyState{known: true, current: oldBundle}, 10*time.Minute, waitEndingsPlan, cluster.RolloutWait)
		var pending *ApplyPendingError
		if !errors.As(err, &pending) || pending.Bundle != newBundle || pending.Current != oldBundle || pending.Waited != 10*time.Minute {
			t.Fatalf("err = %#v, want an *ApplyPendingError naming both bundles and the wait", err)
		}
		if !errors.Is(err, errBundleNotApplied) {
			t.Errorf("a pending apply must still satisfy errors.Is(errBundleNotApplied): %v", err)
		}
		msg := err.Error()
		if strings.Contains(msg, "TIMED OUT") || strings.Contains(msg, "fail ") ||
			!strings.Contains(msg, "still applying") || !strings.Contains(msg, "nothing has failed") ||
			!strings.Contains(msg, shortDigest(oldBundle)) || !strings.Contains(msg, "forge env status prod --wait --promotion promo-1") {
			t.Fatalf("err = %q, want a still-applying message with the current revision and the watch command", msg)
		}
		for _, item := range waitEndingsPlan {
			if o := seen[item.Name]; o.state != cluster.RolloutStateNotWaited || !errors.Is(o.err, errBundleNotApplied) {
				t.Errorf("%s observed %+v, want NOT WAITED with the pending apply", item.Name, o)
			}
		}
	})

	t.Run("a failed apply reports the reconciler's reason, not not-yet-applied", func(t *testing.T) {
		seen := map[string]recordedObservation{}
		state := bundleApplyState{known: true, current: oldBundle, failure: "BuildFailed: kustomize build failed"}
		err := observingProvider(seen).applyNotLanded("prod", "promo-1", newBundle, state, time.Minute, waitEndingsPlan, cluster.RolloutWait)
		var pending *ApplyPendingError
		if errors.As(err, &pending) {
			t.Errorf("a failed apply was reported as still applying: %v", err)
		}
		if err == nil || !strings.Contains(err.Error(), "failed to apply this release") || !strings.Contains(err.Error(), "kustomize build failed") {
			t.Fatalf("err = %v, want the reconciler's failure", err)
		}
		if strings.Contains(err.Error(), "has not applied this release yet") {
			t.Errorf("a failed apply was reported as not-yet-applied: %v", err)
		}
		if o := seen["api"]; o.state != cluster.RolloutStateTimedOut || o.err == nil || !strings.Contains(o.err.Error(), "kustomize build failed") {
			t.Errorf("api observed %+v, want TIMED OUT carrying the failure", o)
		}
	})

	t.Run("--rollout warn prints the error and does not fail", func(t *testing.T) {
		var err error
		out := captureStdout(t, func() {
			err = observingProvider(map[string]recordedObservation{}).applyNotLanded("prod", "", newBundle, bundleApplyState{known: true}, time.Minute, waitEndingsPlan, cluster.RolloutWarn)
		})
		if err != nil {
			t.Fatalf("err = %v, want nil under --rollout warn", err)
		}
		if !strings.Contains(out, "Warning:") || !strings.Contains(out, "still applying") || !strings.Contains(out, "currently none") {
			t.Errorf("warning not printed:\n%s", out)
		}
	})
}

func TestReadinessTimedOut(t *testing.T) {
	waitPolicy := cluster.RolloutPolicy{Mode: cluster.RolloutWait, Timeout: 5 * time.Minute}

	t.Run("only the workloads the last read named are timed out", func(t *testing.T) {
		seen := map[string]recordedObservation{}
		err := observingProvider(seen).readinessTimedOut("prod", map[string]string{"web": "observed progressing"}, nil, waitEndingsPlan, waitPolicy)
		if err == nil || !strings.Contains(err.Error(), "TIMED OUT after 5m0s") || !strings.Contains(err.Error(), "web: observed progressing") {
			t.Fatalf("err = %v, want TIMED OUT naming web's reason", err)
		}
		if strings.Contains(err.Error(), "api:") {
			t.Errorf("api was ready on the last read and must not be listed: %v", err)
		}
		if seen["api"].state != cluster.RolloutStateReady || seen["web"].state != cluster.RolloutStateTimedOut {
			t.Errorf("observations = %+v, want api ready and web TIMED OUT", seen)
		}
	})

	t.Run("no successful read times every workload out and names the read error", func(t *testing.T) {
		seen := map[string]recordedObservation{}
		err := observingProvider(seen).readinessTimedOut("prod", nil, errors.New("connection refused"), waitEndingsPlan, waitPolicy)
		if err == nil || !strings.Contains(err.Error(), "api: no status reported") || !strings.Contains(err.Error(), "web: no status reported") ||
			!strings.Contains(err.Error(), "last status read failed: connection refused") {
			t.Fatalf("err = %v, want both workloads unreported and the read error", err)
		}
		for _, item := range waitEndingsPlan {
			if seen[item.Name].state != cluster.RolloutStateTimedOut {
				t.Errorf("%s observed %+v, want TIMED OUT", item.Name, seen[item.Name])
			}
		}
	})

	t.Run("--rollout warn prints the error and does not fail", func(t *testing.T) {
		var err error
		out := captureStdout(t, func() {
			err = observingProvider(map[string]recordedObservation{}).readinessTimedOut("prod", nil, nil, waitEndingsPlan,
				cluster.RolloutPolicy{Mode: cluster.RolloutWarn, Timeout: time.Minute})
		})
		if err != nil || !strings.Contains(out, "Warning:") || !strings.Contains(out, "TIMED OUT") {
			t.Fatalf("err = %v, out = %q; want nil and a printed warning", err, out)
		}
	})
}

// heartbeat prints one "still waiting" line per applyHeartbeat while nothing
// changes, with the elapsed time against the budget.
func TestApplyProgressHeartbeat(t *testing.T) {
	var progress applyProgress
	state := bundleApplyState{known: true, current: oldBundle}
	t0 := time.Date(2026, 10, 29, 16, 0, 0, 0, time.UTC)
	out := captureStdout(t, func() {
		for _, at := range []time.Duration{0, 3 * time.Second, 30 * time.Second, 61 * time.Second, 90 * time.Second, 125 * time.Second} {
			progress.heartbeat(t0.Add(at), state, newBundle, HostedApplyBudget)
		}
	})
	if n := strings.Count(out, "still waiting for the platform to apply"); n != 2 {
		t.Fatalf("heartbeat printed %d lines, want 2 (at 61s and 125s):\n%s", n, out)
	}
	if !strings.Contains(out, "1m1s elapsed, waiting up to 10m0s") || !strings.Contains(out, "2m5s elapsed") {
		t.Errorf("heartbeat lacks the elapsed time and budget:\n%s", out)
	}
}

// announce prints each change in the platform's apply once, not once per poll.
func TestApplyProgressAnnouncesEachChangeOnce(t *testing.T) {
	var progress applyProgress
	pending := bundleApplyState{known: true, current: oldBundle}
	failed := bundleApplyState{known: true, current: oldBundle, failure: "BuildFailed: no such file"}

	out := captureStdout(t, func() {
		progress.announce(pending, newBundle)
		progress.announce(pending, newBundle)
		progress.announce(failed, newBundle)
		progress.announce(failed, newBundle)
	})

	if n := strings.Count(out, "waiting for the platform to apply"); n != 1 {
		t.Errorf("revision announced %d times, want once:\n%s", n, out)
	}
	if n := strings.Count(out, "failed: BuildFailed: no such file"); n != 1 {
		t.Errorf("failure announced %d times, want once:\n%s", n, out)
	}
}
