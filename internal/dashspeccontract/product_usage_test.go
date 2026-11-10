// Copyright (c) 2025 Reliant Labs

package dashspeccontract

import (
	"encoding/json"
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
// JOIN, unless it opens a subquery.
var tableRef = regexp.MustCompile(`(?i)\b(?:FROM|JOIN)\s+([^\s(]+)`)

// The two predicates every product_events read must carry: the viewer's
// environment restriction and the dashboard's service filter. Each matches
// every row when its bound list is empty (an unrestricted viewer, no service
// chosen), so they are filters, not the authorization boundary: that is the
// org's own database the statement runs in (TestUsageDashboardBindsTheViewersScope).
const (
	envPredicate     = "(empty({env_ids:Array(String)}) OR has({env_ids:Array(String)}, EnvId))"
	servicePredicate = "(empty({services:Array(String)}) OR has({services:Array(String)}, ServiceName))"
)

// TestUsageDashboardReadsOnlyScopedProductEvents: every panel of the shipped
// product-usage dashboard is a sql panel whose statement reads only the
// unqualified product_events table, and narrows each read of it by the
// environment and service filters. An unqualified table resolves in the
// database the hosted executor runs the statement in (the org's own,
// obsquery.Scope.Database), so the statement itself names no other tenant's
// data: that, not the predicates, is the isolation boundary. A read without
// the predicates would ignore a restricted viewer's environments.
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
	for _, p := range d.Panels {
		t.Run(p.ID, func(t *testing.T) {
			if p.Kind != dashspec.KindSQL {
				t.Fatalf("kind = %s; the product-usage panels are sql panels", p.Kind)
			}
			reads := 0
			for _, m := range tableRef.FindAllStringSubmatch(p.SQL, -1) {
				if m[1] != "product_events" {
					t.Errorf("reads table %q; a product-usage panel reads only the unqualified product_events of the org's own database", m[1])
					continue
				}
				reads++
			}
			if reads == 0 {
				t.Fatalf("reads no product_events:\n%s", p.SQL)
			}
			if n := strings.Count(p.SQL, envPredicate); n != reads {
				t.Errorf("%d reads of product_events, %d narrowed by the environment scope %s:\n%s", reads, n, envPredicate, p.SQL)
			}
			if n := strings.Count(p.SQL, servicePredicate); n != reads {
				t.Errorf("%d reads of product_events, %d narrowed by the service filter %s:\n%s", reads, n, servicePredicate, p.SQL)
			}
		})
	}
}

// TestUsageDashboardBindsTheViewersScope compiles every shipped panel with
// forge's sql-panel compiler, the one the hosted console runs, and checks what
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
		for name, c := range cases {
			t.Run(p.ID+"/"+name, func(t *testing.T) {
				stmt, err := obsquery.SQLPanel{Scope: c.scope, SQL: p.SQL, StepSeconds: 3600}.Compile()
				if err != nil {
					t.Fatalf("compile: %v", err)
				}
				if stmt.SQL != p.SQL {
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
		t.Run(p.ID+"/server database", func(t *testing.T) {
			s := org(nil)
			s.Database = obsquery.LocalDatabase
			if _, err := (obsquery.SQLPanel{Scope: s, SQL: p.SQL, StepSeconds: 3600}).Compile(); err == nil {
				t.Errorf("a hosted scope naming the server's %q database compiles; it must be refused", obsquery.LocalDatabase)
			}
		})
	}
}
