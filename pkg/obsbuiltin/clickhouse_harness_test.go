package obsbuiltin

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/dashspec"
	"github.com/reliant-labs/forge/pkg/obsquery"
)

// The ClickHouse harness: the hosted tenant tables the panels read (the
// columns they name; the control plane's tenant schema v28 has these and
// more), seeded per test into a database of its own, and every statement
// bound the way the console binds a sql panel (obsquery.SQLPanel).
//
// Set FORGE_ALERT_CLICKHOUSE to a running container with clickhouse-client
// and password "pw" (the convention of internal/obsdash's ClickHouse tests):
//
//	docker run -d --name ch -e CLICKHOUSE_PASSWORD=pw clickhouse/clickhouse-server
//	FORGE_ALERT_CLICKHOUSE=ch task test -- ./pkg/obsbuiltin/...

// tenantTables creates db's product_events (with the v28 edge columns and
// v29's OrgId) and
// error_events.
func tenantTables(db string) string {
	return `
DROP DATABASE IF EXISTS ` + db + `;
CREATE DATABASE ` + db + `;
CREATE TABLE ` + db + `.product_events (
    Timestamp DateTime64(9), ServiceName LowCardinality(String), EnvId LowCardinality(String),
    SessionId String, UserId String, EventName LowCardinality(String), Props Map(LowCardinality(String), String),
    Client LowCardinality(String), VisitorId String,
    DayVisitor String DEFAULT '', Country LowCardinality(String) DEFAULT '', Region LowCardinality(String) DEFAULT '',
    DeviceType LowCardinality(String) DEFAULT '', Browser LowCardinality(String) DEFAULT '', OS LowCardinality(String) DEFAULT '',
    Privacy LowCardinality(String) DEFAULT '', SaltScope LowCardinality(String) DEFAULT '',
    OrgId String DEFAULT ''
) ENGINE = MergeTree ORDER BY (ServiceName, EventName, Timestamp);
CREATE TABLE ` + db + `.error_events (
    Timestamp DateTime64(9), ServiceName LowCardinality(String), EnvId LowCardinality(String), SessionId String
) ENGINE = MergeTree ORDER BY (ServiceName, Timestamp);
`
}

// clickhouse is a seeded database in the test container.
type clickhouse struct {
	t         *testing.T
	container string
	db        string
	// settings are extra clickhouse-client flags (query settings) for every
	// statement, e.g. "--join_use_nulls=1".
	settings []string
}

// with is c running every statement with the extra query settings.
func (c *clickhouse) with(settings ...string) *clickhouse {
	d := *c
	d.settings = append(append([]string(nil), c.settings...), settings...)
	return &d
}

// newClickHouse seeds db with seed (after the tenant tables), or skips the
// test when there is no container.
func newClickHouse(t *testing.T, db, seed string) *clickhouse {
	t.Helper()
	container := os.Getenv("FORGE_ALERT_CLICKHOUSE")
	if testing.Short() || container == "" {
		t.Skip("needs a ClickHouse container; set FORGE_ALERT_CLICKHOUSE=<container> (runs in task test)")
	}
	c := &clickhouse{t: t, container: container, db: db}
	cmd := exec.Command("docker", "exec", "-i", container, "clickhouse-client", "--password", "pw", "--multiquery")
	cmd.Stdin = strings.NewReader(tenantTables(db) + seed)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("seed: %v\n%s", err, out)
	}
	return c
}

// run binds sql to the range [start, end) over envs, as the console binds a
// sql panel, and returns the TSV rows.
func (c *clickhouse) run(sql string, start, end time.Time, envs ...string) string {
	c.t.Helper()
	out, err := c.try(sql, start, end, envs...)
	if err != nil {
		c.t.Fatalf("%v\n--- sql ---\n%s", err, sql)
	}
	return out
}

// try is run, returning the failure instead of failing the test.
func (c *clickhouse) try(sql string, start, end time.Time, envs ...string) (string, error) {
	c.t.Helper()
	sc := obsquery.Scope{Database: "t_org_a", Start: start, End: end, EnvRestricted: true, EnvIDs: envs}
	for _, e := range envs {
		sc.Envs = append(sc.Envs, obsquery.Env{ID: e, Name: e})
	}
	st, err := obsquery.SQLPanel{Scope: sc, SQL: sql, StepSeconds: 86400}.Compile()
	if err != nil {
		c.t.Fatalf("compile: %v\n%s", err, sql)
	}
	args := []string{"exec", "-i", c.container, "clickhouse-client", "--password", "pw",
		"--database", c.db, "--format", "TSV"}
	args = append(args, c.settings...)
	for k, v := range st.Params {
		args = append(args, "--param_"+k+"="+v)
	}
	cmd := exec.Command("docker", args...)
	cmd.Stdin = strings.NewReader(st.SQL)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("clickhouse: %v\n%s", err, firstLines(string(out), 4))
	}
	return strings.TrimSpace(string(out)), nil
}

// firstLines is s's first n lines: a ClickHouse error repeats the whole
// statement after them.
func firstLines(s string, n int) string {
	lines := strings.SplitN(s, "\n", n+1)
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}

// panel finds id on d.
func panel(t *testing.T, d dashspec.Dashboard, id string) dashspec.Panel {
	t.Helper()
	for _, p := range d.Panels {
		if p.ID == id {
			return p
		}
	}
	t.Fatalf("dashboard %s has no panel %s", d.ID, id)
	return dashspec.Panel{}
}
