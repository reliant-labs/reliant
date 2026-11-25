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

func TestOpsNamesValidation(t *testing.T) {
	got, err := opsNames([]string{" todo.Create", "todo.Create", "GET /healthz "})
	if err != nil || fmt.Sprint(got) != "[todo.Create GET /healthz]" {
		t.Errorf("trimmed and deduplicated in order: %q, %v", got, err)
	}
	if got, err := opsNames(nil); err != nil || got != nil {
		t.Errorf("no names: %v, %v", got, err)
	}
	full := make([]string, maxOpsNames)
	for i := range full {
		full[i] = fmt.Sprintf("pkg.Op%d", i)
	}
	if _, err := opsNames(full); err != nil {
		t.Errorf("%d names: %v", maxOpsNames, err)
	}
	if _, err := opsNames(append(full, full[0])); err != nil {
		t.Errorf("a duplicate does not count twice: %v", err)
	}
	for name, raw := range map[string][]string{
		"empty":     {"a.B", "  "},
		"too long":  {strings.Repeat("x", maxOpsNameLen+1)},
		"too many":  append(append([]string{}, full...), "pkg.OneMore"),
		"empty csv": {""},
	} {
		if _, err := opsNames(raw); exitCodeForError(err) != 1 {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if _, err := opsNames([]string{strings.Repeat("é", maxOpsNameLen)}); err != nil {
		t.Errorf("the limit counts characters, not bytes: %v", err)
	}
}

// namedOpsAnswers answer a --name request: the component and server panels
// hold only the names they were asked for, as a narrowed query would.
func namedOpsAnswers() []scriptedAnswer {
	row := func(labels []any, vals ...string) []any {
		out := []any{labels}
		for _, v := range vals {
			out = append(out, jn(v))
		}
		return out
	}
	return []scriptedAnswer{
		{"mapContains(SpanAttributes", obsbackend.Result{}},
		{"match(SpanName", obsbackend.Result{Rows: [][]any{
			row([]any{"api", "todo.Create"}, "600", "60", "3", "900", "1500"),
		}}},
		{"otel_traces", obsbackend.Result{Rows: [][]any{
			row([]any{"api", "todo.v1.TodoService/CreateTodo"}, "3600", "36", "4", "20", "40"),
		}}},
	}
}

func TestObsOpsNameAsksForTheNamedOpsAndListsEachOne(t *testing.T) {
	sr := &scriptedRunner{answers: namedOpsAnswers()}
	out, _, err := obsRun(t, devFacts(), sr, "ops", "dev", "--json",
		"--name", "todo.Create", "--name", "todo.v1.TodoService/CreateTodo,claimable.IsUnclaimedUser")
	if err != nil {
		t.Fatal(err)
	}
	if len(sr.ran) != 3 {
		t.Fatalf("ran %d statements, want the server, component and code panels", len(sr.ran))
	}
	for _, sql := range sr.ran {
		if !strings.Contains(sql, "SpanName IN (") {
			t.Errorf("a panel not narrowed to the names: %s", sql)
		}
	}
	doc := decode(t, out)
	if fmt.Sprint(doc["names"]) != "[todo.Create todo.v1.TodoService/CreateTodo claimable.IsUnclaimedUser]" {
		t.Errorf("names = %v", doc["names"])
	}
	var got []string
	for _, o := range doc["ops"].([]any) {
		m := o.(map[string]any)
		got = append(got, fmt.Sprint(m["name"], " found=", m["found"], " calls=", m["calls"]))
	}
	want := "[todo.v1.TodoService/CreateTodo found=true calls=3600 todo.Create found=true calls=600 claimable.IsUnclaimedUser found=false calls=0]"
	if fmt.Sprint(got) != want {
		t.Errorf("ops = %v\nwant %s", got, want)
	}
	missing := doc["ops"].([]any)[2].(map[string]any)
	if missing["kind"] != "component" || missing["service"] != "" {
		t.Errorf("the op with no spans: %v", missing)
	}
	if doc["truncated"] == true {
		t.Errorf("--name defaults --top to every requested op: %v", doc)
	}
	if !strings.Contains(fmt.Sprint(doc["suggested"]), "--route todo.v1.TodoService/CreateTodo") {
		t.Errorf("suggested = %v", doc["suggested"])
	}

	// Human: the op with no spans says so.
	out, _, err = obsRun(t, devFacts(), &scriptedRunner{answers: namedOpsAnswers()}, "ops", "dev", "--name", "claimable.IsUnclaimedUser")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"(no spans)", "claimable.IsUnclaimedUser", "(1 requested op(s) had no spans in the window)"} {
		if !strings.Contains(out, want) {
			t.Errorf("human output lacks %q:\n%s", want, out)
		}
	}

	// An explicit --top caps the rows, ops with no spans included.
	out, _, err = obsRun(t, devFacts(), &scriptedRunner{answers: namedOpsAnswers()}, "ops", "dev", "--json", "--top", "1",
		"--name", "todo.Create,claimable.IsUnclaimedUser")
	if err != nil {
		t.Fatal(err)
	}
	doc = decode(t, out)
	if ops := doc["ops"].([]any); len(ops) != 1 || ops[0].(map[string]any)["name"] != "todo.Create" || doc["omitted"] != float64(1) {
		t.Errorf("--top 1: ops %v, omitted %v", ops, doc["omitted"])
	}
}

func TestObsOpsNameRefusals(t *testing.T) {
	for _, args := range [][]string{
		{"--name", ""},
		{"--name", "a.B,,c.D"},
		{"--name", strings.Repeat("x", maxOpsNameLen+1)},
		{"--name", "todo.Create", "--kind", "rpc"},
	} {
		sr := &scriptedRunner{answers: namedOpsAnswers()}
		if _, _, err := obsRun(t, devFacts(), sr, append([]string{"ops", "dev", "--json"}, args...)...); exitCodeForError(err) != 1 {
			t.Errorf("%q: err = %v", args, err)
		}
		if len(sr.ran) != 0 {
			t.Errorf("%q: ran %d statements", args, len(sr.ran))
		}
	}
}

// --name with --explain compiles the narrowed statements and measures
// nothing: no statement runs, and no op, found or missing, is listed.
func TestObsOpsNameExplainListsNoOps(t *testing.T) {
	sr := &scriptedRunner{answers: namedOpsAnswers()}
	out, _, err := obsRun(t, devFacts(), sr, "ops", "dev", "--json", "--explain", "--name", "todo.Create,claimable.IsUnclaimedUser")
	if err != nil {
		t.Fatal(err)
	}
	if len(sr.ran) != 0 {
		t.Fatalf("--explain ran %d statements", len(sr.ran))
	}
	doc := decode(t, out)
	if ops, ok := doc["ops"].([]any); !ok || len(ops) != 0 {
		t.Errorf("--explain ops = %v, want []", doc["ops"])
	}
	if doc["truncated"] == true || doc["suggested"] != nil {
		t.Errorf("--explain carries measurements: %v", doc)
	}
	stmts, _ := doc["statements"].([]any)
	if len(stmts) != 2 {
		t.Fatalf("statements = %v, want the component and code panels", stmts)
	}
	for _, s := range stmts {
		st := s.(map[string]any)
		if !strings.Contains(fmt.Sprint(st["sql"]), "SpanName IN (") {
			t.Errorf("%v not narrowed by name: %v", st["purpose"], st["sql"])
		}
		params := fmt.Sprint(st["params"])
		if !strings.Contains(params, "todo.Create") || !strings.Contains(params, "claimable.IsUnclaimedUser") {
			t.Errorf("%v params lack the names: %s", st["purpose"], params)
		}
	}
}
