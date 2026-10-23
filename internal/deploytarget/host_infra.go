package deploytarget

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/reliant-labs/forge/internal/hostinfra"
)

// HostInfraProvider brings up the dev infrastructure an environment
// declares as `forge.HostInfra` — a real server binary forge fetches and
// supervises as a HOST PROCESS, with no container runtime involved.
//
// It is a Provider like any other so that the thing which decides HOW dev
// infra runs is the environment's KCL, not a branch in forge's Go. An env
// that declares `forge.HostInfra {engine = "postgres", …}` gets a host
// postgres; one that declares `forge.Compose {service = "postgres"}` gets
// the container; and the code path that dispatches them is the same
// group-then-look-up-the-provider loop in both cases. Adding a mode meant
// adding a provider, which is the seam that already existed.
//
// See internal/hostinfra for the postgres implementation and for why the
// data directory persists while the server outlives the process that
// started it.
type HostInfraProvider struct {
	// ProjectDir is the project root. Data directories declared relative
	// (the default) resolve against it. Empty means the working directory.
	ProjectDir string
}

// Name returns the provider identifier — the ProviderID a host-infra
// ServiceGroup carries.
func (HostInfraProvider) Name() string { return "host-infra" }

// Deploy starts every declared instance in the group, or confirms it is
// already serving.
//
// EVERY instance is attempted even after one fails, and the failures are
// reported together. A group is a set of independent servers, and stopping
// at the first one that could not start would hide the state of the rest
// behind a single message — which is exactly how a port collision on
// postgres used to take an entire dev stack down with it while naming only
// postgres.
func (p HostInfraProvider) Deploy(ctx context.Context, group ServiceGroup) error {
	var failures []error
	for _, svc := range startOrder(group.Services) {
		if svc.HostInfra == nil {
			failures = append(failures, fmt.Errorf("%s: HostInfra spec is nil (group misrouted?)", svc.Name))
			continue
		}
		if group.DryRun {
			fmt.Printf("  [DRY-RUN] would start %s (%s) on :%d as a host process\n",
				svc.Name, svc.HostInfra.Engine, svc.HostInfra.Port)
			continue
		}
		if err := hostinfra.Start(ctx, p.projectDir(), specOf(svc.Name, svc.HostInfra)); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// Stop shuts every instance in the group down cleanly, leaving its data in
// place. This is `forge env down`'s half of the lifecycle; nothing on the
// Provider interface covers it, because compose and k8s targets are stopped
// by their own runtimes and forge never had to.
//
// Best-effort and aggregated for the same reason Deploy is: one instance
// that will not stop must not leave the others running with no report.
func (p HostInfraProvider) Stop(group ServiceGroup) error {
	var failures []error
	// Reverse of the start order: a consumer goes down before the server it
	// stores its state in, so it is never left writing into a database that
	// has gone away underneath it.
	svcs := startOrder(group.Services)
	for i := len(svcs) - 1; i >= 0; i-- {
		svc := svcs[i]
		if svc.HostInfra == nil {
			continue
		}
		if err := hostinfra.Stop(p.projectDir(), specOf(svc.Name, svc.HostInfra)); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (p HostInfraProvider) projectDir() string {
	if p.ProjectDir != "" {
		return p.ProjectDir
	}
	return "."
}

// startOrder sorts a host-infra group so a server that BACKS another comes
// up first.
//
// There is exactly one such dependency today and it is a real one: the dev
// IdP (zitadel) stores its whole state in postgres, so starting it against
// a postgres that is not serving yet fails during the declarative bootstrap
// — and fails in a way that names the IdP, three steps downstream of the
// cause.
//
// It is expressed as a stable partial order over ENGINES rather than as a
// general dependency graph on purpose. A graph would be a declaration
// surface (`depends_on:` on HostInfra), which is a thing to design, teach
// and validate for a set of engines forge can enumerate on one hand. The
// day a third engine has a dependency that is not "needs the database", the
// graph is the right answer and this function is where it lands.
//
// sort.SliceStable, so instances of the same engine keep the order the
// environment declared them in — the group's own reporting is per-instance
// and reordering peers would make it read differently run to run.
func startOrder(services []ResolvedService) []ResolvedService {
	out := make([]ResolvedService, len(services))
	copy(out, services)
	sort.SliceStable(out, func(i, j int) bool {
		return engineRank(out[i]) < engineRank(out[j])
	})
	return out
}

// engineRank places each engine in the start order: storage first,
// everything that stores state in it after.
func engineRank(svc ResolvedService) int {
	if svc.HostInfra == nil {
		return 0
	}
	switch svc.HostInfra.Engine {
	case "postgres":
		return 0
	default:
		return 1
	}
}

// specOf converts the rendered deploy block into the hostinfra package's
// Spec. The two shapes are deliberately separate types: this one mirrors
// the KCL schema (it is the wire contract), and hostinfra.Spec is what the
// supervisor needs. Collapsing them would make internal/hostinfra depend on
// the render layer's JSON tags.
func specOf(name string, spec *HostInfraSpec) hostinfra.Spec {
	return hostinfra.Spec{
		Name:            name,
		Engine:          spec.Engine,
		Port:            spec.Port,
		Database:        spec.Database,
		User:            spec.User,
		Password:        spec.Password,
		DataDir:         spec.DataDir,
		Version:         spec.Version,
		IDPDatabase:     spec.IDPDatabase,
		IDPDatabasePort: spec.IDPDatabasePort,
		IDPMasterKey:    spec.IDPMasterKey,
		IDPStepsFile:    spec.IDPStepsFile,
		IDPPATPath:      spec.IDPPATPath,

		NATSMonitorPort:    spec.NATSMonitorPort,
		TemporalNamespaces: spec.TemporalNamespaces,
		TemporalUIPort:     spec.TemporalUIPort,
	}
}
