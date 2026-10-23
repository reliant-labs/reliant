package obsquery

import (
	"sort"
	"strings"
)

// Signal selects the table family a key or filter is compiled against.
type Signal int

// The signals.
const (
	SigTraces Signal = iota + 1
	SigLogs
	SigMetrics
)

// SignalOf maps a dashboard panel's signal name to a Signal.
func SignalOf(name string) (Signal, bool) {
	switch name {
	case "traces":
		return SigTraces, true
	case "logs":
		return SigLogs, true
	case "metrics":
		return SigMetrics, true
	}
	return 0, false
}

// attrColumn is the signal's own attribute map.
func (s Signal) attrColumn() string {
	switch s {
	case SigLogs:
		return "LogAttributes"
	case SigMetrics:
		return "Attributes"
	}
	return "SpanAttributes"
}

// Key classes. A resource key reads the resource only, so a data-point
// attribute can never spoof identity; every other key reads the signal's own
// attributes first and the resource's second.
const (
	classResource = iota + 1
	classSpan
	classEvent
	classMetric
)

// The allowlist of attribute keys a caller may filter or group by (console v2,
// contract K1). Anything else is refused; a metric's own declared attributes
// are the one extension (Scope.MetricKeys).
var attrClasses = func() map[string]int {
	m := map[string]int{}
	for _, k := range []string{
		"service.name", "service.version", "service.namespace", "service.instance.id",
		"reliant.env.id", "reliant.deployment.id", "deployment.environment.name",
		"k8s.namespace.name", "k8s.node.name", "k8s.pod.name", "k8s.pod.uid",
		"k8s.workload.name", "k8s.container.name",
		"container.image.name", "container.image.tag",
	} {
		m[k] = classResource
	}
	for _, k := range []string{
		"status", "rpc.method", "rpc.service", "rpc.system",
		"http.request.method", "http.method", "http.route",
		"http.response.status_code", "http.status_code", "server.address",
		"db.system", "db.operation.name", "db.operation",
		"messaging.system", "messaging.destination.name", "error.type", "peer.service",
	} {
		m[k] = classSpan
	}
	for _, k := range []string{
		"k8s.event.reason", "k8s.event.type", "k8s.event.count", "k8s.object.kind", "k8s.object.name",
	} {
		m[k] = classEvent
	}
	m["k8s.container.last_terminated_reason"] = classMetric
	return m
}()

// AllowedKeys lists the allowlisted keys, sorted.
func AllowedKeys() []string {
	out := make([]string, 0, len(attrClasses))
	for k := range attrClasses {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Allowed reports whether key is on the allowlist.
func Allowed(key string) bool { _, ok := attrClasses[key]; return ok }

// NeedsDeclared reports whether any key is outside the allowlist, so a metric
// query must look up the metric's declared attribute keys before compiling.
func NeedsDeclared(keys ...string) bool {
	for _, k := range keys {
		if k != "" && !Allowed(k) && !hasAttrPrefix(k) {
			return true
		}
	}
	return false
}

// hasAttrPrefix reports whether a key is in a panel's field vocabulary
// (metric.attr.<k>, resource.attr.<k>), which is open-ended by design.
func hasAttrPrefix(k string) bool {
	return strings.HasPrefix(k, "metric.attr.") || strings.HasPrefix(k, "resource.attr.")
}

const (
	statusExpr = "lower(replaceOne(StatusCode, 'STATUS_CODE_', ''))"
	envIDExpr  = "ResourceAttributes['reliant.env.id']"
)

// classify validates a key against the allowlist (and, for metrics, the
// declared keys) and returns its class. It binds nothing.
func (b *builder) classify(sig Signal, key string) (int, error) {
	if key == "" || len(key) > 128 {
		return 0, invalid("attribute keys must be 1-128 characters")
	}
	if cls, ok := attrClasses[key]; ok {
		return cls, nil
	}
	if sig == SigMetrics && b.declared[key] {
		return classMetric, nil
	}
	return 0, invalid("%q is not a filterable attribute: use one of the allowlisted resource, span or event keys (or, for a metric, one of its declared attributes)", key)
}

// keyExpr compiles an allowlisted key to a String expression over the signal's
// columns, and reports whether the key is a resource-level one.
func (b *builder) keyExpr(sig Signal, key string) (string, bool, error) {
	cls, err := b.classify(sig, key)
	if err != nil {
		return "", false, err
	}
	switch key {
	case "service.name":
		return "ServiceName", true, nil
	case "deployment.environment.name":
		return b.envNameExpr(), true, nil
	case "status":
		if sig == SigTraces {
			return statusExpr, false, nil
		}
	}
	k := b.str(key)
	if cls == classResource {
		return "ResourceAttributes[" + k + "]", true, nil
	}
	col := sig.attrColumn()
	return "if(mapContains(" + col + ", " + k + "), " + col + "[" + k + "], ResourceAttributes[" + k + "])", false, nil
}

// groupExpr is keyExpr for a group-by: a panel-vocabulary key (metric.attr.<k>,
// resource.attr.<k>) is read as that map entry.
func (b *builder) groupExpr(sig Signal, key string) (string, bool, error) {
	switch {
	case strings.HasPrefix(key, "metric.attr."):
		if sig != SigMetrics {
			return "", false, invalid("%q groups metrics only", key)
		}
		k, err := attrKey(key, "metric.attr.")
		if err != nil {
			return "", false, err
		}
		return "Attributes[" + b.str(k) + "]", false, nil
	case strings.HasPrefix(key, "resource.attr."):
		k, err := attrKey(key, "resource.attr.")
		if err != nil {
			return "", false, err
		}
		return "ResourceAttributes[" + b.str(k) + "]", true, nil
	}
	return b.keyExpr(sig, key)
}

// envNameExpr is the deployment.environment.name expression: the Reliant
// environment's name, resolved from reliant.env.id (node-derived infra
// telemetry carries no deployment.environment.name), falling back to the
// attribute the application emitted.
func (b *builder) envNameExpr() string {
	emitted := "ResourceAttributes['deployment.environment.name']"
	if len(b.envs) == 0 {
		return emitted
	}
	ids := make([]string, len(b.envs))
	names := make([]string, len(b.envs))
	for i, e := range b.envs {
		ids[i], names[i] = e.ID, e.Name
	}
	return "transform(" + envIDExpr + ", " + b.strArray(ids) + ", " + b.strArray(names) + ", " + emitted + ")"
}

// chStringLit renders s as a single-quoted ClickHouse string literal.
func chStringLit(s string) string {
	var sb strings.Builder
	sb.WriteByte('\'')
	for _, r := range s {
		switch r {
		case '\\':
			sb.WriteString(`\\`)
		case '\'':
			sb.WriteString(`\'`)
		case '\n':
			sb.WriteString(`\n`)
		case '\r':
			sb.WriteString(`\r`)
		case '\t':
			sb.WriteString(`\t`)
		case 0:
			sb.WriteString(`\0`)
		default:
			sb.WriteRune(r)
		}
	}
	sb.WriteByte('\'')
	return sb.String()
}

// strArray binds vals as ONE Array(String) parameter.
func (b *builder) strArray(vals []string) string {
	lits := make([]string, len(vals))
	for i, v := range vals {
		lits[i] = chStringLit(v)
	}
	return b.add("Array(String)", "["+strings.Join(lits, ",")+"]")
}

// AttrFilter is a structured attribute predicate (K1 filters[]).
type AttrFilter struct {
	Key    string
	Op     string // "" | "eq" | "neq" | "in" | "contains" | "exists"
	Value  string
	Values []string
}

func (f AttrFilter) tuple() string {
	return f.Key + "\x01" + f.Op + "\x01" + f.Value + "\x01" + strings.Join(f.Values, "\x02")
}

// filterCond compiles one filter over a signal; resource reports whether the
// key is a resource-level one.
func (b *builder) filterCond(sig Signal, f AttrFilter) (cond string, resource bool, err error) {
	if f.Op == "exists" {
		return b.existsCond(sig, f)
	}
	expr, resource, err := b.keyExpr(sig, f.Key)
	if err != nil {
		return "", false, err
	}
	if len(f.Value) > maxPanelValueLen {
		return "", false, invalid("filter value is too long")
	}
	lower := f.Key == "status" && sig == SigTraces
	val := f.Value
	if lower {
		val = strings.ToLower(val)
	}
	switch f.Op {
	case "", "eq":
		return expr + " = " + b.str(val), resource, nil
	case "neq":
		return expr + " != " + b.str(val), resource, nil
	case "contains":
		if f.Value == "" {
			return "", false, invalid("a contains filter needs a value")
		}
		return "positionCaseInsensitive(" + expr + ", " + b.str(f.Value) + ") > 0", resource, nil
	case "in":
		l, err := b.strList(f.Values, lower)
		if err != nil {
			return "", false, err
		}
		return expr + " IN (" + l + ")", resource, nil
	}
	return "", false, invalid("unknown filter operator %q", f.Op)
}

func (b *builder) existsCond(sig Signal, f AttrFilter) (string, bool, error) {
	cls, err := b.classify(sig, f.Key)
	if err != nil {
		return "", false, err
	}
	switch {
	case f.Key == "service.name":
		return "ServiceName != ''", true, nil
	case f.Key == "deployment.environment.name":
		return b.envNameExpr() + " != ''", true, nil
	case f.Key == "status" && sig == SigTraces:
		return "StatusCode != ''", false, nil
	case cls == classResource:
		return "mapContains(ResourceAttributes, " + b.str(f.Key) + ")", true, nil
	}
	k := b.str(f.Key)
	return "(mapContains(" + sig.attrColumn() + ", " + k + ") OR mapContains(ResourceAttributes, " + k + "))", false, nil
}
