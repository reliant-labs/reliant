package obsbackend

// Release health over the project's own ClickStack. There are no promotions
// locally, so the caller names the instant P (`--since`, or the start of the
// last `forge env up`); the windows, thresholds and verdict are
// pkg/releasehealth's, the same rule the control plane applies.
//
// Not evaluated locally, and said so in the report: regressed issues (issues
// carry no state here), firing alerts and workload restarts (no alert state or
// orchestrator).

import (
	"context"
	"sort"
	"strconv"
	"time"

	"github.com/reliant-labs/forge/pkg/obsquery"
	"github.com/reliant-labs/forge/pkg/release"
	"github.com/reliant-labs/forge/pkg/releasehealth"
)

// localNewIssuesSQL groups server errors by the sfp1 fingerprint over both
// windows and keeps the groups first seen at or after P. It is the
// query-time form of the control plane's `issues` table; the expressions are
// pkg/obsquery's, the values are bound parameters.
const localNewIssuesSQL = `SELECT ` + obsquery.ServerFingerprintSQL + ` AS fp,
       any(` + obsquery.ServerTitleSQL + `) AS title,
       any(` + obsquery.ServerCulpritSQL + `) AS culprit,
       any(ServiceName) AS service,
       count() AS events,
       min(Timestamp) AS first_seen
FROM otel_logs
WHERE Timestamp >= {from:DateTime64(9)} AND Timestamp < {to:DateTime64(9)}
  AND ` + obsquery.ServerErrorFilterSQL + `
  AND ResourceAttributes['deployment.environment.name'] = {env:String}
GROUP BY fp
HAVING first_seen >= {since:DateTime64(9)}
ORDER BY events DESC, fp
LIMIT 100`

// ReleaseHealth implements HealthReader.
func (l Local) ReleaseHealth(ctx context.Context, req HealthRequest) (HealthResult, error) {
	if req.PromotedAt.IsZero() {
		return HealthResult{}, Wrong("a local env has no promotions: give the instant to judge from (--since)")
	}
	th, err := releasehealth.Resolve(req.Options)
	if err != nil {
		return HealthResult{}, Wrong("%s", trimInvalid(err))
	}
	now := req.Now
	if now.IsZero() {
		now = time.Now()
	}
	if req.PromotedAt.After(now) {
		return HealthResult{}, Wrong("--since is in the future")
	}
	win := releasehealth.Plan(req.PromotedAt, th.Window, nil, nil, now)
	var out HealthResult

	before, err := l.windowStats(ctx, win.Before, &out)
	if err != nil {
		return out, err
	}
	after, err := l.windowStats(ctx, win.After, &out)
	if err != nil {
		return out, err
	}
	issues, err := l.newIssues(ctx, win, &out)
	if err != nil {
		return out, err
	}

	names := map[string]bool{}
	for s := range before {
		names[s] = true
	}
	for s := range after {
		names[s] = true
	}
	var services []releasehealth.ServiceHealth
	for s := range names {
		services = append(services, releasehealth.ServiceHealth{Service: s, Before: before[s], After: after[s]})
	}
	sort.Slice(services, func(i, j int) bool { return services[i].Service < services[j].Service })

	out.Report = releasehealth.Evaluate(releasehealth.Input{
		Subject:      releasehealth.Subject{PromotedAt: req.PromotedAt.UTC()},
		Windows:      win,
		Services:     services,
		NewIssues:    issues,
		Thresholds:   th,
		NotEvaluated: []string{release.HealthRegressedIssue, release.HealthAlertFiring, release.HealthWorkloadRestarts},
	})
	return out, nil
}

// windowStats is the per-service RED figures of one window over weighted
// server spans.
func (l Local) windowStats(ctx context.Context, r releasehealth.Range, out *HealthResult) (map[string]releasehealth.WindowStats, error) {
	stats := map[string]releasehealth.WindowStats{}
	if r.To.Sub(r.From) < time.Second {
		return stats, nil // a window the compiler refuses as empty has no traffic to count
	}
	st, err := obsquery.Overview{Scope: LocalScope(Narrow{Range: Range{From: r.From, To: r.To}}, l.Env)}.RED()
	if err != nil {
		return nil, compileErr(err)
	}
	out.Statements = append(out.Statements, stmt("red "+r.From.UTC().Format(time.RFC3339), st))
	res, err := l.run(ctx, st, DefaultLimits())
	if err != nil {
		return nil, err
	}
	for _, row := range res.Rows {
		if len(row) < 4 {
			return nil, Undetermined("the local ClickStack returned %d columns for release health, want 4", len(row))
		}
		q := AsFloats(row[3])
		if len(q) < 3 {
			q = []float64{0, 0, 0}
		}
		ws := releasehealth.WindowStats{Requests: AsInt(row[1]), P50Ms: finite(q[0]), P95Ms: finite(q[1]), P99Ms: finite(q[2])}
		if ws.Requests > 0 {
			ws.ErrorRate = float64(AsInt(row[2])) / float64(ws.Requests)
		}
		stats[AsString(row[0])] = ws
	}
	return stats, nil
}

// newIssues is the sfp1 groups first seen at or after P.
func (l Local) newIssues(ctx context.Context, win releasehealth.Windows, out *HealthResult) ([]releasehealth.Issue, error) {
	end := win.After.To
	if end.Sub(win.Before.From) < time.Second {
		return nil, nil
	}
	sc := l.sqlScope(Narrow{Range: Range{From: win.Before.From, To: end}})
	st, err := obsquery.SQLPanel{Scope: sc, SQL: localNewIssuesSQL, StepSeconds: 60}.Compile()
	if err != nil {
		return nil, compileErr(err)
	}
	st.Params["env"] = l.Env
	st.Params["since"] = unixNanoText(win.After.From)
	out.Statements = append(out.Statements, stmt("new issues (sfp1 groups first seen after the promotion)", st))
	res, err := l.run(ctx, st, DefaultLimits())
	if err != nil {
		return nil, err
	}
	var issues []releasehealth.Issue
	for _, row := range res.Rows {
		if len(row) < 6 {
			return nil, Undetermined("the local ClickStack returned %d columns for issues, want 6", len(row))
		}
		issues = append(issues, releasehealth.Issue{
			ID: "sfp1-" + AsString(row[0]), Title: AsString(row[1]), Culprit: AsString(row[2]), Service: AsString(row[3]),
			Events: AsInt(row[4]), FirstSeenAt: AsTime(row[5]),
		})
	}
	return issues, nil
}

// unixNanoText renders t as the "<seconds>.<9 digits>" a DateTime64(9)
// parameter parses the same in every session timezone.
func unixNanoText(t time.Time) string {
	ns := t.UnixNano()
	sec, frac := ns/1e9, ns%1e9
	if frac < 0 {
		sec, frac = sec-1, frac+1e9
	}
	f := strconv.FormatInt(frac, 10)
	for len(f) < 9 {
		f = "0" + f
	}
	return strconv.FormatInt(sec, 10) + "." + f
}

func trimInvalid(err error) string {
	s := err.Error()
	const p = "invalid release-health request: "
	if len(s) > len(p) && s[:len(p)] == p {
		return s[len(p):]
	}
	return s
}
