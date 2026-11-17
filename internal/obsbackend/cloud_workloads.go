package obsbackend

// Workload health over the control plane: ListWorkloads, which reads the
// cluster's own state (restarts, termination reasons, readiness, Kubernetes
// warning events and Job runs) from the node and cluster collectors and the
// deploy ledger. A hosted user has no kubeconfig; this is how they see it.
// The wire shapes are declared locally (protojson: camelCase, int64 as a
// string, enums by name); the field names are those of control-plane
// proto/services/observability/v1.

import (
	"context"
	"sort"
	"strings"
	"time"
)

const procListWorkloads = "controlplane.v1.ObservabilityService/ListWorkloads"

// Workload health verdicts, the control plane's WorkloadHealth names lower
// case and unprefixed.
const (
	WorkloadHealthy      = "healthy"
	WorkloadDegraded     = "degraded"
	WorkloadCrashLooping = "crash_looping"
	WorkloadPending      = "pending"
	WorkloadSuspended    = "suspended"
	WorkloadUnknown      = "unknown"
)

// Job results, the control plane's WorkloadJobResult names lower case and
// unprefixed.
const (
	JobRunning   = "running"
	JobSucceeded = "succeeded"
	JobFailed    = "failed"
)

// WorkloadEvent is a Kubernetes event about a workload.
type WorkloadEvent struct {
	At         time.Time `json:"at,omitzero"`
	Type       string    `json:"type,omitempty"`
	Reason     string    `json:"reason,omitempty"`
	Plain      string    `json:"plain_reason,omitempty"`
	Message    string    `json:"message,omitempty"`
	Count      int64     `json:"count,omitempty"`
	ObjectKind string    `json:"object_kind,omitempty"`
	ObjectName string    `json:"object_name,omitempty"`
}

// WorkloadContainer is one container's restarts and last termination.
type WorkloadContainer struct {
	Container        string    `json:"container"`
	Image            string    `json:"image,omitempty"`
	Restarts         int64     `json:"restarts"`
	LastExitReason   string    `json:"last_exit_reason,omitempty"`
	LastExitAt       time.Time `json:"last_exit_at,omitzero"`
	MemoryLimitRatio *float64  `json:"memory_limit_ratio,omitempty"`
}

// Workload is one workload's health over the range.
type Workload struct {
	EnvID     string `json:"env_id,omitempty"`
	EnvName   string `json:"env_name,omitempty"`
	Workload  string `json:"workload"`
	Namespace string `json:"namespace,omitempty"`
	Kind      string `json:"kind,omitempty"`
	Health    string `json:"health"`
	// Cause says whose side a fault is on: workload, platform, undetermined.
	Cause         string              `json:"cause,omitempty"`
	Reason        string              `json:"reason,omitempty"`
	Detail        string              `json:"detail,omitempty"`
	Since         time.Time           `json:"since,omitzero"`
	SinceKnown    bool                `json:"since_known,omitempty"`
	Ready         *int32              `json:"ready,omitempty"`
	Desired       *int32              `json:"desired,omitempty"`
	Restarts      int64               `json:"restarts"`
	LastExit      string              `json:"last_exit_reason,omitempty"`
	LastExitAt    time.Time           `json:"last_exit_at,omitzero"`
	Release       string              `json:"release,omitempty"`
	Version       string              `json:"service_version,omitempty"`
	Containers    []WorkloadContainer `json:"containers,omitempty"`
	LatestWarning *WorkloadEvent      `json:"latest_warning,omitempty"`
}

// Unhealthy reports whether the workload needs a look: any verdict but
// healthy or suspended, or a restart in the range.
func (w Workload) Unhealthy() bool {
	return (w.Health != WorkloadHealthy && w.Health != WorkloadSuspended) || w.Restarts > 0
}

// Job is one Kubernetes Job run.
type Job struct {
	EnvName    string    `json:"env_name,omitempty"`
	Name       string    `json:"name"`
	Workload   string    `json:"workload,omitempty"`
	Result     string    `json:"result"`
	Reason     string    `json:"reason,omitempty"`
	Release    string    `json:"release,omitempty"`
	StartedAt  time.Time `json:"started_at,omitzero"`
	FinishedAt time.Time `json:"finished_at,omitzero"`
	Seconds    int64     `json:"duration_seconds,omitempty"`
}

// WorkloadsResult is the env's workloads, worst first, and its Job runs,
// newest first.
type WorkloadsResult struct {
	Workloads     []Workload
	Truncated     bool
	Jobs          []Job
	JobsTruncated bool
}

type wireWorkloadEvent struct {
	Time        time.Time `json:"time"`
	Type        string    `json:"type"`
	Reason      string    `json:"reason"`
	PlainReason string    `json:"plainReason"`
	Message     string    `json:"message"`
	Count       jsonInt   `json:"count"`
	ObjectKind  string    `json:"objectKind"`
	ObjectName  string    `json:"objectName"`
}

type wireWorkload struct {
	EnvID            string  `json:"envId"`
	EnvName          string  `json:"envName"`
	Workload         string  `json:"workload"`
	Namespace        string  `json:"namespace"`
	ServiceVersion   string  `json:"serviceVersion"`
	RestartsInWindow jsonInt `json:"restartsInWindow"`
	Containers       []struct {
		Container            string    `json:"container"`
		ImageName            string    `json:"imageName"`
		ImageTag             string    `json:"imageTag"`
		RestartsInWindow     jsonInt   `json:"restartsInWindow"`
		LastTerminatedReason string    `json:"lastTerminatedReason"`
		LastTerminatedAt     time.Time `json:"lastTerminatedAt"`
		Memory               struct {
			LimitUtilizationLatest *float64 `json:"limitUtilizationLatest"`
		} `json:"memory"`
	} `json:"containers"`
	LatestWarning    *wireWorkloadEvent `json:"latestWarning"`
	Kind             string             `json:"kind"`
	Health           string             `json:"health"`
	HealthReason     string             `json:"healthReason"`
	ReadyReplicas    *int32             `json:"readyReplicas"`
	DesiredReplicas  *int32             `json:"desiredReplicas"`
	ReleaseVersion   string             `json:"releaseVersion"`
	HealthCause      string             `json:"healthCause"`
	HealthDetail     string             `json:"healthDetail"`
	HealthSince      time.Time          `json:"healthSince"`
	HealthSinceKnown bool               `json:"healthSinceKnown"`
}

type wireWorkloadJob struct {
	EnvName         string    `json:"envName"`
	Name            string    `json:"name"`
	Workload        string    `json:"workload"`
	Result          string    `json:"result"`
	Reason          string    `json:"reason"`
	ReleaseVersion  string    `json:"releaseVersion"`
	StartedAt       time.Time `json:"startedAt"`
	FinishedAt      time.Time `json:"finishedAt"`
	DurationSeconds jsonInt   `json:"durationSeconds"`
}

// Workloads reads ListWorkloads for the env over n.Range (at most 7 days).
// n.Services narrows by workload name.
func (c Cloud) Workloads(ctx context.Context, n Narrow) (WorkloadsResult, error) {
	var out struct {
		Workloads     []wireWorkload    `json:"workloads"`
		Truncated     bool              `json:"truncated"`
		Jobs          []wireWorkloadJob `json:"jobs"`
		JobsTruncated bool              `json:"jobsTruncated"`
	}
	if err := c.call(ctx, procListWorkloads, map[string]any{"scope": c.scope(n)}, &out); err != nil {
		return WorkloadsResult{}, err
	}
	res := WorkloadsResult{Truncated: out.Truncated, JobsTruncated: out.JobsTruncated}
	for _, w := range out.Workloads {
		res.Workloads = append(res.Workloads, workloadOfWire(w))
	}
	sort.SliceStable(res.Workloads, func(i, j int) bool {
		a, b := res.Workloads[i], res.Workloads[j]
		if ra, rb := healthRank(a.Health), healthRank(b.Health); ra != rb {
			return ra < rb
		}
		if a.Restarts != b.Restarts {
			return a.Restarts > b.Restarts
		}
		return a.Workload < b.Workload
	})
	for _, j := range out.Jobs {
		res.Jobs = append(res.Jobs, Job{
			EnvName: j.EnvName, Name: j.Name, Workload: j.Workload, Result: enumName(j.Result, "WORKLOAD_JOB_RESULT_"),
			Reason: j.Reason, Release: j.ReleaseVersion, StartedAt: unsetEpoch(j.StartedAt), FinishedAt: unsetEpoch(j.FinishedAt),
			Seconds: int64(j.DurationSeconds),
		})
	}
	return res, nil
}

func workloadOfWire(w wireWorkload) Workload {
	out := Workload{
		EnvID: w.EnvID, EnvName: w.EnvName, Workload: w.Workload, Namespace: w.Namespace,
		Kind: enumName(w.Kind, "WORKLOAD_KIND_"), Health: enumName(w.Health, "WORKLOAD_HEALTH_"),
		Cause: enumName(w.HealthCause, "WORKLOAD_CAUSE_"), Reason: w.HealthReason, Detail: w.HealthDetail,
		Since: unsetEpoch(w.HealthSince), SinceKnown: w.HealthSinceKnown,
		Ready: w.ReadyReplicas, Desired: w.DesiredReplicas, Restarts: int64(w.RestartsInWindow),
		Release: w.ReleaseVersion, Version: w.ServiceVersion,
	}
	if out.Health == "" {
		out.Health = WorkloadUnknown
	}
	if out.Cause == "none" {
		out.Cause = ""
	}
	for _, c := range w.Containers {
		img := c.ImageName
		if c.ImageTag != "" {
			img += ":" + c.ImageTag
		}
		wc := WorkloadContainer{
			Container: c.Container, Image: img, Restarts: int64(c.RestartsInWindow),
			LastExitReason: c.LastTerminatedReason, LastExitAt: unsetEpoch(c.LastTerminatedAt),
			MemoryLimitRatio: c.Memory.LimitUtilizationLatest,
		}
		out.Containers = append(out.Containers, wc)
		// The workload's last exit is its latest container termination.
		if wc.LastExitReason != "" && (out.LastExit == "" || wc.LastExitAt.After(out.LastExitAt)) {
			out.LastExit, out.LastExitAt = wc.LastExitReason, wc.LastExitAt
		}
	}
	if e := w.LatestWarning; e != nil && e.Reason != "" {
		out.LatestWarning = &WorkloadEvent{
			At: unsetEpoch(e.Time), Type: e.Type, Reason: e.Reason, Plain: e.PlainReason, Message: e.Message,
			Count: int64(e.Count), ObjectKind: e.ObjectKind, ObjectName: e.ObjectName,
		}
	}
	return out
}

// enumName is a protojson enum name lower case without its prefix; the
// UNSPECIFIED value is "".
func enumName(v, prefix string) string {
	v = strings.ToLower(strings.TrimPrefix(v, prefix))
	if v == "unspecified" {
		return ""
	}
	return v
}

// healthRank orders the worst verdict first.
func healthRank(h string) int {
	switch h {
	case WorkloadCrashLooping:
		return 0
	case WorkloadDegraded:
		return 1
	case WorkloadPending:
		return 2
	case WorkloadUnknown:
		return 3
	case WorkloadHealthy:
		return 4
	}
	return 5
}
