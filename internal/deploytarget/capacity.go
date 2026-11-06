package deploytarget

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
)

// procCheckDeployCapacity is controlplane.v1.DeployService/CheckDeployCapacity:
// a read-only "would this org be allowed to run this?" asked before anything
// is built, pushed or recorded. The server enforces the same decision at
// RecordBundle / Promote, so this is advisory — its job is to fail in seconds
// instead of after a nine-minute build.
const procCheckDeployCapacity = "controlplane.v1.DeployService/CheckDeployCapacity"

// CapacityDemand is what a hosted deploy would run, summed across its items.
// It mirrors controlplane.v1.DeployCapacityDemand; the server re-derives the
// demand from the recorded bundle when it enforces, so this is never trusted
// for admission.
type CapacityDemand struct {
	CPUMillicores int64
	MemoryBytes   int64
	StorageGiB    int64
	Workloads     int // services and jobs: anything that runs a pod
	Databases     int
	Builds        int
	StaticSites   int
}

// DemandOf sums the admitted hosted items. REQUESTS, not limits, are what a
// workload reserves, and a workload with a volume runs exactly one replica.
// builds is how many images forge would build for this deploy (0 for a deploy
// of an already-cut release).
func DemandOf(items []HostedPreflightItem, builds int) CapacityDemand {
	d := CapacityDemand{Builds: builds}
	for _, it := range items {
		switch {
		case it.Workload != nil:
			spec := it.Workload
			res := spec.Resources.WithDefaults()
			replicas := int64(spec.Replicas)
			if replicas < 1 || spec.StorageGiB > 0 {
				replicas = 1
			}
			switch spec.Kind {
			case v1alpha1.KindJob, v1alpha1.KindCron, v1alpha1.KindTool:
				replicas = 1
			}
			d.Workloads++
			d.CPUMillicores += res.CPURequestMillicores * replicas
			d.MemoryBytes += res.MemoryRequestBytes * replicas
			d.StorageGiB += int64(spec.StorageGiB)
		case it.Database != nil:
			instances := int64(it.Database.Instances)
			if instances < 1 {
				instances = 1
			}
			storage := int64(it.Database.StorageGiB)
			if storage < 1 {
				storage = int64(v1alpha1.DefaultDatabaseStorageGiB)
			}
			d.Databases++
			d.StorageGiB += instances * storage
		case it.Static != nil:
			d.StaticSites++
		}
	}
	return d
}

// RunsCompute reports whether the demand needs a compute plan at all.
func (d CapacityDemand) RunsCompute() bool {
	return d.Workloads > 0 || d.Databases > 0 || d.Builds > 0
}

// String is the demand in one line, for the refusal and the plan output.
func (d CapacityDemand) String() string {
	return fmt.Sprintf("%dm CPU, %s memory, %d GiB storage; %d workload(s), %d database(s), %d build(s), %d static site(s)",
		d.CPUMillicores, gibString(d.MemoryBytes), d.StorageGiB, d.Workloads, d.Databases, d.Builds, d.StaticSites)
}

func gibString(b int64) string { return fmt.Sprintf("%.1f GiB", float64(b)/(1<<30)) }

// The DeployCapacityCode wire names this client acts on.
const (
	CapacityCodeOK                = "DEPLOY_CAPACITY_CODE_OK"
	CapacityCodeNoComputePlan     = "DEPLOY_CAPACITY_CODE_NO_COMPUTE_PLAN"
	CapacityCodeExceedsCeiling    = "DEPLOY_CAPACITY_CODE_EXCEEDS_CEILING"
	CapacityCodeSpendCapReached   = "DEPLOY_CAPACITY_CODE_SPEND_CAP_REACHED"
	CapacityCodePlanUnknown       = "DEPLOY_CAPACITY_CODE_PLAN_UNKNOWN"
	capacityWireCodeUnimplemented = "unimplemented"
	capacityCodePrefix            = "DEPLOY_CAPACITY_CODE_"
)

// CapacityVerdict is the control plane's answer.
type CapacityVerdict struct {
	Allowed        bool
	Code           string
	Reason         string
	Fix            string
	HasComputePlan bool
	CeilingCPU     int64
	CeilingMemory  int64
	CeilingStorage int64
	// Unchecked is true when the control plane does not serve the procedure
	// (an older server): nothing was decided, and the server still enforces at
	// RecordBundle / Promote.
	Unchecked bool
	// Holds is set when Allowed is false but the control plane would ACCEPT
	// the deploy and QUEUE it on a human action (billing) rather than refuse
	// it. A deploy then proceeds — build, record, promote — and is reported
	// queued. An older control plane never sends it, so it keeps refusing.
	Holds []HostedHold
}

// Queued reports a verdict that refuses to RUN the deploy now but would
// accept and queue it: proceed, and report the queue.
func (v CapacityVerdict) Queued() bool { return !v.Allowed && !v.Unchecked && len(v.Holds) > 0 }

// CheckHostedCapacity asks the control plane whether demand fits, for the
// environment envID ("" when the environment does not exist yet). A server
// that does not serve the procedure yields Unchecked=true with no error; every
// other failure is returned.
func CheckHostedCapacity(ctx context.Context, c HostedCaller, envID string, demand CapacityDemand) (CapacityVerdict, error) {
	var resp struct {
		Allowed             bool         `json:"allowed"`
		Code                string       `json:"code"`
		Reason              string       `json:"reason"`
		Fix                 string       `json:"fix"`
		HasComputePlan      bool         `json:"hasComputePlan"`
		CeilingCPUMillicore wireInt64    `json:"ceilingCpuMillicores"`
		CeilingMemoryBytes  wireInt64    `json:"ceilingMemoryBytes"`
		CeilingStorageGiB   wireInt64    `json:"ceilingStorageGib"`
		Holds               []HostedHold `json:"holds,omitempty"`
	}
	req := map[string]any{
		"environmentId": envID,
		"demand": map[string]any{
			"cpuMillicores": demand.CPUMillicores,
			"memoryBytes":   demand.MemoryBytes,
			"storageGib":    demand.StorageGiB,
			"workloads":     demand.Workloads,
			"databases":     demand.Databases,
			"builds":        demand.Builds,
			"staticSites":   demand.StaticSites,
		},
	}
	if err := c.Call(ctx, procCheckDeployCapacity, req, &resp); err != nil {
		var coded codedWireError
		if errors.As(err, &coded) && coded.HasCode(capacityWireCodeUnimplemented) {
			return CapacityVerdict{Unchecked: true}, nil
		}
		return CapacityVerdict{}, fmt.Errorf("check deploy capacity: %w", err)
	}
	return CapacityVerdict{
		Allowed: resp.Allowed, Code: strings.TrimPrefix(resp.Code, capacityCodePrefix),
		Reason: resp.Reason, Fix: resp.Fix, HasComputePlan: resp.HasComputePlan,
		CeilingCPU: resp.CeilingCPUMillicore.Int64(), CeilingMemory: resp.CeilingMemoryBytes.Int64(),
		CeilingStorage: resp.CeilingStorageGiB.Int64(), Holds: resp.Holds,
	}, nil
}

// CapacityRefusedError is a refused pre-flight. It renders as a runbook:
// what was asked for, what the plan allows, and the literal next step.
type CapacityRefusedError struct {
	Env     string
	Verdict CapacityVerdict
	Demand  CapacityDemand
}

func (e *CapacityRefusedError) Error() string {
	var b strings.Builder
	switch e.Verdict.Code {
	case "PLAN_UNKNOWN":
		fmt.Fprintf(&b, "capacity pre-flight for env %q could not be decided (PLAN_UNKNOWN): %s", e.Env, e.Verdict.Reason)
	default:
		fmt.Fprintf(&b, "capacity pre-flight refused env %q (%s): %s", e.Env, e.Verdict.Code, e.Verdict.Reason)
	}
	fmt.Fprintf(&b, "\n  expected: this deploy needs %s", e.Demand)
	if e.Verdict.HasComputePlan {
		fmt.Fprintf(&b, "\n  found:    a compute plan with a ceiling of %dm CPU, %s memory, %d GiB storage",
			e.Verdict.CeilingCPU, gibString(e.Verdict.CeilingMemory), e.Verdict.CeilingStorage)
	} else {
		b.WriteString("\n  found:    no active compute plan")
	}
	if e.Verdict.Fix != "" {
		fmt.Fprintf(&b, "\n  fix:      %s", e.Verdict.Fix)
	}
	if e.Verdict.Code == "PLAN_UNKNOWN" {
		b.WriteString("\n  nothing was built, pushed or recorded; retry in a moment")
	} else {
		b.WriteString("\n  nothing was built, pushed or recorded")
	}
	return b.String()
}

// Summary is the one-line form for plan and deploy output.
func (v CapacityVerdict) Summary() string {
	switch {
	case v.Unchecked:
		return "not pre-checked (control plane predates CheckDeployCapacity; enforced at record/promote)"
	case v.Allowed && v.HasComputePlan:
		return fmt.Sprintf("OK (ceiling %dm CPU, %s memory, %d GiB storage)", v.CeilingCPU, gibString(v.CeilingMemory), v.CeilingStorage)
	case v.Allowed:
		return "OK (nothing that needs a plan)"
	case v.Queued():
		return "QUEUED on " + v.Holds[0].Label() + " — accepted and recorded, live once that is done: " + v.Reason
	default:
		return "REFUSED (" + v.Code + "): " + v.Reason
	}
}
