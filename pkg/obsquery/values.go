package obsquery

import (
	"strconv"
	"strings"
)

// Attribute-value lookup limits.
const (
	DefaultValuesLimit = 50
	MaxValuesLimit     = 100
	maxValuePrefixLen  = 128
)

// AttrValues is a ListAttributeValues request: the values one allowlisted key
// takes in the scope, most frequent first, for filter autocomplete.
type AttrValues struct {
	Scope  Scope
	Key    string
	Prefix string
	// Signal is where to read; zero picks the key's natural home: span keys
	// read traces, event keys logs, a metric's attribute metrics, and resource
	// keys traces AND metrics (logs only when asked, they are the largest).
	Signal Signal
	// Metric names the metric whose declared attribute Key is; required for a
	// key that is not on the allowlist.
	Metric string
	Limit  int
}

func (q AttrValues) sources() ([]Signal, error) {
	if q.Signal != 0 {
		return []Signal{q.Signal}, nil
	}
	switch attrClasses[q.Key] {
	case classResource:
		return []Signal{SigTraces, SigMetrics}, nil
	case classSpan:
		return []Signal{SigTraces}, nil
	case classEvent:
		return []Signal{SigLogs}, nil
	case classMetric:
		return []Signal{SigMetrics}, nil
	}
	return []Signal{SigMetrics}, nil
}

// Compile builds the value statement. Columns: v (String), c (UInt64); it
// fetches Limit+1 rows so the caller can tell the list was cut.
//
// Scope filters on the SAME key are dropped so the list is not narrowed to the
// current selection of that very control.
func (q AttrValues) Compile() (Stmt, error) {
	if err := q.Scope.validate(MaxRangeOverview); err != nil {
		return Stmt{}, err
	}
	if q.Limit <= 0 || q.Limit > MaxValuesLimit {
		return Stmt{}, invalid("limit must be 1-%d", MaxValuesLimit)
	}
	if len(q.Prefix) > maxValuePrefixLen {
		return Stmt{}, invalid("prefix is longer than %d characters", maxValuePrefixLen)
	}
	if len(q.Metric) > 256 {
		return Stmt{}, invalid("metric_name is longer than 256 characters")
	}
	if !Allowed(q.Key) && (q.Signal != SigMetrics || q.Metric == "") {
		return Stmt{}, invalid("%q is not a filterable attribute: use an allowlisted key, or name a metric_name (signal metrics) for one of its declared attributes", q.Key)
	}
	srcs, err := q.sources()
	if err != nil {
		return Stmt{}, err
	}

	sc := q.Scope
	sc.Filters = nil
	for _, f := range q.Scope.Filters {
		if f.Key != q.Key {
			sc.Filters = append(sc.Filters, f)
		}
	}
	if !Allowed(q.Key) {
		sc.Declared = map[string]bool{q.Key: true}
	}
	b := newBuilder(sc)
	prefix := strings.ToLower(q.Prefix)

	var branches []string
	for _, sig := range srcs {
		for _, tbl := range valueTables(sig, q.Key, q.Metric) {
			e, _, err := b.keyExpr(sig, q.Key)
			if err != nil {
				return Stmt{}, err
			}
			var conds []string
			switch sig {
			case SigMetrics:
				conds = append(conds, b.rangeCondSec("TimeUnix", sc))
				switch {
				case q.Metric != "":
					conds = append(conds, "MetricName = "+b.str(q.Metric))
				case q.Key == "k8s.container.last_terminated_reason":
					conds = append(conds, "MetricName = "+b.str(MetricRestarts))
				}
			default:
				conds = append(conds, b.rangeCond("Timestamp", sc))
			}
			fc, err := b.scopeConds(sig, sc)
			if err != nil {
				return Stmt{}, err
			}
			conds = append(conds, fc...)
			conds = append(conds, e+" != ''")
			if prefix != "" {
				conds = append(conds, "startsWith(lower("+e+"), "+b.str(prefix)+")")
			}
			branches = append(branches, "SELECT "+e+" AS v, count() AS cnt FROM "+b.table(sc, tbl)+" "+where(conds)+" GROUP BY v")
		}
	}
	return b.stmt("SELECT v, sum(cnt) AS c FROM (\n" + strings.Join(branches, "\nUNION ALL\n") + "\n) GROUP BY v ORDER BY c DESC, v LIMIT " + strconv.Itoa(q.Limit+1)), nil
}

// valueTables names the tables a signal's values are read from.
func valueTables(sig Signal, key, metric string) []string {
	switch sig {
	case SigTraces:
		return []string{"otel_traces"}
	case SigLogs:
		return []string{"otel_logs"}
	}
	if metric != "" {
		return []string{MetricGauge.table(), MetricSum.table(), MetricHistogram.table()}
	}
	return []string{MetricGauge.table()}
}
