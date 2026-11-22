package metrics

import "fmt"

// builtinSince is the forge version the built-in set was first declared in.
const (
	builtinSince = "v0.1.45"
	// predates the registry: the metric existed, the declaration did not.
	legacySince = "v0.1.0"
	// drainSince is the release serverkit's shutdown phases were first measured in.
	drainSince = "v0.1.46"
	// resourceSince is the release pkg/resmetrics first reported in.
	resourceSince = "v1.10.0"
)

const rejectedName = "forge.metrics.rejected_attributes"

// connectCodes are the values connect.Code.String() yields for a failed rpc.
var connectCodes = []string{
	"canceled", "unknown", "invalid_argument", "deadline_exceeded", "not_found",
	"already_exists", "permission_denied", "resource_exhausted", "failed_precondition",
	"aborted", "out_of_range", "unimplemented", "internal", "unavailable",
	"data_loss", "unauthenticated",
}

func attr(key string, values ...string) Attribute { return Attribute{Key: key, Values: values} }

// builtinSpecs is every metric forge's own runtime emits. Three groups:
//
//   - rpc.server.*: the Connect interceptor (pkg/observe), created through the
//     registry from BuiltinSpec;
//   - go.*: the Go runtime instrumentation and the SQL pool/query metrics,
//     created by those libraries and described here with the units they emit;
//   - forge.metrics.*: the registry's own bookkeeping;
//   - process.*, system.*, forge.process.*, forge.system.*, forge.cgroup.*
//     and forge.resource.scope: the process and host resource metrics of
//     pkg/resmetrics (resourceSpecs).
//
// Per-component call metrics (<package>.calls, .errors, .duration) are
// declared as components are constructed, see DescribeCallMetrics.
func builtinSpecs() []Spec {
	return append([]Spec{
		{Name: "rpc.server.calls", Type: Counter, Unit: "{call}", Description: "Total RPC calls received.", Attributes: []Attribute{attr("procedure")}, Stability: Stable, Since: legacySince},
		{Name: "rpc.server.errors", Type: Counter, Unit: "{error}", Description: "Total RPC calls that returned an error, by connect code and error class (server fault, user error, canceled). A call the server ended on purpose (svcerr.ExpectedClose) is not counted.", Attributes: []Attribute{attr("procedure"), attr("code", connectCodes...), attr("error_class", "server", "user", "canceled")}, Stability: Stable, Since: legacySince},
		{Name: "rpc.server.duration", Type: Histogram, Unit: "s", Description: "RPC duration.", Attributes: []Attribute{attr("procedure")}, Stability: Stable, Since: legacySince},

		{Name: "go.memory.used", Type: UpDownCounter, Unit: "By", Description: "Memory used by the Go runtime.", Attributes: []Attribute{attr("go.memory.type", "stack", "other")}, Stability: Stable, Since: legacySince},
		{Name: "go.memory.limit", Type: UpDownCounter, Unit: "By", Description: "Go runtime memory limit configured by the user, when one exists.", Stability: Stable, Since: legacySince},
		{Name: "go.memory.allocated", Type: Counter, Unit: "By", Description: "Memory allocated to the heap by the application.", Stability: Stable, Since: legacySince},
		{Name: "go.memory.allocations", Type: Counter, Unit: "{allocation}", Description: "Count of allocations to the heap by the application.", Stability: Stable, Since: legacySince},
		{Name: "go.memory.gc.goal", Type: UpDownCounter, Unit: "By", Description: "Heap size target for the end of the GC cycle.", Stability: Stable, Since: legacySince},
		{Name: "go.goroutine.count", Type: UpDownCounter, Unit: "{goroutine}", Description: "Count of live goroutines.", Stability: Stable, Since: legacySince},
		{Name: "go.processor.limit", Type: UpDownCounter, Unit: "{thread}", Description: "The number of OS threads that can execute user-level Go code simultaneously.", Stability: Stable, Since: legacySince},
		{Name: "go.config.gogc", Type: UpDownCounter, Unit: "%", Description: "Heap size target percentage configured by the user, otherwise 100.", Stability: Stable, Since: legacySince},

		{Name: "go.sql.query_timing", Type: Histogram, Unit: "ms", Description: "Timing of processed database queries.", Attributes: []Attribute{attr("db.operation"), attr("db.sql.table"), attr("db.system")}, Stability: Stable, Since: legacySince},
		{Name: "go.sql.connections_max_open", Type: Gauge, Unit: "{connection}", Description: "Maximum number of open connections to the database.", Attributes: []Attribute{attr("db.system")}, Stability: Stable, Since: legacySince},
		{Name: "go.sql.connections_open", Type: Gauge, Unit: "{connection}", Description: "Established connections, in use and idle.", Attributes: []Attribute{attr("db.system")}, Stability: Stable, Since: legacySince},
		{Name: "go.sql.connections_in_use", Type: Gauge, Unit: "{connection}", Description: "Connections currently in use.", Attributes: []Attribute{attr("db.system")}, Stability: Stable, Since: legacySince},
		{Name: "go.sql.connections_idle", Type: Gauge, Unit: "{connection}", Description: "Idle connections.", Attributes: []Attribute{attr("db.system")}, Stability: Stable, Since: legacySince},
		{Name: "go.sql.connections_wait_count", Type: Counter, Unit: "{wait}", Description: "Total number of times a caller waited for a connection.", Attributes: []Attribute{attr("db.system")}, Stability: Stable, Since: legacySince},
		{Name: "go.sql.connections_wait_duration", Type: Counter, Unit: "ns", Description: "Total time blocked waiting for a new connection.", Attributes: []Attribute{attr("db.system")}, Stability: Stable, Since: legacySince},
		{Name: "go.sql.connections_closed_max_idle", Type: Counter, Unit: "{connection}", Description: "Connections closed because of SetMaxIdleConns.", Attributes: []Attribute{attr("db.system")}, Stability: Stable, Since: legacySince},
		{Name: "go.sql.connections_closed_max_idle_time", Type: Counter, Unit: "{connection}", Description: "Connections closed because of SetConnMaxIdleTime.", Attributes: []Attribute{attr("db.system")}, Stability: Stable, Since: legacySince},
		{Name: "go.sql.connections_closed_max_lifetime", Type: Counter, Unit: "{connection}", Description: "Connections closed because of SetConnMaxLifetime.", Attributes: []Attribute{attr("db.system")}, Stability: Stable, Since: legacySince},

		{Name: "forge.server.drain.duration", Type: Histogram, Unit: "s", Description: "How long each phase of a graceful shutdown took: the pre-stop pause, the listener drain (unary requests and long-lived streams), stopping workers and operators, the caller's teardown, each node a lifecyclekit supervisor stops (node), and the whole shutdown (total).", Attributes: []Attribute{attr("phase", "pre_stop", "listener", "workers", "operators", "teardown", "node", "total")}, Stability: Experimental, Since: drainSince},

		{Name: rejectedName, Type: Counter, Unit: "{attribute}", Description: "Measurements whose attribute was not in the metric's declaration, corrected before export. A rising value means code records an attribute the schema does not list.", Attributes: []Attribute{attr("metric"), attr("reason", RejectUndeclaredKey, RejectUndeclaredValue)}, Stability: Experimental, Since: builtinSince},
	}, resourceSpecs()...)
}

// resourceSpecs are the process and host resource metrics pkg/resmetrics
// emits. The names follow the OpenTelemetry semantic conventions for process.*
// and system.* (development status, so they are pinned here as literals, not
// taken from a semconv package) and are forge.* where the conventions have no
// name. Every attribute is closed over a listed set: no datapoint names a
// command line, an executable, a user, a pid, a host or a mount path.
func resourceSpecs() []Spec {
	cpuModes := attr("cpu.mode", "user", "system", "idle", "iowait", "steal")
	role := attr("forge.filesystem.role", "cwd", "tmp", "data")
	pressure := []Attribute{attr("resource", "cpu", "memory", "io"), attr("kind", "some", "full")}
	spec := func(name string, t Type, unit, desc string, attrs ...Attribute) Spec {
		return Spec{Name: name, Type: t, Unit: unit, Description: desc, Attributes: attrs, Stability: Experimental, Since: resourceSince}
	}
	return []Spec{
		// Group process: the binary itself.
		spec("process.cpu.time", Counter, "s", "CPU time the process has used, by mode.", attr("cpu.mode", "user", "system")),
		spec("process.cpu.utilization", Gauge, "1", "CPU the process used since the previous sample, in cores (above 1 when more than one core is busy)."),
		spec("process.memory.usage", UpDownCounter, "By", "Resident set size of the process."),
		spec("process.memory.virtual", UpDownCounter, "By", "Virtual memory size of the process."),
		spec("forge.process.memory.go_gap", Gauge, "By", "Resident set size minus the memory the Go runtime has mapped: what cgo, mmap and fragmentation hold. Negative when the runtime has mapped more than is resident."),
		spec("process.unix.file_descriptor.count", UpDownCounter, "{fd}", "Open file descriptors of the process."),
		spec("forge.process.file_descriptor.limit", Gauge, "{fd}", "Soft RLIMIT_NOFILE of the process."),
		spec("forge.process.file_descriptor.utilization", Gauge, "1", "Open file descriptors over the soft limit."),
		spec("process.thread.count", UpDownCounter, "{thread}", "OS threads of the process."),
		spec("process.context_switches", Counter, "{switch}", "Times the scheduler switched away from the process, by whether it yielded.", attr("process.context_switch_type", "voluntary", "involuntary")),
		spec("process.paging.faults", Counter, "{fault}", "Page faults the process has taken, by type.", attr("system.paging.fault.type", "major", "minor")),
		spec("process.disk.io", Counter, "By", "Bytes the process caused to be read from or written to storage.", attr("disk.io.direction", "read", "write")),
		spec("process.uptime", Gauge, "s", "Time since the process started."),
		spec("forge.process.handle.count", UpDownCounter, "{handle}", "Open kernel object handles of the process (windows, which has no file descriptors)."),

		// Group host: the machine, or the container's share of it.
		spec("forge.resource.scope", Gauge, "{scope}", "1 for the view the host metrics report: host (the whole machine) or cgroup (inside a Kubernetes pod, the container only).", attr("scope", "host", "cgroup")),
		spec("system.cpu.time", Counter, "s", "CPU time of the whole machine by mode, summed over every CPU. Nice time is counted as user, interrupt time as system.", cpuModes),
		spec("system.cpu.utilization", Gauge, "1", "Share of all CPU time in each mode since the previous sample, from 0 to 1.", cpuModes),
		spec("system.cpu.logical.count", UpDownCounter, "{cpu}", "Logical CPUs the process may run on, lowered to the cgroup's CPU quota when one is set."),
		spec("system.cpu.load_average.1m", Gauge, "{thread}", "Run-queue load average over 1 minute."),
		spec("system.cpu.load_average.5m", Gauge, "{thread}", "Run-queue load average over 5 minutes."),
		spec("system.cpu.load_average.15m", Gauge, "{thread}", "Run-queue load average over 15 minutes."),
		spec("system.memory.usage", UpDownCounter, "By", "Memory of the machine by state; cached is page cache, buffers and reclaimable slab, and the three add up to the total.", attr("system.memory.state", "used", "free", "cached")),
		spec("system.memory.limit", UpDownCounter, "By", "Memory the process may use: the cgroup's memory.max when set, otherwise the machine's total."),
		spec("system.linux.memory.available", UpDownCounter, "By", "The kernel's estimate of memory available to a new workload without swapping (MemAvailable)."),
		spec("system.paging.usage", UpDownCounter, "By", "Swap space of the machine by state.", attr("system.paging.state", "used", "free")),
		spec("system.filesystem.usage", UpDownCounter, "By", "Space on the volumes the process writes to, by state and by the volume's role (never its path). Free is what an unprivileged process may use.", attr("system.filesystem.state", "used", "free"), role),
		spec("system.filesystem.utilization", Gauge, "1", "Used space over used plus free, per volume role.", role),
		spec("forge.system.file_descriptor.count", Gauge, "{fd}", "File handles allocated on the machine."),
		spec("forge.system.file_descriptor.limit", Gauge, "{fd}", "The machine's file handle limit (fs.file-max)."),
		spec("forge.system.process.count", Gauge, "{process}", "Processes on the machine."),
		spec("forge.system.process.limit", Gauge, "{process}", "The lower of the kernel's pid_max and threads-max."),

		// Group host, from the process's cgroup v2.
		spec("forge.cgroup.memory.working_set", UpDownCounter, "By", "The cgroup's memory.current minus inactive_file: the kubelet's working set."),
		spec("forge.cgroup.memory.limit", UpDownCounter, "By", "The cgroup's memory.max; absent when it is max."),
		spec("forge.cgroup.memory.oom_kills", Counter, "{kill}", "Processes the kernel killed for the cgroup's memory limit (memory.events oom_kill)."),
		spec("forge.cgroup.pressure.stall_time", Counter, "s", "Time tasks in the cgroup stalled on a resource: some is at least one task, full is every task.", pressure...),
		spec("forge.cgroup.pressure.avg10", Gauge, "1", "Share of the last ten seconds tasks in the cgroup stalled on a resource, from 0 to 1.", pressure...),
		spec("forge.cgroup.cpu.throttled_periods", Counter, "{period}", "CPU quota periods in which the cgroup was throttled."),
		spec("forge.cgroup.cpu.throttled_time", Counter, "s", "Time the cgroup was throttled by its CPU quota."),
		spec("forge.cgroup.pids.count", Gauge, "{pid}", "Tasks in the cgroup (pids.current)."),
		spec("forge.cgroup.pids.limit", Gauge, "{pid}", "The cgroup's pids.max; absent when it is max."),
	}
}

// BuiltinSpec returns the declaration of one of forge's built-in metrics, so the
// code that creates the instrument and the schema cannot disagree about its
// unit or description. It panics for a name that is not built in.
func BuiltinSpec(name string) Spec {
	for _, s := range builtinSpecs() {
		if s.Name == name {
			return s
		}
	}
	panic(fmt.Sprintf("metrics: %q is not a built-in metric", name))
}

// DescribeCallMetrics declares the three per-component metrics the generated
// component wrappers emit — <namespace>.calls, .errors and .duration — on the
// default registry, so the schema lists them. A namespace that is not a valid
// metric name segment is skipped: the wrapper still records, it is just not
// listed.
func DescribeCallMetrics(namespace string) {
	reg := Default()
	method := []Attribute{{Key: "method"}}
	for _, s := range []Spec{
		{Name: namespace + ".calls", Type: Counter, Unit: "{call}", Description: "Total method calls.", Attributes: method},
		{Name: namespace + ".errors", Type: Counter, Unit: "{error}", Description: "Total method errors.", Attributes: method},
		{Name: namespace + ".duration", Type: Histogram, Unit: "s", Description: "Method duration in seconds.", Attributes: method},
	} {
		s.Stability, s.Since = Experimental, legacySince
		_ = reg.Describe(s)
	}
}

// IsBuiltin reports whether name is one of forge's own metrics, as opposed to
// one a project declares.
func IsBuiltin(name string) bool {
	for _, s := range builtinSpecs() {
		if s.Name == name {
			return true
		}
	}
	return false
}
