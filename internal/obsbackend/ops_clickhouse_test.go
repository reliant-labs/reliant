package obsbackend_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/obsbackend"
	"github.com/reliant-labs/forge/internal/obsbackend/obstest"
	"github.com/reliant-labs/forge/pkg/testkit"
)

// TestOpsAgainstClickHouse runs the ops panels through the real
// clickhouse-client invocation: the name-shape matches, the code attribute
// lookup and the group-bys must all compile and select what the unit tests
// assume.
func TestOpsAgainstClickHouse(t *testing.T) {
	testkit.Slow(t, "boots a ClickHouse container and runs the ops panels against it")
	runner, write := obstest.Start(t)
	// A fixed instant: every span sits a few seconds before it, and the query
	// range is pinned around it, so nothing depends on the wall clock.
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	span := func(i int, svc, name, kind, status string, dur time.Duration, env, attrs string) string {
		return fmt.Sprintf(`INSERT INTO otel_traces (Timestamp, TraceId, SpanId, SpanName, SpanKind, ServiceName, ResourceAttributes, SpanAttributes, Duration, StatusCode)
SELECT toDateTime64('%s', 9), 't%d', 's%d', '%s', '%s', '%s', map('deployment.environment.name','%s'), map(%s), %d, '%s'`,
			now.Add(-time.Duration(i+1)*time.Second).Format("2006-01-02 15:04:05.000000000"), i, i, name, kind, svc, env, attrs, dur.Nanoseconds(), status)
	}
	code := `'code.file.path','internal/todo/service.go','code.function.name','x/internal/todo.Service.Create','forge.op','todo.Create'`
	var stmts []string
	for i := range 10 {
		stmts = append(stmts, span(i, "api", "todo.v1.TodoService/CreateTodo", "Server", "Unset", 20*time.Millisecond, "dev", ""))
	}
	for i := 10; i < 14; i++ {
		stmts = append(stmts, span(i, "api", "GET /healthz", "Server", "Unset", time.Millisecond, "dev", ""))
	}
	for i := 20; i < 26; i++ {
		status := map[bool]string{true: "Error", false: "Unset"}[i < 23]
		attrs := code
		if i == 25 {
			attrs = "" // a span from a build that predates the attribute contract
		}
		stmts = append(stmts, span(i, "api", "todo.Create", "Internal", status, 300*time.Millisecond, "dev", attrs))
	}
	stmts = append(stmts,
		span(30, "api", "SELECT todos", "Internal", "Unset", time.Millisecond, "dev", ""), // not a component's name
		span(31, "api", "todo.Create", "Internal", "Error", time.Second, "prod", code),    // another env
	)
	write(t, strings.Join(stmts, ";\n")+";")

	l := obsbackend.Local{Env: "dev", Runner: runner}
	n := obsbackend.Narrow{Range: obsbackend.Range{From: now.Add(-time.Hour), To: now.Add(time.Minute)}}
	res, err := l.Ops(context.Background(), obsbackend.OpsRequest{Narrow: n})
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]obsbackend.OpStats{}
	for _, o := range res.Ops {
		byName[o.Name] = o
	}
	if len(res.Ops) != 3 {
		t.Fatalf("want the rpc, the route and the component: %+v", res.Ops)
	}
	if o := byName["todo.v1.TodoService/CreateTodo"]; o.Kind != obsbackend.OpKindRPC || o.Calls != 10 || o.P95Ms != 20 {
		t.Errorf("rpc: %+v", o)
	}
	if o := byName["GET /healthz"]; o.Kind != obsbackend.OpKindEndpoint || o.Calls != 4 {
		t.Errorf("endpoint: %+v", o)
	}
	c := byName["todo.Create"]
	if c.Kind != obsbackend.OpKindComponent || c.Calls != 6 || c.Errors != 3 || c.ErrorRatio != 0.5 {
		t.Errorf("component, one op across builds with and without the attributes: %+v", c)
	}
	if c.CodeFilePath != "internal/todo/service.go" || c.CodeFunction != "x/internal/todo.Service.Create" {
		t.Errorf("component code: %+v", c)
	}

	res, err = l.Ops(context.Background(), obsbackend.OpsRequest{Narrow: n, Names: []string{"todo.Create", "GET /healthz"}})
	if err != nil {
		t.Fatal(err)
	}
	named := map[string]bool{}
	for _, o := range res.Ops {
		named[o.Name] = true
	}
	if len(res.Ops) != 2 || !named["todo.Create"] || !named["GET /healthz"] {
		t.Errorf("named ops only, the rpc left out: %+v", res.Ops)
	}
	res, err = l.Ops(context.Background(), obsbackend.OpsRequest{Narrow: n, Names: []string{"todo.Create"}, Kinds: []string{obsbackend.OpKindComponent, obsbackend.OpKindRPC}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Ops) != 1 || res.Ops[0].Name != "todo.Create" {
		t.Errorf("named, rpcs and components only: %+v", res.Ops)
	}
	res, err = l.Ops(context.Background(), obsbackend.OpsRequest{Narrow: n, Kinds: []string{obsbackend.OpKindRPC}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Ops) != 1 || res.Ops[0].Kind != obsbackend.OpKindRPC {
		t.Errorf("rpcs only, matched in ClickHouse: %+v", res.Ops)
	}
}
