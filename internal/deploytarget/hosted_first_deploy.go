package deploytarget

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/reliant-labs/forge/internal/cluster"
)

// HostedFirstDeployFailureGrace is how long a workload of a FIRST deploy may
// keep reporting the same failure before the deploy stops waiting and fails.
//
// WHY A FIRST DEPLOY IS JUDGED BEFORE THE APPLY LANDS. The platform reports a
// bundle applied only once Flux's health checks pass, and a forge.dev tier that
// refused to start (phase Failed, Ready=False: a declared secret that is not in
// the store, a rejected image) is never Stalled, so those checks keep reading
// it as still in progress. The apply wait then sat out its whole budget and
// ended "still applying, nothing has failed" while the workload had said why it
// cannot start a minute in. Before the apply lands, workload status normally
// describes the PREVIOUS revision and must not be judged as this deploy's; but
// when the platform has never applied any revision of the env there is no
// previous revision, so what the workloads report is about these bytes.
//
// WHY A GRACE AND NOT THE FIRST SIGHTING. Some failures resolve on their own
// within seconds: the platform mints the env's identity secrets moments after
// its first deploy, and a workload reads ManagedSecretFailed until it does. Two
// minutes is well past that, and far short of the ten-minute apply budget.
const HostedFirstDeployFailureGrace = 2 * time.Minute

// workloadFailure is one workload's reported failure and when it was first
// seen (reset when the message changes).
type workloadFailure struct {
	message string
	since   time.Time
}

// FirstDeployFailedError ends the apply wait of a first deploy whose
// workloads report a failure that did not clear within the grace.
type FirstDeployFailedError struct {
	Env string
	// Failures is workload name → the platform's message for it.
	Failures map[string]string
	Grace    time.Duration
}

func (e *FirstDeployFailedError) Error() string {
	names := make([]string, 0, len(e.Failures))
	for n := range e.Failures {
		names = append(names, n)
	}
	sort.Strings(names)
	lines := make([]string, 0, len(names))
	for _, n := range names {
		lines = append(lines, fmt.Sprintf("%s: %s", n, e.Failures[n]))
	}
	return fmt.Sprintf("hosted env %q: FAILED: the platform cannot start this release (it has kept reporting the failure for %s):\n  %s\n"+
		"  fix the cause and deploy again; `forge env status %s` shows the workloads' current state",
		e.Env, e.Grace, strings.Join(lines, "\n  "), e.Env)
}

// firstDeployFailure is one apply-wait tick's check of a first deploy's
// workloads. It reads workload status only when the platform positively
// reports no revision of the env ever applied (state known, current empty);
// otherwise status may describe the previous revision and it returns nil
// without reading. Each newly reported failure is printed once. When any
// workload has reported the same failure for at least the grace, it returns
// the error that ends the wait. A failed status read is not evidence of
// anything: the tick is skipped.
func (p HostedProvider) firstDeployFailure(ctx context.Context, c HostedCaller, envID string, state bundleApplyState, plan []hostedPlanItem, progress *applyProgress, now time.Time) *FirstDeployFailedError {
	if !state.known || state.current != "" {
		return nil
	}
	var resp wireStatusResponse
	if err := c.Call(ctx, procGetStatus, map[string]any{"environmentId": envID}, &resp); err != nil {
		return nil
	}
	reported := map[string]string{}
	for _, d := range resp.Deployments {
		if obs := d.Deployment.Observed; obs != nil && obs.State == wireObservedDegraded && obs.LastError != "" {
			reported[d.Deployment.Name] = obs.LastError
		}
	}
	grace := p.FirstDeployFailureGrace
	if grace <= 0 {
		grace = HostedFirstDeployFailureGrace
	}
	if progress.workloadFailures == nil {
		progress.workloadFailures = map[string]workloadFailure{}
	}
	expired := map[string]string{}
	for _, item := range plan {
		msg, failing := reported[item.Name]
		if !failing {
			delete(progress.workloadFailures, item.Name)
			continue
		}
		seen, ok := progress.workloadFailures[item.Name]
		if !ok || seen.message != msg {
			seen = workloadFailure{message: msg, since: now}
			progress.workloadFailures[item.Name] = seen
			fmt.Printf("  workload %s reports: %s (still waiting up to %s in case it clears)\n", item.Name, msg, grace)
		}
		if now.Sub(seen.since) >= grace {
			expired[item.Name] = msg
		}
	}
	if len(expired) == 0 {
		return nil
	}
	return &FirstDeployFailedError{Failures: expired, Grace: grace}
}

// firstDeployFailed ends the wait on a first deploy's persistent workload
// failure: each failing workload is observed FAILED with its message, the
// rest NOT WAITED. Under --rollout warn the error is printed and nil returned.
func (p HostedProvider) firstDeployFailed(envName string, ferr *FirstDeployFailedError, plan []hostedPlanItem, mode cluster.RolloutMode) error {
	ferr.Env = envName
	for _, item := range plan {
		if msg, failed := ferr.Failures[item.Name]; failed {
			p.observe(item.Name, cluster.RolloutStateFailed, fmt.Errorf("%s", msg))
		} else {
			p.observe(item.Name, cluster.RolloutStateNotWaited, nil)
		}
	}
	if mode == cluster.RolloutWarn {
		fmt.Printf("  Warning: %v\n", ferr)
		return nil
	}
	return ferr
}
