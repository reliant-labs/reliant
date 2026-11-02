package metrics

import "fmt"

// builtinSince is the forge version the built-in set was first declared in.
const (
	builtinSince = "v0.1.45"
	// predates the registry: the metric existed, the declaration did not.
	legacySince = "v0.1.0"
	// drainSince is the release serverkit's shutdown phases were first measured in.
	drainSince = "v0.1.46"
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
//   - forge.metrics.*: the registry's own bookkeeping.
//
// Per-component call metrics (<package>.calls, .errors, .duration) are
// declared as components are constructed, see DescribeCallMetrics.
func builtinSpecs() []Spec {
	return []Spec{
		{Name: "rpc.server.calls", Type: Counter, Unit: "{call}", Description: "Total RPC calls received.", Attributes: []Attribute{attr("procedure")}, Stability: Stable, Since: legacySince},
		{Name: "rpc.server.errors", Type: Counter, Unit: "{error}", Description: "Total RPC calls that returned an error, by connect code and error class (server fault, user error, canceled).", Attributes: []Attribute{attr("procedure"), attr("code", connectCodes...), attr("error_class", "server", "user", "canceled")}, Stability: Stable, Since: legacySince},
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
