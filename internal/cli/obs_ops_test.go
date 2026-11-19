package cli

import (
	"fmt"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/obsbackend"
)

// opsAnswers answer the three ops panels: the code panel (its mapContains),
// the component panel (its name match), then the server panel.
func opsAnswers() []scriptedAnswer {
	row := func(labels []any, vals ...string) []any {
		out := []any{labels}
		for _, v := range vals {
			out = append(out, jn(v))
		}
		return out
	}
	return []scriptedAnswer{
		{"mapContains(SpanAttributes", obsbackend.Result{Rows: [][]any{
			row([]any{"todo.Create", "internal/todo/service.go", "github.com/x/internal/todo.Service.Create"}, "500"),
		}}},
		{"match(SpanName", obsbackend.Result{Rows: [][]any{
			row([]any{"api", "todo.Create"}, "600", "60", "3", "900", "1500"),
			row([]any{"api", "billing.Charge"}, "50", "0", "1", "2", "3"),
		}}},
		{"otel_traces", obsbackend.Result{Rows: [][]any{
			row([]any{"api", "todo.v1.TodoService/CreateTodo"}, "3600", "36", "4", "20", "40"),
			row([]any{"api", "GET /healthz"}, "100", "0", "1", "2", "3"),
		}}},
	}
}

func TestObsOpsJSON(t *testing.T) {
	sr := &scriptedRunner{answers: opsAnswers()}
	out, _, err := obsRun(t, devFacts(), sr, "ops", "dev", "--json")
	if err != nil {
		t.Fatal(err)
	}
	doc := decode(t, out)
	for k, want := range map[string]any{"schema": "forge.obs.ops/v1", "ok": true, "exit_code": float64(0), "query_mode": "structured", "sort": "calls"} {
		if doc[k] != want {
			t.Errorf("%s = %v, want %v", k, doc[k], want)
		}
	}
	ops := doc["ops"].([]any)
	var got []string
	for _, o := range ops {
		m := o.(map[string]any)
		got = append(got, fmt.Sprint(m["kind"], " ", m["name"]))
	}
	if fmt.Sprint(got) != "[rpc todo.v1.TodoService/CreateTodo component todo.Create endpoint GET /healthz component billing.Charge]" {
		t.Errorf("ops by calls: %v", got)
	}
	c := ops[1].(map[string]any)
	if c["code_file_path"] != "internal/todo/service.go" || c["code_source"] != "telemetry" || c["code_function"] != "github.com/x/internal/todo.Service.Create" || c["error_ratio"] != 0.1 {
		t.Errorf("component op: %v", c)
	}
	if _, ok := ops[3].(map[string]any)["code_source"]; ok {
		t.Errorf("an op no one located has no code_source: %v", ops[3])
	}
	if sug := fmt.Sprint(doc["suggested"]); !strings.Contains(sug, "forge obs traces dev --service api --route todo.v1.TodoService/CreateTodo --slow") {
		t.Errorf("suggested = %s", sug)
	}
	if len(sr.ran) != 3 {
		t.Errorf("ran %d statements, want the server, component and code panels", len(sr.ran))
	}
}

func TestObsOpsHumanFieldsExplainAndRefusals(t *testing.T) {
	out, _, err := obsRun(t, devFacts(), &scriptedRunner{answers: opsAnswers()}, "ops", "dev", "--kind", "component", "--sort", "p95", "--top", "1")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"SERVICE", "KIND", "CODE", "todo.Create", "internal/todo/service.go", "(1 more; --top to show them)"} {
		if !strings.Contains(out, want) {
			t.Errorf("human output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "CreateTodo") {
		t.Errorf("--kind component shows an rpc:\n%s", out)
	}

	out, _, err = obsRun(t, devFacts(), &scriptedRunner{answers: opsAnswers()}, "ops", "dev", "--json", "--fields", "ops.name,ops.kind")
	if err != nil {
		t.Fatal(err)
	}
	doc := decode(t, out)
	if _, ok := doc["suggested"]; ok {
		t.Error("--fields drops everything not selected")
	}
	if first := doc["ops"].([]any)[0].(map[string]any); len(first) != 2 || first["kind"] != "rpc" {
		t.Errorf("projected op: %v", first)
	}

	sr := &scriptedRunner{}
	out, _, err = obsRun(t, devFacts(), sr, "ops", "dev", "--explain")
	if err != nil {
		t.Fatal(err)
	}
	if len(sr.ran) != 0 {
		t.Fatalf("--explain ran %d statements", len(sr.ran))
	}
	for _, want := range []string{"-- ops server", "-- ops component", "-- ops code", "FROM default.otel_traces"} {
		if !strings.Contains(out, want) {
			t.Errorf("explain output lacks %q:\n%s", want, out)
		}
	}

	for _, args := range [][]string{{"--kind", "rpcs"}, {"--sort", "requests"}, {"--fields", "endpoints.name"}} {
		if _, _, err := obsRun(t, devFacts(), &scriptedRunner{}, append([]string{"ops", "dev"}, args...)...); exitCodeForError(err) != 1 {
			t.Errorf("%v: err = %v", args, err)
		}
	}
}

func TestOpDocLocatesFromTheCheckoutOnlyWhereTelemetryCannot(t *testing.T) {
	locate := func(name string) (string, int, string, bool) {
		if name == "todo.Create" || name == "todo.v1.TodoService/CreateTodo" {
			return "internal/todo/service.go", 42, "(*service).Create", true
		}
		return "", 0, "", false
	}
	static := opDoc(obsbackend.OpStats{Name: "todo.Create", Kind: obsbackend.OpKindComponent}, locate)
	if static.CodeSource != "static" || static.CodeFilePath != "internal/todo/service.go" || static.CodeLine != 42 || static.CodeFunction != "(*service).Create" {
		t.Errorf("static: %+v", static)
	}
	tel := opDoc(obsbackend.OpStats{Name: "todo.Create", Kind: obsbackend.OpKindComponent, CodeFilePath: "internal/todo/service.go", CodeFunction: "x/todo.Service.Create"}, locate)
	if tel.CodeSource != "telemetry" || tel.CodeLine != 42 || tel.CodeFunction != "x/todo.Service.Create" {
		t.Errorf("telemetry keeps its fields and borrows the line of the same file: %+v", tel)
	}
	moved := opDoc(obsbackend.OpStats{Name: "todo.Create", Kind: obsbackend.OpKindComponent, CodeFilePath: "internal/todo/old.go"}, locate)
	if moved.CodeLine != 0 || moved.CodeFilePath != "internal/todo/old.go" {
		t.Errorf("a line from another file is not borrowed: %+v", moved)
	}
	if rpc := opDoc(obsbackend.OpStats{Name: "todo.v1.TodoService/CreateTodo", Kind: obsbackend.OpKindRPC}, locate); rpc.CodeLine != 42 {
		t.Errorf("an rpc is located by its handler: %+v", rpc)
	}
	if none := opDoc(obsbackend.OpStats{Name: "pkg.Gone", Kind: obsbackend.OpKindComponent}, locate); none.CodeSource != "" || none.CodeFilePath != "" {
		t.Errorf("not found is no location, never an error: %+v", none)
	}
	if bare := opDoc(obsbackend.OpStats{Name: "todo.Create", Kind: obsbackend.OpKindComponent}, nil); bare.CodeSource != "" {
		t.Errorf("no checkout: %+v", bare)
	}
	if got := staticOpLocator(t.TempDir()); got == nil {
		t.Error("staticOpLocator returns a locator")
	} else if _, _, _, ok := got("todo.Create"); ok {
		t.Error("an empty project locates nothing")
	}
}

func TestOpSuggestionsFindTheOpsTraces(t *testing.T) {
	for _, c := range []struct {
		op   obsOpDoc
		want string
	}{
		{obsOpDoc{Service: "api", Name: "GET /x", Kind: "endpoint"}, "forge obs traces prod --service api --route 'GET /x' --slow"},
		{obsOpDoc{Service: "api", Name: "todo.Create", Kind: "component", CodeSource: "telemetry"}, "forge obs traces prod --service api --filter span.attr.forge.op=todo.Create --errors"},
		{obsOpDoc{Service: "api", Name: "todo.Create", Kind: "component", CodeSource: "static"}, "forge obs traces prod --service api --slow"},
	} {
		if got := fmt.Sprint(opSuggestions("prod", c.op)); !strings.Contains(got, c.want) {
			t.Errorf("%+v: %s, want %s", c.op, got, c.want)
		}
	}
}
