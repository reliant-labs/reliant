package obsdash

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/dashspec"
	"github.com/reliant-labs/forge/pkg/obsquery"
)

func TestProductUsageDashboardGolden(t *testing.T) {
	d := withReleaseMarkers(BuildProductUsage("shop"))
	b, err := marshalDashboardFile(d)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "shop-usage_gen.spec.json", b)
	if _, err := ParseDashboard("shop-usage_gen.json", b); err != nil {
		t.Errorf("the product usage dashboard does not validate as a spec dashboard:\n%v", err)
	}
}

// Every panel's statement must pass what the console runs a sql panel through:
// the alert-rule guard (dashspec.CheckSQL) and obsquery's compile, which binds
// the dashboard scope. A statement that names a parameter the scope does not
// bind would fail at query time in a customer's console, so that is checked
// too.
func TestProductUsagePanelsPassTheSQLGuard(t *testing.T) {
	sc := obsquery.Scope{Database: "t_org_a", Start: time.Unix(1700000000, 0), End: time.Unix(1700086400, 0),
		EnvRestricted: true, EnvIDs: []string{"e1"}, Envs: []obsquery.Env{{ID: "e1", Name: "prod"}}}
	param := regexp.MustCompile(`\{([a-z_]+):[^}]+\}`)
	d := BuildProductUsage("shop")
	if len(d.Panels) == 0 {
		t.Fatal("no panels")
	}
	for _, p := range d.Panels {
		if p.Kind != KindSQL {
			t.Errorf("%s: kind %s, want sql (product_events is read with sql panels)", p.ID, p.Kind)
			continue
		}
		if err := dashspec.CheckSQL(p.SQL); err != nil {
			t.Errorf("%s: refused by the SQL guard: %v\n%s", p.ID, err, p.SQL)
		}
		st, err := obsquery.SQLPanel{Scope: sc, SQL: p.SQL, StepSeconds: 3600}.Compile()
		if err != nil {
			t.Errorf("%s: does not compile: %v", p.ID, err)
			continue
		}
		for _, m := range param.FindAllStringSubmatch(p.SQL, -1) {
			if _, ok := st.Params[m[1]]; !ok {
				t.Errorf("%s: uses {%s}, which the console does not bind", p.ID, m[1])
			}
		}
		if !strings.Contains(p.SQL, "FROM product_events") {
			t.Errorf("%s: does not read product_events", p.ID)
		}
		// Scoped to the viewer's environments and services, always.
		if !strings.Contains(p.SQL, "{env_ids:Array(String)}") || !strings.Contains(p.SQL, "{services:Array(String)}") {
			t.Errorf("%s: ignores the environment or service filter", p.ID)
		}
		// Generic: nothing of one product's own vocabulary.
		for _, word := range []string{"internal_user", "message_sent", "workflow_", "reliant"} {
			if strings.Contains(p.SQL, word) {
				t.Errorf("%s: carries %q, which belongs to one product, not every forge project", p.ID, word)
			}
		}
	}
}

// The automatic CRUD events are matched by suffix, never by a list of
// entities, so an entity the project adds later shows without a regenerate;
// and pages are read from page_viewed's route.
func TestProductUsageReadsTheAutomaticEvents(t *testing.T) {
	panels := map[string]Panel{}
	for _, p := range BuildProductUsage("shop").Panels {
		panels[p.ID] = p
	}
	for _, id := range []string{"entity-actions", "entity-actions-by-entity"} {
		if !strings.Contains(panels[id].SQL, "match(EventName, '_(created|updated|deleted)$')") {
			t.Errorf("%s does not select the CRUD events by suffix:\n%s", id, panels[id].SQL)
		}
	}
	if sql := panels["top-pages"].SQL; !strings.Contains(sql, "EventName = 'page_viewed'") || !strings.Contains(sql, "Props['route']") {
		t.Errorf("top-pages does not read page_viewed by route:\n%s", sql)
	}
	for _, id := range []string{"active-users", "daily-active-users", "new-vs-returning", "weekly-retention", "most-active-users", "events-by-name"} {
		if _, ok := panels[id]; !ok {
			t.Errorf("missing panel %s", id)
		}
	}
}

// Retention reads at most twelve weekly cohorts: the default product_events
// retention is 90 days, so a thirteenth week would be a partial cohort.
func TestProductUsageRetentionFitsTheDefaultRetention(t *testing.T) {
	sql := weeklyRetentionSQL()
	if retentionWeeks*7 > 90 {
		t.Fatalf("%d weeks exceed the 90-day default retention", retentionWeeks)
	}
	if !strings.Contains(sql, "AS week_11") || strings.Contains(sql, "week_12") {
		t.Errorf("retention should report weeks 1..11 after the cohort week:\n%s", sql)
	}
	if !strings.Contains(sql, "toMonday({to:DateTime64(9)}) - INTERVAL 11 WEEK") {
		t.Errorf("retention should keep the 12 cohorts before the end of the range:\n%s", sql)
	}
}

// First-seen is read from a fixed window before the end of the range, never
// from whatever the table still holds: product_events expires rows, so the
// oldest row would make a long-time user new again the day their earlier
// events aged out. The window fits the default retention, and covers every
// retention cohort.
func TestProductUsageFirstSeenIsBoundedToAWindow(t *testing.T) {
	if firstSeenDays > 90 {
		t.Fatalf("first-seen window of %d days exceeds the 90-day default retention", firstSeenDays)
	}
	if firstSeenDays < retentionWeeks*7 {
		t.Fatalf("first-seen window of %d days does not cover the %d retention cohorts", firstSeenDays, retentionWeeks)
	}
	for name, c := range map[string]struct{ sql, bound string }{
		"new-vs-returning": {newVsReturningSQL, "Timestamp >= least({from:DateTime64(9)}, {to:DateTime64(9)} - INTERVAL 90 DAY) AND Timestamp < {to:DateTime64(9)}"},
		// Retention's window ignores the range, as its cohorts do.
		"weekly-retention": {weeklyRetentionSQL(), "Timestamp >= {to:DateTime64(9)} - INTERVAL 90 DAY AND Timestamp < {to:DateTime64(9)}"},
	} {
		sql, bound := c.sql, c.bound
		if strings.Count(sql, "FROM product_events") != strings.Count(sql, "Timestamp >= ") {
			t.Errorf("%s: a product_events read has no lower time bound:\n%s", name, sql)
		}
		if !strings.Contains(sql, bound) {
			t.Errorf("%s: first-seen is not bounded to the %d-day window:\n%s", name, firstSeenDays, sql)
		}
	}
}
