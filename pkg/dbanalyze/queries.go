package dbanalyze

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/reliant-labs/forge/internal/sqllex"
	"github.com/reliant-labs/forge/pkg/querycapture"
)

// MinQueryServerVersionNum is the oldest postgres the query pass plans on:
// EXPLAIN (GENERIC_PLAN), which plans a statement whose $n parameters have
// no values, is new in 16.
const MinQueryServerVersionNum = 160000

// ErrServerTooOld is AnalyzeQueries' error on a postgres older than
// MinQueryServerVersionNum.
var ErrServerTooOld = errors.New("the query pass needs postgres 16 or newer (EXPLAIN (GENERIC_PLAN))")

// AnalyzeQueries plans each shape on the session's schema with EXPLAIN
// (GENERIC_PLAN) twice — as postgres would run it, and with enable_seqscan
// off — and reports, per table, whether an index serves the statement's
// filters, joins and ORDER BY … LIMIT (see classify), with the findings.
//
// db-analyze-query-no-usable-index is true at any size, so it works on an
// empty scratch database; its severity comes from the table's size: error
// when it is known (the target's statistics, else cfg.TableRows) and at least
// SmallTableRows, dropped when it is known and smaller, warning when it is
// unknown. db-analyze-query-large-seqscan needs a dsn target with
// statistics: the normal plan seq-scans a table of at least LargeTableRows.
//
// A shape that cannot be planned (a table only its test created, a
// parameter whose type postgres cannot infer) is returned with its Error and
// no verdicts. The error return is for the session failing, or a postgres
// older than 16 (ErrServerTooOld).
func AnalyzeQueries(ctx context.Context, s *Session, info TargetInfo, shapes []querycapture.Shape, cfg Config) ([]Query, []Finding, error) {
	if info.ServerVersionNum < MinQueryServerVersionNum {
		return nil, nil, fmt.Errorf("%w; the target runs %s", ErrServerTooOld, info.ServerVersion)
	}
	cat := &catalog{s: s, rels: map[string]*relInfo{}}
	queries := make([]Query, 0, len(shapes))
	var findings []Finding
	for _, sh := range shapes {
		q := Query{Fingerprint: sh.Fingerprint, SQL: sh.SQL, Operation: sh.Operation,
			QueryDetail: &QueryDetail{Tables: sh.Tables, Callers: sh.Callers, Access: []TableAccess{}}}
		if q.Tables == nil {
			q.Tables = []string{}
		}
		if q.Callers == nil {
			q.Callers = []querycapture.CallSite{}
		}
		access, err := planShape(ctx, s, cat, &q)
		if err != nil {
			return queries, findings, err
		}
		for _, a := range access {
			rc := tableRows(info, cfg, a.Table, a.reltuples)
			if rc.Value != nil {
				a.Rows = &rc
			}
			q.Access = append(q.Access, a.TableAccess)
			if f, ok := noUsableIndex(q, a.TableAccess, rc, cfg); ok {
				findings = append(findings, f)
			}
			if info.Kind == TargetDSN && info.HasStatistics && a.SeqScan && a.reltuples >= float64(cfg.largeTableRows()) {
				findings = append(findings, queryFinding(q, Finding{Rule: RuleQueryLargeSeqScan, Severity: SeverityError,
					Relation: a.Table, Rows: &rc, Fix: fixLargeSeqScan,
					Message: fmt.Sprintf("the plan reads every row of %s (about %d, at least large_table_rows %d) with a Seq Scan",
						a.Table, int64(a.reltuples), cfg.largeTableRows())}))
			}
		}
		queries = append(queries, q)
	}
	return queries, findings, nil
}

// planShape explains q twice and classifies its tables. A statement that
// fails to plan is q.Error, not an error: the error return is the session's.
func planShape(ctx context.Context, s *Session, cat *catalog, q *Query) ([]classified, error) {
	if n := len(SplitStatements(q.SQL)); n != 1 {
		q.Error = fmt.Sprintf("the shape holds %d statements, not one: it is not explained", n)
		return nil, nil
	}
	if kw, ok := queryStatement(q.SQL); !ok {
		q.Error = fmt.Sprintf("the shape is a %s statement, not a query (%s): it is not explained", kw, strings.Join(explainedKeywords, ", "))
		return nil, nil
	}
	plans := [2]*rawPlan{}
	for i, noSeqScan := range []bool{false, true} {
		doc, err := s.explainJSON(ctx, q.SQL, noSeqScan)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if sqlState(err) == "" {
				// Not the statement's own error: the session's.
				return nil, fmt.Errorf("explain %s: %w", q.Fingerprint, err)
			}
			q.Error, q.SQLState = err.Error(), sqlState(err)
			return nil, nil
		}
		if plans[i], err = parsePlan(doc); err != nil {
			q.Error = err.Error()
			return nil, nil
		}
	}
	q.Plan, q.PlanNoSeqScan = plans[0].trim(), plans[1].trim()
	return classify(plans[0], plans[1], func(schema, name string) (*relInfo, error) {
		return cat.lookup(ctx, schema, name)
	})
}

// noUsableIndex is the finding for an unserved table, sized: dropped when
// the table is known to be small, a warning when its size is unknown.
func noUsableIndex(q Query, a TableAccess, rc RowCount, cfg Config) (Finding, bool) {
	if a.Verdict != AccessUnserved {
		return Finding{}, false
	}
	sev := SeverityWarning
	if rc.Value != nil {
		if small := cfg.smallTableRows(); small > 0 && *rc.Value < small {
			return Finding{}, false
		}
		sev = SeverityError
	}
	var msg string
	switch a.How {
	case UnservedFilter:
		reads := "reads every row of " + a.Table
		if a.Index != "" {
			reads = fmt.Sprintf("walks %s in full", a.Index)
		}
		msg = fmt.Sprintf("no index serves the filter %s on %s: with sequential scans disabled the planner still %s and filters each row", a.Detail, a.Table, reads)
	case UnservedOrder:
		msg = fmt.Sprintf("no index provides the order %s on %s: every row is sorted to return the first few", a.Detail, a.Table)
	case UnservedJoin:
		msg = fmt.Sprintf("%s is read in full to join it on %s: no index serves that join (none leads with the column, or the plan does not read by it)", a.Table, a.Detail)
	}
	f := Finding{Rule: RuleQueryNoUsableIndex, Severity: sev, Relation: a.Table, Message: msg, Fix: fixNoUsableIndex}
	if rc.Value != nil {
		f.Rows = &rc
	}
	return queryFinding(q, f), true
}

// queryFinding names the shape and its first caller on f.
func queryFinding(q Query, f Finding) Finding {
	f.FindingQuery = &FindingQuery{Query: q.Fingerprint, Callers: q.Callers}
	if len(q.Callers) > 0 {
		f.File, f.Line = q.Callers[0].Caller.File, q.Callers[0].Caller.Line
	}
	return f
}

// catalog resolves scanned relations, once each per pass: the schema does
// not change while queries are planned.
type catalog struct {
	s    *Session
	rels map[string]*relInfo
}

const relationInfoQuery = `
SELECT c.oid::bigint,
       CASE WHEN rn.nspname = 'public' THEN r.relname ELSE rn.nspname || '.' || r.relname END,
       r.reltuples
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
JOIN pg_class r ON r.oid = COALESCE(pg_partition_root(c.oid), c.oid)
JOIN pg_namespace rn ON rn.oid = r.relnamespace
WHERE c.relname = $2 AND (n.nspname = $1 OR ($1 = '' AND pg_table_is_visible(c.oid)))
LIMIT 1`

const relationIndexesQuery = `
SELECT i.relname, x.indpred IS NOT NULL
FROM pg_index x
JOIN pg_class i ON i.oid = x.indexrelid
WHERE x.indrelid = $1 AND x.indisvalid`

func (c *catalog) lookup(ctx context.Context, schema, name string) (*relInfo, error) {
	key := schema + "\x00" + name
	if ri, ok := c.rels[key]; ok {
		return ri, nil
	}
	ri := &relInfo{indexes: map[string]indexInfo{}}
	var oid int64
	err := c.s.conn.QueryRowContext(ctx, relationInfoQuery, schema, name).Scan(&oid, &ri.table, &ri.reltuples)
	if err != nil {
		return nil, fmt.Errorf("look up %s in the catalog: %w", strings.TrimPrefix(schema+"."+name, "."), err)
	}
	rows, err := c.s.conn.QueryContext(ctx, relationIndexesQuery, oid)
	if err != nil {
		return nil, fmt.Errorf("read the indexes of %s: %w", ri.table, err)
	}
	for rows.Next() {
		var ix string
		var info indexInfo
		if err := rows.Scan(&ix, &info.partial); err != nil {
			_ = rows.Close()
			return nil, err
		}
		ri.indexes[ix] = info
	}
	if err := closeRows(rows); err != nil {
		return nil, err
	}
	c.rels[key] = ri
	return ri, nil
}

// explainedKeywords are the statements the query pass plans: queries, and
// the data changes a plain EXPLAIN plans without running. Anything else a
// test captured (DDL, ANALYZE, EXECUTE, DECLARE, CREATE TABLE AS …) is
// recorded with an Error and never sent, and the EXPLAIN that is sent runs
// read-only (Session.explainJSON).
var explainedKeywords = []string{"SELECT", "WITH", "VALUES", "TABLE", "INSERT", "UPDATE", "DELETE", "MERGE"}

// queryStatement is sql's first keyword, past comments and opening
// parentheses, and whether it is one of explainedKeywords.
func queryStatement(sql string) (string, bool) {
	i := 0
	for i < len(sql) {
		switch c := sql[i]; {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '(':
			i++
		case strings.HasPrefix(sql[i:], "--"):
			if nl := strings.IndexByte(sql[i:], '\n'); nl >= 0 {
				i += nl + 1
			} else {
				i = len(sql)
			}
		case strings.HasPrefix(sql[i:], "/*"):
			i = sqllex.BlockCommentEnd(sql, i)
		default:
			j := i
			for j < len(sql) && sqllex.IsIdentByte(sql[j]) {
				j++
			}
			kw := strings.ToUpper(sql[i:j])
			if kw == "" {
				return fmt.Sprintf("%q", sql[i:i+1]), false
			}
			return kw, slices.Contains(explainedKeywords, kw)
		}
	}
	return "empty", false
}
