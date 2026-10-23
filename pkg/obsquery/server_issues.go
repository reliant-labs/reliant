package obsquery

import (
	"regexp"
	"strconv"
	"strings"
)

// Query-time server issues: the sfp1 grouping of ERROR log records evaluated
// directly over otel_logs, for a backend that has no issue store (forge's local
// ClickStack). The control plane groups the same records in an ingest view and
// keeps state (status, first/last seen across retention); these statements keep
// none, so every issue they return is "unresolved" and its counts are those of
// the queried range.
//
// The grouping key is ServerFingerprintSQL and the event filter is
// ServerErrorFilterSQL, so a local issue and a hosted one carry the same
// fingerprint for the same record.

// Issue list sort orders.
const (
	IssueSortLast   = "last"   // most recently seen first (default)
	IssueSortFirst  = "first"  // most recently first-seen first
	IssueSortEvents = "events" // most events first
)

// Issue page sizes and the caps on an issue's side lists.
const (
	DefaultIssueLimit = 25
	MaxIssueLimit     = 100
	MaxIssueEvents    = 100
	maxIssueVersions  = 20
	maxCoOccurring    = 10
)

var fingerprintRe = regexp.MustCompile(`^[0-9a-f]{16}$`)

// ValidFingerprint reports whether s is the shape of an sfp1 fingerprint: 16
// lowercase hex digits.
func ValidFingerprint(s string) bool { return fingerprintRe.MatchString(s) }

// issueConds is the WHERE of every server-issue statement: the range, the
// scope, server error events, an optional service and an optional fingerprint.
func (b *builder) issueConds(s Scope, service, fingerprint string) ([]string, error) {
	conds := []string{b.rangeCond("Timestamp", s)}
	sc, err := b.scopeConds(SigLogs, s)
	if err != nil {
		return nil, err
	}
	conds = append(conds, sc...)
	conds = append(conds, ServerErrorFilterSQL)
	if service != "" {
		conds = append(conds, "ServiceName = "+b.str(service))
	}
	if fingerprint != "" {
		conds = append(conds, ServerFingerprintSQL+" = "+b.str(fingerprint))
	}
	return conds, nil
}

func checkFingerprint(fp string, required bool) error {
	switch {
	case fp == "" && required:
		return invalid("a fingerprint is required")
	case fp != "" && !ValidFingerprint(fp):
		return invalid("fingerprint %q is not 16 lowercase hex digits", fp)
	}
	return nil
}

// ServerIssues lists the server issues of a scope.
type ServerIssues struct {
	Scope Scope
	// Service narrows to one service.name, beside Scope.Services.
	Service string
	// Fingerprint narrows to one issue; empty lists them all.
	Fingerprint string
	Limit       int // issues to return; the statement fetches one more
	Sort        string
}

// ServerIssuesColumns names the select list, in order, for the code that scans it.
var ServerIssuesColumns = []string{
	"fingerprint", "service", "title", "culprit", "exception_type", "events", "affected",
	"first_seen", "last_seen", "latest_trace_id", "last_version", "first_version",
}

// Compile builds the grouping statement. Columns: ServerIssuesColumns.
func (q ServerIssues) Compile() (Stmt, error) {
	if err := q.Scope.validate(MaxRangeLogs); err != nil {
		return Stmt{}, err
	}
	if q.Limit <= 0 || q.Limit > MaxIssueLimit {
		return Stmt{}, invalid("page size must be 1-%d", MaxIssueLimit)
	}
	if err := checkFingerprint(q.Fingerprint, false); err != nil {
		return Stmt{}, err
	}
	var order string
	switch q.Sort {
	case "", IssueSortLast:
		order = "last_seen DESC"
	case IssueSortFirst:
		order = "first_seen DESC"
	case IssueSortEvents:
		order = "events DESC"
	default:
		return Stmt{}, invalid("unknown sort %q (use %s, %s or %s)", q.Sort, IssueSortLast, IssueSortFirst, IssueSortEvents)
	}
	b := newBuilder(q.Scope)
	conds, err := b.issueConds(q.Scope, q.Service, q.Fingerprint)
	if err != nil {
		return Stmt{}, err
	}
	sql := "SELECT " + ServerFingerprintSQL + " AS fingerprint, ServiceName AS service,\n" +
		"       argMax(" + ServerTitleSQL + ", Timestamp) AS title,\n" +
		"       argMax(" + ServerCulpritSQL + ", Timestamp) AS culprit,\n" +
		"       argMax(LogAttributes['exception.type'], Timestamp) AS exception_type,\n" +
		"       count() AS events, uniqIf(TraceId, TraceId != '') AS affected,\n" +
		"       min(Timestamp) AS first_seen, max(Timestamp) AS last_seen,\n" +
		"       argMax(TraceId, Timestamp) AS latest_trace_id,\n" +
		"       argMax(ResourceAttributes['service.version'], Timestamp) AS last_version,\n" +
		"       argMin(ResourceAttributes['service.version'], Timestamp) AS first_version\n" +
		"FROM " + b.table(q.Scope, "otel_logs") + "\n" + where(conds) + "\n" +
		"GROUP BY fingerprint, service\n" +
		"ORDER BY " + order + ", fingerprint\n" +
		"LIMIT " + strconv.Itoa(q.Limit+1)
	return b.stmt(sql), nil
}

// ServerIssueEvents reads the newest events of one issue.
type ServerIssueEvents struct {
	Scope       Scope
	Fingerprint string
	Limit       int
}

// ServerIssueEventColumns names the select list of ServerIssueEvents, in order.
var ServerIssueEventColumns = []string{"timestamp", "trace_id", "span_id", "service", "version", "body", "attributes"}

// Compile builds the newest-first events statement. Columns:
// ServerIssueEventColumns.
func (q ServerIssueEvents) Compile() (Stmt, error) {
	if err := q.Scope.validate(MaxRangeLogs); err != nil {
		return Stmt{}, err
	}
	if q.Limit <= 0 || q.Limit > MaxIssueEvents {
		return Stmt{}, invalid("events must be 1-%d", MaxIssueEvents)
	}
	if err := checkFingerprint(q.Fingerprint, true); err != nil {
		return Stmt{}, err
	}
	b := newBuilder(q.Scope)
	conds, err := b.issueConds(q.Scope, "", q.Fingerprint)
	if err != nil {
		return Stmt{}, err
	}
	return b.stmt("SELECT Timestamp, TraceId, SpanId, ServiceName, ResourceAttributes['service.version'], Body, LogAttributes\n" +
		"FROM " + b.table(q.Scope, "otel_logs") + "\n" + where(conds) + "\n" +
		"ORDER BY Timestamp DESC\nLIMIT " + strconv.Itoa(q.Limit+1)), nil
}

// ServerIssueVersions counts one issue's events per service.version.
type ServerIssueVersions struct {
	Scope       Scope
	Fingerprint string
}

// Compile builds the per-version statement, newest version first. Columns:
// version, events, affected, first_seen, last_seen.
func (q ServerIssueVersions) Compile() (Stmt, error) {
	if err := q.Scope.validate(MaxRangeLogs); err != nil {
		return Stmt{}, err
	}
	if err := checkFingerprint(q.Fingerprint, true); err != nil {
		return Stmt{}, err
	}
	b := newBuilder(q.Scope)
	conds, err := b.issueConds(q.Scope, "", q.Fingerprint)
	if err != nil {
		return Stmt{}, err
	}
	return b.stmt("SELECT ResourceAttributes['service.version'] AS version, count() AS events, uniqIf(TraceId, TraceId != '') AS affected,\n" +
		"       min(Timestamp) AS first_seen, max(Timestamp) AS last_seen\n" +
		"FROM " + b.table(q.Scope, "otel_logs") + "\n" + where(conds) + "\n" +
		"GROUP BY version\nORDER BY last_seen DESC\nLIMIT " + strconv.Itoa(maxIssueVersions)), nil
}

// ServerIssueCoOccurring finds the other issues that fired in the same traces
// as one issue, most often first.
type ServerIssueCoOccurring struct {
	Scope       Scope
	Fingerprint string
}

// Compile builds the co-occurrence statement. Columns: fingerprint, shared
// (the number of traces both issues appear in).
func (q ServerIssueCoOccurring) Compile() (Stmt, error) {
	if err := q.Scope.validate(MaxRangeLogs); err != nil {
		return Stmt{}, err
	}
	if err := checkFingerprint(q.Fingerprint, true); err != nil {
		return Stmt{}, err
	}
	b := newBuilder(q.Scope)
	outer, err := b.issueConds(q.Scope, "", "")
	if err != nil {
		return Stmt{}, err
	}
	inner, err := b.issueConds(q.Scope, "", q.Fingerprint)
	if err != nil {
		return Stmt{}, err
	}
	outer = append(outer, "TraceId != ''", ServerFingerprintSQL+" != "+b.str(q.Fingerprint),
		"TraceId IN (SELECT TraceId FROM "+b.table(q.Scope, "otel_logs")+" "+where(append(inner, "TraceId != ''"))+")")
	return b.stmt("SELECT " + ServerFingerprintSQL + " AS fingerprint, uniqExact(TraceId) AS shared\n" +
		"FROM " + b.table(q.Scope, "otel_logs") + "\n" + where(outer) + "\n" +
		"GROUP BY fingerprint\nORDER BY shared DESC, fingerprint\nLIMIT " + strconv.Itoa(maxCoOccurring)), nil
}

// LocalIssuePrefix starts the id of a query-time issue: "sfp1-<fingerprint>".
const LocalIssuePrefix = "sfp1-"

// LocalIssueID is the id a query-time issue carries.
func LocalIssueID(fingerprint string) string { return LocalIssuePrefix + fingerprint }

// ParseLocalIssueID extracts the fingerprint of a query-time issue id.
func ParseLocalIssueID(id string) (string, error) {
	fp, ok := strings.CutPrefix(id, LocalIssuePrefix)
	if !ok || !ValidFingerprint(fp) {
		return "", invalid("%q is not a local issue id (they look like %s0123456789abcdef)", id, LocalIssuePrefix)
	}
	return fp, nil
}
