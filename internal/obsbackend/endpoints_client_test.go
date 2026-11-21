package obsbackend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/pkg/obsquery"
)

// The server rows every client-join test reads: two rpcs and a plain HTTP
// route no browser span can name.
func endpointServerRows() [][]any {
	row := func(svc, name string, req float64) []any {
		return []any{[]any{svc, name}, num64(fmt.Sprint(req)), num64("0"), num64("1"), num64("5"), num64("9")}
	}
	return [][]any{
		row("api", "todo.v1.TodoService/CreateTodo", 300),
		row("api", "todo.v1.TodoService/ListTodos", 200),
		row("api", "GET /healthz", 100),
	}
}

func clientRow(svc, method string, calls, p95 float64) []any {
	return []any{[]any{svc, method}, num64(fmt.Sprint(calls)), num64(fmt.Sprint(p95))}
}

// byName indexes endpoints by span name.
func byName(es []Endpoint) map[string]Endpoint {
	out := map[string]Endpoint{}
	for _, e := range es {
		out[e.Name] = e
	}
	return out
}

// endpointsRunner answers the server panel and the client panel apart. The
// client statement is known by its bound attribute key: span attribute keys
// are parameters, never inlined.
type endpointsRunner struct {
	mu             sync.Mutex
	ran            []obsquery.Stmt
	server, client Result
	clientErr      error
}

func isClientStmt(st obsquery.Stmt) bool {
	for _, v := range st.Params {
		if v == "forge.rpc.client" {
			return true
		}
	}
	return false
}

func (r *endpointsRunner) Run(_ context.Context, st obsquery.Stmt, _ Limits) (Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ran = append(r.ran, st)
	if !isClientStmt(st) {
		return r.server, nil
	}
	return r.client, r.clientErr
}

func TestLocalEndpointsJoinClientRPCs(t *testing.T) {
	fr := &endpointsRunner{server: Result{Rows: endpointServerRows()}, client: Result{Rows: [][]any{
		// One row per rpc, all frontends together: the panel groups by
		// rpc.service/rpc.method only, so web and admin calling CreateTodo
		// are one weighted p95 already.
		clientRow("todo.v1.TodoService", "CreateTodo", 450, 42),
		// An rpc of another service, not among the endpoints: no match.
		clientRow("billing.v1.BillingService", "Charge", 9, 700),
		// A client span missing its rpc attributes joins nothing.
		clientRow("", "", 5, 1),
	}}}
	n := rng()
	n.Services = []string{"api"}
	res, err := (Local{Env: "dev", Runner: fr}).Endpoints(context.Background(), EndpointsRequest{Narrow: n})
	if err != nil {
		t.Fatal(err)
	}
	got := byName(res.Endpoints)
	if e := got["todo.v1.TodoService/CreateTodo"]; e.ClientCalls != 450 || e.ClientP95Ms != 42 || e.Service != "api" {
		t.Errorf("CreateTodo joins its browser spans across services: %+v", e)
	}
	for _, name := range []string{"todo.v1.TodoService/ListTodos", "GET /healthz"} {
		if e := got[name]; e.ClientCalls != 0 || e.ClientP95Ms != 0 {
			t.Errorf("%s has no client spans, so no client value: %+v", name, e)
		}
	}
	if len(res.Endpoints) != 3 || res.ClientUnavailable != "" {
		t.Errorf("endpoints %d, note %q", len(res.Endpoints), res.ClientUnavailable)
	}
	if len(fr.ran) != 2 || !isClientStmt(fr.ran[1]) {
		t.Fatalf("ran %d statements, want the server and then the client panel", len(fr.ran))
	}
	client := fr.ran[1]
	for _, want := range []string{"default.otel_traces", "SpanAttributes[", "quantileExactWeightedIf(0.95)"} {
		if !strings.Contains(client.SQL, want) {
			t.Errorf("client sql lacks %q:\n%s", want, client.SQL)
		}
	}
	params := map[string]bool{}
	for _, v := range client.Params {
		params[v] = true
	}
	for _, want := range []string{"forge.rpc.client", "true", "rpc.service", "rpc.method"} {
		if !params[want] {
			t.Errorf("client statement binds no %q: %v", want, client.Params)
		}
	}
	// --service narrows the server endpoints; the browser's spans are under
	// the frontend's service.name, so the client read must not carry it.
	if params["api"] || params["server"] {
		t.Errorf("the client read carries the server's narrowing: %v", client.Params)
	}
}

func TestLocalEndpointsExplainListsTheClientStatement(t *testing.T) {
	fr := &endpointsRunner{}
	n := rng()
	n.Explain = true
	res, err := (Local{Env: "dev", Runner: fr}).Endpoints(context.Background(), EndpointsRequest{Narrow: n})
	if err != nil {
		t.Fatal(err)
	}
	if len(fr.ran) != 0 || len(res.Statements) != 2 || res.Statements[1].Purpose != "endpoints client" ||
		res.Statements[1].Params["p4"] != "forge.rpc.client" {
		t.Errorf("explain: ran %d, statements %+v", len(fr.ran), res.Statements)
	}
}

func TestLocalEndpointsClientFailureKeepsTheEndpoints(t *testing.T) {
	fr := &endpointsRunner{server: Result{Rows: endpointServerRows()}, clientErr: errors.New("Code: 47. Unknown identifier")}
	res, err := (Local{Env: "dev", Runner: fr}).Endpoints(context.Background(), EndpointsRequest{Narrow: rng()})
	if err != nil {
		t.Fatalf("an enrichment failure fails the verb: %v", err)
	}
	if len(res.Endpoints) != 3 || !strings.Contains(res.ClientUnavailable, "Unknown identifier") {
		t.Errorf("endpoints %d, note %q", len(res.Endpoints), res.ClientUnavailable)
	}

	// A caller that has gone is not an enrichment failure: it propagates.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	fr.clientErr = ctx.Err()
	if _, err := (Local{Env: "dev", Runner: fr}).Endpoints(ctx, EndpointsRequest{Narrow: rng()}); !errors.Is(err, context.Canceled) {
		t.Errorf("a cancelled read: %v", err)
	}
}

// endpointsPanelCaller answers QueryPanel by the panel's id, so one fake serves the
// server and the client panel differently.
type endpointsPanelCaller struct {
	replies map[string]string
	errs    map[string]error
	panels  map[string]map[string]any
}

func (p *endpointsPanelCaller) Call(_ context.Context, procedure string, req, out any) error {
	if procedure != procQueryPanel {
		return fmt.Errorf("unexpected %s", procedure)
	}
	b, _ := json.Marshal(req)
	var m struct {
		Scope map[string]any `json:"scope"`
		Panel map[string]any `json:"panel"`
	}
	_ = json.Unmarshal(b, &m)
	id, _ := m.Panel["id"].(string)
	if p.panels == nil {
		p.panels = map[string]map[string]any{}
	}
	p.panels[id] = map[string]any{"scope": m.Scope, "panel": m.Panel}
	if err := p.errs[id]; err != nil {
		return err
	}
	r, ok := p.replies[id]
	if !ok {
		return fmt.Errorf("unexpected panel %q", id)
	}
	return json.Unmarshal([]byte(r), out)
}

const cloudServerReply = `{"rows":[
	{"group":{"service.name":"api","span.name":"todo.v1.TodoService/CreateTodo"},"values":[300,0,1,5,9]},
	{"group":{"service.name":"api","span.name":"todo.v1.TodoService/ListTodos"},"values":[200,0,1,5,9]}]}`

func TestCloudEndpointsJoinClientRPCs(t *testing.T) {
	pc := &endpointsPanelCaller{replies: map[string]string{
		"endpoints": cloudServerReply,
		"endpoints-client": `{"rows":[
			{"group":{"span.attr.rpc.service":"todo.v1.TodoService","span.attr.rpc.method":"CreateTodo"},"values":[450,42]},
			{"group":{"span.attr.rpc.service":"billing.v1.BillingService","span.attr.rpc.method":"Charge"},"values":[9,700]}]}`,
	}}
	res, err := (Cloud{Env: "prod", EnvID: "env-1", OrgID: "org-1", Caller: pc}).Endpoints(context.Background(), EndpointsRequest{Narrow: narrowC()})
	if err != nil {
		t.Fatal(err)
	}
	got := byName(res.Endpoints)
	if e := got["todo.v1.TodoService/CreateTodo"]; e.ClientCalls != 450 || e.ClientP95Ms != 42 {
		t.Errorf("CreateTodo: %+v", e)
	}
	if e := got["todo.v1.TodoService/ListTodos"]; e.ClientCalls != 0 || e.ClientP95Ms != 0 {
		t.Errorf("ListTodos has no client spans: %+v", e)
	}
	client := pc.panels["endpoints-client"]
	panel := client["panel"].(map[string]any)
	if fmt.Sprint(panel["groupBy"]) != "[span.attr.rpc.service span.attr.rpc.method]" ||
		fmt.Sprint(panel["where"]) != "[map[field:span.attr.forge.rpc.client op:eq value:true]]" {
		t.Errorf("client panel %v", panel)
	}
	scope := client["scope"].(map[string]any)
	if scope["services"] != nil || scope["filters"] != nil || fmt.Sprint(scope["envIds"]) != "[env-1]" {
		t.Errorf("the client read keeps the env, drops the server's --service/--filter: %v", scope)
	}
}

func TestCloudEndpointsClientFailureKeepsTheEndpoints(t *testing.T) {
	pc := &endpointsPanelCaller{
		replies: map[string]string{"endpoints": cloudServerReply},
		// An older control plane that refuses the client panel.
		errs: map[string]error{"endpoints-client": &cloud.Error{Code: cloud.CodeInvalidArgument, Message: "unknown field"}},
	}
	c := Cloud{Env: "prod", EnvID: "env-1", OrgID: "org-1", Caller: pc}
	res, err := c.Endpoints(context.Background(), EndpointsRequest{Narrow: narrowC()})
	if err != nil {
		t.Fatalf("an enrichment failure fails the verb: %v", err)
	}
	if len(res.Endpoints) != 2 || !strings.Contains(res.ClientUnavailable, "unknown field") {
		t.Errorf("endpoints %d, note %q", len(res.Endpoints), res.ClientUnavailable)
	}
	n := narrowC()
	n.Explain = true
	res, err = c.Endpoints(context.Background(), EndpointsRequest{Narrow: n})
	if err != nil || res.ClientUnavailable == "" {
		t.Errorf("explain with a refused client panel: %v %q", err, res.ClientUnavailable)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	pc.errs["endpoints-client"] = context.Canceled
	if _, err := c.Endpoints(ctx, EndpointsRequest{Narrow: narrowC()}); !errors.Is(err, context.Canceled) {
		t.Errorf("a cancelled read: %v", err)
	}
}
