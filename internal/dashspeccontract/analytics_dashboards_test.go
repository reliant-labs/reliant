// Copyright (c) 2025 Reliant Labs

package dashspeccontract

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/dashspec"
	"github.com/reliant-labs/forge/pkg/obsquery"
)

// specPath is the observability spec this repository ships (forge generates
// it; the release bundle carries it to the hosted console). It is read with
// os.ReadFile, in this process, so a regenerate that changes it re-runs these
// tests even under -short.
var specPath = filepath.Join("..", "..", "deploy", "observability", "spec", "project_gen.json")

// analyticsDashboardIDs are forge's built-in product-analytics dashboards
// (forge pkg/obsbuiltin, since forge #2075, which replaced the generated
// "usage" dashboard): the Users, Product and Acquisition dashboards, and the
// sql panels the Overview gains from them.
var analyticsDashboardIDs = []string{"overview", "users", "product", "acquisition"}

// tenantTables are the hosted tables a product-analytics statement may read:
// unqualified, so they resolve in the org's own database.
var tenantTables = map[string]bool{"product_events": true, "error_events": true}

// shippedSpecDashboards returns every dashboard of the shipped spec (the
// overview and the list), keyed by id, parsed by the validator control-plane
// runs on every dashboard it accepts.
func shippedSpecDashboards(t *testing.T) map[string]dashspec.Dashboard {
	t.Helper()
	raw, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("read the shipped spec: %v", err)
	}
	var spec struct {
		Overview   json.RawMessage   `json:"overview"`
		Dashboards []json.RawMessage `json:"dashboards"`
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatalf("decode %s: %v", specPath, err)
	}
	out := map[string]dashspec.Dashboard{}
	for _, d := range append([]json.RawMessage{spec.Overview}, spec.Dashboards...) {
		var probe struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(d, &probe); err != nil {
			t.Fatalf("decode a dashboard of %s: %v", specPath, err)
		}
		parsed, err := dashspec.ParseDashboard(probe.ID+".json", d)
		if err != nil {
			for _, p := range dashspec.ProblemsOf(err) {
				t.Errorf("%s: %s", p.Path, p.Message)
			}
			t.Fatalf("forge's validator refuses the shipped %q dashboard: %v", probe.ID, err)
		}
		out[probe.ID] = parsed
	}
	return out
}

// statement is one SELECT a panel runs: its SQL, or its TrendSQL (the
// sparkline, which the console runs as a sql panel of its own, through the
// same hosted executor, scope and parameters).
type statement struct {
	name, sql string
}

// analyticsStatements returns every statement the shipped analytics
// dashboards' sql panels run, named "<dashboard>/<panel>" and
// "<dashboard>/<panel>/trend". Each dashboard must exist and carry an env
// filter, and between them they must run some trends.
func analyticsStatements(t *testing.T) []statement {
	t.Helper()
	dashboards := shippedSpecDashboards(t)
	var out []statement
	trends := 0
	for _, id := range analyticsDashboardIDs {
		d, ok := dashboards[id]
		if !ok {
			t.Fatalf("%s has no %q dashboard", specPath, id)
		}
		envFilter := false
		for _, f := range d.Filters {
			envFilter = envFilter || (f.ID == "env" && f.Field == "deployment.environment.name")
		}
		if !envFilter {
			t.Errorf("the %q dashboard has no env filter; the console binds it to {env_ids}", id)
		}
		sqlPanels := 0
		for _, p := range d.Panels {
			if p.Kind != dashspec.KindSQL {
				continue
			}
			sqlPanels++
			out = append(out, statement{id + "/" + p.ID, p.SQL})
			if strings.TrimSpace(p.TrendSQL) != "" {
				trends++
				out = append(out, statement{id + "/" + p.ID + "/trend", p.TrendSQL})
			}
		}
		if sqlPanels == 0 {
			t.Errorf("the %q dashboard has no sql panels", id)
		}
	}
	// The headline figures carry sparklines. A generator change that drops
	// them all would leave the checks below running over SQL alone; say so.
	if trends == 0 {
		t.Errorf("no analytics panel has a trendSql; the trend checks ran over nothing")
	}
	return out
}

// tableRef matches every name a statement reads FROM or JOINs, unless it
// opens a subquery. Group 1 is set for an ARRAY JOIN, which unfolds an array
// expression of the rows already read (`ARRAY JOIN range(7) AS offset`) and
// reads no table.
var tableRef = regexp.MustCompile(`(?i)\b(?:FROM|(ARRAY\s+)?JOIN)\s+([A-Za-z_][A-Za-z0-9_.]*)`)

// cteName matches each common table expression a statement defines
// (`WITH web AS (`, `, visits AS (`); a read of one reads no table.
var cteName = regexp.MustCompile(`(?i)(?:\bWITH|,)\s*([A-Za-z_][A-Za-z0-9_]*)\s+AS\s*\(`)

// quoted matches a string literal or a quoted column label, which may hold
// "FROM a visit" without reading anything.
var quoted = regexp.MustCompile(`'(?:[^'\\]|\\.)*'|"[^"]*"`)

// envPredicate narrows a read to the viewer's environments. It matches every
// row when the bound list is empty (an unrestricted viewer), so it is a
// filter, not the authorization boundary: that is the org's own database the
// statement runs in (TestAnalyticsDashboardsBindTheViewersScope).
const envPredicate = "(empty({env_ids:Array(String)}) OR has({env_ids:Array(String)}, EnvId))"

// scopeProblems returns why a statement breaks the analytics contract: it
// reads a table other than the unqualified tenant tables (or one of its own
// CTEs), reads none, or reads a tenant table without the environment
// predicate. Empty means it holds.
func scopeProblems(sql string) []string {
	var problems []string
	ctes := map[string]bool{}
	for _, m := range cteName.FindAllStringSubmatch(sql, -1) {
		ctes[m[1]] = true
	}
	reads := 0
	for _, m := range tableRef.FindAllStringSubmatch(quoted.ReplaceAllString(sql, "''"), -1) {
		switch {
		case m[1] != "":
			// ARRAY JOIN: an array expression, not a table.
		case ctes[m[2]]:
			// One of the statement's own CTEs, checked where it is defined.
		case tenantTables[m[2]]:
			reads++
		default:
			problems = append(problems, fmt.Sprintf("reads table %q; an analytics statement reads only the unqualified tenant tables of the org's own database", m[2]))
		}
	}
	if reads == 0 {
		return append(problems, "reads no tenant table")
	}
	if n := strings.Count(sql, envPredicate); n < reads {
		problems = append(problems, fmt.Sprintf("%d reads of a tenant table, %d narrowed by the environment scope %s", reads, n, envPredicate))
	}
	return problems
}

// TestAnalyticsDashboardsReadOnlyScopedTenantTables: every statement the
// shipped analytics dashboards run (each sql panel's SQL and TrendSQL) reads
// only the unqualified product_events and error_events, and narrows each read
// by the environment predicate. An unqualified table resolves in the database
// the hosted executor runs the statement in (the org's own,
// obsquery.Scope.Database), so the statement itself names no other tenant's
// data: that, not the predicate, is the isolation boundary. A read without the
// predicate would ignore a restricted viewer's environments.
func TestAnalyticsDashboardsReadOnlyScopedTenantTables(t *testing.T) {
	for _, s := range analyticsStatements(t) {
		t.Run(s.name, func(t *testing.T) {
			if err := dashspec.CheckSQL(s.sql); err != nil {
				t.Errorf("refused by the SQL guard: %v", err)
			}
			for _, problem := range scopeProblems(s.sql) {
				t.Errorf("%s:\n%s", problem, s.sql)
			}
		})
	}
}

// TestScopeProblemsRefusesAnUnscopedRead proves the check above fails when it
// should, on a statement shaped like the shipped ones (a CTE over
// product_events, a quoted label holding "FROM", a rolling window under an
// ARRAY JOIN): with the predicate removed, with a second, unscoped read, with
// a read of another database's product_events or of another table.
func TestScopeProblemsRefusesAnUnscopedRead(t *testing.T) {
	scoped := "WITH web AS (SELECT DISTINCT UserId, toDate(Timestamp) AS seen FROM product_events WHERE Timestamp < {to:DateTime64(9)} AND " +
		envPredicate + ") SELECT addDays(seen, offset) AS day, uniqExact(UserId) AS \"Users FROM a visit\" FROM web ARRAY JOIN range(7) AS offset GROUP BY day ORDER BY day"
	if p := scopeProblems(scoped); len(p) != 0 {
		t.Fatalf("the scoped statement is refused: %v", p)
	}
	for name, sql := range map[string]string{
		"no environment predicate": strings.Replace(scoped, " AND "+envPredicate, "", 1),
		"a second, unscoped read":  scoped + " UNION ALL SELECT toDate(Timestamp), count() FROM error_events GROUP BY 1",
		"another org's database":   strings.Replace(scoped, "FROM product_events", "FROM t_org_b.product_events", 1),
		"another table":            strings.Replace(scoped, "FROM product_events", "FROM otel_logs", 1),
		"no table at all":          "SELECT today() AS day, 1 AS n",
	} {
		t.Run(name, func(t *testing.T) {
			if p := scopeProblems(sql); len(p) == 0 {
				t.Errorf("accepted:\n%s", sql)
			}
		})
	}
}

// TestAnalyticsDashboardsBindTheViewersScope compiles every shipped analytics
// statement with forge's sql-panel compiler, the one the hosted console runs,
// and checks what it binds: a viewer restricted to no environment gets a list
// no row matches (so the env predicate is false for every row), a viewer
// restricted to one environment gets that environment alone, an unrestricted
// viewer gets an empty list (the predicate then matches every row of the
// org's own database: it filters, it does not authorize), and a scope that
// names the server's own database rather than an org's is refused, which is
// the boundary.
func TestAnalyticsDashboardsBindTheViewersScope(t *testing.T) {
	end := time.Date(2026, 3, 16, 12, 0, 0, 0, time.UTC)
	org := func(mut func(*obsquery.Scope)) obsquery.Scope {
		s := obsquery.Scope{Database: "t_org_a", Start: end.Add(-30 * 24 * time.Hour), End: end}
		if mut != nil {
			mut(&s)
		}
		return s
	}
	cases := map[string]struct {
		scope  obsquery.Scope
		envIDs string
	}{
		"restricted to no environment": {
			scope:  org(func(s *obsquery.Scope) { s.EnvRestricted = true }),
			envIDs: "['(none)']",
		},
		"unrestricted": {
			scope:  org(nil),
			envIDs: "[]",
		},
		"restricted to one environment": {
			scope: org(func(s *obsquery.Scope) {
				s.EnvRestricted = true
				s.EnvIDs = []string{"env-a"}
				s.Envs = []obsquery.Env{{ID: "env-a", Name: "prod"}}
			}),
			envIDs: "['env-a']",
		},
	}
	for _, st := range analyticsStatements(t) {
		for name, c := range cases {
			t.Run(st.name+"/"+name, func(t *testing.T) {
				stmt, err := obsquery.SQLPanel{Scope: c.scope, SQL: st.sql, StepSeconds: 3600}.Compile()
				if err != nil {
					t.Fatalf("compile: %v", err)
				}
				if stmt.SQL != st.sql {
					t.Errorf("the compiled statement differs from the shipped one: nothing may be spliced into it")
				}
				if got := stmt.Params[dashspec.SQLParamEnvIDs]; got != c.envIDs {
					t.Errorf("env_ids = %s; want %s", got, c.envIDs)
				}
			})
		}
		t.Run(st.name+"/server database", func(t *testing.T) {
			s := org(nil)
			s.Database = obsquery.LocalDatabase
			if _, err := (obsquery.SQLPanel{Scope: s, SQL: st.sql, StepSeconds: 3600}).Compile(); err == nil {
				t.Errorf("a hosted scope naming the server's %q database compiles; it must be refused", obsquery.LocalDatabase)
			}
		})
	}
}
