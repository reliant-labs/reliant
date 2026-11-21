package cli

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"testing"

	"github.com/reliant-labs/forge/internal/obsdash"
	"github.com/reliant-labs/forge/pkg/dashspec"
	"github.com/reliant-labs/forge/pkg/telemetry"
)

// A forge.ManagedDatabase is watched by default (kcl/database_telemetry.k).
// The cluster and hosted render-contract goldens each declare one database
// ("db") and no Telemetry, so what they carry is exactly the default on that
// runtime: these tests drive the production decoder, Normalize, Validate,
// dashspec.ParseDashboard and obsdash over it.

func TestRenderContract_ManagedDatabaseTelemetryByDefault(t *testing.T) {
	cases := []struct {
		golden  string
		runtime telemetry.Runtime
		// The service names the env carries: the database's, plus the
		// CloudNativePG operator's on a cluster (on hosting the operator
		// is the platform's).
		services []string
		cluster  string
		ns       []string
	}{
		{"cluster", telemetry.RuntimeCluster, []string{"cloudnative-pg", "db-postgres"}, "k3d-acme", []string{"acme-dev"}},
		{"hosted", telemetry.RuntimeHosted, []string{"db-postgres"}, "", []string{}},
	}
	for _, c := range cases {
		t.Run(c.golden, func(t *testing.T) {
			e, _ := loadContract(t, c.golden)
			if err := validateEnvTelemetry(c.golden, e); err != nil {
				t.Fatalf("the default telemetry is invalid: %v", err)
			}
			by := map[string]telemetry.Target{}
			var got []string
			for _, tg := range e.Telemetry {
				by[tg.ServiceName] = tg
				got = append(got, tg.ServiceName)
			}
			sort.Strings(got)
			if fmt.Sprint(got) != fmt.Sprint(c.services) {
				t.Fatalf("services = %v, want %v", got, c.services)
			}

			db := by["db-postgres"]
			if db.Attachment != telemetry.AttachManagedDatabase || db.Source != "manageddatabase:db" || db.Runtime != c.runtime ||
				db.Cluster != c.cluster || fmt.Sprint(db.Namespaces) != fmt.Sprint(c.ns) || db.Selector["cnpg.io/cluster"] != "db" || len(db.Selector) != 1 {
				t.Errorf("database placement = %s %s %s %q %v %v", db.Attachment, db.Source, db.Runtime, db.Cluster, db.Namespaces, db.Selector)
			}
			if len(db.Metrics) != 1 || db.Metrics[0].Port != 9187 || db.Metrics[0].Allow == "" || db.Metrics[0].SampleLimit != 2000 {
				t.Errorf("database scrape = %+v", db.Metrics)
			}
			if len(db.Checks) != 1 || db.Checks[0].Port != 5432 || db.Checks[0].Kind != "tcp" {
				t.Errorf("database check = %+v", db.Checks)
			}
			if l := db.Logs; l == nil || l.Format != "json" || l.Severity == nil || l.Severity.Field != "record.error_severity" ||
				l.Body != "record.message" || l.Timestamp == nil || l.Timestamp.Field != "ts" || l.Severity.Mapping["fatal"][1] != "PANIC" {
				t.Errorf("database logs = %+v", db.Logs)
			}
			if db.AdmitCollector() {
				t.Error("a database target must not admit the collector by default: a policy selecting the instances denies every other Postgres caller")
			}
			if !db.DefaultAlerts || len(db.Dashboards) != 1 {
				t.Errorf("default_alerts %v, %d dashboards", db.DefaultAlerts, len(db.Dashboards))
			}
			if op, ok := by["cloudnative-pg"]; ok {
				if op.Source != "manageddatabase-operator:k3d-acme" || op.Cluster != "k3d-acme" || len(op.Namespaces) != 0 ||
					op.Selector["app.kubernetes.io/name"] != "cloudnative-pg" || op.Metrics[0].Port != 8080 || op.Metrics[0].Allow == "" {
					t.Errorf("operator target = %+v", op)
				}
			}
			for _, tg := range e.Telemetry {
				assertDashboardsWithinAllowList(t, tg)
			}

			// The dashboards obsdash generates from it: the system dashboard
			// and the declared one per service, every one valid.
			systems, err := obsdash.TelemetrySystemsOf([]obsdash.EnvTelemetry{{Env: "dev", Targets: e.Telemetry}})
			if err != nil {
				t.Fatal(err)
			}
			if len(systems) != len(c.services) {
				t.Fatalf("%d systems, want %d", len(systems), len(c.services))
			}
			for _, s := range systems {
				if len(s.Dashboards) != 1 || s.Dashboards[0].ID != s.Service {
					t.Errorf("%s: declared dashboards = %+v", s.Service, s.Dashboards)
				}
				raw, err := json.Marshal(obsdash.BuildSystemDashboard("acme", s))
				if err != nil {
					t.Fatal(err)
				}
				if _, err := dashspec.ParseDashboard(s.Service+" system dashboard", raw); err != nil {
					t.Errorf("%v", err)
				}
			}
		})
	}
}

// assertDashboardsWithinAllowList is the coverage rule the declared-telemetry
// lint applies (F8): every metric a target's dashboards draw is one its
// scrapes keep. A Prometheus histogram or summary lands under its base name,
// so it is kept when its exposition series (_bucket, _sum, _count) are.
func assertDashboardsWithinAllowList(t *testing.T, tg telemetry.Target) {
	t.Helper()
	var allows []*regexp.Regexp
	for _, m := range tg.Metrics {
		if m.Allow == "" {
			return // a scrape without an allow list keeps every name
		}
		allows = append(allows, regexp.MustCompile("^(?:"+m.Allow+")$"))
	}
	kept := func(name string) bool {
		for _, re := range allows {
			if re.MatchString(name) {
				return true
			}
		}
		return false
	}
	for i, raw := range tg.Dashboards {
		d, err := dashspec.ParseDashboard(fmt.Sprintf("%s dashboards[%d]", tg.ServiceName, i), raw)
		if err != nil {
			t.Errorf("%v", err)
			continue
		}
		for _, p := range d.Panels {
			if p.Signal != dashspec.Metrics {
				continue
			}
			for _, s := range p.Series {
				ok := kept(s.Field)
				if !ok && (s.MetricType == dashspec.MetricTypeHistogram || s.MetricType == dashspec.MetricTypeSummary) {
					ok = kept(s.Field+"_sum") && kept(s.Field+"_count") && (s.MetricType == dashspec.MetricTypeSummary || kept(s.Field+"_bucket"))
				}
				if !ok {
					t.Errorf("%s: dashboard %s panel %s draws %q, which no scrape's allow list keeps", tg.ServiceName, d.ID, p.ID, s.Field)
				}
			}
		}
	}
}
