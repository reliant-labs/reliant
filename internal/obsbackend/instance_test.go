package obsbackend

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/devtelemetry"
	"github.com/reliant-labs/forge/pkg/obsquery"
)

func hostedRecord() *devtelemetry.Record {
	return &devtelemetry.Record{Env: "dev", Backend: "cloud", OrgID: "org-1", EnvironmentID: "env-dev",
		InstanceID: "sess-0123456789abcdef01234567", InstanceLabel: "alice/fix-login", InstanceUser: "alice@x"}
}

// A local env whose `forge env up` recorded the hosted store resolves to the
// cloud backend, bound to the record, instead of refusing as "not declared".
func TestResolveHostedDevRecord(t *testing.T) {
	cp := "https://cp.example.com"
	local := Facts{Env: "dev", HasLocal: true, ControlPlane: cp, DevTelemetry: hostedRecord()}

	dec, err := Resolve(local, "")
	if err != nil || dec.Backend != BackendCloud || dec.HostedDev == nil || dec.HostedDev.EnvironmentID != "env-dev" {
		t.Fatalf("Resolve = %+v, %v; want cloud bound to the record", dec, err)
	}
	if dec.Reason != "dev_telemetry=hosted: instance alice/fix-login" {
		t.Errorf("reason = %q", dec.Reason)
	}
	declared := local
	declared.Declared, declared.Enabled = true, true
	if dec, err := Resolve(declared, ""); err != nil || dec.HostedDev == nil {
		t.Errorf("a declared block with a hosted record: %+v, %v", dec, err)
	}
	if dec, err := Resolve(local, BackendCloud); err != nil || dec.HostedDev == nil || !strings.HasPrefix(dec.Reason, "--backend cloud: ") {
		t.Errorf("--backend cloud with a hosted record: %+v, %v", dec, err)
	}
	if dec, err := Resolve(local, BackendLocal); err != nil || dec.Backend != BackendLocal || dec.HostedDev != nil {
		t.Errorf("--backend local still wins: %+v, %v", dec, err)
	}

	for name, c := range map[string]struct {
		f      Facts
		errHas string
	}{
		"disabled wins over a stale record": {func() Facts { f := local; f.Declared = true; return f }(), "its observability is disabled"},
		"no control plane to query":         {func() Facts { f := local; f.ControlPlane = ""; return f }(), "declares no control_plane to query it through"},
		"record without an environment": {func() Facts {
			f := local
			r := *hostedRecord()
			r.EnvironmentID = ""
			f.DevTelemetry = &r
			return f
		}(), "names no org, environment or instance"},
		// The default read is this checkout's instance: without one it would
		// read every instance of the env.
		"record without an instance": {func() Facts {
			f := local
			r := *hostedRecord()
			r.InstanceID = ""
			f.DevTelemetry = &r
			return f
		}(), "names no org, environment or instance"},
	} {
		if _, err := Resolve(c.f, ""); err == nil || !strings.Contains(err.Error(), c.errHas) || err.(*Error).ExitCode() != ExitUndetermined {
			t.Errorf("%s: err = %v, want exit 2 containing %q", name, err, c.errHas)
		}
	}

	// A record says nothing for an env that no longer runs here, nor when it
	// recorded the local store: today's table decides.
	for name, f := range map[string]Facts{
		"not local": func() Facts { f := local; f.HasLocal = false; return f }(),
		"recorded local": func() Facts {
			f := local
			r := *hostedRecord()
			r.Backend = "local"
			f.DevTelemetry = &r
			return f
		}(),
	} {
		if dec, err := Resolve(f, ""); err == nil || dec.HostedDev != nil {
			t.Errorf("%s: %+v, %v; want today's refusal", name, dec, err)
		}
	}
}

// With no record, an undeclared block is still off, and the refusal names the
// KCL that turns it on.
func TestResolveOffNamesTheKCLToAdd(t *testing.T) {
	_, err := Resolve(Facts{Env: "dev", HasLocal: true}, "")
	if err == nil {
		t.Fatal("an undeclared dev env resolved")
	}
	for _, want := range []string{
		"telemetry is off for dev: its observability is not declared",
		`observability = forge.Observability {dev_telemetry = "auto"}`, "control_plane", "forge env up dev",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal lacks %q: %v", want, err)
		}
	}
}

// The cloud backend sends its instance narrowing on every scope it builds:
// telemetry, issues and sessions.
func TestCloudSendsInstanceNarrowing(t *testing.T) {
	echo := `{"instanceScope":{"instanceIds":["sess-1"],"instanceUsers":["u1"]}}`
	f := &fakeCaller{replies: map[string]string{procSearchLogs: echo, procListIssues: echo, procListSessions: echo}}
	c := cloudWith(f)
	c.Instances = Instances{IDs: []string{"sess-1"}, Users: []string{"u1"}}
	ctx := context.Background()
	n := narrowC()
	if _, err := c.Logs(ctx, LogsRequest{Narrow: n, Limit: 5}); err != nil {
		t.Fatal(err)
	}
	n.Filters = nil
	if _, err := c.ListIssues(ctx, ListIssuesRequest{Narrow: n, Limit: 5}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ListSessions(ctx, ListSessionsRequest{Narrow: n, Limit: 5}); err != nil {
		t.Fatal(err)
	}
	for _, proc := range []string{procSearchLogs, procListIssues, procListSessions} {
		scope, _ := f.reqs[proc]["scope"].(map[string]any)
		if fmt.Sprint(scope["instanceIds"]) != "[sess-1]" || fmt.Sprint(scope["instanceUsers"]) != "[u1]" || fmt.Sprint(scope["envIds"]) != "[env-1]" {
			t.Errorf("%s scope = %v", proc, scope)
		}
	}

	// A whole-env read sends neither field: the request every control plane knew.
	f = &fakeCaller{replies: map[string]string{procSearchLogs: `{}`}}
	if _, err := cloudWith(f).Logs(ctx, LogsRequest{Narrow: narrowC(), Limit: 5}); err != nil {
		t.Fatal(err)
	}
	scope := f.reqs[procSearchLogs]["scope"].(map[string]any)
	if _, ok := scope["instanceIds"]; ok {
		t.Errorf("an unnarrowed scope sent instanceIds: %v", scope)
	}
	if _, ok := scope["instanceUsers"]; ok {
		t.Errorf("an unnarrowed scope sent instanceUsers: %v", scope)
	}
}

// The local backend narrows its compiled statements the same way, and the
// cloud backend's --explain shows the narrowing it will ask for.
func TestLocalAndExplainNarrowByInstance(t *testing.T) {
	fr := &fakeRunner{}
	l := Local{Env: "dev", Runner: fr, Instances: Instances{IDs: []string{"sess-1"}}}
	ctx := context.Background()
	if _, err := l.Logs(ctx, LogsRequest{Narrow: rng(), Limit: 5}); err != nil {
		t.Fatal(err)
	}
	// No spans answer: the trace read still compiled, narrowed, before it said so.
	_, _ = l.Trace(ctx, TraceRequest{Narrow: rng(), TraceID: "ab", LogLimit: 5})
	if _, err := l.ListIssues(ctx, ListIssuesRequest{Narrow: rng(), Limit: 5}); err != nil {
		t.Fatal(err)
	}
	if len(fr.ran) < 3 {
		t.Fatalf("ran %d statements", len(fr.ran))
	}
	for _, st := range fr.ran {
		if !strings.Contains(st.SQL, "ResourceAttributes['reliant.instance.id']") {
			t.Errorf("a local statement is not narrowed to the instance:\n%s", st.SQL)
		}
	}

	c := cloudWith(&fakeCaller{})
	c.Instances = Instances{IDs: []string{"sess-1"}}
	n := narrowC()
	n.Explain = true
	res, err := c.Logs(ctx, LogsRequest{Narrow: n, Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Statements) == 0 || !strings.Contains(res.Statements[0].SQL, "reliant.instance.id") {
		t.Errorf("--explain on a cloud env omits the instance: %+v", res.Statements)
	}
}

// An older control plane drops the instance fields it does not know and
// answers for the whole env. Without its acknowledgement the answer is
// refused (exit 2), never shown as the instance's.
func TestCloudRefusesAnUnacknowledgedNarrowing(t *testing.T) {
	ctx := context.Background()
	wholeEnv := `{"logs":[{"timestamp":"2026-10-22T11:59:00Z","service":"api","body":"another stack's line"}]}`
	for name, reply := range map[string]string{
		"older server: no echo":       wholeEnv,
		"echo of another instance":    `{"logs":[],"instanceScope":{"instanceIds":["sess-2"]}}`,
		"echo that dropped the users": `{"logs":[],"instanceScope":{"instanceIds":["sess-1"]}}`,
	} {
		t.Run(name, func(t *testing.T) {
			c := cloudWith(&fakeCaller{replies: map[string]string{procSearchLogs: reply, procListIssues: reply, procListSessions: reply}})
			c.Instances = Instances{IDs: []string{"sess-1"}, Users: []string{"u1"}}
			n := narrowC()
			res, err := c.Logs(ctx, LogsRequest{Narrow: n, Limit: 5})
			if err == nil || err.(*Error).ExitCode() != ExitUndetermined || len(res.Logs) != 0 {
				t.Errorf("logs: %+v, %v; want exit 2 and nothing shown", res.Logs, err)
			}
			n.Filters = nil
			if _, err := c.ListIssues(ctx, ListIssuesRequest{Narrow: n, Limit: 5}); err == nil || err.(*Error).ExitCode() != ExitUndetermined {
				t.Errorf("issues: %v; want exit 2", err)
			}
			if _, err := c.ListSessions(ctx, ListSessionsRequest{Narrow: n, Limit: 5}); err == nil || err.(*Error).ExitCode() != ExitUndetermined {
				t.Errorf("sessions: %v; want exit 2", err)
			}
		})
	}

	// The same older server answers a whole-env read as it always did.
	c := cloudWith(&fakeCaller{replies: map[string]string{procSearchLogs: wholeEnv}})
	res, err := c.Logs(ctx, LogsRequest{Narrow: narrowC(), Limit: 5})
	if err != nil || len(res.Logs) != 1 {
		t.Errorf("an unnarrowed read: %+v, %v", res.Logs, err)
	}
	// An acknowledged narrowing is read normally, in any order.
	c = cloudWith(&fakeCaller{replies: map[string]string{procSearchLogs: `{"logs":[{"timestamp":"2026-10-22T11:59:00Z","service":"api","body":"mine"}],"instanceScope":{"instanceIds":["b","a"]}}`}})
	c.Instances = Instances{IDs: []string{"a", "b"}}
	if res, err := c.Logs(ctx, LogsRequest{Narrow: narrowC(), Limit: 5}); err != nil || len(res.Logs) != 1 {
		t.Errorf("an acknowledged narrowing: %+v, %v", res.Logs, err)
	}
}

// A narrowed backend refuses a telemetry read that carries no narrowing (the
// env's alerts, its telemetry deliveries): it would answer for the whole env
// under the instance's scope. Reads about the env as a whole (an issue's
// status, the activity feed, the dashboards' definitions) still run.
func TestCloudRefusesEnvWideTelemetryUnderNarrowing(t *testing.T) {
	ctx := context.Background()
	f := &fakeCaller{replies: map[string]string{
		procListAlerts:              `{"alerts":[{"rule":"HighErrorRate","state":"firing"}]}`,
		procListTelemetryDeliveries: `{"enabled":true}`,
		procUpdateIssueStatus:       `{"issue":{"id":"i1","status":"ISSUE_STATUS_RESOLVED"}}`,
		procGetIssue:                `{"issue":{"id":"i1","status":"ISSUE_STATUS_UNRESOLVED"}}`,
		procListDashboards:          `{}`,
	}}
	c := cloudWith(f)
	c.Instances = Instances{IDs: []string{"sess-1"}}
	if alerts, err := c.Alerts(ctx); err == nil || err.(*Error).ExitCode() != ExitUndetermined || len(alerts) != 0 {
		t.Errorf("alerts under an instance scope: %v, %v; want exit 2", alerts, err)
	}
	if _, err := c.TelemetryDeliveries(ctx); err == nil || err.(*Error).ExitCode() != ExitUndetermined {
		t.Errorf("deliveries under an instance scope: %v; want exit 2", err)
	}
	if _, ok := f.reqs[procListAlerts]; ok {
		t.Error("the env-wide alert read reached the control plane")
	}
	if _, err := c.UpdateIssueStatus(ctx, UpdateIssueStatusRequest{ID: "i1", Status: IssueResolved}); err != nil {
		t.Errorf("an issue's status change is not telemetry: %v", err)
	}
	if _, err := c.ListDashboards(ctx, "p"); err != nil {
		t.Errorf("the dashboards' definitions are not telemetry: %v", err)
	}
	// Unnarrowed, the same reads run as they always did.
	if alerts, err := cloudWith(f).Alerts(ctx); err != nil || len(alerts) != 1 {
		t.Errorf("unnarrowed alerts: %v, %v", alerts, err)
	}
}

// An issue read by id carries the narrowing beside its id and must be
// acknowledged like any other narrowed read.
func TestCloudNarrowsAnIssueReadByID(t *testing.T) {
	ctx := context.Background()
	f := &fakeCaller{replies: map[string]string{
		procGetIssue:        `{"issue":{"id":"i1"},"instanceScope":{"instanceIds":["sess-1"]}}`,
		procListIssueEvents: `{"events":[]}`,
	}}
	c := cloudWith(f)
	c.Instances = Instances{IDs: []string{"sess-1"}}
	if _, err := c.GetIssue(ctx, GetIssueRequest{Narrow: narrowC(), ID: "i1"}); err != nil {
		t.Fatalf("an acknowledged issue read: %v", err)
	}
	if fmt.Sprint(f.reqs[procGetIssue]["instanceIds"]) != "[sess-1]" {
		t.Errorf("GetIssue request = %v", f.reqs[procGetIssue])
	}
	if _, err := c.ListIssueEvents(ctx, ListIssueEventsRequest{Narrow: narrowC(), ID: "i1", Limit: 5}); err == nil || err.(*Error).ExitCode() != ExitUndetermined {
		t.Errorf("an unacknowledged issue-event read: %v; want exit 2", err)
	}
}

// Raw SQL (a --sql statement, a sql panel) is its author's: nothing narrows
// it, so a narrowed backend refuses it (exit 1, naming --all-instances)
// before anything runs, locally and hosted.
func TestNarrowedBackendsRefuseRawSQL(t *testing.T) {
	ctx := context.Background()
	inst := Instances{IDs: []string{"sess-1"}}
	sqlPanel := &obsquery.Panel{ID: "p", Kind: "sql", SQL: "SELECT count() FROM otel_logs"}

	fr := &fakeRunner{}
	l := Local{Env: "dev", Runner: fr, Instances: inst}
	for name, req := range map[string]QueryRequest{
		"--sql":       {Narrow: rng(), SQL: "SELECT count() FROM otel_logs", Limit: 5},
		"a sql panel": {Narrow: rng(), Panel: sqlPanel, Limit: 5},
	} {
		if _, err := l.Query(ctx, req); err == nil || err.(*Error).ExitCode() != ExitWrong || !strings.Contains(err.Error(), "--all-instances") {
			t.Errorf("local %s under an instance scope: %v; want exit 1 naming --all-instances", name, err)
		}
	}
	if len(fr.ran) != 0 {
		t.Errorf("narrowed raw SQL ran: %v", fr.ran)
	}

	f := &fakeCaller{}
	c := cloudWith(f)
	c.Instances = inst
	for name, req := range map[string]QueryRequest{
		"--sql":       {Narrow: rng(), SQL: "SELECT count() FROM otel_logs", Limit: 5},
		"a sql panel": {Narrow: rng(), Panel: sqlPanel, Limit: 5},
	} {
		if _, err := c.Query(ctx, req); err == nil || err.(*Error).ExitCode() != ExitWrong || !strings.Contains(err.Error(), "--all-instances") {
			t.Errorf("cloud %s under an instance scope: %v; want exit 1 naming --all-instances", name, err)
		}
	}
	if _, err := c.QueryPanelReadout(ctx, PanelReadoutRequest{Narrow: rng(), Panel: *sqlPanel}); err == nil || err.(*Error).ExitCode() != ExitWrong {
		t.Errorf("cloud sql panel readout under an instance scope: %v; want exit 1", err)
	}
	if len(f.reqs) != 0 {
		t.Errorf("narrowed raw SQL reached the control plane: %v", f.reqs)
	}

	// The whole env (--all-instances) runs the same statement.
	l.Instances = Instances{}
	if _, err := l.Query(ctx, QueryRequest{Narrow: rng(), SQL: "SELECT count() FROM otel_logs", Limit: 5}); err != nil {
		t.Errorf("unnarrowed raw SQL: %v", err)
	}
}
