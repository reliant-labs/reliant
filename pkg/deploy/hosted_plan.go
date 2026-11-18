package deploy

import (
	"fmt"
	"strings"

	"github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
)

// THE HOSTED PLANS — the ONE place a hosted workload's resources are judged.
//
// A hosted environment publishes under one of two plans, and each has its own
// resource rule:
//
//   - HostedPlanStandard, an organization's own (billed) deploys: the shape
//     band (CheckShapeBand). Its rule is EQUALITY to the 4 GiB-per-vCPU band,
//     because the bill is the max of the request and what the band implies.
//   - HostedPlanClaimable, an anonymous deploy (`forge env deploy <env>
//     --claimable`): the platform pays and never bills, so the band does not
//     apply. Its rule is a CEILING: at most ClaimableLimits'
//     WorkloadCPUMillicores and WorkloadMemoryBytes, request AND limit, no
//     burst.
//
// The two rules used to live in different places (the band in the hosted
// plan, the caps in the CLI's eligibility check), so `forge env shape` judged
// an anonymous env by the band and told an author that 125m needs exactly
// 512 MiB while the anonymous plan refused anything above its cap. No shape
// satisfies both, and none needs to: an env is judged by the plan it deploys
// under, and CheckHostedResources is the only entry point.
//
// The numbers are control-plane's (internal/planlimits/claimable.go, design
// §2), read from its public GetClaimableAvailability answer; the control
// plane enforces them at admission and forge checks them first, in the same
// words, so a refusal costs no build.

// HostedPlan is the plan a hosted environment is admitted under.
type HostedPlan string

const (
	// HostedPlanStandard is an organization's own, billed plan. The zero
	// value: an env that names no plan is judged as standard.
	HostedPlanStandard HostedPlan = ""
	// HostedPlanClaimable is the anonymous, platform-funded plan of a
	// claimable deploy.
	HostedPlanClaimable HostedPlan = "claimable"
)

// String names the plan for messages.
func (p HostedPlan) String() string {
	if p == HostedPlanClaimable {
		return "claimable (anonymous)"
	}
	return "standard"
}

// ClaimableLimits is the claimable plan's caps as the control plane publishes
// them (GetClaimableAvailability's `limits`; control-plane
// internal/planlimits/claimable.go is their one declaration). Per workload,
// and with no burst: a limit is held to the cap as well as the request.
//
// forge does not own these numbers. A caller asks the control plane for them
// (internal/cli's claimable limits) and falls back to
// ClaimableLimitsFallback only when it cannot, saying so.
type ClaimableLimits struct {
	MaxServices      int
	MaxReplicas      int
	MaxMigrationJobs int
	// WorkloadCPUMillicores and WorkloadMemoryBytes are each workload's
	// request AND limit cap (the service, and separately its migration job).
	WorkloadCPUMillicores int64
	WorkloadMemoryBytes   int64

	MaxDatabases      int
	DatabaseInstances int
	// DatabaseDeclaredStorageGiB is the largest storageGiB a ManagedDatabase
	// may declare. The platform provisions DatabaseStorageBytes whatever it
	// declares, and sizes the Postgres pod itself (DatabaseCPUMillicores,
	// DatabaseMemoryBytes, DatabaseMaxConnections): informational here.
	DatabaseDeclaredStorageGiB int32
	DatabaseCPUMillicores      int64
	DatabaseMemoryBytes        int64
	DatabaseStorageBytes       int64
	DatabaseMaxConnections     int

	MaxStaticSites int
}

// ClaimableLimitsFallback is forge's compiled-in copy of the claimable plan's
// caps (control-plane, 2026-10-29), for when the control plane cannot be
// asked. A caller that uses it
// says so: the platform's own answer wins.
func ClaimableLimitsFallback() ClaimableLimits {
	return ClaimableLimits{
		MaxServices: 1, MaxReplicas: 1, MaxMigrationJobs: 1,
		WorkloadCPUMillicores: 125, WorkloadMemoryBytes: 128 << 20,
		MaxDatabases: 1, DatabaseInstances: 1, DatabaseDeclaredStorageGiB: 1,
		DatabaseCPUMillicores: 125, DatabaseMemoryBytes: 128 << 20,
		DatabaseStorageBytes: 256 << 20, DatabaseMaxConnections: 10,
		MaxStaticSites: 1,
	}
}

// Valid reports whether l can judge anything: a published answer that left
// the workload caps zero (an older or broken control plane) cannot, and the
// caller falls back instead of refusing every workload.
func (l ClaimableLimits) Valid() bool {
	return l.WorkloadCPUMillicores > 0 && l.WorkloadMemoryBytes > 0 && l.MaxServices > 0
}

// WorkloadShape is each workload's cap as prose: "125m / 128 MiB".
func (l ClaimableLimits) WorkloadShape() string {
	return fmt.Sprintf("%dm / %d MiB", l.WorkloadCPUMillicores, l.WorkloadMemoryBytes>>20)
}

// DatabaseStorage is the database's storage as prose, stating both numbers:
// the data the platform provisions and the whole-GiB volume that holds it
// ("256 MiB of data (1 GiB volume)").
func (l ClaimableLimits) DatabaseStorage() string {
	data := fmt.Sprintf("%d MiB of data", l.DatabaseStorageBytes>>20)
	if l.DatabaseDeclaredStorageGiB <= 0 {
		return data
	}
	return fmt.Sprintf("%s (%d GiB volume)", data, l.DatabaseDeclaredStorageGiB)
}

// Resources is the largest shape the claimable plan admits, request equal to
// limit. It is what a refusal suggests.
func (l ClaimableLimits) Resources() v1alpha1.Resources {
	return v1alpha1.Resources{
		CPURequestMillicores: l.WorkloadCPUMillicores, CPULimitMillicores: l.WorkloadCPUMillicores,
		MemoryRequestBytes: l.WorkloadMemoryBytes, MemoryLimitBytes: l.WorkloadMemoryBytes,
	}
}

// ResourcesRefusal is a resources block a plan does not admit, with the
// conformant block to declare instead. Its message is self-contained; Fix is
// the KCL literal a caller that knows WHERE the block is declared prints
// beside it.
type ResourcesRefusal struct {
	Plan   HostedPlan
	Reason string
	// Want is the conformant block: the nearest shape the plan admits.
	Want v1alpha1.Resources
}

func (e *ResourcesRefusal) Error() string {
	return e.Reason + " (the " + e.Plan.String() + " plan)"
}

// Fix is Want as the KCL a binder or workload declares:
//
//	resources = fw.Resources {cpuRequestMillicores = 250, ...}
func (e *ResourcesRefusal) Fix() string { return KCLResources(e.Want) }

// KCLResources renders r as a `resources = fw.Resources {...}` KCL field,
// memory in forge.mib, omitting unset limits.
func KCLResources(r v1alpha1.Resources) string {
	parts := []string{fmt.Sprintf("cpuRequestMillicores = %d", r.CPURequestMillicores)}
	if r.CPULimitMillicores > 0 {
		parts = append(parts, fmt.Sprintf("cpuLimitMillicores = %d", r.CPULimitMillicores))
	}
	parts = append(parts, "memoryRequestBytes = "+kclBytes(r.MemoryRequestBytes))
	if r.MemoryLimitBytes > 0 {
		parts = append(parts, "memoryLimitBytes = "+kclBytes(r.MemoryLimitBytes))
	}
	return "resources = fw.Resources {" + strings.Join(parts, ", ") + "}"
}

func kclBytes(b int64) string {
	if b%(1<<20) == 0 {
		return fmt.Sprintf("forge.mib(%d)", b>>20)
	}
	return fmt.Sprintf("%d", b)
}

// CheckHostedResourcesWithin judges one hosted workload's resources under
// plan: the shape band for the standard plan, the claimable caps (claimable)
// for the claimable one. A refusal is a *ResourcesRefusal naming the
// conformant block.
func CheckHostedResourcesWithin(plan HostedPlan, claimable ClaimableLimits, r v1alpha1.Resources) error {
	if plan == HostedPlanClaimable {
		return claimable.CheckResources(r)
	}
	if err := CheckShapeBand(r); err != nil {
		return &ResourcesRefusal{Plan: plan, Reason: err.Error(), Want: bandFix(r)}
	}
	return nil
}

// CheckResources holds one workload's resources to the claimable caps.
func (l ClaimableLimits) CheckResources(r v1alpha1.Resources) error {
	d := r.WithDefaults()
	var why []string
	if cpu := max(d.CPURequestMillicores, d.CPULimitMillicores); cpu > l.WorkloadCPUMillicores {
		why = append(why, fmt.Sprintf("%dm of CPU (at most %dm, no burst)", cpu, l.WorkloadCPUMillicores))
	}
	if mem := max(d.MemoryRequestBytes, d.MemoryLimitBytes); mem > l.WorkloadMemoryBytes {
		why = append(why, fmt.Sprintf("%d MiB of memory (at most %d MiB, no burst)", mem>>20, l.WorkloadMemoryBytes>>20))
	}
	if len(why) == 0 {
		return nil
	}
	want := v1alpha1.Resources{
		CPURequestMillicores: min(d.CPURequestMillicores, l.WorkloadCPUMillicores),
		MemoryRequestBytes:   min(d.MemoryRequestBytes, l.WorkloadMemoryBytes),
	}
	want.CPULimitMillicores, want.MemoryLimitBytes = want.CPURequestMillicores, want.MemoryRequestBytes
	return &ResourcesRefusal{Plan: HostedPlanClaimable, Reason: "asks for " + strings.Join(why, " and "), Want: want}
}

// bandFix is the on-band shape nearest r: its CPU request rounded UP to the
// band's granularity, with exactly the memory the band implies. A limit below
// the new request is raised to it (a limit may never be under its request).
func bandFix(r v1alpha1.Resources) v1alpha1.Resources {
	d := r.WithDefaults()
	cpu := d.CPURequestMillicores
	if cpu <= 0 {
		cpu = BandGranularityMillicores
	}
	if rem := cpu % BandGranularityMillicores; rem != 0 {
		cpu += BandGranularityMillicores - rem
	}
	want := v1alpha1.Resources{CPURequestMillicores: cpu, MemoryRequestBytes: BandMemoryBytes(cpu)}
	if r.CPULimitMillicores > 0 {
		want.CPULimitMillicores = max(r.CPULimitMillicores, cpu)
	}
	if r.MemoryLimitBytes > 0 {
		want.MemoryLimitBytes = max(r.MemoryLimitBytes, want.MemoryRequestBytes)
	}
	return want
}

// RunsBeforeRollout reports whether a job workload runs to completion BEFORE
// the workloads it belongs to roll out: a standalone job in the pre-rollout
// phase, or a GATING job (`before = [...]`, `before = [fw.BEFORE_ALL]`), which
// the renderer runs ahead of every workload it names. Either is the shape of
// a service's own migration, which is what the claimable plan admits beside
// its one service.
//
// The two spellings are one fact. EffectiveDeployPhase answers only for a
// standalone job (a gating job's phase is its dependents'), so judging a
// migration by it alone refused the scaffold's `before = [fw.BEFORE_ALL]`
// migrate job, while `before` and `deployPhase` cannot both be set.
func RunsBeforeRollout(s v1alpha1.WorkloadSpec) bool {
	if s.EffectiveKind() != v1alpha1.KindJob {
		return false
	}
	return len(s.Before) > 0 || s.EffectiveDeployPhase() == v1alpha1.DeployPhasePreRollout
}
