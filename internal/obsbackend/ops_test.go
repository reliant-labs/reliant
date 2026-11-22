package obsbackend

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/obsquery"
)

// opsRunner answers each ops panel by what its statement selects: the code
// panel by its mapContains, the component panel by the name shape it
// matches, and the server panel otherwise.
type opsRunner struct {
	server, component, code [][]any
	ran                     []obsquery.Stmt
}

func (r *opsRunner) Run(_ context.Context, st obsquery.Stmt, _ Limits) (Result, error) {
	r.ran = append(r.ran, st)
	switch {
	case strings.Contains(st.SQL, "mapContains(SpanAttributes"):
		return Result{Rows: r.code}, nil
	case strings.Contains(params(st), componentOpPattern):
		return Result{Rows: r.component}, nil
	}
	return Result{Rows: r.server}, nil
}

func opRow(labels []string, vals ...float64) []any {
	row := []any{toAny(labels)}
	for _, v := range vals {
		row = append(row, num64(fmt.Sprint(v)))
	}
	return row
}

func toAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// params is every parameter value a statement binds, for asserting what a
// predicate restricted it to.
func params(st obsquery.Stmt) string {
	var vs []string
	for _, v := range st.Params {
		vs = append(vs, v)
	}
	slices.Sort(vs)
	return strings.Join(vs, " ")
}

func purposes(sts []Statement) []string {
	var out []string
	for _, s := range sts {
		out = append(out, s.Purpose)
	}
	return out
}

func fixtureOpsRunner() *opsRunner {
	return &opsRunner{
		server: [][]any{
			opRow([]string{"api", "todo.v1.TodoService/CreateTodo"}, 600, 6, 4, 20, 40),
			opRow([]string{"api", "GET /healthz"}, 3600, 0, 1, 2, 3),
		},
		component: [][]any{
			opRow([]string{"api", "todo.Create"}, 600, 60, 3, 900, 1500),
		},
		code: [][]any{
			opRow([]string{"todo.Create", "internal/todo/service.go", "github.com/x/internal/todo.Service.Create"}, 500),
			// A lighter location for the same op (an older build) loses.
			opRow([]string{"todo.Create", "internal/todo/old.go", ""}, 100),
		},
	}
}

func TestOpKindOf(t *testing.T) {
	for name, want := range map[string]string{
		"todo.v1.TodoService/CreateTodo": OpKindRPC,
		"grpc.health.v1.Health/Check":    OpKindRPC,
		"todo.Create":                    OpKindComponent,
		"billing_v2.ChargeCard":          OpKindComponent,
		"GET /v1/jobs":                   OpKindEndpoint,
		"TodoService/CreateTodo":         OpKindEndpoint, // no proto package: not otelconnect's name
		"Todo.Create":                    OpKindEndpoint, // a Go package is lowercase
		"todo.create":                    OpKindEndpoint, // an exported method is not
	} {
		if got := OpKindOf(name); got != want {
			t.Errorf("OpKindOf(%q) = %s, want %s", name, got, want)
		}
	}
}

func TestLocalOpsJoinsServerAndComponentPanels(t *testing.T) {
	r := fixtureOpsRunner()
	l := Local{Env: "dev", Runner: r}
	res, err := l.Ops(context.Background(), OpsRequest{Narrow: rng()})
	if err != nil {
		t.Fatal(err)
	}
	if got := purposes(res.Statements); fmt.Sprint(got) != "[ops server ops component ops code]" {
		t.Errorf("statements %v", got)
	}
	var names []string
	for _, o := range res.Ops {
		names = append(names, o.Kind+" "+o.Name)
	}
	if fmt.Sprint(names) != "[endpoint GET /healthz rpc todo.v1.TodoService/CreateTodo component todo.Create]" {
		t.Errorf("busiest first, every kind: %v", names)
	}
	c := res.Ops[2]
	if c.CodeFilePath != "internal/todo/service.go" || c.CodeFunction != "github.com/x/internal/todo.Service.Create" {
		t.Errorf("the location most spans report: %+v", c)
	}
	if c.ErrorRatio != 0.1 || c.RatePerSecond != 600.0/3600 || c.P95Ms != 900 || c.Service != "api" {
		t.Errorf("figures: %+v", c)
	}
	if res.Ops[1].CodeFilePath != "" {
		t.Errorf("a server op takes no component location: %+v", res.Ops[1])
	}
	for _, st := range r.ran {
		if !strings.Contains(st.SQL, "FROM default.otel_traces") {
			t.Errorf("not over the env's traces: %s", st.SQL)
		}
	}
}

func TestOpsSortAndTop(t *testing.T) {
	l := Local{Env: "dev", Runner: fixtureOpsRunner()}
	for sort, want := range map[string]string{"": "GET /healthz", "calls": "GET /healthz", "rate": "GET /healthz", "p95": "todo.Create", "errors": "todo.Create"} {
		res, err := l.Ops(context.Background(), OpsRequest{Narrow: rng(), Sort: sort, Top: 2})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Ops) != 2 || res.Ops[0].Name != want || res.Omitted != 1 {
			t.Errorf("sort %q: %+v omitted %d, want %s first", sort, res.Ops, res.Omitted, want)
		}
	}
	for _, req := range []OpsRequest{{Sort: "requests"}, {Kinds: []string{"rpcs"}}} {
		req.Narrow = rng()
		if _, err := l.Ops(context.Background(), req); exitOf(err) != ExitWrong {
			t.Errorf("%+v: %v", req, err)
		}
	}
}

func TestOpsKindsPickThePanels(t *testing.T) {
	r := fixtureOpsRunner()
	res, err := Local{Env: "dev", Runner: r}.Ops(context.Background(), OpsRequest{Narrow: rng(), Kinds: []string{OpKindRPC}})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.ran) != 1 || !strings.Contains(r.ran[0].SQL, "match(SpanName") || !strings.Contains(params(r.ran[0]), rpcOpPattern) {
		t.Fatalf("rpcs alone: one server panel narrowed to Connect procedures: %v", r.ran)
	}
	if len(res.Ops) != 1 || res.Ops[0].Kind != OpKindRPC {
		t.Errorf("%+v", res.Ops)
	}

	r = fixtureOpsRunner()
	res, err = Local{Env: "dev", Runner: r}.Ops(context.Background(), OpsRequest{Narrow: rng(), Kinds: []string{OpKindComponent}})
	if err != nil {
		t.Fatal(err)
	}
	if got := purposes(res.Statements); fmt.Sprint(got) != "[ops component ops code]" || len(res.Ops) != 1 || res.Ops[0].Kind != OpKindComponent {
		t.Errorf("components alone: %v %+v", got, res.Ops)
	}

	r = fixtureOpsRunner()
	res, _ = Local{Env: "dev", Runner: r}.Ops(context.Background(), OpsRequest{Narrow: rng(), Kinds: []string{OpKindEndpoint}})
	if len(res.Ops) != 1 || res.Ops[0].Name != "GET /healthz" || strings.Contains(r.ran[0].SQL, "match(") {
		t.Errorf("endpoints alone: an unnarrowed server panel, rpcs dropped: %+v", res.Ops)
	}
}

func TestOpsNamesRestrictEachPanelToItsNames(t *testing.T) {
	r := fixtureOpsRunner()
	l := Local{Env: "dev", Runner: r}
	_, err := l.Ops(context.Background(), OpsRequest{Narrow: rng(), Names: []string{"todo.v1.TodoService/CreateTodo", "todo.Create"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.ran) != 3 {
		t.Fatalf("ran %d", len(r.ran))
	}
	server, component, code := params(r.ran[0]), params(r.ran[1]), params(r.ran[2])
	if !strings.Contains(server, "todo.v1.TodoService/CreateTodo") || strings.Contains(server, "todo.Create ") || strings.HasSuffix(server, "todo.Create") {
		t.Errorf("server panel params: %s", server)
	}
	for _, p := range []string{component, code} {
		if !strings.Contains(p, "todo.Create") || strings.Contains(p, "CreateTodo") {
			t.Errorf("component panel params: %s", p)
		}
	}
	for _, st := range r.ran {
		if !strings.Contains(st.SQL, "SpanName IN (") {
			t.Errorf("not restricted by name: %s", st.SQL)
		}
	}

	// Component names only: the server panel is not asked.
	r = fixtureOpsRunner()
	res, _ := Local{Env: "dev", Runner: r}.Ops(context.Background(), OpsRequest{Narrow: rng(), Names: []string{"todo.Create"}})
	if got := purposes(res.Statements); fmt.Sprint(got) != "[ops component ops code]" {
		t.Errorf("statements %v", got)
	}

	// More names than one `in` takes are asked in chunks.
	var many []string
	for i := range 2*obsquery.MaxPanelValues + 1 {
		many = append(many, fmt.Sprintf("pkg.Op%d", i))
	}
	r = fixtureOpsRunner()
	res, err = Local{Env: "dev", Runner: r}.Ops(context.Background(), OpsRequest{Narrow: rng(), Names: many})
	if err != nil {
		t.Fatal(err)
	}
	if got := purposes(res.Statements); fmt.Sprint(got) != "[ops component ops component ops component ops code ops code ops code]" {
		t.Errorf("chunked statements %v", got)
	}
}

func TestOpsExplainRunsNothing(t *testing.T) {
	r := fixtureOpsRunner()
	n := rng()
	n.Explain = true
	res, err := Local{Env: "dev", Runner: r}.Ops(context.Background(), OpsRequest{Narrow: n})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.ran) != 0 || len(res.Ops) != 0 || len(res.Statements) != 3 || res.Statements[0].SQL == "" {
		t.Errorf("explain: ran %d, %+v", len(r.ran), res)
	}
	// Tail sampling: the compiler weights counts and quantiles.
	if !strings.Contains(res.Statements[0].SQL, "quantileExactWeightedIf") {
		t.Errorf("unweighted: %s", res.Statements[0].SQL)
	}
}

func TestLocalOpsShortRowIsUndetermined(t *testing.T) {
	r := &opsRunner{server: [][]any{{[]any{"api", "GET /x"}, num64("1")}}}
	if _, err := (Local{Env: "dev", Runner: r}).Ops(context.Background(), OpsRequest{Narrow: rng()}); exitOf(err) != ExitUndetermined {
		t.Errorf("%v", err)
	}
}

// panelCaller answers QueryPanel per panel id and records the panels sent.
type panelCaller struct {
	replies map[string]string
	err     error
	panels  []map[string]any
}

func (p *panelCaller) Call(_ context.Context, procedure string, req, out any) error {
	if procedure != procQueryPanel {
		return fmt.Errorf("unexpected %s", procedure)
	}
	b, _ := json.Marshal(req)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	panel := m["panel"].(map[string]any)
	p.panels = append(p.panels, panel)
	if p.err != nil {
		return p.err
	}
	return json.Unmarshal([]byte(p.replies[fmt.Sprint(panel["id"])]), out)
}

func TestCloudOpsRunsThePanelsThroughQueryPanel(t *testing.T) {
	pc := &panelCaller{replies: map[string]string{
		"ops-server": `{"compiledSql":[{"sql":"SELECT server"}],"rows":[
			{"group":{"service.name":"api","span.name":"todo.v1.TodoService/CreateTodo"},"values":[100,1,2,5,9]},
			{"group":{"service.name":"api","span.name":"GET /b"},"values":[10,5,20,50,90]}]}`,
		"ops-component": `{"compiledSql":[{"sql":"SELECT component"}],"rows":[
			{"group":{"service.name":"api","span.name":"todo.Create"},"values":[50,0,1,3,4]}]}`,
		"ops-code": `{"compiledSql":[{"sql":"SELECT code"}],"rows":[
			{"group":{"span.name":"todo.Create","span.attr.code.file.path":"internal/todo/service.go"},"values":[50]}]}`,
	}}
	c := Cloud{Env: "prod", EnvID: "env-1", OrgID: "org-1", Caller: pc}
	res, err := c.Ops(context.Background(), OpsRequest{Narrow: narrowC(), Sort: "errors", Top: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Ops) != 2 || res.Ops[0].Name != "GET /b" || res.Ops[0].Kind != OpKindEndpoint || res.Omitted != 1 || res.Ops[0].ErrorRatio != 0.5 {
		t.Errorf("%+v", res)
	}
	if got := purposes(res.Statements); fmt.Sprint(got) != "[ops server ops component ops code]" || res.Statements[1].SQL != "SELECT component" {
		t.Errorf("statements %+v", res.Statements)
	}
	res, _ = c.Ops(context.Background(), OpsRequest{Narrow: narrowC(), Kinds: []string{OpKindComponent}})
	if len(res.Ops) != 1 || res.Ops[0].CodeFilePath != "internal/todo/service.go" || res.Ops[0].CodeFunction != "" {
		t.Errorf("component from the cloud: %+v", res.Ops)
	}
	if g := fmt.Sprint(pc.panels[0]["groupBy"]); g != "[service.name span.name]" {
		t.Errorf("server panel groupBy %s", g)
	}
	if g := fmt.Sprint(pc.panels[2]["groupBy"]); g != "[span.name span.attr.code.file.path span.attr.code.function.name]" {
		t.Errorf("code panel groupBy %s", g)
	}

	pc.replies["ops-server"] = `{"rows":[{"group":{"service.name":"api","span.name":"x"},"values":[1]}]}`
	if _, err := c.Ops(context.Background(), OpsRequest{Narrow: narrowC()}); exitOf(err) != ExitUndetermined {
		t.Errorf("a short row: %v", err)
	}
	// A control plane that refuses a panel answers with its own error.
	refused := Wrong("at most 3 group_by fields")
	pc.err = refused
	if _, err := c.Ops(context.Background(), OpsRequest{Narrow: narrowC()}); err != refused {
		t.Errorf("the control plane's refusal: %v", err)
	}
}

func TestOpsByVersionSplitsTheFiguresPanels(t *testing.T) {
	r := &opsRunner{
		server: [][]any{
			opRow([]string{"api", "todo.v1.TodoService/CreateTodo", "v2"}, 100, 0, 4, 20, 40),
			opRow([]string{"api", "todo.v1.TodoService/CreateTodo", "v1"}, 300, 90, 4, 20, 40),
		},
		component: [][]any{opRow([]string{"api", "todo.Create", ""}, 50, 0, 3, 9, 15)},
		code:      fixtureOpsRunner().code,
	}
	res, err := Local{Env: "dev", Runner: r}.Ops(context.Background(), OpsRequest{Narrow: rng(), ByVersion: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.ran) != 3 {
		t.Fatalf("ran %d", len(r.ran))
	}
	for i, st := range r.ran {
		byVersion := strings.Contains(params(st), "service.version")
		if want := i < 2; byVersion != want {
			t.Errorf("statement %d groups by service.version: %v, want %v (only the figures panels):\n%s", i, byVersion, want, st.SQL)
		}
	}
	got := map[string]OpStats{}
	for _, o := range res.Ops {
		got[o.Name+"@"+o.Version] = o
	}
	if o := got["todo.v1.TodoService/CreateTodo@v2"]; o.Calls != 100 || o.ErrorRatio != 0 {
		t.Errorf("v2 row: %+v", o)
	}
	if o := got["todo.v1.TodoService/CreateTodo@v1"]; o.Calls != 300 || o.ErrorRatio != 0.3 {
		t.Errorf("v1 row: %+v", o)
	}
	if o, ok := got["todo.Create@"]; !ok || o.CodeFilePath != "internal/todo/service.go" {
		t.Errorf("a component row with no version keeps Version \"\" and its code: %+v", res.Ops)
	}

	// Unset: today's panels, and no row carries a version even when a
	// backend answers a third label.
	r = fixtureOpsRunner()
	r.server = [][]any{opRow([]string{"api", "GET /x", "v9"}, 10, 0, 1, 2, 3)}
	res, err = Local{Env: "dev", Runner: r}.Ops(context.Background(), OpsRequest{Narrow: rng()})
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range r.ran {
		if strings.Contains(params(st), "service.version") {
			t.Errorf("unversioned request groups by service.version:\n%s", st.SQL)
		}
	}
	for _, o := range res.Ops {
		if o.Version != "" {
			t.Errorf("unversioned request: %+v", o)
		}
	}
}

func TestCloudOpsByVersion(t *testing.T) {
	pc := &panelCaller{replies: map[string]string{
		"ops-server": `{"rows":[
			{"group":{"service.name":"api","span.name":"todo.v1.TodoService/CreateTodo","resource.attr.service.version":"v2"},"values":[100,1,2,5,9]},
			{"group":{"service.name":"api","span.name":"todo.v1.TodoService/CreateTodo","resource.attr.service.version":"v1"},"values":[10,5,20,50,90]}]}`,
		"ops-component": `{"rows":[{"group":{"service.name":"api","span.name":"todo.Create"},"values":[50,0,1,3,4]}]}`,
		"ops-code":      `{"rows":[]}`,
	}}
	c := Cloud{Env: "prod", EnvID: "env-1", OrgID: "org-1", Caller: pc}
	res, err := c.Ops(context.Background(), OpsRequest{Narrow: narrowC(), ByVersion: true})
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"[service.name span.name resource.attr.service.version]", "[service.name span.name resource.attr.service.version]", "[span.name span.attr.code.file.path span.attr.code.function.name]"} {
		if g := fmt.Sprint(pc.panels[i]["groupBy"]); g != want {
			t.Errorf("panel %d groupBy %s, want %s", i, g, want)
		}
	}
	var got []string
	for _, o := range res.Ops {
		got = append(got, fmt.Sprintf("%s@%s=%v", o.Name, o.Version, o.Calls))
	}
	slices.Sort(got)
	if want := "[todo.Create@=50 todo.v1.TodoService/CreateTodo@v1=10 todo.v1.TodoService/CreateTodo@v2=100]"; fmt.Sprint(got) != want {
		t.Errorf("rows %v, want %s", got, want)
	}
}

// A named request reaches the control plane as an `in` on span.name in each
// panel it asks, so the server scans only those ops: the client never ranks
// every op and filters afterwards.
func TestCloudOpsPushesNamesDownToQueryPanel(t *testing.T) {
	pc := &panelCaller{replies: map[string]string{
		"ops-server":    `{"rows":[{"group":{"service.name":"api","span.name":"todo.v1.TodoService/CreateTodo"},"values":[100,1,2,5,9]}]}`,
		"ops-component": `{"rows":[]}`,
		"ops-code":      `{"rows":[]}`,
	}}
	c := Cloud{Env: "prod", EnvID: "env-1", OrgID: "org-1", Caller: pc}
	names := []string{"todo.v1.TodoService/CreateTodo", "claimable.IsUnclaimedUser"}
	res, err := c.Ops(context.Background(), OpsRequest{Narrow: narrowC(), Names: names})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Ops) != 1 || res.Ops[0].Name != names[0] {
		t.Errorf("ops %+v", res.Ops)
	}
	want := map[string]string{"ops-server": names[0], "ops-component": names[1], "ops-code": names[1]}
	if len(pc.panels) != 3 {
		t.Fatalf("sent %d panels, want server, component and code", len(pc.panels))
	}
	for _, p := range pc.panels {
		var in []any
		for _, w := range p["where"].([]any) {
			if w := w.(map[string]any); w["field"] == "span.name" && w["op"] == "in" {
				in = w["values"].([]any)
			}
		}
		if fmt.Sprint(in) != "["+want[fmt.Sprint(p["id"])]+"]" {
			t.Errorf("panel %v: span.name in %v, want [%s]", p["id"], in, want[fmt.Sprint(p["id"])])
		}
	}
}

func TestOpsNameOutsideKindIsRefused(t *testing.T) {
	r := fixtureOpsRunner()
	_, err := Local{Env: "dev", Runner: r}.Ops(context.Background(), OpsRequest{Narrow: rng(), Kinds: []string{OpKindRPC}, Names: []string{"todo.Create"}})
	if exitOf(err) != ExitWrong || !strings.Contains(err.Error(), `"todo.Create" is a component op`) {
		t.Errorf("err = %v", err)
	}
	if len(r.ran) != 0 {
		t.Errorf("ran %d statements for a refused request", len(r.ran))
	}
}
