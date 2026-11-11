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

// usageDashboardID is forge's generated product-usage dashboard
// (obsdash.ProductUsageID).
const usageDashboardID = "usage"

// usageDashboard returns the shipped product-usage dashboard, parsed by the
// validator control-plane runs on every dashboard it accepts.
func usageDashboard(t *testing.T) dashspec.Dashboard {
	t.Helper()
	raw, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("read the shipped spec: %v", err)
	}
	var spec struct {
		Dashboards []json.RawMessage `json:"dashboards"`
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatalf("decode %s: %v", specPath, err)
	}
	for _, d := range spec.Dashboards {
		var probe struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(d, &probe); err != nil {
			t.Fatalf("decode a dashboard of %s: %v", specPath, err)
		}
		if probe.ID != usageDashboardID {
			continue
		}
		parsed, err := dashspec.ParseDashboard(usageDashboardID+".json", d)
		if err != nil {
			for _, p := range dashspec.ProblemsOf(err) {
				t.Errorf("%s: %s", p.Path, p.Message)
			}
			t.Fatalf("forge's validator refuses the shipped %q dashboard: %v", usageDashboardID, err)
		}
		return parsed
	}
	t.Fatalf("%s has no %q dashboard", specPath, usageDashboardID)
	return dashspec.Dashboard{}
}

// tableRef matches every table a statement reads: the name after FROM or
// JOIN, unless it opens a subquery. Group 1 is set for an ARRAY JOIN, which
// unfolds an array expression of the rows already read (`ARRAY JOIN range(7)
// AS offset`) and reads no table; any FROM inside its expression is matched on
// its own.
var tableRef = regexp.MustCompile(`(?i)\b(?:FROM|(ARRAY\s+)?JOIN)\s+([^\s(]+)`)

// The two predicates every product_events read must carry: the viewer's
// environment restriction and the dashboard's service filter. Each matches
// every row when its bound list is empty (an unrestricted viewer, no service
// chosen), so they are filters, not the authorization boundary: that is the
// org's own database the statement runs in (TestUsageDashboardBindsTheViewersScope).
const (
	envPredicate     = "(empty({env_ids:Array(String)}) OR has({env_ids:Array(String)}, EnvId))"
	servicePredicate = "(empty({services:Array(String)}) OR has({services:Array(String)}, ServiceName))"
)

// statement is one SELECT a panel runs: its SQL, or its TrendSQL (the
// sparkline, which the console runs as a sql panel of its own, through the
// same hosted executor, scope and parameters).
type statement struct {
	name, sql string
}

// statements returns every statement the panel runs: SQL as "<id>", and
// TrendSQL, when set, as "<id>/trend".
func statements(p dashspec.Panel) []statement {
	out := []statement{{p.ID, p.SQL}}
	if strings.TrimSpace(p.TrendSQL) != "" {
		out = append(out, statement{p.ID + "/trend", p.TrendSQL})
	}
	return out
}

// scopeProblems returns why a statement breaks the product-usage contract:
// it reads a table other than the unqualified product_events, reads none, or
// reads product_events without both the environment and the service
// predicate. Empty means it holds.
func scopeProblems(sql string) []string {
	var problems []string
	reads := 0
	for _, m := range tableRef.FindAllStringSubmatch(sql, -1) {
		if m[1] != "" {
			continue // ARRAY JOIN: an array expression, not a table
		}
		if m[2] != "product_events" {
			problems = append(problems, fmt.Sprintf("reads table %q; a product-usage statement reads only the unqualified product_events of the org's own database", m[2]))
			continue
		}
		reads++
	}
	if reads == 0 {
		return append(problems, "reads no product_events")
	}
	if n := strings.Count(sql, envPredicate); n != reads {
		problems = append(problems, fmt.Sprintf("%d reads of product_events, %d narrowed by the environment scope %s", reads, n, envPredicate))
	}
	if n := strings.Count(sql, servicePredicate); n != reads {
		problems = append(problems, fmt.Sprintf("%d reads of product_events, %d narrowed by the service filter %s", reads, n, servicePredicate))
	}
	return problems
}

// TestUsageDashboardReadsOnlyScopedProductEvents: every panel of the shipped
// product-usage dashboard is a sql panel, and every statement it runs (its
// SQL and its TrendSQL sparkline) reads only the unqualified product_events
// table, and narrows each read of it by the environment and service filters.
// An unqualified table resolves in the database the hosted executor runs the
// statement in (the org's own, obsquery.Scope.Database), so the statement
// itself names no other tenant's data: that, not the predicates, is the
// isolation boundary. A read without the predicates would ignore a restricted
// viewer's environments.
func TestUsageDashboardReadsOnlyScopedProductEvents(t *testing.T) {
	d := usageDashboard(t)
	if len(d.Panels) == 0 {
		t.Fatalf("the %q dashboard has no panels", usageDashboardID)
	}
	filters := map[string]string{}
	for _, f := range d.Filters {
		filters[f.ID] = f.Field
	}
	if filters["env"] != "deployment.environment.name" || filters["service"] != "service.name" {
		t.Errorf("the %q dashboard's filters are %v; want env and service, which the console binds to {env_ids} and {services}", usageDashboardID, filters)
	}
	trends := 0
	for _, p := range d.Panels {
		if p.Kind != dashspec.KindSQL {
			t.Errorf("%s: kind = %s; the product-usage panels are sql panels", p.ID, p.Kind)
			continue
		}
		for _, s := range statements(p) {
			if s.name != p.ID {
				trends++
			}
			t.Run(s.name, func(t *testing.T) {
				for _, problem := range scopeProblems(s.sql) {
					t.Errorf("%s:\n%s", problem, s.sql)
				}
			})
		}
	}
	// forge #2010 gave the headline figures sparklines. A generator change
	// that drops them all would leave this test checking SQL alone; say so.
	if trends == 0 {
		t.Errorf("no panel of the %q dashboard has a trendSql; the trend checks above ran over nothing", usageDashboardID)
	}
}

// TestScopeProblemsRefusesAnUnscopedTrend proves the check above fails when it
// should, on a statement shaped like the shipped rolling-window trends (a
// subquery read under an ARRAY JOIN): with either predicate removed, with a
// second, unscoped read, or with a read of another database's product_events.
func TestScopeProblemsRefusesAnUnscopedTrend(t *testing.T) {
	scoped := "SELECT addDays(seen, offset) AS day, uniqExact(UserId) AS wau FROM (SELECT DISTINCT UserId, toDate(Timestamp) AS seen FROM product_events WHERE Timestamp < {to:DateTime64(9)} AND " +
		envPredicate + " AND " + servicePredicate + ") ARRAY JOIN range(7) AS offset GROUP BY day ORDER BY day"
	if p := scopeProblems(scoped); len(p) != 0 {
		t.Fatalf("the scoped trend is refused: %v", p)
	}
	for name, sql := range map[string]string{
		"no environment predicate": strings.Replace(scoped, " AND "+envPredicate, "", 1),
		"no service predicate":     strings.Replace(scoped, " AND "+servicePredicate, "", 1),
		"a second, unscoped read":  scoped + " UNION ALL SELECT toDate(Timestamp), count() FROM product_events GROUP BY 1",
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

// TestUsageDashboardBindsTheViewersScope compiles every statement a shipped
// panel runs (its SQL and its TrendSQL sparkline) with forge's sql-panel
// compiler, the one the hosted console runs, and checks what
// it binds: a viewer restricted to no environment gets a list no row matches
// (so the env predicate above is false for every row), a viewer restricted to
// one environment gets that environment alone, the service filter is bound,
// an unrestricted viewer gets empty lists (the predicates then match every row
// of the org's own database: they filter, they do not authorize), and a scope
// that names the server's own database rather than an org's is refused, which
// is the boundary.
func TestUsageDashboardBindsTheViewersScope(t *testing.T) {
	d := usageDashboard(t)
	end := time.Date(2026, 3, 16, 12, 0, 0, 0, time.UTC)
	org := func(mut func(*obsquery.Scope)) obsquery.Scope {
		s := obsquery.Scope{Database: "t_org_a", Start: end.Add(-30 * 24 * time.Hour), End: end}
		if mut != nil {
			mut(&s)
		}
		return s
	}
	cases := map[string]struct {
		scope            obsquery.Scope
		envIDs, services string
	}{
		"restricted to no environment": {
			scope:  org(func(s *obsquery.Scope) { s.EnvRestricted = true }),
			envIDs: "['(none)']", services: "[]",
		},
		"unrestricted, no service filter": {
			scope:  org(nil),
			envIDs: "[]", services: "[]",
		},
		"restricted to one environment, one service": {
			scope: org(func(s *obsquery.Scope) {
				s.EnvRestricted = true
				s.EnvIDs = []string{"env-a"}
				s.Envs = []obsquery.Env{{ID: "env-a", Name: "prod"}}
				s.Services = []string{"api"}
			}),
			envIDs: "['env-a']", services: "['api']",
		},
	}
	for _, p := range d.Panels {
		for _, st := range statements(p) {
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
					if got := stmt.Params[dashspec.SQLParamServices]; got != c.services {
						t.Errorf("services = %s; want %s", got, c.services)
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
}
