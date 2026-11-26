package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/devtelemetry"
	"github.com/reliant-labs/forge/internal/obsbackend"
)

// Instance ids are the control plane's row ids (UUIDs), never forge's own
// sess-… session id.
const (
	aliceInstance = "0b7c5a52-1111-4a5e-9d7a-6a1f3a2b4c01"
	bobInstance   = "0b7c5a52-2222-4a5e-9d7a-6a1f3a2b4c02"
	carolInstance = "0b7c5a52-3333-4a5e-9d7a-6a1f3a2b4c03"
)

func hostedDevRec() devtelemetry.Record {
	return devtelemetry.Record{Env: "dev", Backend: "cloud", OrgID: "org-1", EnvironmentID: "env-dev",
		InstanceID: aliceInstance, InstanceLabel: "alice/fix-login", InstanceUser: "alice@x"}
}

// machineRecords are what `forge env up` recorded on this machine: this
// checkout's (alice) and two sibling worktrees'.
func machineRecords() []devtelemetry.Record {
	bob := hostedDevRec()
	bob.InstanceID, bob.InstanceLabel, bob.InstanceUser = bobInstance, "bob/main", "bob@x"
	carol := hostedDevRec()
	carol.InstanceID, carol.InstanceLabel, carol.InstanceUser = carolInstance, "carol/feat/x", "carol@x"
	return []devtelemetry.Record{hostedDevRec(), bob, carol}
}

// echoingCaller is a control plane that applies the instance narrowing it is
// sent and acknowledges it in every answer, as a current control plane does.
type echoingCaller struct {
	fakeCloudCaller
	noEcho bool // an older control plane: drops the fields, acknowledges nothing
}

func (e *echoingCaller) Call(ctx context.Context, procedure string, req, out any) error {
	var raw json.RawMessage
	if err := e.fakeCloudCaller.Call(ctx, procedure, req, &raw); err != nil {
		return err
	}
	if !e.noEcho {
		var body struct {
			Scope map[string]any `json:"scope"`
		}
		b, _ := json.Marshal(req)
		_ = json.Unmarshal(b, &body)
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		if m == nil {
			m = map[string]any{}
		}
		m["instanceScope"] = map[string]any{"instanceIds": body.Scope["instanceIds"], "instanceUsers": body.Scope["instanceUsers"]}
		raw, _ = json.Marshal(m)
	}
	return json.Unmarshal(raw, out)
}

// obsRunHostedDev runs `forge obs <args>` against a dev env whose `forge env
// up` recorded the hosted store, with caller as its control plane.
func obsRunHostedDev(t *testing.T, caller obsbackend.Caller, args ...string) (string, error) {
	t.Helper()
	rec := hostedDevRec()
	var out, errb bytes.Buffer
	old := obsDepsOverride
	t.Cleanup(func() { obsDepsOverride = old })
	obsDepsOverride = &obsDeps{
		Facts: func(context.Context, io.Writer, string) (obsbackend.Facts, error) {
			return obsbackend.Facts{Env: "dev", HasLocal: true, ControlPlane: "https://cp.example.com", DevTelemetry: &rec}, nil
		},
		Local: func(env string) obsbackend.Backend { return obsbackend.Local{Env: env, Runner: &scriptedRunner{}} },
		Cloud: func(context.Context, string) (obsbackend.Cloud, error) {
			t.Fatal("a hosted-dev env must not look up a hosted environment")
			return obsbackend.Cloud{}, nil
		},
		HostedDev: func(_ context.Context, env string, r devtelemetry.Record) (obsbackend.Cloud, error) {
			return obsbackend.Cloud{Env: env, EnvID: r.EnvironmentID, OrgID: r.OrgID, Caller: caller}, nil
		},
		InstanceRecords: func(context.Context, string) ([]devtelemetry.Record, error) { return machineRecords(), nil },
		Now:             func() time.Time { return obsNow },
		Release:         func(context.Context, string, time.Time) (*liveRelease, error) { return nil, nil },
	}
	cmd := newTestRootCmd(t)
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	cmd.SetArgs(append([]string{"obs"}, args...))
	err := cmd.ExecuteContext(context.Background())
	return out.String(), err
}

func lastScope(t *testing.T, c *echoingCaller) map[string]any {
	t.Helper()
	if len(c.calls) == 0 {
		t.Fatal("nothing reached the control plane")
	}
	m := decode(t, c.calls[len(c.calls)-1].body)
	s, _ := m["scope"].(map[string]any)
	return s
}

func logsCaller() *echoingCaller {
	return &echoingCaller{fakeCloudCaller: fakeCloudCaller{replies: map[string]string{
		"controlplane.v1.ObservabilityService/SearchLogs": `{"logs":[{"timestamp":"2026-10-22T11:59:00Z","service":"api","body":"a line"}]}`,
	}}}
}

// A hosted-dev env reads THIS checkout's instance by default, bound to the
// record's org and environment, and says so in scope.instance.
func TestObsHostedDevDefaultsToTheRecordedInstance(t *testing.T) {
	c := logsCaller()
	out, err := obsRunHostedDev(t, c, "logs", "dev", "--json")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	scope := lastScope(t, c)
	if scope["orgId"] != "org-1" || jsonList(scope["envIds"]) != "env-dev" || jsonList(scope["instanceIds"]) != aliceInstance {
		t.Errorf("scope sent = %v", scope)
	}
	doc := decode(t, out)
	ds := doc["scope"].(map[string]any)
	in, _ := ds["instance"].(map[string]any)
	if ds["backend"] != "cloud" || in["id"] != aliceInstance || in["label"] != "alice/fix-login" || in["user"] != "alice@x" {
		t.Errorf("document scope = %v", ds)
	}
}

// A control plane that predates instance scopes drops the fields and answers
// for the whole env: the verb refuses (exit 2) and shows none of it. The same
// server still answers --all-instances, which asks for the whole env.
func TestObsHostedDevFailsClosedOnAnOlderControlPlane(t *testing.T) {
	c := logsCaller()
	c.noEcho = true
	out, err := obsRunHostedDev(t, c, "logs", "dev", "--json")
	if exitCodeForError(err) != 2 {
		t.Fatalf("exit = %d, want 2: %v\n%s", exitCodeForError(err), err, out)
	}
	doc := decode(t, out)
	if doc["ok"] != false || strings.Contains(out, "a line") || !strings.Contains(doc["error"].(string), "did not confirm") {
		t.Errorf("an unacknowledged narrowing was shown:\n%s", out)
	}
	for _, args := range [][]string{{"--user", "bob@x"}, {"--instance", "bob/main"}} {
		if _, err := obsRunHostedDev(t, c, append([]string{"logs", "dev", "--json"}, args...)...); exitCodeForError(err) != 2 {
			t.Errorf("%v on an older control plane: exit %d, want 2", args, exitCodeForError(err))
		}
	}

	out, err = obsRunHostedDev(t, c, "logs", "dev", "--json", "--all-instances")
	if err != nil || !strings.Contains(out, "a line") {
		t.Errorf("--all-instances on an older control plane: %v\n%s", err, out)
	}
}

func TestObsHostedDevInstanceFlags(t *testing.T) {
	for name, c := range map[string]struct {
		args      []string
		instances string
		users     string
		docID     string
	}{
		"--all-instances reads the whole env":    {[]string{"--all-instances"}, "", "", ""},
		"an id":                                  {[]string{"--instance", carolInstance}, carolInstance, "", carolInstance},
		"a legacy row id":                        {[]string{"--instance", "sess-0123456789abcdef01234567"}, "sess-0123456789abcdef01234567", "", "sess-0123456789abcdef01234567"},
		"the record's own label":                 {[]string{"--instance", "alice/fix-login"}, aliceInstance, "", aliceInstance},
		"a sibling worktree's label":             {[]string{"--instance", "bob/main"}, bobInstance, "", bobInstance},
		"a bare branch":                          {[]string{"--instance", "main"}, bobInstance, "", bobInstance},
		"a branch with a slash, bare":            {[]string{"--instance", "feat/x"}, carolInstance, "", carolInstance},
		"a branch with a slash, qualified":       {[]string{"--instance", "carol/feat/x"}, carolInstance, "", carolInstance},
		"a user widens the default":              {[]string{"--user", "bob@x"}, "", "bob@x", ""},
		"a user and an instance narrow together": {[]string{"--user", "bob@x", "--instance", bobInstance}, bobInstance, "bob@x", bobInstance},
	} {
		t.Run(name, func(t *testing.T) {
			caller := logsCaller()
			out, err := obsRunHostedDev(t, caller, append([]string{"logs", "dev", "--json"}, c.args...)...)
			if err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			scope := lastScope(t, caller)
			if jsonList(scope["instanceIds"]) != c.instances || jsonList(scope["instanceUsers"]) != c.users {
				t.Errorf("scope sent = %v", scope)
			}
			in, _ := decode(t, out)["scope"].(map[string]any)["instance"].(map[string]any)
			if c.instances == "" && c.users == "" {
				if in != nil {
					t.Errorf("a whole-env read names an instance: %v", in)
				}
				return
			}
			if id, _ := in["id"].(string); id != c.docID {
				t.Errorf("scope.instance = %v, want id %q", in, c.docID)
			}
		})
	}

	for name, args := range map[string][]string{
		"an unknown label": {"--instance", "nope"},
		// The qualifier is the owner: alice has no instance on main, and
		// bob's must not be read as hers.
		"another user's branch under my name": {"--instance", "alice/main"},
		"--all-instances with --instance":     {"--all-instances", "--instance", aliceInstance},
		// forge's own session id is not an instance id; as a label it names nothing.
		"forge's client session id": {"--instance", "sess-0123"},
	} {
		t.Run(name, func(t *testing.T) {
			caller := logsCaller()
			out, err := obsRunHostedDev(t, caller, append([]string{"logs", "dev", "--json"}, args...)...)
			if exitCodeForError(err) != 1 {
				t.Errorf("exit = %d, want 1: %v\n%s", exitCodeForError(err), err, out)
			}
			if len(caller.calls) != 0 {
				t.Errorf("a refused selection reached the control plane: %v", caller.calls)
			}
		})
	}
}

func TestObsInstanceLabelAmbiguity(t *testing.T) {
	dup := hostedDevRec()
	dup.InstanceID, dup.InstanceLabel = carolInstance, "dave/fix-login"
	recs := func(context.Context, string) ([]devtelemetry.Record, error) {
		return []devtelemetry.Record{hostedDevRec(), dup}, nil
	}
	_, err := instanceByLabel(context.Background(), "dev", "fix-login", recs)
	if err == nil || !strings.Contains(err.Error(), aliceInstance) || !strings.Contains(err.Error(), carolInstance) {
		t.Errorf("two users' fix-login: %v; want both ids listed", err)
	}
	if r, err := instanceByLabel(context.Background(), "dev", "dave/fix-login", recs); err != nil || r.InstanceID != carolInstance {
		t.Errorf("a qualified label picks its owner's: %+v, %v", r, err)
	}
}

// A non-default narrowing rides along on every suggested telemetry read,
// spelt the way the destination verb takes it.
func TestObsInstanceCarriesIntoSuggestions(t *testing.T) {
	rec := hostedDevRec()
	cmds := []string{
		"forge obs traces dev --errors", "forge obs sessions dev", "forge obs session s1 dev",
		"forge obs changes dev", "forge env releases show dev v1", "forge obs query --help",
	}
	unchanged := cmds[3:]
	for name, c := range map[string]struct {
		f    obsInstanceFlags
		want []string
	}{
		"an id": {obsInstanceFlags{instance: carolInstance}, append([]string{
			"forge obs traces dev --errors --instance " + carolInstance, "forge obs sessions dev --instance " + carolInstance,
			"forge obs session s1 dev --instance " + carolInstance,
		}, unchanged...)},
		// From sessions (--instance-user) and from any other verb (--user),
		// the user lands as each destination spells it.
		"a user, from sessions": {obsInstanceFlags{user: "bob@x", userFlag: "instance-user"}, append([]string{
			"forge obs traces dev --errors --user bob@x", "forge obs sessions dev --instance-user bob@x",
			"forge obs session s1 dev --user bob@x",
		}, unchanged...)},
		"a user, from logs": {obsInstanceFlags{user: "bob@x", userFlag: "user"}, append([]string{
			"forge obs traces dev --errors --user bob@x", "forge obs sessions dev --instance-user bob@x",
			"forge obs session s1 dev --user bob@x",
		}, unchanged...)},
		"--all-instances": {obsInstanceFlags{all: true}, append([]string{
			"forge obs traces dev --errors --all-instances", "forge obs sessions dev --all-instances",
			"forge obs session s1 dev --all-instances",
		}, unchanged...)},
		"the default carries nothing": {obsInstanceFlags{}, cmds},
	} {
		f := c.f
		if _, err := f.resolve(context.Background(), "dev", &rec, nil); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := f.carrySuggested(cmds); strings.Join(got, "\n") != strings.Join(c.want, "\n") {
			t.Errorf("%s: carried\n%s\nwant\n%s", name, strings.Join(got, "\n"), strings.Join(c.want, "\n"))
		}
	}
}

// Following a carried suggestion parses, and the instance user lands on each
// verb's instance flag, not on sessions' app-user --user.
func TestObsCarriedSuggestionsParse(t *testing.T) {
	rec := hostedDevRec()
	for _, from := range []string{"user", "instance-user"} {
		f := obsInstanceFlags{user: "bob@x", userFlag: from}
		if _, err := f.resolve(context.Background(), "dev", &rec, nil); err != nil {
			t.Fatal(err)
		}
		for _, cmd := range f.carrySuggested([]string{"forge obs sessions dev", "forge obs session s1 dev", "forge obs logs dev"}) {
			args := strings.Fields(strings.TrimPrefix(cmd, "forge "))
			root := newTestRootCmd(t)
			sub, rest, err := root.Find(args)
			if err != nil {
				t.Fatalf("%s: %v", cmd, err)
			}
			if err := sub.ParseFlags(rest); err != nil {
				t.Errorf("%s (from --%s): %v", cmd, from, err)
				continue
			}
			want := "user"
			if sub.Name() == "sessions" {
				want = "instance-user"
			}
			if v, _ := sub.Flags().GetString(want); v != "bob@x" {
				t.Errorf("%s: --%s = %q; the instance user did not land on the instance flag", cmd, want, v)
			}
		}
	}
}

// `forge obs status dev` for a hosted-dev instance reads its freshness (the
// newest log and span) from the control plane, narrowed to the instance, and
// names the instance in its header.
func TestObsStatusHostedDevShowsFreshness(t *testing.T) {
	c := &echoingCaller{fakeCloudCaller: fakeCloudCaller{replies: map[string]string{
		procSearchLogs: `{"logs":[{"timestamp":"2026-10-22T11:59:00Z","severityText":"INFO","service":"api","body":"ok"}]}`,
		procTraces:     `{"traces":[{"traceId":"t1","startTime":"2026-10-22T11:59:30Z","durationMs":12}]}`,
	}}}
	out, _ := obsRunHostedDev(t, c, "status", "dev", "--json")
	doc := decode(t, out)
	tel, _ := doc["telemetry"].(map[string]any)
	if tel["last_log_at"] != "2026-10-22T11:59:00Z" || tel["last_span_at"] != "2026-10-22T11:59:30Z" {
		t.Errorf("telemetry = %v\n%s", tel, out)
	}
	for _, proc := range []string{procSearchLogs, procTraces} {
		call := callTo(c.calls, proc)
		if call == nil || !strings.Contains(call.body, `"instanceIds":["`+aliceInstance+`"]`) {
			t.Errorf("%s is not narrowed to the instance: %v", proc, call)
		}
	}
	if in, _ := doc["scope"].(map[string]any)["instance"].(map[string]any); in["id"] != aliceInstance {
		t.Errorf("scope.instance = %v", in)
	}

	text, _ := obsRunHostedDev(t, c, "status", "dev")
	if !strings.Contains(text, "instance alice/fix-login ("+aliceInstance+")") || !strings.Contains(text, "dev (cloud backend)") {
		t.Errorf("status text:\n%s", text)
	}
}

func jsonList(v any) string {
	l, _ := v.([]any)
	s := make([]string, len(l))
	for i, x := range l {
		s[i], _ = x.(string)
	}
	return strings.Join(s, ",")
}

// Alerts and telemetry deliveries are the env's: an instance-scoped status
// never asks for them, and never shows another stack's firing alert or failed
// delivery under scope.instance. It names them not_evaluated instead.
func TestObsStatusHostedDevLeavesEnvWideSectionsOut(t *testing.T) {
	c := &echoingCaller{fakeCloudCaller: fakeCloudCaller{replies: map[string]string{
		procSearchLogs: `{"logs":[{"timestamp":"2026-10-22T11:59:00Z","severityText":"INFO","service":"api","body":"ok"}]}`,
		procTraces:     `{"traces":[{"traceId":"t1","startTime":"2026-10-22T11:59:30Z","durationMs":12}]}`,
		procAlerts:     `{"alerts":[{"envId":"env-dev","rule":"BobsStackIsDown","state":"firing","activeAt":"2026-10-22T11:40:00Z"}]}`,
		procDeliveries: `{"enabled":true,"deliveries":[{"environmentId":"env-dev","cluster":"bobs","name":"t","state":"DELIVERY_STATE_FAILING"}]}`,
	}}}
	out, _ := obsRunHostedDev(t, c, "status", "dev", "--json")
	doc := decode(t, out)
	if strings.Contains(out, "BobsStackIsDown") || strings.Contains(out, "DELIVERY_STATE_FAILING") || doc["alerts"] != nil {
		t.Errorf("env-wide evidence shown under the instance's scope:\n%s", out)
	}
	if callTo(c.calls, procAlerts) != nil || callTo(c.calls, procDeliveries) != nil {
		t.Errorf("an instance-scoped status read env-wide sections: %v", c.calls)
	}
	ne := jsonList(doc["not_evaluated"])
	if !strings.Contains(ne, statusAlerts) || !strings.Contains(ne, statusDeliveries) {
		t.Errorf("not_evaluated = %q; want alerts and telemetry.deliveries named", ne)
	}
	text, _ := obsRunHostedDev(t, c, "status", "dev")
	if !strings.Contains(text, "not evaluated for an instance") {
		t.Errorf("status text does not say why:\n%s", text)
	}

	// --all-instances reads the whole env, alerts included.
	c.calls = nil
	out, _ = obsRunHostedDev(t, c, "status", "dev", "--json", "--all-instances")
	if !strings.Contains(out, "BobsStackIsDown") {
		t.Errorf("--all-instances left the env's alerts out:\n%s", out)
	}
}

// Raw SQL is its author's: an instance-scoped `obs query --sql` is refused
// before anything runs, naming --all-instances, rather than returning the
// whole env's rows under scope.instance.
func TestObsQuerySQLRefusedUnderAnInstance(t *testing.T) {
	const procQueryPanel = "controlplane.v1.ObservabilityService/QueryPanel"
	c := &echoingCaller{fakeCloudCaller: fakeCloudCaller{replies: map[string]string{procQueryPanel: `{}`}}}
	out, err := obsRunHostedDev(t, c, "query", "dev", "--sql", "SELECT count() AS n FROM otel_logs", "--json")
	if exitCodeForError(err) != 1 {
		t.Fatalf("exit = %d, want 1: %v\n%s", exitCodeForError(err), err, out)
	}
	if msg, _ := decode(t, out)["error"].(string); !strings.Contains(msg, "--all-instances") {
		t.Errorf("the refusal does not name --all-instances: %q", msg)
	}
	if len(c.calls) != 0 {
		t.Errorf("narrowed raw SQL reached the control plane: %v", c.calls)
	}
	_, _ = obsRunHostedDev(t, c, "query", "dev", "--sql", "SELECT count() AS n FROM otel_logs", "--json", "--all-instances")
	if callTo(c.calls, procQueryPanel) == nil {
		t.Error("--all-instances did not run the statement over the whole env")
	}
}
