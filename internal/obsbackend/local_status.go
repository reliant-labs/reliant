package obsbackend

import (
	"context"
	"sort"

	"github.com/reliant-labs/forge/pkg/obsquery"
)

// Overview implements OverviewReader over the local ClickStack: the
// overview's RED statement (server spans) and its log statement (error-level
// records), merged by service. A service that only logged has zero request
// figures, as on the control plane.
func (l Local) Overview(ctx context.Context, req OverviewRequest) (OverviewResult, error) {
	q := obsquery.Overview{Scope: LocalScope(req.Narrow, l.Env)}
	red, err := q.RED()
	if err != nil {
		return OverviewResult{}, compileErr(err)
	}
	logs, err := q.Logs()
	if err != nil {
		return OverviewResult{}, compileErr(err)
	}
	res := OverviewResult{Statements: []Statement{stmt("overview (rate, errors, duration)", red), stmt("overview (error logs)", logs)}}
	if req.Explain {
		return res, nil
	}
	rr, err := l.run(ctx, red, DefaultLimits())
	if err != nil {
		return res, err
	}
	secs := req.Range.To.Sub(req.Range.From).Seconds()
	by := map[string]*ServiceOverview{}
	for _, row := range rr.Rows {
		if len(row) < 4 {
			return res, Undetermined("the local ClickStack returned %d columns for the overview, want 4", len(row))
		}
		s := ServiceOverview{Service: AsString(row[0]), Requests: AsInt(row[1]), Errors: AsInt(row[2])}
		if qs := AsFloats(row[3]); len(qs) == 3 {
			s.P50Ms, s.P95Ms, s.P99Ms = finite(qs[0]), finite(qs[1]), finite(qs[2])
		}
		if s.Requests > 0 {
			s.ErrorRate = float64(s.Errors) / float64(s.Requests)
		}
		if secs > 0 {
			s.RatePerSecond = float64(s.Requests) / secs
		}
		by[s.Service] = &s
	}
	lr, err := l.run(ctx, logs, DefaultLimits())
	if err != nil {
		return res, err
	}
	for _, row := range lr.Rows {
		if len(row) < 3 {
			return res, Undetermined("the local ClickStack returned %d columns for the overview's logs, want 3", len(row))
		}
		name := AsString(row[0])
		s, ok := by[name]
		if !ok {
			s = &ServiceOverview{Service: name}
			by[name] = s
		}
		s.ErrorLogs = AsInt(row[2])
	}
	for _, s := range by {
		if s.Service != "" {
			res.Services = append(res.Services, *s)
		}
	}
	sortOverview(res.Services)
	return res, nil
}

// sortOverview orders services busiest first, then by error logs, then by
// name: the control plane's order.
func sortOverview(ss []ServiceOverview) {
	sort.Slice(ss, func(i, j int) bool {
		a, b := ss[i], ss[j]
		if a.Requests != b.Requests {
			return a.Requests > b.Requests
		}
		if a.ErrorLogs != b.ErrorLogs {
			return a.ErrorLogs > b.ErrorLogs
		}
		return a.Service < b.Service
	})
}
