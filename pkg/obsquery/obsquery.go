// Package obsquery compiles typed observability requests (log search, traces,
// metrics, dashboard panels, workloads, profiles) into parameterized ClickHouse
// SQL over the OTel exporter's tables.
//
// One compiler serves two backends. The control plane runs it against an org's
// per-org database (t_<org>, provisioned by its obstenant schema), and forge
// runs it against a project's local ClickStack (LocalDatabase). The backend
// chooses the database through Scope.Database and runs the Stmt itself: this
// package never opens a connection. Tables a backend does
// not have (the control plane's browser tables, locally) fail at execution, not
// here.
//
// It builds statements and parses results' shape. The caller runs a Stmt on its
// own connection, so the database, not this package, is the org boundary. Where
// a request takes several statements (EvalMetricPanel), the Runner is the
// caller's: obsquery opens no connection and holds no credential or setting.
// What this package adds is that nothing a caller sends can become
// SQL text:
//
//   - every caller-supplied VALUE is a server-side parameter ({pN:Type}),
//     never concatenated;
//   - every IDENTIFIER (database, table, column) is either a constant here or
//     the database name, which must pass IsIdentifier;
//   - the log query language is parsed into an AST and compiled; there is no
//     pass-through of query text.
//
// Raw SQL is the escape hatch beside the compiler, not through it: SQLPanel
// binds a caller's statement to a scope with the dashspec.SQLParam*
// parameters, behind the alerting/dashspec lexical guard.
package obsquery

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Per-RPC maximum time ranges.
const (
	MaxRangeLogs       = 7 * 24 * time.Hour
	MaxRangeTraces     = 7 * 24 * time.Hour
	MaxRangeOverview   = 7 * 24 * time.Hour
	MaxRangeMetrics    = 30 * 24 * time.Hour
	MaxRangeProfiles   = 30 * 24 * time.Hour
	MaxRangeDashboards = 30 * 24 * time.Hour
	minRange           = time.Second
)

// ErrInvalid marks a request the caller must fix (InvalidArgument).
var ErrInvalid = errors.New("invalid request")

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

// ErrInvalidf builds an ErrInvalid error for callers outside this package.
func ErrInvalidf(format string, a ...any) error { return invalid(format, a...) }

func sprintf(format string, a ...any) string { return fmt.Sprintf(format, a...) }

// Stmt is a statement and the server-side parameter values it binds.
type Stmt struct {
	SQL    string
	Params map[string]string
}

// LocalDatabase is the database a local ClickStack's OTel exporter writes to.
const LocalDatabase = "default"

var identRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// IsIdentifier reports whether s may be used unquoted as a ClickHouse database
// name.
func IsIdentifier(s string) bool { return len(s) <= 128 && identRe.MatchString(s) }

// Env is a Reliant environment the caller may see.
type Env struct{ ID, Name string }

// Scope is what every query is bounded by (console v2, contract K1).
type Scope struct {
	// Database qualifies every table: the org's database (t_<org>) in
	// the control plane, LocalDatabase against a local ClickStack.
	Database string
	// Local marks a scope over a project's own local ClickStack. Only a local
	// scope may name LocalDatabase: in the control plane `default` is the
	// server's own database, and an org scope that names it is refused.
	Local bool
	// EnvIDs narrows to these environments (reliant.env.id) when EnvRestricted
	// is set; an empty restricted list matches nothing. Unrestricted = every
	// environment of the org, and rows that carry no environment at all.
	EnvIDs        []string
	EnvRestricted bool
	// Envs are the environments in scope, for resolving
	// deployment.environment.name to the Reliant environment's name.
	Envs []Env
	// Services narrows to these service.name values; empty = all.
	Services []string
	// Filters are generic attribute predicates, ANDed.
	Filters []AttrFilter
	// Declared are the attribute keys the queried metric's own data points
	// carry; the one extension to the key allowlist, for metrics.
	Declared map[string]bool
	// Start inclusive, End exclusive.
	Start, End time.Time
}

// Fingerprint identifies the parts of a scope a page cursor belongs to. The
// time range is deliberately not part of it.
func (s Scope) Fingerprint() []string {
	out := []string{s.Database, strconv.FormatBool(s.EnvRestricted)}
	out = append(out, s.EnvIDs...)
	out = append(out, "svc")
	out = append(out, s.Services...)
	out = append(out, "flt")
	for _, f := range s.Filters {
		out = append(out, f.tuple())
	}
	return out
}

// CheckRange validates the half-open range against a per-RPC maximum.
func CheckRange(start, end time.Time, max time.Duration) error {
	if start.IsZero() || end.IsZero() {
		return invalid("time_range start and end are required")
	}
	d := end.Sub(start)
	if d < minRange {
		return invalid("time_range end must be at least %s after start", minRange)
	}
	if d > max {
		return invalid("time_range of %s exceeds the maximum of %s for this call", d.Round(time.Second), max)
	}
	return nil
}

func (s Scope) validate(max time.Duration) error {
	if err := s.checkDatabase(); err != nil {
		return err
	}
	if len(s.Services) > 50 {
		return invalid("at most 50 services may be named")
	}
	if len(s.Filters) > MaxScopeFilters {
		return invalid("at most %d filters", MaxScopeFilters)
	}
	if len(s.EnvIDs) > MaxScopeEnvs {
		return invalid("at most %d environments", MaxScopeEnvs)
	}
	return CheckRange(s.Start, s.End, max)
}

// checkDatabase refuses a database that is not a plain identifier, and
// LocalDatabase on a scope that is not Local.
func (s Scope) checkDatabase() error {
	if !IsIdentifier(s.Database) {
		return invalid("database %q is not a plain identifier", s.Database)
	}
	if s.Database == LocalDatabase && !s.Local {
		return invalid("database %q is only valid on a local scope", s.Database)
	}
	return nil
}

// Scope limits.
const (
	MaxScopeFilters = 20
	MaxScopeEnvs    = 500
)

// builder allocates parameter names and collects their values. It carries the
// scope's environment names and declared metric keys, which key expressions use.
type builder struct {
	n        int
	params   map[string]string
	envs     []Env
	declared map[string]bool
}

func newBuilder(s Scope) *builder {
	return &builder{params: map[string]string{}, envs: s.Envs, declared: s.Declared}
}

func (b *builder) add(typ, val string) string {
	b.n++
	name := "p" + strconv.Itoa(b.n)
	b.params[name] = val
	return "{" + name + ":" + typ + "}"
}

func (b *builder) str(v string) string            { return b.add("String", v) }
func (b *builder) i64(v int64) string             { return b.add("Int64", strconv.FormatInt(v, 10)) }
func (b *builder) u32(v uint32) string            { return b.add("UInt32", strconv.FormatUint(uint64(v), 10)) }
func (b *builder) f64(v float64) string           { return b.add("Float64", strconv.FormatFloat(v, 'g', -1, 64)) }
func (b *builder) stmt(sql string) Stmt           { return Stmt{SQL: sql, Params: b.params} }
func (b *builder) table(s Scope, t string) string { return s.Database + "." + t }

// nanoTime renders a DateTime64(9) timestamp parameter.
func (b *builder) nanoTime(t time.Time) string {
	return "fromUnixTimestamp64Nano(" + b.i64(t.UnixNano()) + ")"
}

// secTime renders a DateTime (seconds) timestamp parameter.
func (b *builder) secTime(t time.Time) string {
	return "toDateTime(" + b.u32(uint32(t.Unix())) + ")"
}

// rangeCond bounds a DateTime64(9) column to the scope's range.
func (b *builder) rangeCond(col string, s Scope) string {
	return col + " >= " + b.nanoTime(s.Start) + " AND " + col + " < " + b.nanoTime(s.End)
}

// rangeCondSec bounds a DateTime column to the scope's range.
func (b *builder) rangeCondSec(col string, s Scope) string {
	return col + " >= " + b.secTime(s.Start) + " AND " + col + " < " + b.secTime(s.End)
}

// envCond restricts to the scope's environments, or returns "" when it does not.
func (b *builder) envCond(s Scope) string {
	if !s.EnvRestricted {
		return ""
	}
	switch len(s.EnvIDs) {
	case 0:
		return "1 = 0"
	case 1:
		return envIDExpr + " = " + b.str(s.EnvIDs[0])
	}
	return "has(" + b.strArray(s.EnvIDs) + ", " + envIDExpr + ")"
}

// scopeBase is the environment and service predicates shared by every signal.
func (b *builder) scopeBase(s Scope) []string {
	var c []string
	if e := b.envCond(s); e != "" {
		c = append(c, e)
	}
	if sv := b.serviceIn(s.Services); sv != "" {
		c = append(c, sv)
	}
	return c
}

// scopeConds is scopeBase plus the scope's attribute filters compiled for sig.
func (b *builder) scopeConds(sig Signal, s Scope) ([]string, error) {
	where, _, err := b.scopeParts(sig, s, false)
	return where, err
}

// scopeParts splits the scope's filters: with spanHaving, span-level filters
// come back as per-trace HAVING terms (a trace is kept when ANY of its spans
// matches) while resource-level ones stay in WHERE.
func (b *builder) scopeParts(sig Signal, s Scope, spanHaving bool) (where, having []string, err error) {
	where = b.scopeBase(s)
	for _, f := range s.Filters {
		c, resource, err := b.filterCond(sig, f)
		if err != nil {
			return nil, nil, err
		}
		if spanHaving && !resource {
			having = append(having, "countIf("+c+") > 0")
			continue
		}
		where = append(where, c)
	}
	return where, having, nil
}

// resourceConds is scopeBase plus only the RESOURCE-level filters, for a signal
// the span-level ones do not describe.
func (b *builder) resourceConds(sig Signal, s Scope) ([]string, error) {
	where := b.scopeBase(s)
	for _, f := range s.Filters {
		// Classify BEFORE compiling: compiling binds parameters, and a filter
		// that is then dropped would leave them bound but unused.
		cls, err := b.filterClass(sig, f.Key)
		if err != nil {
			return nil, err
		}
		if cls != classResource {
			continue
		}
		c, _, err := b.filterCond(sig, f)
		if err != nil {
			return nil, err
		}
		where = append(where, c)
	}
	return where, nil
}

func (b *builder) serviceIn(services []string) string {
	if len(services) == 0 {
		return ""
	}
	ps := make([]string, len(services))
	for i, v := range services {
		ps[i] = b.str(v)
	}
	return "ServiceName IN (" + strings.Join(ps, ", ") + ")"
}

func where(conds []string) string {
	if len(conds) == 0 {
		return ""
	}
	return "WHERE " + strings.Join(conds, "\n  AND ")
}

// OperatorSource reports whether a reliant.source value marks Reliant operator
// data (the platform's own telemetry, present only in the operator's own org).
func OperatorSource(source string) bool {
	switch source {
	case "daemon", "platform", "gateway":
		return true
	}
	return false
}

// SpanErrorExpr and SpanServerExpr cover both spellings the pipeline writes
// (the ClickHouse exporter's "Error"/"Server" and the OTel enum names).
const (
	spanErrorExpr  = "StatusCode IN ('Error', 'STATUS_CODE_ERROR')"
	spanServerExpr = "SpanKind IN ('Server', 'SPAN_KIND_SERVER')"
	// spanStreamExpr matches the server span of a streaming RPC, whose
	// duration is the stream's lifetime (minutes to hours), not a request's
	// latency. serverkit stamps StreamTypeAttr on it; spans from emitters
	// that predate the stamp are recognised by a Connect procedure named
	// Stream<Name> (".../StreamChatUpdates"). Latency quantiles exclude it;
	// request and error counts keep it.
	spanStreamExpr = "(SpanAttributes['" + StreamTypeAttr + "'] != '' OR match(SpanName, '/Stream[A-Z][^/]*$'))"
)
