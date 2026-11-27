// Copyright (c) 2026 Reliant Labs

package analytics

import (
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/dashspec"

	"github.com/reliant-labs/reliant/internal/gtm"
)

var update = flag.Bool("update", false, "rewrite the golden SQL in testdata/")

const tenantZero = "55949942-3ee9-4cfc-bb7c-817bdba75186"

var (
	from = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	to   = time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)
)

// sqlQueries are one query per sql template, the golden SQL's inputs.
var sqlQueries = map[string]gtm.AnalyticsQuery{
	"event_counts":          {Template: gtm.AnalyticsEventCounts, From: from, To: to, Limit: 50},
	"event_counts_filtered": {Template: gtm.AnalyticsEventCounts, Events: []string{"signup_completed", "project_created"}, From: from, To: to, Limit: 20},
	"event_trend":           {Template: gtm.AnalyticsEventTrend, Events: []string{"page_viewed", "chat.started"}, From: from, To: to, Limit: 200},
	"property_breakdown":    {Template: gtm.AnalyticsPropertyBreakdown, Events: []string{"page_viewed"}, Property: "route", From: from, To: to, Limit: 10},
}

func TestGoldenSQL(t *testing.T) {
	for name, q := range sqlQueries {
		t.Run(name, func(t *testing.T) {
			if err := validate(tenantZero, q); err != nil {
				t.Fatalf("validate: %v", err)
			}
			got := buildSQL(q) + "\n"
			path := filepath.Join("testdata", name+".sql")
			if *update {
				if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read golden (go test -update to write it): %v", err)
			}
			if got != string(want) {
				t.Errorf("SQL differs from %s:\n--- got\n%s--- want\n%s", path, got, want)
			}
		})
	}
}

// TestTemplatesPassTheControlPlaneGuard runs every template through the
// statement guard QueryPanel applies to a sql panel (forge dashspec.CheckSQL:
// one read-only SELECT, no writes, SETTINGS, FORMAT or table functions).
func TestTemplatesPassTheControlPlaneGuard(t *testing.T) {
	for name, q := range sqlQueries {
		if err := dashspec.CheckSQL(buildSQL(q)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// identifierish is a column word that names an identifier.
var identifierish = regexp.MustCompile(`(?i)(^id$|id$|ids$|_id|uuid|visitor|session|email|^ip$|token|user)`)

// TestNoTemplateMentionsAnIdentifier: no template selects, groups by, or
// otherwise names a user, session, visitor or any other identifier column.
// The exceptions: EnvId inside the environment-scope predicate, which only
// narrows rows, and UserId (twice) inside the one
// uniqIf(UserId, UserId != ”) the k-anonymity threshold counts. Neither is
// ever returned.
func TestNoTemplateMentionsAnIdentifier(t *testing.T) {
	literal := regexp.MustCompile(`'(?:[^'\\]|\\.)*'`)
	word := regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)
	for name, q := range sqlQueries {
		sql := buildSQL(q)
		if strings.Count(sql, scope) != 1 {
			t.Fatalf("%s: the scope predicate should appear once:\n%s", name, sql)
		}
		kAnon := q.Template != gtm.AnalyticsEventTrend
		if peopleExpr != "uniqIf(UserId, UserId != '') AS people" {
			t.Fatalf("the threshold must count signed-in users only, uniqIf(UserId, UserId != ''): %q", peopleExpr)
		}
		exprs := map[bool]int{true: 1, false: 0}[kAnon]
		if strings.Count(sql, peopleExpr) != exprs || strings.Count(sql, "UserId") != 2*exprs {
			t.Fatalf("%s: UserId should appear only inside %q, %d time(s):\n%s", name, peopleExpr, exprs, sql)
		}
		rest := strings.Replace(strings.Replace(sql, scope, "", 1), peopleExpr, "", 1)
		rest = literal.ReplaceAllString(rest, "''")
		for _, w := range word.FindAllString(rest, -1) {
			if identifierish.MatchString(w) {
				t.Errorf("%s: names %q:\n%s", name, w, sql)
			}
		}
		for _, banned := range []string{"UserId", "user_id", "SessionId", "DayVisitor", "VisitorId", "OrgId", "TraceId", "SpanId", "EnvId"} {
			if strings.Contains(rest, banned) {
				t.Errorf("%s: mentions %s outside the scope predicate and the people count:\n%s", name, banned, sql)
			}
		}
		if kAnon {
			// The statement returns kind, the dimension and the counts; the
			// per-group user count stays inside it.
			outer := sql[strings.Index(sql, ")\nSELECT ")+2:]
			outer = outer[:strings.Index(outer, "\n")]
			if strings.Contains(outer, "people") || !strings.HasSuffix(outer, countColumn+", "+groupsColumn+", "+hiddenColumn+" FROM (") {
				t.Errorf("%s: returns %q", name, outer)
			}
			if !strings.Contains(sql, "WHERE people >= 3 ") || !strings.Contains(sql, "countIf(people < 3) AS hidden_groups") {
				t.Errorf("%s: no k=3 threshold:\n%s", name, sql)
			}
		}
	}
	// The scope predicate's only column besides Timestamp is EnvId.
	for _, w := range word.FindAllString(literal.ReplaceAllString(scope, ""), -1) {
		if identifierish.MatchString(w) && w != "EnvId" && w != "env_ids" {
			t.Errorf("scope names %q", w)
		}
	}
}

func TestSQLLimitIsTheQueryLimit(t *testing.T) {
	for _, limit := range []int{1, 50, gtm.MaxAnalyticsRows} {
		for _, name := range []string{"event_counts", "property_breakdown"} {
			q := sqlQueries[name]
			q.Limit = limit
			if sql := buildSQL(q); strings.Count(sql, " LIMIT ") != 1 || !strings.Contains(sql, " LIMIT "+strconv.Itoa(limit)+")\n") {
				t.Errorf("%s limit %d: %s", name, limit, sql)
			}
		}
		q := sqlQueries["event_trend"]
		q.Limit = limit
		if sql := buildSQL(q); !strings.HasSuffix(sql, "\nLIMIT "+strconv.Itoa(limit)) {
			t.Errorf("event_trend limit %d: %s", limit, sql)
		}
	}
}

func TestLitEscapes(t *testing.T) {
	for in, want := range map[string]string{
		"plain":  `'plain'`,
		"it's":   `'it\'s'`,
		`back\`:  `'back\\'`,
		`\'`:     `'\\\''`,
		"":       `''`,
		"a.b:c-": `'a.b:c-'`,
	} {
		if got := lit(in); got != want {
			t.Errorf("lit(%q) = %s, want %s", in, got, want)
		}
	}
}

// envPredicate is the environment filter every sql template must carry: the
// control plane only binds {env_ids} for a sql panel and never enforces it,
// so an Environment narrows these templates only through this predicate.
const envPredicate = "(empty({env_ids:Array(String)}) OR has({env_ids:Array(String)}, EnvId))"

func TestEveryTemplateFiltersByEnvironment(t *testing.T) {
	if !strings.Contains(scope, envPredicate) {
		t.Fatalf("scope lost the environment predicate: %s", scope)
	}
	for _, tmpl := range []gtm.AnalyticsTemplate{gtm.AnalyticsEventCounts, gtm.AnalyticsEventTrend, gtm.AnalyticsPropertyBreakdown} {
		found := false
		for name, q := range sqlQueries {
			if q.Template != tmpl {
				continue
			}
			found = true
			sql := buildSQL(q)
			if strings.Count(sql, "FROM product_events") != 1 || !strings.Contains(sql, "WHERE "+scope) {
				t.Errorf("%s: the product_events read is not filtered by environment:\n%s", name, sql)
			}
			if !strings.Contains(sql, envPredicate) {
				t.Errorf("%s: no {env_ids} predicate:\n%s", name, sql)
			}
		}
		if !found {
			t.Errorf("no golden query covers %s", tmpl)
		}
	}
}
