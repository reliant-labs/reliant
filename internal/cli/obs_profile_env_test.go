package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/obsbackend"
	"github.com/reliant-labs/forge/pkg/dashspec"
	"github.com/reliant-labs/forge/pkg/profilequery"
)

var profileEnvNow = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

// flamegraphCaller answers GetFlamegraph as a control plane does: the stacks
// as speedscope JSON in a string, the total as a JSON string. echoEnv answers
// as a control plane that applies the env filter.
type flamegraphCaller struct {
	stacks  []profilequery.Stack
	echoEnv bool
	reqs    []map[string]any
}

func (f *flamegraphCaller) Call(_ context.Context, procedure string, req, out any) error {
	if procedure != "controlplane.v1.ObservabilityService/GetFlamegraph" {
		return errors.New("unexpected procedure " + procedure)
	}
	raw, _ := json.Marshal(req)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	f.reqs = append(f.reqs, m)
	var b bytes.Buffer
	p := profilequery.Profile{Service: "api", SampleType: "cpu", Unit: "nanoseconds", Stacks: f.stacks}
	if err := p.Speedscope(&b); err != nil {
		return err
	}
	reply := map[string]any{"speedscopeJson": b.String(), "totalValue": "500", "unit": "nanoseconds", "stackCount": len(f.stacks),
		"availableProfileTypes": []string{"cpu", "inuse_space"}}
	if f.echoEnv {
		reply["env"] = m["env"]
	}
	rb, _ := json.Marshal(reply)
	return json.Unmarshal(rb, out)
}

func profileEnvDeps(hosted bool, cp obsbackend.Caller) obsDeps {
	return obsDeps{
		Facts: func(_ context.Context, _ io.Writer, env string) (obsbackend.Facts, error) {
			if hosted {
				return obsbackend.Facts{Env: env, HasHosted: true, ControlPlane: "https://cp.example"}, nil
			}
			return obsbackend.Facts{Env: env, Declared: true, Enabled: true, HasLocal: true}, nil
		},
		Local: func(env string) obsbackend.Backend { return obsbackend.Local{Env: env} },
		Cloud: func(_ context.Context, env string) (obsbackend.Cloud, error) {
			return obsbackend.Cloud{Env: env, EnvID: "env-" + env, OrgID: "org-1", Caller: cp}, nil
		},
		Now: func() time.Time { return profileEnvNow },
	}
}

func noLocalRunner(t *testing.T) func(string) profileRunner {
	return func(env string) profileRunner {
		t.Errorf("env %s is hosted, but its local ClickStack was read", env)
		return nil
	}
}

// `forge obs profile prod` on a hosted env reads the control plane's
// flamegraph of that env and writes it as the local export would.
func TestObsProfileEnv_HostedReadsTheControlPlane(t *testing.T) {
	cp := &flamegraphCaller{echoEnv: true, stacks: []profilequery.Stack{{Frames: []string{"main.main", "main.work"}, Value: 500}}}
	var out, errb bytes.Buffer
	o := obsProfileOpts{service: "api", sampleType: "cpu", since: time.Hour, format: "folded"}
	if err := runObsProfileEnv(context.Background(), profileEnvDeps(true, cp), &out, &errb, "prod", o, noLocalRunner(t)); err != nil {
		t.Fatal(err)
	}
	if out.String() != "main.main;main.work 500\n" {
		t.Errorf("output = %q", out.String())
	}
	if errb.Len() != 0 {
		t.Errorf("stderr = %q, want nothing: the control plane applied the env", errb.String())
	}
	if len(cp.reqs) != 1 || cp.reqs[0]["service"] != "api" || cp.reqs[0]["profileType"] != "cpu" || cp.reqs[0]["env"] != "prod" {
		t.Fatalf("requests = %v", cp.reqs)
	}
	tr := cp.reqs[0]["scope"].(map[string]any)["timeRange"].(map[string]any)
	if tr["start"] != profileEnvNow.Add(-time.Hour).Format(time.RFC3339) || tr["end"] != profileEnvNow.Format(time.RFC3339) {
		t.Errorf("range = %v", tr)
	}
}

// An older control plane reads the service in every environment and says so
// by not echoing the env: the export still works, with a note. A cut is
// noted too.
func TestObsProfileEnv_HostedNotesWhatTheControlPlaneDidNotNarrow(t *testing.T) {
	cp := &flamegraphCaller{stacks: []profilequery.Stack{{Frames: []string{"main.main"}, Value: 500}}}
	var out, errb bytes.Buffer
	o := obsProfileOpts{service: "api", sampleType: "cpu", since: time.Hour, format: "folded"}
	if err := runObsProfileEnv(context.Background(), profileEnvDeps(true, cp), &out, &errb, "prod", o, noLocalRunner(t)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errb.String(), "every environment") {
		t.Errorf("stderr = %q, want a note that the profile spans every environment", errb.String())
	}
}

// What a hosted env cannot serve is refused before anything is read.
func TestObsProfileEnv_Refusals(t *testing.T) {
	cp := &flamegraphCaller{}
	for name, o := range map[string]obsProfileOpts{
		"span":         {service: "api", sampleType: "cpu", since: time.Hour, format: "folded", trace: "0af7651916cd43dd8448eb211c80319c", span: "b7ad6b7169203331"},
		"env disagree": {service: "api", sampleType: "cpu", since: time.Hour, format: "folded", env: "staging"},
		"bad format":   {service: "api", sampleType: "cpu", since: time.Hour, format: "svg"},
	} {
		err := runObsProfileEnv(context.Background(), profileEnvDeps(true, cp), &bytes.Buffer{}, &bytes.Buffer{}, "prod", o, noLocalRunner(t))
		if err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	if len(cp.reqs) != 0 {
		t.Errorf("a refused export read the control plane: %v", cp.reqs)
	}
}

// A local env reads its own ClickStack, filtered to its samples, through the
// statements the export with no [env] runs.
func TestObsProfileEnv_LocalReadsTheEnvsClickStack(t *testing.T) {
	var ranOn string
	var params []map[string]string
	local := func(env string) profileRunner {
		ranOn = env
		return func(_ context.Context, sql string, p map[string]string) ([]byte, error) {
			params = append(params, p)
			return []byte(cliRows), nil
		}
	}
	var out bytes.Buffer
	o := obsProfileOpts{service: "api", sampleType: "cpu", since: time.Hour, format: "folded"}
	if err := runObsProfileEnv(context.Background(), profileEnvDeps(false, nil), &out, &bytes.Buffer{}, "staging", o, local); err != nil {
		t.Fatal(err)
	}
	if ranOn != "staging" || len(params) != 1 || params[0]["env"] != "staging" || out.String() != "main.main;main.work 500\n" {
		t.Errorf("ran on %q with %v, output %q", ranOn, params, out.String())
	}
}

// --sql with an [env] prints a statement `forge obs query` runs against it,
// and the command that does, without reading anything.
func TestObsProfileEnv_SQLIsForObsQuery(t *testing.T) {
	cp := &flamegraphCaller{}
	var out, errb bytes.Buffer
	o := obsProfileOpts{service: "api", sampleType: "cpu", since: 6 * time.Hour, format: "pprof", printSQL: true}
	if err := runObsProfileEnv(context.Background(), profileEnvDeps(true, cp), &out, &errb, "prod", o, noLocalRunner(t)); err != nil {
		t.Fatal(err)
	}
	if len(cp.reqs) != 0 {
		t.Errorf("--sql read the control plane: %v", cp.reqs)
	}
	if !strings.Contains(out.String(), "FROM profile_samples_1m") || strings.Contains(out.String(), "default.") {
		t.Errorf("statement = %s", out.String())
	}
	if want := "forge obs query prod --sql-file profile.sql --service api --since 6h0m0s"; !strings.Contains(errb.String(), want) {
		t.Errorf("stderr = %q, want %q", errb.String(), want)
	}
}

// Without --service, a hosted env's services are listed through obs query's
// path, and the only one is read.
func TestObsProfileCloud_FindsTheOnlyService(t *testing.T) {
	var asked string
	b := profileCloudBackend{
		MockBackend: &obsbackend.MockBackend{QueryFunc: func(_ context.Context, req obsbackend.QueryRequest) (obsbackend.QueryResult, error) {
			asked = req.SQL
			return obsbackend.QueryResult{Columns: []obsbackend.Column{{Name: "service"}, {Name: "rows"}}, Rows: [][]any{{"api", 9}}}, nil
		}},
		stacks: []profilequery.Stack{{Frames: []string{"main.main"}, Value: 7}},
	}
	var out bytes.Buffer
	o := obsProfileOpts{sampleType: "cpu", since: time.Hour, format: "folded", queryEnv: "prod", env: "prod"}
	if err := runObsProfileCloud(context.Background(), profileEnvNow, &out, &bytes.Buffer{}, o, &b); err != nil {
		t.Fatal(err)
	}
	if b.got.Service != "api" || out.String() != "main.main 7\n" {
		t.Errorf("read %+v, output %q", b.got, out.String())
	}
	if !strings.Contains(asked, "FROM profile_samples_1m") || dashspec.CheckSQL(asked) != nil {
		t.Errorf("services statement = %s", asked)
	}

	o.listServices = true
	out.Reset()
	if err := runObsProfileCloud(context.Background(), profileEnvNow, &out, &bytes.Buffer{}, o, &b); err != nil {
		t.Fatal(err)
	}
	if out.String() != `{"rows":9,"service":"api"}`+"\n" {
		t.Errorf("--list = %q", out.String())
	}
}

// No stacks: the error says so, and names the sample types the service has
// when the one asked for is not among them.
func TestObsProfileCloud_NothingNamesTheTypes(t *testing.T) {
	b := profileCloudBackend{MockBackend: &obsbackend.MockBackend{}, types: []string{"cpu"}}
	o := obsProfileOpts{service: "api", sampleType: "goroutine", since: time.Hour, format: "folded", queryEnv: "prod", env: "prod"}
	err := runObsProfileCloud(context.Background(), profileEnvNow, &bytes.Buffer{}, &bytes.Buffer{}, o, &b)
	if err == nil || !strings.Contains(err.Error(), "no profile samples") || !strings.Contains(err.Error(), "sample types in the range: cpu") ||
		!strings.Contains(err.Error(), "forge obs profile prod --list") {
		t.Errorf("err = %v", err)
	}
}

type profileCloudBackend struct {
	*obsbackend.MockBackend
	stacks []profilequery.Stack
	types  []string
	got    obsbackend.FlamegraphRequest
}

func (b *profileCloudBackend) Flamegraph(_ context.Context, req obsbackend.FlamegraphRequest) (obsbackend.FlamegraphResult, error) {
	b.got = req
	return obsbackend.FlamegraphResult{Profile: profilequery.Profile{Service: req.Service, SampleType: req.SampleType, Unit: "nanoseconds", Stacks: b.stacks},
		AvailableTypes: b.types, EnvApplied: true}, nil
}

// Every statement the export runs rewrites into one obs query accepts: the
// guard passes it, the tables are unqualified, and every parameter left is
// one obs query binds. Literals are checked before they are written.
func TestObsQueryProfileStatement(t *testing.T) {
	from, to := profileEnvNow.Add(-time.Hour), profileEnvNow
	for name, q := range map[string]profilequery.Query{
		"range":    {Service: "api", SampleType: "cpu", Env: "prod", From: from, To: to},
		"snapshot": {Service: "api", SampleType: "inuse_space", Env: "prod", From: from, To: to},
		"trace":    {SampleType: "cpu", TraceID: "0AF7651916CD43DD8448EB211C80319C", SpanID: "b7ad6b7169203331"},
	} {
		sql, err := obsQueryProfileSQL(q)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if err := dashspec.CheckSQL(sql); err != nil {
			t.Errorf("%s: obs query's guard refuses it: %v\n%s", name, err, sql)
		}
		if strings.Contains(sql, "default.") || strings.Contains(sql, "FORMAT") || strings.Contains(sql, "UInt32}") {
			t.Errorf("%s: not rewritten:\n%s", name, sql)
		}
		if !strings.Contains(sql, "'"+q.SampleType+"'") {
			t.Errorf("%s: the sample type is not a literal:\n%s", name, sql)
		}
	}
	sql, _ := obsQueryProfileSQL(profilequery.Query{Service: "api", SampleType: "cpu", From: from, To: to})
	if !strings.Contains(sql, "has({services:Array(String)}, a.ServiceName)") || !strings.Contains(sql, "has({envs:Array(String)}, a.Env)") {
		t.Errorf("range statement does not take the service and env from obs query's scope:\n%s", sql)
	}
	sql, _ = obsQueryProfileSQL(profilequery.Query{SampleType: "cpu", TraceID: "0af7651916cd43dd8448eb211c80319c"})
	if !strings.Contains(sql, "TraceId = '0af7651916cd43dd8448eb211c80319c'") || !strings.Contains(sql, "empty({services:Array(String)})") {
		t.Errorf("trace statement:\n%s", sql)
	}
	for name, q := range map[string]profilequery.Query{
		"type":  {Service: "api", SampleType: "cpu' OR 1=1 --", From: from, To: to},
		"trace": {SampleType: "cpu", TraceID: "not-hex'"},
		"span":  {SampleType: "cpu", TraceID: "0af7651916cd43dd8448eb211c80319c", SpanID: "x'"},
	} {
		if _, err := obsQueryProfileSQL(q); err == nil {
			t.Errorf("%s: an unchecked literal was written", name)
		}
	}
}
