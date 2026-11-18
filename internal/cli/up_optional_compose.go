package cli

// Optional compose services and a missing Docker daemon.
//
// `forge env up` runs Postgres and the dev IdP as host processes, so a machine
// with no Docker daemon (a cloud agent sandbox, a CI container) can run the
// whole stack except what the env binds to forge.OnCompose. The scaffolded
// dev env binds local observability (ClickStack, the profiles collector,
// Alertmanager) there, and used to fail the whole run at `compose pull` for
// tooling the app does not need.
//
// The env DECLARES which compose services it can live without
// (OnCompose.optional) and which workloads only make sense beside them
// (Workload.requires). Nothing here knows a service by name.

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/clickstack"
	"github.com/reliant-labs/forge/internal/cliutil"
)

// otelSDKDisabledEnv switches off every OpenTelemetry export in a process.
const otelSDKDisabledEnv = "OTEL_SDK_DISABLED"

// degradeWithoutDocker removes from e what cannot run because the Docker
// daemon is unreachable, when the declaration says that is acceptable.
//
//   - No compose workload in scope, or a reachable daemon: nothing changes.
//   - A compose workload that is not optional: the run fails, naming Docker
//     and the services that need it.
//   - Only optional compose workloads: they, and every workload that
//     `requires` one of them (transitively), are dropped from e, and one
//     warning says what, why, and how to get it back.
//
// e.OptionalSkipped records the names so the deploy phase, which renders the
// env again, drops the same ones.
func degradeWithoutDocker(ctx context.Context, e *KCLEntities, env string, targets []string, w io.Writer) error {
	if e == nil {
		return nil
	}
	var optional, required []string
	for _, wl := range e.WorkloadsOn(RuntimeCompose) {
		if !inTargetSet(targets, wl.Name) {
			continue
		}
		if wl.Runtime.Compose != nil && wl.Runtime.Compose.Optional {
			optional = append(optional, wl.Name)
		} else {
			required = append(required, wl.Name)
		}
	}
	if len(optional)+len(required) == 0 {
		return nil
	}
	dockerErr := ensureDockerDaemon(ctx)
	if dockerErr == nil {
		return nil
	}
	if len(required) > 0 {
		return cliutil.UserErr(Name()+" env up (docker)",
			fmt.Sprintf("compose service(s) %s need Docker, and the Docker daemon is not available: %v",
				strings.Join(required, ", "), dockerErr),
			"deploy/kcl/"+env+"/main.k",
			"start Docker and re-run; or bind the service to a runtime that needs no daemon (forge.OnHost, or forge.HostInfra for Postgres and the dev IdP); or mark it `optional = True` on its forge.OnCompose if the app runs without it")
	}

	skip := map[string]bool{}
	for _, n := range optional {
		skip[n] = true
	}
	collectorSkipped := skip[localCollectorWorkload(e)]
	dependents := closeOverRequires(e, skip)

	kept := make([]WorkloadEntity, 0, len(e.Workloads))
	for _, wl := range e.Workloads {
		if !skip[wl.Name] {
			kept = append(kept, wl)
		}
	}
	e.Workloads = kept
	e.OptionalSkipped = mergeSorted(e.OptionalSkipped, sortedKeys(skip))

	if collectorSkipped {
		disableLocalTelemetry(e)
	}
	printNoDockerSkip(w, env, optional, dependents, collectorSkipped, dockerErr)
	return nil
}

// disableLocalTelemetry turns local telemetry off for this run: the collector
// the host processes were told to export to is not coming up, and exporting
// to it would only fail on every flush.
func disableLocalTelemetry(e *KCLEntities) {
	if e.Observability != nil {
		o := *e.Observability
		o.Enabled = false
		e.Observability = &o
	}
	for _, wl := range e.Workloads {
		h := wl.Runtime.Host
		if h == nil {
			continue
		}
		if _, set := wl.HostEnv()[otelSDKDisabledEnv]; set {
			continue
		}
		if h.LaunchEnv == nil {
			h.LaunchEnv = map[string]string{}
		}
		h.LaunchEnv[otelSDKDisabledEnv] = "true"
	}
}

// closeOverRequires adds to skip every workload that requires a skipped one,
// to a fixed point, and returns the names it added.
func closeOverRequires(e *KCLEntities, skip map[string]bool) []string {
	var added []string
	for changed := true; changed; {
		changed = false
		for _, wl := range e.Workloads {
			if skip[wl.Name] {
				continue
			}
			for _, r := range wl.Requires {
				if skip[r] {
					skip[wl.Name] = true
					added = append(added, wl.Name)
					changed = true
					break
				}
			}
		}
	}
	sort.Strings(added)
	return added
}

// localCollectorWorkload is the name of the workload that is the env's local
// telemetry collector, or "" when it has none.
func localCollectorWorkload(e *KCLEntities) string {
	svc := localCollectorService(e)
	if svc == "" {
		return ""
	}
	for _, wl := range e.WorkloadsOn(RuntimeCompose) {
		if composeServiceName(wl) == svc {
			return wl.Name
		}
	}
	return ""
}

func printNoDockerSkip(w io.Writer, env string, optional, dependents []string, collectorSkipped bool, dockerErr error) {
	fmt.Fprintf(w, "\n[up] ⚠ Docker is not available — skipping optional compose service(s): %s", strings.Join(optional, ", "))
	if len(dependents) > 0 {
		fmt.Fprintf(w, " (and %s, which need them)", strings.Join(dependents, ", "))
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "[up]   why: "+strings.ReplaceAll(strings.TrimSpace(dockerErr.Error()), "\n", "\n[up]        "))
	if collectorSkipped {
		fmt.Fprintf(w, "[up]   effect: local telemetry (%s, HyperDX) is off for this run; the rest of the stack comes up\n", clickstack.Service)
	} else {
		fmt.Fprintln(w, "[up]   effect: those services are not running; the rest of the stack comes up")
	}
	fmt.Fprintf(w, "[up]   to enable: start Docker and re-run `%s env up %s`\n", Name(), env)
	fmt.Fprintf(w, "[up]   to silence: drop them from deploy/kcl/%s/main.k (scaffolded envs: `_observability = False`)\n", env)
}

// withoutWorkloads is e minus the named workloads, as a shallow copy.
func withoutWorkloads(e *KCLEntities, names []string) *KCLEntities {
	if e == nil || len(names) == 0 {
		return e
	}
	drop := make(map[string]bool, len(names))
	for _, n := range names {
		drop[n] = true
	}
	out := *e
	out.Workloads = nil
	for _, wl := range e.Workloads {
		if !drop[wl.Name] {
			out.Workloads = append(out.Workloads, wl)
		}
	}
	return &out
}
