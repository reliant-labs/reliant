package dbanalyze

import "github.com/reliant-labs/forge/pkg/querycapture"

// SchemaVersion names the document's shape. A consumer checks it before
// reading anything else; fields are only ever added under it.
const SchemaVersion = "forge.db-analyze/v1"

// Exit codes of an analysis, carried in Document.ExitCode.
const (
	// ExitOK: nothing gating was found.
	ExitOK = 0
	// ExitFindings: at least one finding gates (severity error).
	ExitFindings = 1
	// ExitCouldNotAnalyze: the analysis could not be made — no Postgres, a
	// Postgres too old, a target ahead of the tree, a refused target, a
	// statement that failed. Never read as a pass.
	ExitCouldNotAnalyze = 2
)

// Severities a Finding carries.
const (
	SeverityError   = "error"
	SeverityWarning = "warning"
)

// Document is the `forge db analyze --json` output, `forge.db-analyze/v1`.
type Document struct {
	Schema string     `json:"schema"`
	Target TargetInfo `json:"target"`
	// Base is the revision changes were measured from, as "<ref>@<sha>":
	// where a scratch target's migrations were split, and the query shapes
	// selected. "" when nothing was selected by it (a dsn target's
	// migrations are decided by its own schema_migrations; --all selects
	// every shape).
	Base       string      `json:"base,omitempty"`
	Migrations []Migration `json:"migrations"`
	// Queries is the query-plan pass's output. It is always present, empty
	// until a query pass runs.
	Queries  []Query   `json:"queries"`
	Findings []Finding `json:"findings"`
	Summary  Summary   `json:"summary"`
	ExitCode int       `json:"exit_code"`
	// Error says why the analysis could not be made (ExitCouldNotAnalyze).
	Error string `json:"error,omitempty"`
}

// NewDocument returns an empty document: every array present, so a consumer
// never has to tell a missing field from an empty one.
func NewDocument() *Document {
	return &Document{
		Schema:     SchemaVersion,
		Migrations: []Migration{},
		Queries:    []Query{},
		Findings:   []Finding{},
		Summary:    Summary{Skipped: []Skipped{}},
	}
}

// TargetInfo describes the database the analysis ran on.
type TargetInfo struct {
	// Kind is TargetScratch or TargetDSN.
	Kind TargetKind `json:"kind"`
	// Identity is "host:port/database" — never credentials.
	Identity         string `json:"identity"`
	ServerVersion    string `json:"server_version"`
	ServerVersionNum int    `json:"server_version_num"`
	// HasStatistics reports whether the target's own row estimates
	// (pg_class.reltuples) are used for sizes: a dsn target some table of
	// which has been analyzed. A scratch target never has them.
	HasStatistics bool `json:"has_statistics"`
	// Clone is the disposable copy of a dsn target the migrations ran on
	// (absent for a scratch target, which is disposable itself).
	Clone *CloneInfo `json:"clone,omitempty"`
}

// TargetKind is where the analysis runs.
type TargetKind string

const (
	// TargetScratch is an embedded (or FORGE_TEST_POSTGRES_URL) Postgres
	// migrated to the base: it needs nothing, and has no rows.
	TargetScratch TargetKind = "scratch"
	// TargetDSN is a database the project provides, typically an anonymized
	// snapshot of production.
	TargetDSN TargetKind = "dsn"
)

// Migration is one migration file and what its statements did.
type Migration struct {
	File    string `json:"file"`
	Version uint64 `json:"version"`
	// Analyzed is false for a migration the run did not reach (an earlier
	// statement failed); Reason says why.
	Analyzed   bool              `json:"analyzed"`
	Reason     string            `json:"reason,omitempty"`
	Statements []StatementResult `json:"statements"`
}

// StatementResult is what one statement did, measured inside its file's
// transaction (or on its own, for a statement that cannot be in one).
type StatementResult struct {
	Index int    `json:"index"`
	Line  int    `json:"line"`
	SQL   string `json:"sql"`
	// Executed is false for a statement the run did not reach, and for one
	// it did not run because it changes what the whole server shares
	// (roles, databases, ALTER SYSTEM): Reason says which. A statement that
	// cannot run inside a transaction (CREATE INDEX CONCURRENTLY and the
	// like) is executed, on its own, with its lock reported statically.
	Executed   bool    `json:"executed"`
	Reason     string  `json:"reason,omitempty"`
	DurationMS float64 `json:"duration_ms"`
	// Locks are the relation locks the statement acquired. A mode its
	// file's transaction already held is listed with HeldBefore (see Lock).
	Locks    []Lock              `json:"locks"`
	Rewrites []Rewrite           `json:"rewrites"`
	Scans    []Scan              `json:"scans"`
	Rows     map[string]RowCount `json:"rows,omitempty"`
	// Error is the statement's error, when it failed.
	Error string `json:"error,omitempty"`
	// SQLState is the error's SQLSTATE code.
	SQLState string `json:"sqlstate,omitempty"`
}

// Lock is one relation lock.
type Lock struct {
	Relation string `json:"relation"`
	Mode     string `json:"mode"`
	// New marks a relation created earlier in the run: locking it is free.
	New bool `json:"new,omitempty"`
	// HeldBefore marks a mode the file's transaction already held when the
	// statement ran — taken by an earlier statement of the same file (each
	// file commits, releasing its locks, before the next begins). Postgres
	// does not record a lock twice, so a statement that needs a mode already
	// held shows it only this way.
	HeldBefore bool `json:"held_before,omitempty"`
	// Static marks a lock known from the statement's kind rather than
	// observed: a statement that ran outside a transaction takes and
	// releases its lock within itself, where pg_locks cannot see it.
	Static bool `json:"static,omitempty"`
}

// Rewrite is a relation whose storage the statement replaced
// (pg_class.relfilenode changed): every row was copied.
type Rewrite struct {
	Relation string `json:"relation"`
	// Kind is "table" or "index".
	Kind string `json:"kind"`
	// Table is an index's table.
	Table string `json:"table,omitempty"`
	New   bool   `json:"new,omitempty"`
}

// Scan is a table the statement read sequentially, from
// pg_stat_xact_user_tables.
type Scan struct {
	Relation   string `json:"relation"`
	SeqScan    int64  `json:"seq_scan"`
	SeqTupRead int64  `json:"seq_tup_read"`
	New        bool   `json:"new,omitempty"`
}

// RowCount is a table's size and where it came from.
type RowCount struct {
	// Value is nil when the size is unknown.
	Value *int64 `json:"value"`
	// Source is RowsFromReltuples, RowsFromHints or RowsUnknown.
	Source string `json:"source"`
}

// RowCount sources.
const (
	RowsFromReltuples = "reltuples"
	RowsFromHints     = "table_rows"
	RowsUnknown       = "unknown"
)

// Query is one captured query shape (forge/pkg/querycapture) and what the
// planner makes of it on the head schema. The migration pass leaves the
// array empty.
//
// Query stays comparable, as it was in v1.7.0 (when it had no fields): its
// slices live in the embedded *QueryDetail, which encoding/json flattens into
// the query's own object. AnalyzeQueries always sets it; on a Query built by
// hand, check q.QueryDetail != nil before reading Tables, Callers or Access.
type Query struct {
	Fingerprint string `json:"fingerprint"`
	// SQL is the shape's statement: $n placeholders, inline literals kept.
	SQL       string `json:"sql"`
	Operation string `json:"operation,omitempty"`
	*QueryDetail
	// Plan is the generic plan as postgres would run it, trimmed.
	Plan *PlanNode `json:"plan,omitempty"`
	// PlanNoSeqScan is the generic plan with enable_seqscan off: which
	// indexes can serve the statement at all.
	PlanNoSeqScan *PlanNode `json:"plan_no_seqscan,omitempty"`
	// Error is why the shape could not be planned (a table only a test
	// created, a parameter whose type postgres cannot infer); the shape is
	// listed in the summary's skipped.
	Error    string `json:"error,omitempty"`
	SQLState string `json:"sqlstate,omitempty"`
}

// QueryDetail is a Query's tables, callers and per-table verdicts. Its fields
// appear in the document as the query's own (tables, callers, access).
type QueryDetail struct {
	Tables []string `json:"tables"`
	// Callers are the call sites that ran the shape; the first is the one
	// findings name (a caller in a changed file, when one is).
	Callers []querycapture.CallSite `json:"callers"`
	// Access is, per table the statement reads, whether an index serves it.
	Access []TableAccess `json:"access"`
}

// TableAccess is how the no-seqscan plan reads one table.
type TableAccess struct {
	// Table is the table, "schema.table" outside public; a partition is
	// named by its partitioned root.
	Table string `json:"table"`
	// Verdict is AccessServed, AccessUnserved or AccessUnfiltered.
	Verdict string `json:"verdict"`
	// How says why: for served, ServedByIndexCond, ServedByPartialIndex,
	// ServedByOrder or ServedByTID; for unserved,
	// UnservedFilter, UnservedOrder or UnservedJoin.
	How string `json:"how,omitempty"`
	// Index is the index that serves it, or the one walked in full.
	Index string `json:"index,omitempty"`
	// Scan is the scan node's type in the no-seqscan plan.
	Scan string `json:"scan"`
	// Detail is the predicate, sort key or join columns the verdict is
	// about.
	Detail string `json:"detail,omitempty"`
	// SeqScan reports whether the normal plan reads it with a Seq Scan.
	SeqScan bool      `json:"seq_scan"`
	Rows    *RowCount `json:"rows,omitempty"`
}

// TableAccess verdicts.
const (
	// AccessServed: an index serves the filter, join or order on the table.
	AccessServed = "served"
	// AccessUnserved: the table is filtered, joined in a restricted
	// statement, or ordered under a LIMIT, and no index serves it.
	AccessUnserved = "unserved"
	// AccessUnfiltered: the statement reads the table in full and nothing
	// an index could serve constrains it (an unfiltered list, a count).
	AccessUnfiltered = "unfiltered"
)

// TableAccess.How values.
const (
	ServedByIndexCond    = "index_cond"    // the scan has an Index Cond
	ServedByPartialIndex = "partial_index" // a partial index whose predicate implies the filter
	ServedByOrder        = "order"         // an index scan delivers the ORDER BY … LIMIT order
	ServedByTID          = "tid"           // a ctid lookup
	UnservedFilter       = "filter"        // a predicate evaluated on every row
	UnservedOrder        = "order"         // every row sorted to return the first few
	UnservedJoin         = "join"          // read in full to be joined: no index serves the join key
)

// PlanNode is one node of an EXPLAIN plan, trimmed to what a review reads.
type PlanNode struct {
	NodeType string `json:"node_type"`
	// Operation is a ModifyTable node's (Insert, Update, Delete, Merge).
	Operation string `json:"operation,omitempty"`
	// Relation is the scanned relation, "schema.rel" outside public.
	Relation    string   `json:"relation,omitempty"`
	Alias       string   `json:"alias,omitempty"`
	Index       string   `json:"index,omitempty"`
	IndexCond   string   `json:"index_cond,omitempty"`
	RecheckCond string   `json:"recheck_cond,omitempty"`
	Filter      string   `json:"filter,omitempty"`
	HashCond    string   `json:"hash_cond,omitempty"`
	MergeCond   string   `json:"merge_cond,omitempty"`
	JoinFilter  string   `json:"join_filter,omitempty"`
	SortKey     []string `json:"sort_key,omitempty"`
	// Disabled marks a node the planner was told not to use (postgres 18
	// reports it; a Seq Scan in the no-seqscan plan is one on any version).
	Disabled bool `json:"disabled,omitempty"`
	// Subplan names an InitPlan or SubPlan.
	Subplan string     `json:"subplan,omitempty"`
	Rows    float64    `json:"rows"`
	Cost    float64    `json:"cost"`
	Plans   []PlanNode `json:"plans,omitempty"`
}

// Finding is one rule's verdict on one relation.
type Finding struct {
	Rule     string `json:"rule"`
	Severity string `json:"severity"`
	File     string `json:"file,omitempty"`
	Line     int    `json:"line,omitempty"`
	Relation string `json:"relation,omitempty"`
	Message  string `json:"message"`
	Fix      string `json:"fix,omitempty"`
	// Rows is the relation's size when it is known.
	Rows *RowCount `json:"rows,omitempty"`
	// Note explains a severity the caller adjusted (a rule's warn-first
	// rollout, a project's lint.rules).
	Note string `json:"note,omitempty"`
	// FindingQuery is set on a query-pass finding and nil on a migration
	// pass's: check f.FindingQuery != nil before reading Query or Callers.
	// It is a pointer so that Finding stays comparable, as it was in v1.7.0;
	// encoding/json flattens its fields into the finding's own object
	// (query, callers).
	*FindingQuery
}

// FindingQuery is the query shape a query-pass finding is about. The
// finding's File and Line are then its first caller's (module-relative).
type FindingQuery struct {
	// Query is the shape's fingerprint.
	Query string `json:"query,omitempty"`
	// Callers are the shape's call sites, with their generated frames and
	// the tests that ran them.
	Callers []querycapture.CallSite `json:"callers,omitempty"`
}

// Summary counts what was examined, so an empty analysis is never read as a
// clean one.
type Summary struct {
	MigrationsScanned  int `json:"migrations_scanned"`
	StatementsExecuted int `json:"statements_executed"`
	// QueriesCaptured is how many query shapes the capture held, before
	// selection; QueriesScanned how many were planned.
	QueriesCaptured int `json:"queries_captured"`
	QueriesScanned  int `json:"queries_scanned"`
	// Reason says why no migration was scanned, when none was.
	Reason string `json:"reason,omitempty"`
	// QueriesReason says why no query was scanned, when a query pass was
	// asked for and none was.
	QueriesReason string    `json:"queries_reason,omitempty"`
	Skipped       []Skipped `json:"skipped"`
}

// Skipped is something the analysis did not examine, and why.
type Skipped struct {
	What   string `json:"what"`
	Reason string `json:"reason"`
}

// Finalize fills the summary's counts from the migrations and sets ExitCode
// from the findings' severities — unless the analysis already failed
// (ExitCouldNotAnalyze), which no finding can turn into a pass.
func (d *Document) Finalize() {
	d.Summary.MigrationsScanned, d.Summary.StatementsExecuted = 0, 0
	for _, m := range d.Migrations {
		if m.Analyzed {
			d.Summary.MigrationsScanned++
		}
		for _, s := range m.Statements {
			if s.Executed {
				d.Summary.StatementsExecuted++
			}
		}
	}
	d.Summary.QueriesScanned = 0
	for _, q := range d.Queries {
		if q.Error == "" {
			d.Summary.QueriesScanned++
		}
	}
	if d.Error != "" {
		d.ExitCode = ExitCouldNotAnalyze
		return
	}
	d.ExitCode = ExitOK
	for _, f := range d.Findings {
		if f.Severity == SeverityError {
			d.ExitCode = ExitFindings
			return
		}
	}
}
