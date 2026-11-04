package templates

import (
	"fmt"
	"strings"

	"github.com/reliant-labs/forge/pkg/deploy"
)

// EnvTemplateData is the payload of the env main.k templates
// (deploy/kcl/dev/main.k.tmpl for the local loop, deploy/kcl/cloud/main.k.tmpl
// for staging and prod, which are hosted on the forge control plane). An env
// is a list of per-workload bindings, so the templates hold every binder and
// per-env value; this carries only what they cannot know — including
// Bindings, the one line per workload the generator derives from the
// project's components.
type EnvTemplateData struct {
	// Roots is a composition-graph project: a workload's config env comes
	// from config_gen.workloadEnvMap, which gives one bound to a root
	// (`root = "server"`) exactly what the root reaches.
	Roots       bool
	ProjectName string
	EnvName     string
	// PrimaryWorkload names the workload holding the dev env's
	// `<project>-<env>-api` port key — the port the frontend's dev config
	// reads. The API, `_api` (codegen.APIWorkloadName), unless a caller says
	// otherwise.
	PrimaryWorkload string
	// IngressEnabled wires the dev env's Gateway. A hosted env has no
	// ingress of its own (the platform allocates every public hostname), so
	// the cloud template only points at its ingress.k for a cluster you
	// operate.
	IngressEnabled bool
	HasFrontend    bool
	// DatabaseDisabled omits database infrastructure in a database-free project.
	DatabaseDisabled bool
	FrontendName     string
	// FrontendIdent is the KCL identifier of that frontend: the cloud env
	// declares it once as `_<ident>_frontend` and binds it on one line
	// (`_hosted_frontend(_<ident>_frontend)`), the line `forge env new
	// --bind` rewrites.
	FrontendIdent  string
	FrontendRoutes []string
	// FrontendPath is the frontend's directory relative to the project root:
	// frontends/<name> for a scaffolded one, "." for a site adopted in place
	// (`forge project new --in-place --kind site`), whose app IS the root.
	FrontendPath string
	// FrontendType is the KCL forge.Frontend.type to declare ("vite",
	// "nextjs"); empty leaves the schema default (nextjs) implicit.
	FrontendType string
	// FrontendPublicDir is where the static build lands, relative to
	// FrontendPath: "out" for a Next.js static export, "dist" for Vite.
	FrontendPublicDir string
	// Bindings is the body of the env's `_workloads = [...]` list: one
	// `        _<binder>(wl.<ident>)` line per workload. EnvBinding renders one.
	Bindings string
	// APIWorkload is a hosted env's `_api` declaration — the project
	// binary's `server` as the one workload serving every service
	// (codegen.APIWorkloadStanza). The cloud template only.
	APIWorkload string
	// Claimable is a prod env scaffolded for an anonymous deploy (`forge
	// project new --claimable`): `control_plane` declares `claimable = True`
	// and the hosted binder sizes services and jobs to the claimable plan.
	// The cloud template only.
	Claimable bool
	// ClaimableResources is the body of that plan's largest shape as a KCL
	// fw.Resources block (deploy.ClaimableLimitsFallback), filled by withDefaults
	// so the template states no number of its own.
	ClaimableResources string
	// ClaimableShape is that shape for prose ("125m / 128 MiB"), and
	// ClaimableDatabaseStorageGiB the database storage it declares.
	ClaimableShape              string
	ClaimableDatabaseStorageGiB int32
	// ClaimableDBConnections is that database's max_connections, and
	// ClaimableDBPool the pool each workload is held to inside it.
	ClaimableDBConnections int
	ClaimableDBPool        int
	// Capacity floor of the cloud envs' (unused as scaffolded) `_on_cluster`
	// binder.
	Replicas         int
	CPURequest       string
	CPULimit         string
	MemoryRequestMiB int
	MemoryLimitMiB   int
}

// withDefaults fills the capacity floor (prod-shaped, halved for an env
// named staging) and the example workload, so a caller states only what it
// knows.
func (d EnvTemplateData) withDefaults() interface{} {
	if d.PrimaryWorkload == "" {
		d.PrimaryWorkload = "api" // codegen.APIWorkloadName; codegen imports this package
	}
	if d.FrontendIdent == "" {
		d.FrontendIdent = strings.NewReplacer("-", "_", ".", "_").Replace(d.FrontendName)
	}
	if d.FrontendPath == "" {
		d.FrontendPath = "frontends/" + d.FrontendName
	}
	if d.FrontendPublicDir == "" {
		d.FrontendPublicDir = "out"
	}
	if d.Claimable && d.ClaimableResources == "" {
		// The compiled-in copy of the control plane's caps: a scaffold is
		// written offline, and every deploy check re-reads the published ones.
		caps := deploy.ClaimableLimitsFallback()
		r := caps.Resources()
		d.ClaimableShape = fmt.Sprintf("%dm / %d MiB", r.CPURequestMillicores, r.MemoryRequestBytes>>20)
		d.ClaimableDatabaseStorageGiB = caps.DatabaseDeclaredStorageGiB
		d.ClaimableDBConnections = caps.DatabaseMaxConnections
		d.ClaimableDBPool = claimableDBPool(caps.DatabaseMaxConnections)
		d.ClaimableResources = fmt.Sprintf("    cpuRequestMillicores = %d\n    cpuLimitMillicores = %d\n"+
			"    memoryRequestBytes = forge.mib(%d)\n    memoryLimitBytes = forge.mib(%d)",
			r.CPURequestMillicores, r.CPULimitMillicores, r.MemoryRequestBytes>>20, r.MemoryLimitBytes>>20)
	}
	if d.Replicas == 0 {
		d.Replicas, d.CPURequest, d.CPULimit, d.MemoryRequestMiB, d.MemoryLimitMiB = 3, "0.5", "2.0", 512, 1024
		if d.EnvName == "staging" {
			d.Replicas, d.CPURequest, d.CPULimit, d.MemoryRequestMiB, d.MemoryLimitMiB = 2, "0.25", "1.0", 256, 512
		}
	}
	return d
}

// claimableDBPool is the pool a workload of an anonymous deploy may hold
// against its database's max_connections: half of them, since Postgres
// reserves three for the superuser (the platform's instance manager) and the
// migration job needs its own. Measured on the claimable plan (10
// connections): the default pool of 25 failed 2% of creates under 40-way
// load with "too many clients"; 5 failed none.
func claimableDBPool(maxConnections int) int {
	return max(1, maxConnections/2)
}
