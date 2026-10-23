package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/obsbackend"
	"github.com/reliant-labs/forge/pkg/obsquery"
)

var updateObsGoldens = flag.Bool("update-obs", false, "rewrite the forge obs golden files")

var obsNow = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

// obsRun executes `forge obs <args>` in-process through the real root command,
// against runner (a fake ClickHouse) and the given facts, and returns stdout,
// stderr and the verb's error.
func obsRun(t *testing.T, facts obsbackend.Facts, runner obsbackend.Runner, args ...string) (string, string, error) {
	t.Helper()
	var out, errb bytes.Buffer
	old := obsDepsOverride
	t.Cleanup(func() { obsDepsOverride = old })
	obsDepsOverride = &obsDeps{
		Facts: func(context.Context, io.Writer, string) (obsbackend.Facts, error) { return facts, nil },
		Local: func(env string) obsbackend.Backend { return obsbackend.Local{Env: env, Runner: runner} },
		Now:   func() time.Time { return obsNow },
		// Nothing promoted: a verb test is not about the release ledger.
		Release: func(context.Context, string, time.Time) (*liveRelease, error) { return nil, nil },
	}
	cmd := newTestRootCmd(t)
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	cmd.SetArgs(append([]string{"obs"}, args...))
	err := cmd.ExecuteContext(context.Background())
	return out.String(), errb.String(), err
}

func devFacts() obsbackend.Facts {
	return obsbackend.Facts{Env: "dev", Declared: true, Enabled: true}
}

func obsGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "obs", name)
	if *updateObsGoldens {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update-obs)", err)
	}
	if string(want) != got {
		t.Errorf("%s differs\n--- want\n%s\n--- got\n%s", name, want, got)
	}
}

func decode(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	dec := json.NewDecoder(strings.NewReader(s))
	if err := dec.Decode(&m); err != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", err, s)
	}
	if dec.More() {
		t.Fatalf("stdout carries more than one document:\n%s", s)
	}
	return m
}

// scriptedRunner answers by substring and records the statements. It is safe
// for concurrent use (obs status reads its sections at once).
type scriptedRunner struct {
	mu      sync.Mutex
	ran     []string
	answers []scriptedAnswer
	err     error
}

type scriptedAnswer struct {
	contains string
	res      obsbackend.Result
}

func (s *scriptedRunner) Run(_ context.Context, st obsquery.Stmt, _ obsbackend.Limits) (obsbackend.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ran = append(s.ran, st.SQL)
	if s.err != nil {
		return obsbackend.Result{}, s.err
	}
	for _, a := range s.answers {
		if strings.Contains(st.SQL, a.contains) {
			return a.res, nil
		}
	}
	return obsbackend.Result{}, nil
}

func jn(s string) json.Number { return json.Number(s) }

func logsAnswer() scriptedAnswer {
	row := func(ts, svc, body, trace string, h int) []any {
		return []any{ts, "ERROR", jn("17"), svc, body, trace, "", map[string]any{"http.route": "/v1/jobs"}, map[string]any{}, jn(fmt.Sprint(h))}
	}
	return scriptedAnswer{"otel_logs", obsbackend.Result{Rows: [][]any{
		row("2026-10-22 11:59:50.000000000", "api", "connection refused", "0af7651916cd43dd8448eb211c80319c", 2),
		row("2026-10-22 11:59:40.000000000", "worker", "retrying", "", 1),
	}}}
}

func TestObsLogsJSONGolden(t *testing.T) {
	sr := &scriptedRunner{answers: []scriptedAnswer{logsAnswer()}}
	out, _, err := obsRun(t, devFacts(), sr, "logs", "dev", "service:api", "--json", "--limit", "10")
	if err != nil {
		t.Fatal(err)
	}
	obsGolden(t, "logs.json", out)
	doc := decode(t, out)
	for k, want := range map[string]any{"schema": "forge.obs.logs/v1", "ok": true, "exit_code": float64(0), "query_mode": "structured"} {
		if doc[k] != want {
			t.Errorf("%s = %v, want %v", k, doc[k], want)
		}
	}
	if sc := doc["scope"].(map[string]any); sc["env"] != "dev" || sc["backend"] != "local" {
		t.Errorf("scope = %v", sc)
	}
	if sug := fmt.Sprint(doc["suggested"]); !strings.Contains(sug, "forge obs trace 0af7651916cd43dd8448eb211c80319c dev") {
		t.Errorf("suggested = %s", sug)
	}
}

func TestObsLogsHumanAndPositionalDisambiguation(t *testing.T) {
	sr := &scriptedRunner{answers: []scriptedAnswer{logsAnswer()}}
	out, _, err := obsRun(t, devFacts(), sr, "logs", "service:api AND body:refused")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "connection refused") || !strings.Contains(out, "TIME") {
		t.Errorf("human output:\n%s", out)
	}
	if len(sr.ran) != 1 || !strings.Contains(sr.ran[0], "default.otel_logs") {
		t.Errorf("a lone query positional is the query, env defaults to dev: %v", sr.ran)
	}
	if looksLikeQuery("staging") || looksLikeQuery("prod-eu_1") || !looksLikeQuery("service:api") || !looksLikeQuery("a b") || !looksLikeQuery(`"x"`) {
		t.Error("env-vs-query disambiguation")
	}
}

func TestObsExplainPrintsSQLAndRunsNothing(t *testing.T) {
	sr := &scriptedRunner{}
	out, _, err := obsRun(t, devFacts(), sr, "endpoints", "dev", "--explain")
	if err != nil {
		t.Fatal(err)
	}
	if len(sr.ran) != 0 {
		t.Fatalf("--explain ran %d statements", len(sr.ran))
	}
	for _, want := range []string{"-- endpoints", "FROM default.otel_traces", "--param_p1="} {
		if !strings.Contains(out, want) {
			t.Errorf("explain output lacks %q:\n%s", want, out)
		}
	}
	out2, _, err := obsRun(t, devFacts(), sr, "traces", "dev", "--print-sql", "--json")
	if err != nil {
		t.Fatal(err)
	}
	st := decode(t, out2)["statements"].([]any)
	if len(st) == 0 || st[0].(map[string]any)["sql"] == "" || st[0].(map[string]any)["params"] == nil {
		t.Errorf("--print-sql is --explain; the JSON carries statements with params: %v", st)
	}
}

func TestObsFieldsProjectionAndRejection(t *testing.T) {
	sr := &scriptedRunner{answers: []scriptedAnswer{logsAnswer()}}
	out, _, err := obsRun(t, devFacts(), sr, "logs", "dev", "--json", "--fields", "logs.service,scope.env")
	if err != nil {
		t.Fatal(err)
	}
	obsGolden(t, "logs.fields.json", out)
	doc := decode(t, out)
	if _, ok := doc["suggested"]; ok {
		t.Error("--fields drops everything not selected, suggested included")
	}
	for _, k := range []string{"schema", "ok", "exit_code", "truncated"} {
		if _, ok := doc[k]; !ok {
			t.Errorf("the head and page are always kept; %s is missing", k)
		}
	}
	_, _, err = obsRun(t, devFacts(), sr, "logs", "dev", "--json", "--fields", "logs.srvice")
	if err == nil || !strings.Contains(err.Error(), "logs.service") {
		t.Errorf("a typo lists the real fields: %v", err)
	}
}

func TestObsTelemetryOffExits2WithTheRemedyInJSON(t *testing.T) {
	out, _, err := obsRun(t, obsbackend.Facts{Env: "staging"}, &scriptedRunner{}, "logs", "staging", "--json")
	if exitCodeForError(err) != 2 {
		t.Fatalf("exit code = %d (%v), want 2", exitCodeForError(err), err)
	}
	obsGolden(t, "logs.off.json", out)
	doc := decode(t, out)
	if doc["ok"] != false || doc["exit_code"] != float64(2) || !strings.Contains(doc["error"].(string), "telemetry is off for staging") {
		t.Errorf("failure document = %v", doc)
	}
}

func TestObsStoppedClickStackExits2NamingForgeEnvUp(t *testing.T) {
	down := obsbackend.DockerRunner{Env: "dev", Exec: func(context.Context, string, []string) ([]byte, error) {
		return nil, errors.New(`service "clickstack" is not running`)
	}}
	out, _, err := obsRun(t, devFacts(), down, "traces", "dev", "--json")
	if exitCodeForError(err) != 2 {
		t.Fatalf("exit code = %d, want 2: %v", exitCodeForError(err), err)
	}
	doc := decode(t, out)
	if sug := fmt.Sprint(doc["suggested"]); !strings.Contains(sug, "forge env up dev") {
		t.Errorf("suggested = %s", sug)
	}
	if doc["exit_code"] != float64(2) {
		t.Errorf("document and process disagree: %v", doc["exit_code"])
	}
}

// A cloud-resolved env reads through the control plane: the verb builds the
// cloud backend, and the document names it and the env id.
func TestObsCloudEnvReadsTheControlPlane(t *testing.T) {
	f := obsbackend.Facts{Env: "prod", Declared: true, Enabled: true, HasHosted: true, ControlPlane: "https://cp"}
	fc := &fakeCloudCaller{replies: map[string]string{
		"controlplane.v1.ObservabilityService/SearchLogs": `{"logs":[{"timestamp":"2026-10-22T11:59:00Z","severityText":"ERROR","severityNumber":17,"service":"api","body":"boom","traceId":"0af7651916cd43dd8448eb211c80319c"}]}`,
	}}
	old := obsDepsOverride
	t.Cleanup(func() { obsDepsOverride = old })
	var out, errb bytes.Buffer
	obsDepsOverride = &obsDeps{
		Facts: func(context.Context, io.Writer, string) (obsbackend.Facts, error) { return f, nil },
		Cloud: func(_ context.Context, env string) (obsbackend.Cloud, error) {
			return obsbackend.Cloud{Env: env, EnvID: "env-prod", OrgID: "org-1", Caller: fc}, nil
		},
		Now: func() time.Time { return obsNow },
	}
	cmd := newTestRootCmd(t)
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	cmd.SetArgs([]string{"obs", "logs", "prod", "--json"})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("%v\n%s", err, errb.String())
	}
	doc := decode(t, out.String())
	scope, _ := doc["scope"].(map[string]any)
	if scope["backend"] != "cloud" || scope["env_id"] != "env-prod" {
		t.Errorf("scope %v", scope)
	}
	if !strings.Contains(out.String(), "boom") {
		t.Errorf("the record is missing:\n%s", out.String())
	}
	if len(fc.calls) != 1 || !strings.Contains(fc.calls[0].body, `"envIds":["env-prod"]`) || !strings.Contains(fc.calls[0].body, `"orgId":"org-1"`) {
		t.Errorf("request not bound to the env: %+v", fc.calls)
	}
}

// A cloud env whose control plane cannot be reached is "could not look".
func TestObsCloudUnreachableIsUndetermined(t *testing.T) {
	f := obsbackend.Facts{Env: "prod", Declared: true, Enabled: true, HasHosted: true, ControlPlane: "https://cp"}
	old := obsDepsOverride
	t.Cleanup(func() { obsDepsOverride = old })
	obsDepsOverride = &obsDeps{
		Facts: func(context.Context, io.Writer, string) (obsbackend.Facts, error) { return f, nil },
		Cloud: func(context.Context, string) (obsbackend.Cloud, error) {
			return obsbackend.Cloud{}, obsbackend.Undetermined("env prod: cannot reach its control plane")
		},
		Now: func() time.Time { return obsNow },
	}
	cmd := newTestRootCmd(t)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"obs", "traces", "prod"})
	if err := cmd.ExecuteContext(context.Background()); exitCodeForError(err) != 2 {
		t.Errorf("err = %v, want exit 2", err)
	}
}

// fakeCloudCaller answers procedures with canned JSON and records requests.
// It is safe for concurrent use.
type fakeCloudCaller struct {
	mu      sync.Mutex
	replies map[string]string
	errs    map[string]error
	calls   []fakeCloudCall
}

type fakeCloudCall struct{ procedure, body string }

func (f *fakeCloudCaller) Call(_ context.Context, procedure string, req, out any) error {
	b, _ := json.Marshal(req)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fakeCloudCall{procedure, string(b)})
	if err := f.errs[procedure]; err != nil {
		return err
	}
	reply, ok := f.replies[procedure]
	if !ok {
		return fmt.Errorf("unexpected call %s", procedure)
	}
	return json.Unmarshal([]byte(reply), out)
}

func TestObsQueryRefusesWritesAndCountsRawQueries(t *testing.T) {
	t.Setenv("FORGE_OBS_DEBUG", "1")
	sr := &scriptedRunner{}
	out, errOut, err := obsRun(t, devFacts(), sr, "query", "dev", "--sql", "drop table otel_logs", "--json")
	if exitCodeForError(err) != 1 {
		t.Fatalf("exit = %d, want 1: %v", exitCodeForError(err), err)
	}
	if len(sr.ran) != 0 {
		t.Error("a refused statement reached ClickHouse")
	}
	doc := decode(t, out)
	if doc["ok"] != false || !strings.Contains(doc["error"].(string), "must start with SELECT") {
		t.Errorf("guard message missing: %v", doc)
	}
	if !strings.Contains(errOut, "query_mode=raw") || !strings.Contains(errOut, "shape=") {
		t.Errorf("the raw query's shape is logged to stderr:\n%s", errOut)
	}

	sr = &scriptedRunner{answers: []scriptedAnswer{{"otel_logs", obsbackend.Result{Columns: []obsbackend.Column{{Name: "n", Type: "UInt64"}}, Rows: [][]any{{jn("7")}}}}}}
	out, errOut, err = obsRun(t, devFacts(), sr, "query", "dev", "--sql", "SELECT count() AS n FROM otel_logs WHERE ServiceName = 'checkout-secret-id-77'", "--json")
	if err != nil {
		t.Fatal(err)
	}
	obsGolden(t, "query.json", out)
	if doc := decode(t, out); doc["query_mode"] != "raw" {
		t.Errorf("query_mode = %v", doc["query_mode"])
	}
	if strings.Contains(errOut, "checkout-secret-id-77") || !strings.Contains(errOut, "ServiceName = ?") {
		t.Errorf("the shape log carries no literal:\n%s", errOut)
	}
}

func TestObsRawQueryLogIsSilentByDefault(t *testing.T) {
	t.Setenv("FORGE_OBS_DEBUG", "")
	sr := &scriptedRunner{answers: []scriptedAnswer{{"otel_logs", obsbackend.Result{}}}}
	_, errOut, err := obsRun(t, devFacts(), sr, "query", "dev", "--sql", "SELECT 1 FROM otel_logs", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if errOut != "" {
		t.Errorf("stderr must be silent unless FORGE_OBS_DEBUG is set: %q", errOut)
	}
}

func TestObsQueryNeedsExactlyOneSource(t *testing.T) {
	for _, args := range [][]string{{"query", "dev"}, {"query", "dev", "--sql", "SELECT 1", "--panel", "p.json"}} {
		if _, _, err := obsRun(t, devFacts(), &scriptedRunner{}, args...); exitCodeForError(err) != 1 {
			t.Errorf("%v: err = %v", args, err)
		}
	}
}

func TestObsStructuredQueryModeAndRawAreDistinguishedInEveryDocument(t *testing.T) {
	sr := &scriptedRunner{}
	for _, v := range []string{"logs", "traces", "endpoints"} {
		out, _, err := obsRun(t, devFacts(), sr, v, "dev", "--json")
		if err != nil {
			t.Fatal(err)
		}
		if decode(t, out)["query_mode"] != "structured" {
			t.Errorf("%s: query_mode", v)
		}
	}
}

func TestResolveRange(t *testing.T) {
	r, err := resolveRange(obsNow, time.Hour, "", "")
	if err != nil || !r.To.Equal(obsNow) || !r.From.Equal(obsNow.Add(-time.Hour)) {
		t.Fatalf("%+v %v", r, err)
	}
	r, err = resolveRange(obsNow, time.Hour, "2026-10-22T10:00:00Z", "2026-10-22T10:30:00Z")
	if err != nil || r.To.Sub(r.From) != 30*time.Minute {
		t.Fatalf("%+v %v", r, err)
	}
	for _, bad := range [][3]string{{"yesterday", "", ""}, {"", "x", ""}, {"2026-10-22T13:00:00Z", "", ""}} {
		if _, err := resolveRange(obsNow, time.Hour, bad[0], bad[1]); err == nil || exitCodeForError(err) != 1 {
			t.Errorf("%v must be refused with exit 1: %v", bad, err)
		}
	}
	if _, err := resolveRange(obsNow, 0, "", ""); err == nil {
		t.Error("--since 0")
	}
}

func TestParseParams(t *testing.T) {
	got, err := parseParams([]string{"threshold=500", "name=a=b"})
	if err != nil || got["threshold"] != "500" || got["name"] != "a=b" {
		t.Errorf("%v %v", got, err)
	}
	for _, bad := range []string{"novalue", "=x", "bad name=1", "x;drop=1"} {
		if _, err := parseParams([]string{bad}); err == nil {
			t.Errorf("%q must be refused", bad)
		}
	}
}

func TestObsGroupHasNoObserveAlias(t *testing.T) {
	root := newTestRootCmd(t)
	for _, c := range root.Commands() {
		if c.Name() == "observe" {
			t.Error("`forge observe` is renamed to `forge obs` with no alias")
		}
		for _, a := range c.Aliases {
			if a == "observe" {
				t.Error("no alias for the old name")
			}
		}
	}
	var obs bool
	for _, c := range root.Commands() {
		if c.Name() == "obs" {
			obs = true
			var verbs []string
			for _, s := range c.Commands() {
				verbs = append(verbs, s.Name())
			}
			for _, want := range []string{"logs", "traces", "trace", "endpoints", "metrics", "query", "profile", "alerts", "dashboards"} {
				if !strings.Contains(" "+strings.Join(verbs, " ")+" ", " "+want+" ") {
					t.Errorf("forge obs lacks %s (has %v)", want, verbs)
				}
			}
		}
	}
	if !obs {
		t.Error("no `forge obs` group")
	}
}

type cancelAfterRunner struct {
	inner  obsbackend.Runner
	calls  int
	cancel context.CancelFunc
	after  int
}

func (c *cancelAfterRunner) Run(ctx context.Context, st obsquery.Stmt, lim obsbackend.Limits) (obsbackend.Result, error) {
	c.calls++
	if c.calls >= c.after {
		c.cancel()
	}
	return c.inner.Run(ctx, st, lim)
}

// --follow --json is NDJSON: one record per line, each row once even when a
// later poll returns it again, and a closing envelope line on ^C that is a
// success (ending a follow is how it is used).
func TestObsLogsFollowIsNDJSONDedupedAndEndsWithTheEnvelope(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	run := &cancelAfterRunner{inner: &scriptedRunner{answers: []scriptedAnswer{logsAnswer()}}, cancel: cancel, after: 3}

	var out bytes.Buffer
	d := obsDeps{Stdout: &out, Stderr: io.Discard, Now: func() time.Time { return obsNow },
		Facts: func(context.Context, io.Writer, string) (obsbackend.Facts, error) { return devFacts(), nil },
		Local: func(env string) obsbackend.Backend { return obsbackend.Local{Env: env, Runner: run} }}
	var o obsLogsOpts
	o.out.JSON, o.out.Limit, o.follow, o.interval, o.since = true, 10, true, time.Millisecond, time.Minute
	if err := runObsLogs(ctx, d, "dev", "", o); err != nil {
		t.Fatalf("ending a follow is not a failure: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("want 2 records (each once, though every poll returned both) and the envelope, got %d lines:\n%s", len(lines), out.String())
	}
	for _, l := range lines {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("not one JSON object per line: %v\n%s", err, l)
		}
	}
	if !strings.Contains(lines[0], `"body":"connection refused"`) || !strings.Contains(lines[2], `"end":true`) || !strings.Contains(lines[2], `"ok":true`) {
		t.Errorf("lines:\n%s", out.String())
	}
}

// traceOnlyBackend answers Trace with one span; nothing else is called.
type traceOnlyBackend struct {
	obsbackend.Backend
	start time.Time
}

func (b traceOnlyBackend) Info() obsbackend.Info {
	return obsbackend.Info{Env: "dev", Backend: obsbackend.BackendLocal}
}

func (b traceOnlyBackend) Trace(_ context.Context, req obsbackend.TraceRequest) (obsbackend.TraceResult, error) {
	return obsbackend.TraceResult{TraceID: req.TraceID, Spans: []obsbackend.Span{{SpanID: "a", Name: "GET /x", Service: "api", Start: b.start, DurationNs: 1e6}}}, nil
}

// `obs trace` stamps the release live when the trace started; an unreadable
// ledger warns and leaves the field out, never failing the verb.
func TestObsTraceStampsTheLiveRelease(t *testing.T) {
	start := obsNow.Add(-10 * time.Minute)
	run := func(release func(context.Context, string, time.Time) (*liveRelease, error)) (string, string, error) {
		var out, errb bytes.Buffer
		old := obsDepsOverride
		t.Cleanup(func() { obsDepsOverride = old })
		obsDepsOverride = &obsDeps{
			Facts:   func(context.Context, io.Writer, string) (obsbackend.Facts, error) { return devFacts(), nil },
			Local:   func(string) obsbackend.Backend { return traceOnlyBackend{start: start} },
			Now:     func() time.Time { return obsNow },
			Release: release,
		}
		cmd := newTestRootCmd(t)
		cmd.SetOut(&out)
		cmd.SetErr(&errb)
		cmd.SetArgs([]string{"obs", "trace", "0af7651916cd43dd8448eb211c80319c", "dev", "--json"})
		err := cmd.ExecuteContext(context.Background())
		return out.String(), errb.String(), err
	}
	var askedAt time.Time
	out, _, err := run(func(_ context.Context, env string, at time.Time) (*liveRelease, error) {
		askedAt = at
		return &liveRelease{Version: "v1.4.2", PromotionID: "p-9", PromotedAt: at.Add(-time.Hour)}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !askedAt.Equal(start) {
		t.Errorf("looked up the release at %v, want the trace start %v", askedAt, start)
	}
	rel, _ := decode(t, out)["release"].(map[string]any)
	if rel["version"] != "v1.4.2" || rel["promotion_id"] != "p-9" {
		t.Errorf("release %v", rel)
	}

	out, errOut, err := run(func(context.Context, string, time.Time) (*liveRelease, error) {
		return nil, errors.New("ledger unreadable")
	})
	if err != nil {
		t.Fatalf("an unreadable ledger failed the verb: %v", err)
	}
	if _, ok := decode(t, out)["release"]; ok || !strings.Contains(errOut, "release live in dev is unknown") {
		t.Errorf("stdout %s stderr %s", out, errOut)
	}
}
