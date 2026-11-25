package deploytarget

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Observe reads the control plane's status for the env and maps each declared
// workload onto an ObservedItem.
//
// The control plane is the ONLY witness: forge has no credentials for the
// cluster a hosted workload runs on, so what the platform's observer last
// confirmed is what is reported — with its verdict in Detail, never upgraded.
// A workload the platform does not list is ABSENT; health is only HEALTHY for
// an observed READY state (the verdict may still be converging, which Detail
// says).
func (p HostedProvider) Observe(ctx context.Context, group ServiceGroup) (Observed, error) {
	out := Observed{ProviderID: HostedProviderID, ObservedAt: time.Now().UTC()}
	names := serviceNames(group)
	unknownAll := func(reason string) {
		out.Items = out.Items[:0]
		for _, n := range names {
			out.Items = append(out.Items, ObservedItem{Name: n, Health: HealthUnknown, Detail: reason})
		}
	}
	c, err := p.client()
	if err != nil {
		unknownAll(err.Error())
		return out, err
	}
	st, err := ReadHostedStatus(ctx, c, groupProject(group), group.Env)
	if errors.Is(err, ErrHostedEnvironmentNotFound) {
		// Never deployed: every declared workload is absent, which is a
		// measurement (the platform answered), not an unknown.
		for _, n := range names {
			out.Items = append(out.Items, ObservedItem{Name: n, Health: HealthAbsent,
				Detail: fmt.Sprintf("the control plane has no environment %q — never deployed", group.Env)})
		}
		return out, nil
	}
	if err != nil {
		unknownAll("could not read the control plane's status: " + err.Error())
		return out, err
	}
	byName := map[string]HostedWorkloadStatus{}
	for _, w := range st.Workloads {
		byName[w.Name] = w
	}
	for _, n := range names {
		out.Items = append(out.Items, observedItemFor(n, byName[n], byName[n].Name != ""))
	}
	return out, nil
}

// observedItemFor maps one hosted workload's status. Pure, for the mapping
// table test.
func observedItemFor(name string, w HostedWorkloadStatus, present bool) ObservedItem {
	item := ObservedItem{Name: name, Digest: w.ObservedDigest}
	if !present {
		item.Health = HealthAbsent
		item.Detail = "declared, but the control plane has no deployment of this name — run forge env deploy"
		return item
	}
	switch w.ObservedState {
	case "ready":
		item.Health = HealthHealthy
	case "pending", "progressing", "degraded":
		item.Health = HealthDegraded
	case "suspended", "deleted":
		item.Health = HealthAbsent
	default:
		item.Health = HealthUnknown
	}
	detail := fmt.Sprintf("observed %s, verdict %s", w.ObservedState, w.Verdict)
	if w.VerdictReason != "" {
		detail += " (" + w.VerdictReason + ")"
	}
	if w.LastError != "" {
		detail += ": " + w.LastError
	}
	if w.Drifted {
		detail += "; drifted from the declaration"
	}
	if item.Health == HealthHealthy && w.Verdict == "converged" && !w.Drifted {
		detail = ""
	}
	item.Detail = detail
	return item
}
