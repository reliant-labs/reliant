package obsquery

// TraceExtent is when one trace ran: how many spans it has, the first span's
// start and the last span's end, in one aggregate over its spans however many
// there are. `forge obs logs --trace` scopes its range with it, so the logs of
// a trace older than the logs' default window are still found.
type TraceExtent struct {
	Scope   Scope
	TraceID string
}

// Compile builds the statement: one row, always (spans = 0 when the trace has
// no span in the range). Columns: spans, start (the first span's Timestamp),
// end_ns (the last span's end, unix nanoseconds).
func (q TraceExtent) Compile() (Stmt, error) {
	if err := q.Scope.validate(MaxRangeTraces); err != nil {
		return Stmt{}, err
	}
	id, err := NormalizeTraceID(q.TraceID)
	if err != nil {
		return Stmt{}, err
	}
	b := newBuilder(q.Scope)
	conds := []string{b.rangeCond("Timestamp", q.Scope), "TraceId = " + b.str(id)}
	if e := b.envCond(q.Scope); e != "" {
		conds = append(conds, e)
	}
	if e := b.instanceCond(q.Scope); e != "" {
		conds = append(conds, e)
	}
	return b.stmt("SELECT count() AS spans, min(Timestamp) AS start,\n" +
		"       max(toUnixTimestamp64Nano(Timestamp) + toInt64(Duration)) AS end_ns\n" +
		"FROM " + b.table(q.Scope, "otel_traces") + "\n" + where(conds)), nil
}
