package deploytarget

import (
	"context"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/internal/hostinfra"
)

// Observe reports whether each declared host-infra instance is currently
// serving, on which port, and which engine build.
//
// # Why the data directory and not the port
//
// A port probe is the obvious implementation and it is the wrong one on
// the machine this tier targets. A developer box runs several stacks;
// the declared port may be held by ANOTHER project's postgres or a
// system one, and reporting that as "our instance is up" would show green
// for a database this project has never written to. That is the same
// adoption mistake hostinfra.Start refuses to make, and an observation
// that made it would be worse than the deploy — it would be believed.
//
// So ownership is decided by the DATA DIRECTORY (hostinfra.StatusOf),
// and a port held by something else is reported as its own state rather
// than folded into either "up" or "down".
//
// # Why running is not healthy
//
// A live process is not a server that answers. Postgres writes its pid
// file and then spends its recovery phase refusing connections; the dev
// IdP is alive for the whole of its declarative bootstrap. Reporting
// either as healthy would tell a caller the thing it is about to dial
// will respond, which is exactly the claim that is false. Health is
// HealthHealthy only when the instance both runs AND answers.
//
// # Why Replicas is nil and Digest is empty
//
// Nil rather than &ReplicaCounts{Desired: 1}: a supervised host process
// has no replica concept at all, and this is the distinction the pointer
// exists to carry — "not applicable" versus "nothing is running". Digest
// stays empty because there is no content-addressed identity to report:
// forge fetches a released engine binary by version, and a version string
// is a mutable pointer in the same way an image tag is. The version goes
// in Detail, which is information rather than a claim about bytes.
func (p HostInfraProvider) Observe(ctx context.Context, group ServiceGroup) (Observed, error) {
	out := Observed{
		ProviderID: p.Name(),
		ObservedAt: time.Now().UTC(),
		Items:      make([]ObservedItem, 0, len(group.Services)),
	}
	for _, svc := range group.Services {
		out.Items = append(out.Items, p.observeOne(ctx, svc))
	}
	return out, nil
}

// observeOne reads one instance. Every failure path yields an item, so
// one unreadable instance does not erase its siblings' observations.
func (p HostInfraProvider) observeOne(ctx context.Context, svc ResolvedService) ObservedItem {
	if svc.HostInfra == nil {
		return ObservedItem{
			Name:   svc.Name,
			Health: HealthUnknown,
			Detail: "HostInfra spec is nil (group misrouted?)",
		}
	}
	spec := specOf(svc.Name, svc.HostInfra)
	st, err := hostinfra.StatusOf(ctx, p.projectDir(), spec)
	if err != nil {
		return ObservedItem{
			Name:   svc.Name,
			Health: HealthUnknown,
			Detail: fmt.Sprintf("could not read host-infra status: %v", err),
		}
	}

	switch {
	case st.Foreign:
		// Deliberately DEGRADED rather than absent or healthy. Absent
		// would invite a caller to start it, and starting it is the one
		// thing that cannot work — the port is taken. Healthy would be
		// the adoption mistake. Degraded plus a reason is the only
		// honest verdict, and it names the fix.
		return ObservedItem{
			Name:   svc.Name,
			Health: HealthDegraded,
			Detail: fmt.Sprintf(
				"port %d is held by something this project did not start; "+
					"no instance of forge's own (data dir %s) is running",
				svc.HostInfra.Port, st.DataDir),
		}
	case !st.Running:
		return ObservedItem{
			Name:   svc.Name,
			Health: HealthAbsent,
			Detail: fmt.Sprintf("no %s process running for this project (data dir %s)",
				svc.HostInfra.Engine, st.DataDir),
		}
	case !st.Ready:
		return ObservedItem{
			Name:   svc.Name,
			Health: HealthDegraded,
			Detail: fmt.Sprintf("%s process is running%s but is not answering yet "+
				"(starting up, or wedged)", svc.HostInfra.Engine, portPhrase(st.Port)),
		}
	case st.Port != svc.HostInfra.Port && st.Port > 0:
		// Running, answering — on a port the declaration has since moved
		// off. Everything dialling the DECLARED port fails while the
		// server itself looks perfectly healthy, which is why this is
		// its own reported state rather than a healthy with a footnote.
		return ObservedItem{
			Name:   svc.Name,
			Health: HealthDegraded,
			Detail: fmt.Sprintf(
				"%s is serving on port %d, but this environment declares port %d; "+
					"anything dialling the declared port will not reach it",
				svc.HostInfra.Engine, st.Port, svc.HostInfra.Port),
		}
	default:
		item := ObservedItem{Name: svc.Name, Health: HealthHealthy}
		if st.Version != "" && svc.HostInfra.Version != "" && !sameEngineVersion(svc.HostInfra.Engine, st.Version, svc.HostInfra.Version) {
			// The cluster on disk was created by a different engine
			// version than the one now declared. It is not broken — it is
			// serving — but a re-read of the declaration would not
			// reproduce it, so health drops rather than hiding a drift
			// that only surfaces when the data directory is recreated.
			item.Health = HealthDegraded
			item.Detail = fmt.Sprintf(
				"serving %s %s, but this environment declares version %s; "+
					"the existing data directory (%s) was created by the running version and is not upgraded in place",
				svc.HostInfra.Engine, st.Version, svc.HostInfra.Version, st.DataDir)
		}
		return item
	}
}

// portPhrase renders the port half of a detail line, and says nothing at
// all when the port is not yet known (an instance mid-boot has a live
// process and no published port). Printing "on port 0" or "on port -1"
// would be worse than silence — both read as facts.
func portPhrase(port int) string {
	if port <= 0 {
		return ""
	}
	return fmt.Sprintf(" on port %d", port)
}

// sameEngineVersion reports whether a running instance's version is the one
// declared. A postgres data directory records only its major (PG_VERSION), so
// a declared "16.9.0" and a reported "16" are the same server.
func sameEngineVersion(engine, running, declared string) bool {
	if engine == hostinfra.EnginePostgres {
		return hostinfra.PostgresMajor(running) == hostinfra.PostgresMajor(declared)
	}
	return running == declared
}
