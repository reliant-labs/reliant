package obsdash

// WorkloadsID is the id of the dashboard BuildWorkloads returns, so its file is
// "<project>-workloads_gen.json".
const WorkloadsID = "workloads"

// The customer-metadata metrics the node agent delivers for every container of
// a workload (console v2 contract K2). kubeletstats supplies usage and the
// utilization ratios, k8s_cluster the absolute requests, limits and restarts.
// All are gauges.
const (
	metricCPUUsage      = "container.cpu.usage"
	metricMemWorkingSet = "container.memory.working_set"
	metricCPULimitUtil  = "k8s.container.cpu_limit_utilization"
	metricCPUReqUtil    = "k8s.container.cpu_request_utilization"
	metricMemLimitUtil  = "k8s.container.memory_limit_utilization"
	metricMemReqUtil    = "k8s.container.memory_request_utilization"
	metricRestarts      = "k8s.container.restarts"
)

// The resource and event attributes the same agent stamps (K2).
const (
	fieldWorkload   = "resource.attr.k8s.workload.name"
	fieldContainer  = "resource.attr.k8s.container.name"
	fieldImageTag   = "resource.attr.container.image.tag"
	fieldNamespace  = "resource.attr.k8s.namespace.name"
	fieldLastReason = "metric.attr.k8s.container.last_terminated_reason"
	fieldEventType  = "log.attr.k8s.event.type"
	fieldEventWhy   = "log.attr.k8s.event.reason"
	fieldEventKind  = "log.attr.k8s.object.kind"
	fieldEventName  = "log.attr.k8s.object.name"
	fieldTerminated = "log.attr.k8s.container.last_terminated_reason"
)

// BuildWorkloads is the "Workloads" dashboard: what the platform knows about
// every workload without the workload instrumenting anything. CPU and memory
// against what the container requested and may use, restarts and why the
// container last exited, the image versions that ran, and the Kubernetes
// events (crash loops, failed pulls, evictions).
//
// It reads node-derived telemetry, which only a deployed environment has: a
// `forge env up` stack draws these panels empty. The owning org and environment
// are stamped from namespace labels, never from the payload, so a workload
// cannot spoof what these panels show.
//
// Every panel is a line, number or search panel: a table over metrics is not
// something the console's query engine runs, so the per-container images and
// versions table is the console's workload view, and the version history is
// drawn here as CPU by image tag.
func BuildWorkloads(project string) Dashboard {
	d := Dashboard{
		ID:    WorkloadsID,
		Title: project + ": workloads",
		Group: GroupPlatform,
		Tags:  []string{"k8s"},
		Description: "Per-workload customer metadata from the node agent: CPU and memory against requests and limits, " +
			"restarts and the last exit reason, the image versions that ran, and Kubernetes events. " +
			"Present in deployed environments only (a local forge env up has no node agent).",
	}
	for _, f := range commonFilters() {
		f.Signal = Metrics
		d.Filters = append(d.Filters, f)
	}
	d.Filters = append(d.Filters,
		Filter{ID: "workload", Label: "k8s.workload.name", Field: fieldWorkload, Signal: Metrics},
		Filter{ID: "container", Label: "k8s.container.name", Field: fieldContainer, Signal: Metrics},
		Filter{ID: "namespace", Label: "k8s.namespace.name", Field: fieldNamespace, Signal: Metrics},
	)

	gauge := func(label string, agg Agg, name string) Series {
		return Series{Label: label, Agg: agg, Field: name, MetricType: "gauge"}
	}
	byWorkload := []string{fieldWorkload}
	line := func(id, title, desc string, s []Series, group []string, u Unit, w int) Panel {
		return Panel{ID: id, Title: title, Description: desc, Kind: KindLine, Signal: Metrics, Series: s, GroupBy: group, Unit: u, Width: w, Height: 7}
	}

	d.Panels = []Panel{
		line("cpu-usage", "CPU usage", "Average CPU cores used per container, one line per workload.",
			[]Series{gauge("cpu", AggAvg, metricCPUUsage)}, byWorkload, UnitNone, 12),
		line("memory-working-set", "Memory working set", "Average working-set bytes per container, one line per workload.",
			[]Series{gauge("memory", AggAvg, metricMemWorkingSet)}, byWorkload, UnitBytes, 12),
		line("cpu-vs-request", "CPU against its request", "The busiest container of each workload as a fraction of the CPU it requested (1 = at the request).",
			[]Series{gauge("of request", AggMax, metricCPUReqUtil)}, byWorkload, UnitPercent, 12),
		line("cpu-vs-limit", "CPU against its limit", "The busiest container of each workload as a fraction of its CPU limit. Near 1 the container is being throttled.",
			[]Series{gauge("of limit", AggMax, metricCPULimitUtil)}, byWorkload, UnitPercent, 12),
		line("memory-vs-request", "Memory against its request", "The fullest container of each workload as a fraction of the memory it requested.",
			[]Series{gauge("of request", AggMax, metricMemReqUtil)}, byWorkload, UnitPercent, 12),
		line("memory-vs-limit", "Memory against its limit", "The fullest container of each workload as a fraction of its memory limit. Near 1 the container is about to be OOM-killed.",
			[]Series{gauge("of limit", AggMax, metricMemLimitUtil)}, byWorkload, UnitPercent, 12),
		line("restarts", "Container restarts", "The highest restart count among each workload's containers; a step is a restart.",
			[]Series{gauge("restarts", AggMax, metricRestarts)}, byWorkload, UnitCount, 12),
		line("restarts-by-exit-reason", "Restarts by last exit reason", "The same restart count split by why the container last terminated (OOMKilled, Error, Completed, ...).",
			[]Series{gauge("restarts", AggMax, metricRestarts)}, []string{fieldLastReason}, UnitCount, 12),
		line("cpu-by-image-tag", "CPU by image version", "CPU used per container, split by the image tag it ran (container.image.tag), so a rollout shows as one line replacing another.",
			[]Series{gauge("cpu", AggAvg, metricCPUUsage)}, []string{fieldImageTag}, UnitNone, 12),
		line("memory-by-image-tag", "Memory by image version", "Working-set bytes per container, split by image tag: a version that leaks memory climbs above its predecessor.",
			[]Series{gauge("memory", AggAvg, metricMemWorkingSet)}, []string{fieldImageTag}, UnitBytes, 12),
		{
			ID: "events-by-reason", Title: "Kubernetes events by reason",
			Description: "Count of Kubernetes events per reason (BackOff, Failed, Evicted, Killing, ...) over time.",
			Kind:        KindLine, Signal: Logs,
			Where:   []Predicate{{Field: fieldEventWhy, Op: OpExists}},
			Series:  []Series{{Label: "events", Agg: AggCount}},
			GroupBy: []string{fieldEventWhy}, Unit: UnitCount, Width: 12, Height: 7,
		},
		{
			ID: "warning-events", Title: "Warning events by workload",
			Description: "Warning-type Kubernetes events per workload: crash loops, failed image pulls, evictions, failed scheduling.",
			Kind:        KindLine, Signal: Logs,
			Where:   []Predicate{{Field: fieldEventType, Op: OpEq, Value: "Warning"}},
			Series:  []Series{{Label: "warnings", Agg: AggCount}},
			GroupBy: byWorkload, Unit: UnitCount, Width: 12, Height: 7,
		},
		{
			ID: "container-terminations", Title: "Container terminations",
			Description: "Events that carry a container's last terminated reason.",
			Kind:        KindSearch, Signal: Logs,
			Where:   []Predicate{{Field: fieldTerminated, Op: OpExists}},
			Columns: []string{"timestamp", fieldWorkload, fieldContainer, fieldTerminated, "log.body"},
			Width:   24, Height: 8,
		},
		{
			ID: "events", Title: "Kubernetes events",
			Description: "Every Kubernetes event of the selected workloads, newest first.",
			Kind:        KindSearch, Signal: Logs,
			Where:   []Predicate{{Field: fieldEventWhy, Op: OpExists}},
			Columns: []string{"timestamp", fieldWorkload, fieldEventType, fieldEventWhy, fieldEventKind, fieldEventName, "log.body"},
			Width:   24, Height: 10,
		},
	}
	return d
}
