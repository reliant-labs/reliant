package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/exitcode"
	"github.com/reliant-labs/forge/internal/obsbackend"
	"github.com/reliant-labs/forge/internal/opindex"
	"github.com/reliant-labs/forge/pkg/obsquery"
	"github.com/reliant-labs/forge/pkg/testkit"
)

// brMapping is a synthetic diff: two component ops and two rpcs (one
// removed), over a commit range plus the working tree.
func brMapping() opindex.Mapping {
	uncommitted := opindex.Commit{Subject: opindex.UncommittedSubject}
	c42 := opindex.Commit{SHA: "4242424242424242424242424242424242424242", Subject: "fix(billing): refund ids (#42)", PR: 42}
	c7 := opindex.Commit{SHA: "7777777777777777777777777777777777777777", Subject: "feat(todo): done flag"}
	return opindex.Mapping{
		Base: "baseb4sebaseb4sebaseb4sebaseb4sebaseb4se", Commits: []opindex.Commit{uncommitted, c7, c42}, Total: 2,
		Ops: []opindex.MappedOp{
			{Op: opindex.Op{Name: "todo.v1.TodoService/DeleteTodo", Kind: opindex.KindRPC, RPCService: "todo.v1.TodoService", RPCMethod: "DeleteTodo",
				File: "proto/todo/v1/todo.proto", Line: 9, ProtoFile: "proto/todo/v1/todo.proto", ProtoLine: 9}, Removed: true,
				Reasons: []opindex.Reason{{Via: opindex.ViaProto, Function: "rpc TodoService.DeleteTodo", File: "proto/todo/v1/todo.proto", Line: 9}},
				Commits: []opindex.Commit{c7}},
			{Op: opindex.Op{Name: "billing.Refund", Kind: opindex.KindComponent, Package: "internal/billing", File: "internal/billing/service.go", Line: 20, Function: "(*service).Refund"},
				Reasons: []opindex.Reason{{Via: opindex.ViaHelper, Function: "format", File: "internal/billing/service.go", Line: 30}},
				Commits: []opindex.Commit{c42}},
			{Op: opindex.Op{Name: "todo.v1.TodoService/CreateTodo", Kind: opindex.KindRPC, Package: "internal/handlers/todo", File: "internal/handlers/todo/rpc_create_todo.go", Line: 3,
				Function: "(*Service).CreateTodo", RPCService: "todo.v1.TodoService", RPCMethod: "CreateTodo", ProtoFile: "proto/todo/v1/todo.proto", ProtoLine: 8},
				Reasons: []opindex.Reason{{Via: opindex.ViaHandler, Function: "(*Service).CreateTodo", File: "internal/handlers/todo/rpc_create_todo.go", Line: 3},
					{Via: opindex.ViaProto, Function: "message CreateTodoRequest", File: "proto/todo/v1/todo.proto", Line: 14}},
				Commits: []opindex.Commit{uncommitted, c7}},
			{Op: opindex.Op{Name: "billing.Charge", Kind: opindex.KindComponent, Package: "internal/billing", File: "internal/billing/service.go", Line: 12, Function: "(*service).Charge"},
				Reasons: []opindex.Reason{{Via: opindex.ViaMethod, Function: "(*service).Charge", File: "internal/billing/service.go", Line: 12}},
				Commits: []opindex.Commit{uncommitted, c42, c7, {SHA: "abcdef0123456789", Subject: "older"}}},
		},
		Unattributed: []opindex.Unattributed{{File: "README.md", Reason: opindex.ReasonNotGo, Commits: []opindex.Commit{c7}}},
	}
}

func brOpNames(doc obsBlastRadiusDoc) []string {
	var out []string
	for _, op := range doc.Ops {
		out = append(out, op.Name)
	}
	return out
}

func TestBlastRadiusDocIsTheStaticMappingInNameOrder(t *testing.T) {
	doc := blastRadiusDoc(brMapping(), "main", "working tree")
	doc.Telemetry = obsBRTelemetryDoc{Status: brTelemetryNotRequested, ReasonCode: brReasonNoEnv}
	doc.finish("")
	if got := fmt.Sprint(brOpNames(doc)); got != "[billing.Charge billing.Refund todo.v1.TodoService/CreateTodo todo.v1.TodoService/DeleteTodo]" {
		t.Errorf("ops = %s, want name order", got)
	}
	want := obsBRSummaryDoc{Ops: 4, Components: 2, RPCs: 2, Removed: 1, UnattributedFiles: 1}
	if doc.Summary != want {
		t.Errorf("summary = %+v, want %+v (no traffic split without telemetry)", doc.Summary, want)
	}
	if doc.Order != brOrderName || doc.Suggested != nil {
		t.Errorf("order %q suggested %v", doc.Order, doc.Suggested)
	}
	doc.Stamp(schemaObsBlastRadius, nil)
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	// The stable shape: lists are never null, pr is always present, the
	// working tree's head is "".
	for _, want := range []string{`"schema":"forge.obs.blast_radius/v1"`, `"traffic":[]`, `"pr":0`, `"pr":42`, `"head":""`, `"head_ref":"working tree"`,
		`"base_ref":"main"`, `"total_commits":2`, `"truncated":false`, `"removed":true`, `"order":"name"`, `"telemetry":{"status":"not_requested","reason_code":"no_env"}`} {
		if !strings.Contains(s, want) {
			t.Errorf("document lacks %s:\n%s", want, s)
		}
	}
	if strings.Contains(s, "null") {
		t.Errorf("a list is null:\n%s", s)
	}
}

// Every key the document can carry is a --fields path, so a consumer can
// select anything it reads.
func TestBlastRadiusSchemaDeclaresEveryField(t *testing.T) {
	doc := blastRadiusDoc(brMapping(), "main", "working tree")
	doc.Telemetry = obsBRTelemetryDoc{Env: "prod", Backend: "cloud", From: obsNow.Add(-time.Hour), To: obsNow, Status: brTelemetryOK}
	doc.attachTraffic(obsbackend.OpsResult{Ops: []obsbackend.OpStats{{Service: "api", Name: "billing.Charge", Calls: 1}}})
	doc.Scope = &obsScopeExample
	doc.Warnings = []string{"w"}
	doc.Statements = []obsbackend.Statement{{Purpose: "p", SQL: "s", Params: map[string]string{"k": "v"}}}
	doc.finish("prod")
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	head := map[string]bool{"schema": true, "ok": true, "exit_code": true, "error": true, "truncated": true} // always kept
	var paths []string
	var walk func(prefix string, v any)
	walk = func(prefix string, v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, c := range x {
				if prefix == "" && head[k] {
					continue
				}
				p := k
				if prefix != "" {
					p = prefix + "." + k
				}
				if strings.HasPrefix(p, "statements.params.") {
					p = "statements.params.k"
				}
				paths = append(paths, p)
				walk(p, c)
			}
		case []any:
			for _, c := range x {
				walk(prefix, c)
			}
		}
	}
	walk("", m)
	for _, p := range paths {
		if _, err := obsBlastRadiusSchema.ParseFields(p); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
}

var obsScopeExample = obsScope(obsbackend.Info{Env: "prod", Backend: "cloud"}, obsbackend.Narrow{Range: obsbackend.Range{From: obsNow.Add(-time.Hour), To: obsNow}})

func TestBlastRadiusTrafficOrdersByCallsAcrossServices(t *testing.T) {
	doc := blastRadiusDoc(brMapping(), "main", "working tree")
	doc.Telemetry = obsBRTelemetryDoc{Env: "prod", From: obsNow.Add(-7 * 24 * time.Hour), To: obsNow, Status: brTelemetryOK}
	doc.attachTraffic(obsbackend.OpsResult{Ops: []obsbackend.OpStats{
		{Service: "api", Name: "todo.v1.TodoService/CreateTodo", Calls: 300, Errors: 3, ErrorRatio: 0.01, P95Ms: 40},
		{Service: "api", Name: "billing.Charge", Calls: 200, Errors: 20, ErrorRatio: 0.1, P95Ms: 900},
		{Service: "worker", Name: "billing.Charge", Calls: 250, P95Ms: 100},
		{Service: "api", Name: "GET /healthz", Calls: 9999}, // not a changed op
	}})
	doc.finish("prod")
	if got := fmt.Sprint(brOpNames(doc)); got != "[billing.Charge todo.v1.TodoService/CreateTodo billing.Refund todo.v1.TodoService/DeleteTodo]" {
		t.Errorf("ops = %s, want total calls desc then name", got)
	}
	if doc.Order != brOrderCalls {
		t.Errorf("order = %q", doc.Order)
	}
	charge := doc.Ops[0]
	if len(charge.Traffic) != 2 || charge.Traffic[0].Service != "worker" || charge.Traffic[1].Service != "api" || charge.Traffic[1].ErrorRatio != 0.1 {
		t.Errorf("an op running in two services has a row each, busiest first: %+v", charge.Traffic)
	}
	if len(doc.Ops[3].Traffic) != 0 || doc.Ops[3].Traffic == nil {
		t.Errorf("an op with no telemetry has traffic []: %#v", doc.Ops[3].Traffic)
	}
	want := obsBRSummaryDoc{Ops: 4, Components: 2, RPCs: 2, Removed: 1, WithTraffic: 2, WithoutTraffic: 2, Calls: 750, UnattributedFiles: 1}
	if doc.Summary != want {
		t.Errorf("summary = %+v, want %+v", doc.Summary, want)
	}
	sug := fmt.Sprint(doc.Suggested)
	for _, w := range []string{"forge obs traces prod --service api --route todo.v1.TodoService/CreateTodo --slow", "forge obs ops prod --kind component"} {
		if !strings.Contains(sug, w) {
			t.Errorf("suggested = %s, want %s", sug, w)
		}
	}

	var out bytes.Buffer
	printBlastRadius(obsDeps{Stdout: &out}, doc)
	text := out.String()
	for _, w := range []string{"OP", "VIA", "CALLS", "ERR%", "P95", "CHANGED BY", "billing.Charge", "450", "4.4%", "900ms",
		"uncommitted #42 7777777 +1", "todo.v1.TodoService/DeleteTodo (removed)", "handler,proto",
		"unattributed:", "README.md", "not Go source", "4 ops (2 rpcs, 2 components, 1 removed) from 3 commits of main (baseb4s)..working tree", "prod: 2 with traffic, 750 calls since 2026-10-15T12:00:00Z"} {
		if !strings.Contains(text, w) {
			t.Errorf("table lacks %q:\n%s", w, text)
		}
	}
}

// brRun runs `forge obs blast-radius` through the real root command against
// a synthetic mapping (no git) and the given backend.
func brRun(ctx context.Context, t *testing.T, m func(context.Context, string, string, string) (opindex.Mapping, string, []string, error), deps obsDeps, args ...string) (string, string, error) {
	t.Helper()
	oldMap := blastRadiusMapFn
	t.Cleanup(func() { blastRadiusMapFn = oldMap })
	blastRadiusMapFn = m
	old := obsDepsOverride
	t.Cleanup(func() { obsDepsOverride = old })
	deps.Now = func() time.Time { return obsNow }
	obsDepsOverride = &deps
	var out, errb bytes.Buffer
	cmd := newTestRootCmd(t)
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	cmd.SetArgs(append([]string{"obs", "blast-radius"}, args...))
	err := cmd.ExecuteContext(ctx)
	return out.String(), errb.String(), err
}

func brStatic(context.Context, string, string, string) (opindex.Mapping, string, []string, error) {
	return brMapping(), "origin/main", nil, nil
}

func brLocal(r obsbackend.Runner) obsDeps {
	return obsDeps{
		Facts: func(context.Context, io.Writer, string) (obsbackend.Facts, error) { return devFacts(), nil },
		Local: func(env string) obsbackend.Backend { return obsbackend.Local{Env: env, Runner: r} },
	}
}

func brOpsAnswers() []scriptedAnswer {
	row := func(labels []any, vals ...string) []any {
		out := []any{labels}
		for _, v := range vals {
			out = append(out, jn(v))
		}
		return out
	}
	return []scriptedAnswer{
		{"mapContains(SpanAttributes", obsbackend.Result{}},
		{"match(SpanName", obsbackend.Result{Rows: [][]any{row([]any{"api", "billing.Charge"}, "50", "5", "1", "2", "3")}}},
		{"otel_traces", obsbackend.Result{Rows: [][]any{row([]any{"api", "todo.v1.TodoService/CreateTodo"}, "3600", "36", "4", "20", "40")}}},
	}
}

func TestObsBlastRadiusTelemetry(t *testing.T) {
	ctx := context.Background()

	t.Run("no env is the static mapping, queried nowhere", func(t *testing.T) {
		sr := &scriptedRunner{}
		out, errb, err := brRun(ctx, t, brStatic, brLocal(sr), "--json")
		if err != nil {
			t.Fatal(err)
		}
		doc := decode(t, out)
		tel := doc["telemetry"].(map[string]any)
		if doc["exit_code"] != float64(0) || tel["status"] != "not_requested" || tel["reason_code"] != "no_env" || len(sr.ran) != 0 || errb != "" {
			t.Errorf("doc %v ran %d stderr %q", doc, len(sr.ran), errb)
		}
		if doc["base_ref"] != "origin/main" || doc["order"] != "name" {
			t.Errorf("base_ref %v order %v", doc["base_ref"], doc["order"])
		}
	})

	t.Run("ok attaches traffic and orders by calls", func(t *testing.T) {
		sr := &scriptedRunner{answers: brOpsAnswers()}
		out, _, err := brRun(ctx, t, brStatic, brLocal(sr), "dev", "--json")
		if err != nil {
			t.Fatal(err)
		}
		doc := decode(t, out)
		tel := doc["telemetry"].(map[string]any)
		if tel["status"] != "ok" || tel["env"] != "dev" || tel["backend"] != "local" || tel["from"] == nil {
			t.Errorf("telemetry = %v", tel)
		}
		first := doc["ops"].([]any)[0].(map[string]any)
		if first["name"] != "todo.v1.TodoService/CreateTodo" || len(first["traffic"].([]any)) != 1 {
			t.Errorf("busiest first: %v", first)
		}
		if doc["scope"] == nil || doc["order"] != "calls" {
			t.Errorf("scope %v order %v", doc["scope"], doc["order"])
		}
		sum := doc["summary"].(map[string]any)
		if sum["with_traffic"] != float64(2) || sum["without_traffic"] != float64(2) || sum["calls"] != float64(3650) {
			t.Errorf("summary = %v", sum)
		}
		// One query per panel: the server, component and code panels, each
		// narrowed to the mapped names (SpanName IN).
		if len(sr.ran) != 3 {
			t.Errorf("ran %d statements, want 3", len(sr.ran))
		}
		for _, st := range sr.ran {
			if !strings.Contains(st, "SpanName IN (") {
				t.Errorf("a query not narrowed to the mapped ops:\n%s", st)
			}
		}
	})

	for _, c := range []struct {
		name string
		deps obsDeps
		code string
	}{
		{"the query fails", brLocal(&scriptedRunner{err: obsbackend.Undetermined("the local ClickStack cannot be reached")}), brReasonQueryFailed},
		{"the env cannot be read", obsDeps{Facts: func(context.Context, io.Writer, string) (obsbackend.Facts, error) {
			return obsbackend.Facts{}, obsbackend.Undetermined("cannot read env %q's declaration", "dev")
		}}, brReasonBackendUnavailable},
		{"telemetry is off", obsDeps{Facts: func(context.Context, io.Writer, string) (obsbackend.Facts, error) {
			return obsbackend.Facts{Env: "dev", Declared: true}, nil
		}}, brReasonBackendUnavailable},
	} {
		t.Run(c.name+" degrades to the mapping", func(t *testing.T) {
			out, errb, err := brRun(ctx, t, brStatic, c.deps, "dev", "--json")
			if err != nil {
				t.Fatalf("telemetry failure is not the verb's: %v", err)
			}
			doc := decode(t, out)
			tel := doc["telemetry"].(map[string]any)
			if doc["ok"] != true || tel["status"] != "unavailable" || tel["reason_code"] != c.code || tel["reason"] == "" {
				t.Errorf("doc ok %v telemetry %v", doc["ok"], tel)
			}
			if len(doc["ops"].([]any)) != 4 || doc["order"] != "name" {
				t.Errorf("the mapping is still the answer: %v", doc["ops"])
			}
			if !strings.Contains(errb, "telemetry for dev is unavailable") {
				t.Errorf("stderr = %q", errb)
			}
		})
	}

	t.Run("no ops asks nothing", func(t *testing.T) {
		sr := &scriptedRunner{}
		empty := func(context.Context, string, string, string) (opindex.Mapping, string, []string, error) {
			return opindex.Mapping{Base: "b"}, "main", nil, nil
		}
		out, _, err := brRun(ctx, t, empty, brLocal(sr), "dev")
		if err != nil {
			t.Fatal(err)
		}
		if len(sr.ran) != 0 || !strings.Contains(out, "The diff reaches no ops.") {
			t.Errorf("ran %d:\n%s", len(sr.ran), out)
		}
	})

	t.Run("explain compiles and runs nothing", func(t *testing.T) {
		sr := &scriptedRunner{}
		out, _, err := brRun(ctx, t, brStatic, brLocal(sr), "dev", "--json", "--explain")
		if err != nil {
			t.Fatal(err)
		}
		doc := decode(t, out)
		if len(sr.ran) != 0 || doc["statements"] == nil || doc["telemetry"].(map[string]any)["reason_code"] != "explain" {
			t.Errorf("ran %d doc %v", len(sr.ran), doc)
		}
		// The query asks for every mapped op, the removed rpc included.
		params := fmt.Sprint(doc["statements"])
		for _, name := range []string{"billing.Charge", "billing.Refund", "todo.v1.TodoService/CreateTodo", "todo.v1.TodoService/DeleteTodo"} {
			if !strings.Contains(params, name) {
				t.Errorf("the query does not ask for %s: %s", name, params)
			}
		}
	})

	t.Run("fields project the document", func(t *testing.T) {
		out, _, err := brRun(ctx, t, brStatic, brLocal(&scriptedRunner{}), "--json", "--fields", "ops.name,summary.ops")
		if err != nil {
			t.Fatal(err)
		}
		doc := decode(t, out)
		if _, ok := doc["telemetry"]; ok {
			t.Error("--fields keeps only what was selected")
		}
		if op := doc["ops"].([]any)[0].(map[string]any); len(op) != 1 || op["name"] != "billing.Charge" {
			t.Errorf("projected op: %v", op)
		}
	})

	t.Run("cancellation fails", func(t *testing.T) {
		cctx, cancel := context.WithCancel(ctx)
		defer cancel()
		_, _, err := brRun(cctx, t, brStatic, brLocal(cancellingRunner{cancel}), "dev", "--json")
		if err == nil {
			t.Fatal("a cancelled read is not degraded to success")
		}
	})
}

// cancellingRunner cancels the read it is asked to run, and fails the way a
// cancelled query does.
type cancellingRunner struct{ cancel context.CancelFunc }

func (c cancellingRunner) Run(ctx context.Context, _ obsquery.Stmt, _ obsbackend.Limits) (obsbackend.Result, error) {
	c.cancel()
	return obsbackend.Result{}, ctx.Err()
}

// TestObsBlastRadiusExitParityWithoutGit: the document's exit_code is the
// process's for every way the verb ends, and the classes are the obs ones.
func TestObsBlastRadiusExitParityWithoutGit(t *testing.T) {
	failing := func(err error) func(context.Context, string, string, string) (opindex.Mapping, string, []string, error) {
		return func(context.Context, string, string, string) (opindex.Mapping, string, []string, error) {
			return opindex.Mapping{}, "", nil, err
		}
	}
	unavailable := obsDeps{Facts: func(context.Context, io.Writer, string) (obsbackend.Facts, error) {
		return obsbackend.Facts{}, obsbackend.Undetermined("cannot read env")
	}}
	for _, c := range []struct {
		name string
		m    func(context.Context, string, string, string) (opindex.Mapping, string, []string, error)
		deps obsDeps
		args []string
		want int
	}{
		{"success", brStatic, brLocal(&scriptedRunner{answers: brOpsAnswers()}), []string{"dev"}, 0},
		{"telemetry unavailable", brStatic, unavailable, []string{"prod"}, 0},
		{"a rev names no commit", failing(obsbackend.Wrong("--base %q names no commit", "nope")), obsDeps{}, nil, 1},
		{"not a git checkout", failing(obsbackend.Undetermined("not in a git checkout")), obsDeps{}, nil, 2},
		{"--backend is not a backend", brStatic, obsDeps{}, []string{"dev", "--backend", "nope"}, 1},
		{"--fields names no field", brStatic, obsDeps{}, []string{"--fields", "nope"}, 1},
		{"--since is not positive", brStatic, obsDeps{}, []string{"dev", "--since", "0s"}, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			out, _, err := brRun(context.Background(), t, c.m, c.deps, append(c.args, "--json")...)
			process := exitcode.Of(err)
			doc := decode(t, out)
			if doc["exit_code"] != float64(process) || doc["ok"] != (process == 0) {
				t.Fatalf("document exit_code %v ok %v, process %d (%v)", doc["exit_code"], doc["ok"], process, err)
			}
			if process != c.want {
				t.Errorf("exit = %d, want %d (%v)", process, c.want, err)
			}
			if doc["schema"] != schemaObsBlastRadius {
				t.Errorf("schema = %v", doc["schema"])
			}
		})
	}
}

// ── against a real git repository ───────────────────────────────────────

// brFixture is a forge-shaped project: a component package with its
// generated decorator, and a handler package with its proto.
var brFixture = map[string]string{
	"internal/billing/contract.go": "package billing\n\nimport \"context\"\n\ntype Service interface {\n\tCharge(ctx context.Context, amount int) error\n\tRefund(ctx context.Context, id string) (string, error)\n}\n\ntype Deps struct{}\n",
	"internal/billing/service.go": `package billing

import "context"

type service struct{ deps Deps }

func New(deps Deps) Service { return &service{deps: deps} }

func (s *service) Charge(ctx context.Context, amount int) error {
	return nil
}

func (s *service) Refund(ctx context.Context, id string) (string, error) {
	return format(id), nil
}

func format(id string) string {
	return "r-" + id
}
`,
	"internal/billing/middleware_gen.go": `// Code generated by forge. DO NOT EDIT.
package billing

import (
	"context"
	"log/slog"

	"github.com/reliant-labs/forge/pkg/observe"
)

func NewServiceWithForgeMiddleware(inner Service) Service {
	return &forgeMiddlewareService{inner: inner, chain: observe.DefaultComponentChain("example.com/app/internal/billing", "billing", slog.LevelDebug)}
}

type forgeMiddlewareService struct {
	inner Service
	chain *observe.ComponentChain
}

func (o *forgeMiddlewareService) Charge(ctx context.Context, amount int) error {
	return o.chain.Run(ctx, "billing.Charge", func(ctx context.Context) error {
		return o.inner.Charge(ctx, amount)
	})
}

func (o *forgeMiddlewareService) Refund(ctx context.Context, id string) (string, error) {
	return o.chain.Around(ctx, "billing.Refund", func(ctx context.Context) (string, error) {
		return o.inner.Refund(ctx, id)
	})
}
`,
	"proto/todo/v1/todo.proto": `syntax = "proto3";

package todo.v1;

service TodoService {
  rpc CreateTodo(CreateTodoRequest) returns (CreateTodoResponse);
}

message CreateTodoRequest {
  string title = 1;
}

message CreateTodoResponse {
  string id = 1;
}
`,
	"internal/handlers/todo/service.go":         "package todo\n\ntype Service struct{ deps Deps }\n\ntype Deps struct{}\n\nfunc New(deps Deps) (*Service, error) { return &Service{deps: deps}, nil }\n",
	"internal/handlers/todo/rpc_create_todo.go": "package todo\n\nfunc (s *Service) CreateTodo(req string) (string, error) {\n\treturn req, nil\n}\n",
}

// brGitRepo builds a repository whose main holds the fixture, then a branch
// with a squash-merged-looking commit and an uncommitted edit. It returns
// the project directory and a git runner.
func brGitRepo(t *testing.T) (string, func(args ...string) string) {
	t.Helper()
	dir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(files map[string]string) {
		for rel, body := range files {
			p := filepath.Join(dir, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	git("init", "-q", "-b", "main")
	write(brFixture)
	git("add", "-A")
	git("commit", "-q", "-m", "chore: initial")
	git("checkout", "-q", "-b", "feature")
	write(map[string]string{
		"internal/billing/service.go": strings.Replace(brFixture["internal/billing/service.go"], `"r-" + id`, `"refund-" + id`, 1),
		"README.md":                   "docs\n",
	})
	git("add", "-A")
	git("commit", "-q", "-m", "fix(billing): refund ids (#42)")
	write(map[string]string{"internal/handlers/todo/rpc_create_todo.go": strings.Replace(brFixture["internal/handlers/todo/rpc_create_todo.go"], "return req, nil", `return req + "!", nil`, 1)})
	return dir, git
}

func TestObsBlastRadiusGit(t *testing.T) {
	testkit.Slow(t, "execs git")
	dir, git := brGitRepo(t)
	run := func(args ...string) (map[string]any, error) {
		t.Helper()
		out, _, err := brRunGit(t, append([]string{"-C", dir}, args...)...)
		return decode(t, out), err
	}

	doc, err := run("--json")
	if err != nil {
		t.Fatal(err)
	}
	if doc["base_ref"] != "main" || doc["base"] != git("rev-parse", "main") || doc["head"] != "" || doc["head_ref"] != "working tree" {
		t.Errorf("range: base_ref %v base %v head %v head_ref %v", doc["base_ref"], doc["base"], doc["head"], doc["head_ref"])
	}
	byName := map[string]map[string]any{}
	for _, o := range doc["ops"].([]any) {
		op := o.(map[string]any)
		byName[op["name"].(string)] = op
	}
	refund := byName["billing.Refund"]
	if refund == nil || fmt.Sprint(refund["commits"]) != "[map[pr:42 sha:"+git("rev-parse", "HEAD")+" subject:fix(billing): refund ids (#42)]]" {
		t.Errorf("billing.Refund: %v", refund)
	}
	create := byName["todo.v1.TodoService/CreateTodo"]
	if create == nil || create["commits"].([]any)[0].(map[string]any)["subject"] != "uncommitted changes" {
		t.Errorf("CreateTodo: %v", create)
	}
	if _, ok := byName["billing.Charge"]; ok {
		t.Error("billing.Charge did not change")
	}
	if u := fmt.Sprint(doc["unattributed"]); !strings.Contains(u, "README.md") {
		t.Errorf("unattributed = %s", u)
	}

	doc, err = run("--json", "--base", "HEAD~1", "--head", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if doc["head"] != git("rev-parse", "HEAD") || doc["base_ref"] != "HEAD~1" || len(doc["ops"].([]any)) != 1 {
		t.Errorf("explicit range: %v", doc)
	}

	out, _, err := brRunGit(t, "-C", dir)
	if err != nil || !strings.Contains(out, "CHANGED BY") || !strings.Contains(out, "#42") || !strings.Contains(out, "uncommitted") {
		t.Errorf("table (%v):\n%s", err, out)
	}
}

// TestObsBlastRadiusGitExitParity: the document's exit_code is the
// process's, through git, for each way the verb ends.
func TestObsBlastRadiusGitExitParity(t *testing.T) {
	testkit.Slow(t, "execs git")
	dir, _ := brGitRepo(t)
	plain := t.TempDir()
	// Keep git from finding a repository above the temp dir.
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(plain))
	unavailable := &obsDeps{Facts: func(context.Context, io.Writer, string) (obsbackend.Facts, error) {
		return obsbackend.Facts{}, obsbackend.Undetermined("cannot read env")
	}}
	for _, c := range []struct {
		name string
		args []string
		want int
	}{
		{"success", []string{"-C", dir}, 0},
		{"telemetry unavailable", []string{"prod", "-C", dir}, 0},
		{"--base names no commit", []string{"-C", dir, "--base", "no-such-rev"}, 1},
		{"--head names no commit", []string{"-C", dir, "--head", "no-such-rev"}, 1},
		{"not a git checkout", []string{"-C", plain}, 2},
	} {
		t.Run(c.name, func(t *testing.T) {
			old := obsDepsOverride
			t.Cleanup(func() { obsDepsOverride = old })
			obsDepsOverride = unavailable
			out, _, err := brRunGit(t, append(c.args, "--json")...)
			process := exitcode.Of(err)
			doc := decode(t, out)
			if doc["exit_code"] != float64(process) || doc["ok"] != (process == 0) {
				t.Fatalf("document exit_code %v, process %d (%v)\n%s", doc["exit_code"], process, err, out)
			}
			if process != c.want {
				t.Errorf("exit = %d, want %d (%v)", process, c.want, err)
			}
			if c.want == 0 && c.args[0] == "prod" && doc["telemetry"].(map[string]any)["status"] != "unavailable" {
				t.Errorf("telemetry = %v", doc["telemetry"])
			}
		})
	}
}

// brRunGit runs `forge obs blast-radius <args>` through the root command
// with the real mapping.
func brRunGit(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	var out, errb bytes.Buffer
	cmd := newTestRootCmd(t)
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	cmd.SetArgs(append([]string{"obs", "blast-radius"}, args...))
	err := cmd.ExecuteContext(context.Background())
	return out.String(), errb.String(), err
}
