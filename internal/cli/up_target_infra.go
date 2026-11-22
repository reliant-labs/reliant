package cli

// Shared infrastructure under --target.
//
// --target scopes the build, host and frontend phases to the named apps, but
// the infra pre-warm converged EVERY infrastructure service the env declares.
// `forge env up dev --target api --target web --no-deploy` therefore still
// pulled and started ClickStack, the profiles collector and Alertmanager —
// the heaviest images in the stack, for tooling neither target needs.
//
// The env already says which of its compose services the app can run without
// (OnCompose.optional) and which workloads only make sense beside one
// (Workload.requires); the no-Docker degradation reads the same two facts.
// With targets, an optional service runs only when a target names it or a
// target requires it. Required infrastructure (the database, the dev IdP) is
// still started: nothing declares which host process dials which server, and
// a target without its database is not a smaller stack but a broken one.

import (
	"context"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/clickstack"
)

// composeServiceRunning reports whether the compose service behind w already
// runs for this project and env. A seam: the unit tests have no Docker.
var composeServiceRunning = func(ctx context.Context, projectDir, env string, w WorkloadEntity) bool {
	pubs, err := composePublishersFn(ctx, projectDir, composeSpecFor(projectDir, env, w.Runtime.Compose), composeServiceName(w))
	return err == nil && len(pubs) > 0
}

// scopeOptionalInfra removes from e the optional compose services a targeted
// run does not need, and prints the run's shared-infrastructure plan: what it
// starts and why, and what it leaves out and how to bring it back.
//
// An optional service that is already running (an earlier, untargeted run
// started it) is left exactly as it is: not pulled, not recreated, and still
// receiving telemetry. One that is not running is not started, and when it is
// the env's local collector, the selected host processes export nothing
// rather than failing on every flush. No targets: the whole env runs and
// nothing changes.
func scopeOptionalInfra(ctx context.Context, e *KCLEntities, projectDir, env string, targets []string, w io.Writer) {
	if e == nil || len(targets) == 0 {
		return
	}
	needed := requiredClosure(e, targets)
	var starting, skipped, leftRunning []string
	for _, h := range e.Infra {
		starting = append(starting, h.Name+" (host process)")
	}
	drop := map[string]bool{}
	for _, wl := range e.WorkloadsOn(RuntimeCompose) {
		switch {
		case needed[wl.Name]:
			why := "selected"
			if !inTargetSet(targets, wl.Name) {
				why = "required by a target"
			}
			starting = append(starting, wl.Name+" (compose, "+why+")")
		case wl.Runtime.Compose == nil || !wl.Runtime.Compose.Optional:
			starting = append(starting, wl.Name+" (compose, required by the env)")
		case composeServiceRunning(ctx, projectDir, env, wl):
			drop[wl.Name] = true
			leftRunning = append(leftRunning, wl.Name)
		default:
			drop[wl.Name] = true
			skipped = append(skipped, wl.Name)
		}
	}
	collector := localCollectorWorkload(e)
	collectorSkipped := collector != "" && drop[collector] && !slices.Contains(leftRunning, collector)
	if len(drop) > 0 {
		kept := make([]WorkloadEntity, 0, len(e.Workloads))
		for _, wl := range e.Workloads {
			if !drop[wl.Name] {
				kept = append(kept, wl)
			}
		}
		e.Workloads = kept
		e.OptionalSkipped = mergeSorted(e.OptionalSkipped, sortedKeys(drop))
		if collectorSkipped {
			disableLocalTelemetry(e)
		}
	}
	printTargetInfraPlan(w, targets, starting, skipped, leftRunning, collectorSkipped)
}

// requiredClosure is the target set plus everything a target requires,
// transitively (Workload.requires).
func requiredClosure(e *KCLEntities, targets []string) map[string]bool {
	needed := map[string]bool{}
	byName := map[string]WorkloadEntity{}
	for _, wl := range e.Workloads {
		byName[wl.Name] = wl
	}
	var visit func(string)
	visit = func(name string) {
		if needed[name] {
			return
		}
		needed[name] = true
		for _, r := range byName[name].Requires {
			visit(r)
		}
	}
	for _, t := range targets {
		visit(t)
	}
	return needed
}

func printTargetInfraPlan(w io.Writer, targets, starting, skipped, leftRunning []string, collectorSkipped bool) {
	if len(starting)+len(skipped)+len(leftRunning) == 0 {
		return
	}
	fmt.Fprintf(w, "\n[up] shared infra for --target %s:\n", strings.Join(targets, ", "))
	if len(starting) > 0 {
		fmt.Fprintf(w, "[up]   starting: %s\n", strings.Join(starting, ", "))
	}
	if len(leftRunning) > 0 {
		fmt.Fprintf(w, "[up]   left running: %s (optional, not needed by the targets; already up, so not pulled or restarted)\n",
			strings.Join(leftRunning, ", "))
	}
	if len(skipped) > 0 {
		fmt.Fprintf(w, "[up]   not starting: %s (optional, and no target selects or requires it; add --target <name> to include one)\n",
			strings.Join(skipped, ", "))
		if collectorSkipped {
			fmt.Fprintf(w, "[up]   effect: local telemetry (%s, HyperDX) is off for this run\n", clickstack.Service)
		}
	}
}

// mergeSorted is the sorted union of a and b.
func mergeSorted(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range append(append([]string(nil), a...), b...) {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}
