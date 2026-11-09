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
// ServerErrorFilterSQL (for errors) or the record's issue source (for the
// issues of a source), so a local issue and a hosted one carry the same
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
// scope, the issue events, an optional service and an optional fingerprint.
// The events are those of one issue when a fingerprint is given (whatever its
// source), else those of source: server errors without one for "", the
// records carrying it otherwise.
func (b *builder) issueConds(s Scope, service, fingerprint, source string) ([]string, error) {
	conds := []string{b.rangeCond("Timestamp", s)}
	sc, err := b.scopeConds(SigLogs, s)
	if err != nil {
		return nil, err
	}
	conds = append(conds, sc...)
	switch {
	case fingerprint != "":
		conds = append(conds, IssueEventFilterSQL)
	case source == "":
		conds = append(conds, ServerErrorFilterSQL, IssueSourceSQL+" = ''")
	default:
		conds = append(conds, "ResourceAttributes['rum.sessionId'] = ''", IssueSourceSQL+" = "+b.str(source))
	}
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
	// Source lists the issues of this issue source (LogKeyIssueSource);
	// empty lists errors. Ignored with a Fingerprint, which names its issue
	// whatever its source.
	Source string
	Limit  int // issues to return; the statement fetches one more
	Sort   string
}

// Who an issue touched is counted over the first of these that at least half of
// the issue's events name, widest-first: the people (user.id), else the chats,
// threads and workflows (a worker or reconciler logs on behalf of a run, not a
// request), else the traces. A unit most events do not name is not used: "1
// user" over an issue of hundreds of failures that mostly name nobody would read
// as a handful of people. The unit that was counted travels with the count, so
// "3 affected" is never a bare number.
const (
	AffectedUsers     = "users"
	AffectedChats     = "chats"
	AffectedThreads   = "threads"
	AffectedWorkflows = "workflows"
	AffectedTraces    = "traces"
	AffectedSessions  = "sessions"
)

// AffectedKeys are the units an event can name, in preference order, and the
// log attribute each is read from.
var AffectedKeys = []struct{ Unit, Attribute string }{
	{AffectedUsers, "user.id"},
	{AffectedChats, "reliant.chat.id"},
	{AffectedThreads, "thread.id"},
	{AffectedWorkflows, "workflow.id"},
}

// AffectedSQL selects the affected count of a group of error records of
// otel_logs, and AffectedUnitSQL the unit it counted.
func AffectedSQL() string {
	return affectedChain(func(k struct{ Unit, Attribute string }) string {
		return "uniqIf(LogAttributes['" + k.Attribute + "'], LogAttributes['" + k.Attribute + "'] != '')"
	}, "uniqIf(TraceId, TraceId != '')")
}

// AffectedUnitSQL is AffectedSQL's unit column.
func AffectedUnitSQL() string {
	return affectedChain(func(k struct{ Unit, Attribute string }) string { return "'" + k.Unit + "'" }, "'"+AffectedTraces+"'")
}

func affectedChain(pick func(struct{ Unit, Attribute string }) string, fallback string) string {
	var b strings.Builder
	b.WriteString("multiIf(")
	for _, k := range AffectedKeys {
		b.WriteString("countIf(LogAttributes['" + k.Attribute + "'] != '') * 2 >= count(), " + pick(k) + ", ")
	}
	b.WriteString(fallback + ")")
	return b.String()
}

// ServerIssuesColumns names the select list, in order, for the code that scans it.
var ServerIssuesColumns = []string{
	"fingerprint", "service", "title", "culprit", "exception_type", "events", "affected",
	"first_seen", "last_seen", "latest_trace_id", "last_version", "first_version", "source", "affected_unit",
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
	if q.Source != "" && !ValidIssueSource(q.Source) {
		return Stmt{}, invalid("issue source %q is not a source name (lowercase letters, digits, '_', '.', '-')", q.Source)
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
	conds, err := b.issueConds(q.Scope, q.Service, q.Fingerprint, q.Source)
	if err != nil {
		return Stmt{}, err
	}
	sql := "SELECT " + ServerFingerprintSQL + " AS fingerprint, ServiceName AS service,\n" +
		"       argMax(" + ServerTitleSQL + ", Timestamp) AS title,\n" +
		"       argMax(" + ServerCulpritSQL + ", Timestamp) AS culprit,\n" +
		"       argMax(LogAttributes['exception.type'], Timestamp) AS exception_type,\n" +
		"       count() AS events, " + AffectedSQL() + " AS affected,\n" +
		"       min(Timestamp) AS first_seen, max(Timestamp) AS last_seen,\n" +
		"       argMax(TraceId, Timestamp) AS latest_trace_id,\n" +
		"       argMax(ResourceAttributes['service.version'], Timestamp) AS last_version,\n" +
		"       argMin(ResourceAttributes['service.version'], Timestamp) AS first_version,\n" +
		"       argMax(" + IssueSourceSQL + ", Timestamp) AS source,\n" +
		"       " + AffectedUnitSQL() + " AS affected_unit\n" +
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
	conds, err := b.issueConds(q.Scope, "", q.Fingerprint, "")
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
// version, events, affected, first_seen, last_seen, affected_unit.
func (q ServerIssueVersions) Compile() (Stmt, error) {
	if err := q.Scope.validate(MaxRangeLogs); err != nil {
		return Stmt{}, err
	}
	if err := checkFingerprint(q.Fingerprint, true); err != nil {
		return Stmt{}, err
	}
	b := newBuilder(q.Scope)
	conds, err := b.issueConds(q.Scope, "", q.Fingerprint, "")
	if err != nil {
		return Stmt{}, err
	}
	return b.stmt("SELECT ResourceAttributes['service.version'] AS version, count() AS events, " + AffectedSQL() + " AS affected,\n" +
		"       min(Timestamp) AS first_seen, max(Timestamp) AS last_seen, " + AffectedUnitSQL() + " AS affected_unit\n" +
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
	outer, err := b.issueConds(q.Scope, "", "", "")
	if err != nil {
		return Stmt{}, err
	}
	inner, err := b.issueConds(q.Scope, "", q.Fingerprint, "")
	if err != nil {
		return Stmt{}, err
	}
	outer = append(outer, "TraceId != ''", ServerFingerprintSQL+" != "+b.str(q.Fingerprint),
		"TraceId IN (SELECT TraceId FROM "+b.table(q.Scope, "otel_logs")+" "+where(append(inner, "TraceId != ''"))+")")
	return b.stmt("SELECT " + ServerFingerprintSQL + " AS fingerprint, uniqExact(TraceId) AS shared\n" +
		"FROM " + b.table(q.Scope, "otel_logs") + "\n" + where(outer) + "\n" +
		"GROUP BY fingerprint\nORDER BY shared DESC, fingerprint\nLIMIT " + strconv.Itoa(maxCoOccurring)), nil
}

// ServerIssueIncidents reads, for every server error issue in the range, the
// cause of its newest event and when it first fired in the range: the inputs of
// GroupIncidents. Source issues are not errors and never form an incident.
type ServerIssueIncidents struct {
	Scope Scope
	// Service narrows to one service, as the list it marks does.
	Service string
}

// ServerIssueIncidentColumns names the select list of ServerIssueIncidents, in order.
var ServerIssueIncidentColumns = []string{"fingerprint", "service", "cause", "first_seen"}

// Compile builds the incident-candidates statement, newest first, capped at
// MaxIncidentCandidates issues (a range with more failing issues than that
// groups the newest of them). Columns: ServerIssueIncidentColumns. An issue with
// no cause is left out.
func (q ServerIssueIncidents) Compile() (Stmt, error) {
	if err := q.Scope.validate(MaxRangeLogs); err != nil {
		return Stmt{}, err
	}
	b := newBuilder(q.Scope)
	conds, err := b.issueConds(q.Scope, q.Service, "", "")
	if err != nil {
		return Stmt{}, err
	}
	return b.stmt("SELECT " + ServerFingerprintSQL + " AS fingerprint, ServiceName AS service,\n" +
		"       argMax(" + ServerCauseSQL + ", Timestamp) AS cause, min(Timestamp) AS first_seen\n" +
		"FROM " + b.table(q.Scope, "otel_logs") + "\n" + where(conds) + "\n" +
		"GROUP BY service, fingerprint\nHAVING cause != ''\n" +
		"ORDER BY first_seen DESC, fingerprint\nLIMIT " + strconv.Itoa(MaxIncidentCandidates)), nil
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
