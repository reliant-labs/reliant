package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/clickstack"
	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/devtelemetry"
	"github.com/reliant-labs/forge/pkg/release"
)

// fakeMintCaller answers MintLocalTelemetryToken (and nothing else) with a
// grant numbered by call, or with err.
type fakeMintCaller struct {
	mu    sync.Mutex
	err   error
	calls []map[string]any
	now   time.Time
	// clock, when set, is "now" for the grant's expiry (a fake clock).
	clock func() time.Time
	// minted, when set, receives one value per call (after its answer).
	minted chan struct{}
	// during, when set, runs inside each call before it answers.
	during func()
}

func (f *fakeMintCaller) setErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func (f *fakeMintCaller) Call(_ context.Context, procedure string, req, out any) error {
	if procedure != procMintLocalTelemetryToken {
		return fmt.Errorf("unexpected call %s", procedure)
	}
	b, _ := json.Marshal(req)
	var body map[string]any
	_ = json.Unmarshal(b, &body)
	f.mu.Lock()
	f.calls = append(f.calls, body)
	n := len(f.calls)
	err := f.err
	now := f.now
	if f.clock != nil {
		now = f.clock()
	}
	f.mu.Unlock()
	if f.minted != nil {
		defer func() { f.minted <- struct{}{} }()
	}
	if f.during != nil {
		f.during()
	}
	if err != nil {
		return err
	}
	reply := fmt.Sprintf(`{"processToken":"proc-%d","browserToken":"browser-%d","expiresAt":%q,
		"endpoint":"https://otlp.example.com/","orgId":"org-1","environmentId":"env-1",
		"instance":{"id":"inst-row-42","label":"alice/fix-login","user":"alice@x"},
		"retention":{"tracesDays":3,"metricsDays":3,"logsDays":3,"browserDays":3}}`,
		n, n, now.Add(time.Hour).Format(time.RFC3339))
	return json.Unmarshal([]byte(reply), out)
}

func (f *fakeMintCaller) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

var devTelNow = time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)

func TestResolveDevTelemetryMode(t *testing.T) {
	cases := []struct {
		flag, env, declared string
		want, source        string
		wantErr             bool
	}{
		{"", "", "", "auto", "default", false},
		{"", "", "hosted", "hosted", "observability.dev_telemetry", false},
		{"", "auto", "hosted", "auto", devTelemetryEnvVar, false},
		{"hosted", "auto", "", "hosted", "--telemetry", false},
		// "local" anywhere is the opt-out, and wins over a higher source.
		{"hosted", "", "local", "local", "observability.dev_telemetry", false},
		{"", "local", "hosted", "local", devTelemetryEnvVar, false},
		{"local", "hosted", "hosted", "local", "--telemetry", false},
		{"cloud", "", "", "", "", true},
		{"", "", "remote", "", "", true},
	}
	for _, c := range cases {
		got, err := resolveDevTelemetryMode(c.flag, c.env, c.declared)
		if (err != nil) != c.wantErr {
			t.Errorf("(%q,%q,%q): err = %v, want error %v", c.flag, c.env, c.declared, err, c.wantErr)
			continue
		}
		if !c.wantErr && (got.Value != c.want || got.Source != c.source) {
			t.Errorf("(%q,%q,%q) = %+v, want %s from %s", c.flag, c.env, c.declared, got, c.want, c.source)
		}
	}
}

func liveFacts(mode string, caller cloudCaller, reporter sessionReporter) devTelemetryFacts {
	var log strings.Builder
	return devTelemetryFacts{
		Env:          "dev",
		Mode:         devTelemetryMode{Value: mode, Source: "test"},
		TelemetryOn:  true,
		ControlPlane: true,
		Session:      testUpSession(reporter, &log, time.Hour),
		Client:       caller,
		Project:      "demo",
	}
}

func TestDecideDevTelemetryMatrix(t *testing.T) {
	cerr := func(code, reason, msg string) error {
		return &cloud.Error{Code: code, Reason: reason, Message: msg, Procedure: procMintLocalTelemetryToken}
	}
	type mut func(*devTelemetryFacts, *fakeMintCaller, *fakeSessionReporter)
	cases := []struct {
		name   string
		mutate mut
		// wantHosted under auto; under hosted a non-hosted outcome is an
		// error and under local nothing is ever asked.
		wantHosted bool
		reason     string
	}{
		{"success", nil, true, ""},
		{"telemetry off", func(f *devTelemetryFacts, _ *fakeMintCaller, _ *fakeSessionReporter) { f.TelemetryOn = false }, false, "observability is off"},
		{"no control plane", func(f *devTelemetryFacts, _ *fakeMintCaller, _ *fakeSessionReporter) { f.ControlPlane = false }, false, "no control_plane"},
		{"no credential", func(f *devTelemetryFacts, _ *fakeMintCaller, _ *fakeSessionReporter) {
			f.CredentialErr = errors.New("not signed in\nrun forge login")
		}, false, "no Reliant credential (not signed in)"},
		{"no session", func(f *devTelemetryFacts, _ *fakeMintCaller, _ *fakeSessionReporter) {
			f.Session, f.SessionSkip = nil, "FORGE_LEDGER=machine"
		}, false, "FORGE_LEDGER=machine"},
		{"session report fails", func(_ *devTelemetryFacts, _ *fakeMintCaller, r *fakeSessionReporter) {
			r.err = errors.New("dial tcp: connection refused")
		}, false, "did not take this stack's session"},
		{"unimplemented", func(_ *devTelemetryFacts, m *fakeMintCaller, _ *fakeSessionReporter) {
			m.err = cerr(cloud.CodeUnimplemented, "", "")
		}, false, "predates hosted dev telemetry"},
		{"org switch off", func(_ *devTelemetryFacts, m *fakeMintCaller, _ *fakeSessionReporter) {
			m.err = cerr(cloud.CodeFailedPrecondition, reasonDevTelemetryDisabled, "")
		}, false, reasonDevTelemetryDisabled},
		{"gate excludes org", func(_ *devTelemetryFacts, m *fakeMintCaller, _ *fakeSessionReporter) {
			m.err = cerr(cloud.CodeFailedPrecondition, reasonDevTelemetryNotEnabled, "")
		}, false, reasonDevTelemetryNotEnabled},
		{"instance limit", func(_ *devTelemetryFacts, m *fakeMintCaller, _ *fakeSessionReporter) {
			m.err = cerr(cloud.CodeFailedPrecondition, reasonDevInstanceLimit, "")
		}, false, reasonDevInstanceLimit},
		{"session not live (reason in message only)", func(_ *devTelemetryFacts, m *fakeMintCaller, _ *fakeSessionReporter) {
			m.err = cerr(cloud.CodeFailedPrecondition, "", "refused: session_not_live")
		}, false, reasonSessionNotLive},
		{"not found", func(_ *devTelemetryFacts, m *fakeMintCaller, _ *fakeSessionReporter) {
			m.err = cerr(cloud.CodeNotFound, "", "")
		}, false, "not_found"},
		{"permission denied", func(_ *devTelemetryFacts, m *fakeMintCaller, _ *fakeSessionReporter) {
			m.err = cerr(cloud.CodePermissionDenied, "", "")
		}, false, "permission_denied"},
		{"unreachable", func(_ *devTelemetryFacts, m *fakeMintCaller, _ *fakeSessionReporter) {
			m.err = errors.New("dial tcp: i/o timeout")
		}, false, "unreachable"},
	}
	for _, mode := range []string{devTelemetryAuto, devTelemetryHosted, devTelemetryLocal} {
		for _, c := range cases {
			t.Run(mode+"/"+c.name, func(t *testing.T) {
				caller := &fakeMintCaller{now: devTelNow}
				reporter := &fakeSessionReporter{}
				f := liveFacts(mode, caller, reporter)
				if c.mutate != nil {
					c.mutate(&f, caller, reporter)
				}
				d, err := decideDevTelemetry(context.Background(), f, devTelNow)
				switch {
				case mode == devTelemetryLocal:
					if err != nil || d.Hosted || caller.count() != 0 || len(reporter.snapshot()) != 0 {
						t.Fatalf("local: decision %+v err %v, %d mint(s), %d report(s); want local with no control-plane traffic", d, err, caller.count(), len(reporter.snapshot()))
					}
					if !strings.Contains(d.Reason, "dev_telemetry=local") {
						t.Errorf("reason %q does not name the opt-out", d.Reason)
					}
				case c.wantHosted:
					if err != nil || !d.Hosted {
						t.Fatalf("decision %+v err %v, want hosted", d, err)
					}
					if d.Grant.ProcessToken != "proc-1" || d.Grant.Endpoint != "https://otlp.example.com" || d.Grant.Instance.Label != "alice/fix-login" {
						t.Errorf("grant %+v", d.Grant)
					}
					if len(reporter.snapshot()) != 1 {
						t.Errorf("the session must be reported live before the mint, got %d reports", len(reporter.snapshot()))
					}
				case mode == devTelemetryHosted:
					if err == nil || !strings.Contains(err.Error(), c.reason) {
						t.Fatalf("hosted: err = %v, want a failure naming %q (no fallback)", err, c.reason)
					}
				default:
					if err != nil || d.Hosted {
						t.Fatalf("auto: decision %+v err %v, want a local fallback", d, err)
					}
					if !strings.Contains(d.Reason, c.reason) {
						t.Errorf("auto: reason %q does not name %q", d.Reason, c.reason)
					}
				}
			})
		}
	}
}

func TestDecideDevTelemetrySendsSessionAndRetention(t *testing.T) {
	caller := &fakeMintCaller{now: devTelNow}
	f := liveFacts(devTelemetryAuto, caller, &fakeSessionReporter{})
	o := &ObservabilityEntity{Enabled: true}
	o.Retention.TracesDays, o.Retention.BrowserDays = 3, 2
	o.Retention.Overrides = []RetentionOverrideEntity{{Service: "worker", Signal: "logs", Days: 1}}
	f.Retention = devTelemetryRetentionOf(o)
	if _, err := decideDevTelemetry(context.Background(), f, devTelNow); err != nil {
		t.Fatal(err)
	}
	got, _ := json.Marshal(caller.calls[0])
	want := fmt.Sprintf(`{"environment":"dev","project":"demo","retention":{"browserDays":2,"overrides":[{"days":1,"service":"worker","signal":"logs"}],"tracesDays":3},"sessionId":%q}`, f.Session.session.ID)
	if string(got) != want {
		t.Errorf("mint request\n got %s\nwant %s", got, want)
	}
	// Nothing declared: retention is omitted (the server's 3-day default).
	if r := devTelemetryRetentionOf(&ObservabilityEntity{Enabled: true}); r != nil {
		t.Errorf("an undeclared retention must be omitted, got %+v", r)
	}
}

func TestDevTelemetryFilesAndRenewal(t *testing.T) {
	dir := t.TempDir()
	caller := &fakeMintCaller{now: devTelNow}
	g, err := mintDevTelemetry(context.Background(), caller, "sess-x", "demo", "dev", nil, devTelNow)
	if err != nil {
		t.Fatal(err)
	}
	run := &devTelemetryRun{projectDir: dir, env: "dev", project: "demo", client: caller, sessionID: "sess-x", grant: g}
	if err := run.write(g, devTelNow); err != nil {
		t.Fatal(err)
	}
	assertHeaders := func(proc, browser string) {
		t.Helper()
		for path, tok := range map[string]string{devtelemetry.HeadersPath(dir): proc, devtelemetry.BrowserHeadersPath(dir): browser} {
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(b) != "authorization=Bearer "+tok+"\n" {
				t.Errorf("%s = %q, want the %s token", filepath.Base(path), b, tok)
			}
			// Windows has no POSIX permission bits: every file reads 0666.
			fi, _ := os.Stat(path)
			if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
				t.Errorf("%s mode %v, want 0600", filepath.Base(path), fi.Mode().Perm())
			}
		}
	}
	assertHeaders("proc-1", "browser-1")
	if b, err := os.ReadFile(filepath.Join(dir, ".forge", "obs", ".gitignore")); err != nil || string(b) != "*\n" {
		t.Errorf(".forge/obs/.gitignore = %q, %v; want it to ignore every token file", b, err)
	}

	// The renewal is due at the half-life.
	if got := run.renewAt(); !got.Equal(devTelNow.Add(30 * time.Minute)) {
		t.Fatalf("renewAt = %v, want the half-life", got)
	}
	caller.now = devTelNow.Add(30 * time.Minute)
	if err := run.renewNow(context.Background(), devTelNow.Add(30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	assertHeaders("proc-2", "browser-2")
	st, ok, _ := devtelemetry.ReadState(dir, "dev")
	if !ok || !st.ExpiresAt.Equal(devTelNow.Add(90*time.Minute)) || st.RenewError != "" {
		t.Errorf("state after renewal = %+v", st)
	}

	// A failed renewal is silent: the old tokens stay, the reason is recorded.
	caller.setErr(errors.New("dial tcp: connection refused"))
	if err := run.renewNow(context.Background(), devTelNow.Add(70*time.Minute)); err == nil {
		t.Fatal("a failed mint reported success")
	}
	assertHeaders("proc-2", "browser-2")
	st, _, _ = devtelemetry.ReadState(dir, "dev")
	if !strings.Contains(st.RenewError, "unreachable") {
		t.Errorf("renew error not recorded: %+v", st)
	}
	if line := devTelemetryStatusLine(dir, "dev", devTelNow.Add(70*time.Minute)); line != "" {
		t.Errorf("no decision recorded yet, status line = %q", line)
	}
	_ = recordDevTelemetryDecision(dir, "dev", devTelemetryDecision{Hosted: true, Reason: "auto", Grant: g}, devTelNow)
	if line := devTelemetryStatusLine(dir, "dev", devTelNow.Add(70*time.Minute)); line != "telemetry: hosted (org org-1, instance alice/fix-login, 3d)" {
		t.Errorf("status line = %q", line)
	}
	if line := devTelemetryStatusLine(dir, "dev", devTelNow.Add(2*time.Hour)); !strings.HasPrefix(line, "telemetry: not delivering (token expired; the control plane is unreachable") {
		t.Errorf("expired status line = %q", line)
	}
	_ = recordDevTelemetryDecision(dir, "dev", devTelemetryDecision{Reason: "the env declares no control_plane"}, devTelNow)
	if line := devTelemetryStatusLine(dir, "dev", devTelNow); line != "telemetry: local (the env declares no control_plane)" {
		t.Errorf("local status line = %q", line)
	}
	rec, ok, _ := devtelemetry.Read(dir, "dev")
	if !ok || rec.Backend != "local" {
		t.Errorf("record = %+v", rec)
	}
}

// TestDevTelemetryRecordKeysOnTheServersInstanceID: backend-<env>.json's
// instance_id is the mint response's instance.id (the control plane's row id,
// which the tokens' sid carries), never forge's local session id, which
// repeats across runs and orgs.
func TestDevTelemetryRecordKeysOnTheServersInstanceID(t *testing.T) {
	dir := t.TempDir()
	caller := &fakeMintCaller{now: devTelNow}
	f := liveFacts(devTelemetryAuto, caller, &fakeSessionReporter{})
	d, err := decideDevTelemetry(context.Background(), f, devTelNow)
	if err != nil || !d.Hosted {
		t.Fatalf("decision %+v err %v", d, err)
	}
	if err := recordDevTelemetryDecision(dir, "dev", d, devTelNow); err != nil {
		t.Fatal(err)
	}
	rec, _, _ := devtelemetry.Read(dir, "dev")
	if rec.InstanceID != "inst-row-42" || rec.InstanceID == f.Session.session.ID {
		t.Errorf("instance_id = %q, want the server's instance.id (local session id is %q)", rec.InstanceID, f.Session.session.ID)
	}
	// An answer with no instance id cannot key anything: not hosted.
	if _, err := mintDevTelemetry(context.Background(), noInstanceCaller{}, "s", "p", "dev", nil, devTelNow); err == nil {
		t.Error("a grant without instance.id was accepted")
	}
	// protojson Timestamps carry fractional seconds.
	if _, err := mintDevTelemetry(context.Background(), noInstanceCaller{instance: "i", expires: "2026-11-07T13:00:00.123456789Z"}, "s", "p", "dev", nil, devTelNow); err != nil {
		t.Errorf("fractional expiresAt refused: %v", err)
	}
}

type noInstanceCaller struct{ instance, expires string }

func (c noInstanceCaller) Call(_ context.Context, _ string, _, out any) error {
	exp := c.expires
	if exp == "" {
		exp = "2026-11-07T13:00:00Z"
	}
	return json.Unmarshal([]byte(fmt.Sprintf(`{"processToken":"p","browserToken":"b","expiresAt":%q,"endpoint":"https://e","instance":{"id":%q}}`, exp, c.instance)), out)
}

func TestHostedTelemetryLaunchEnv(t *testing.T) {
	const ep, hf = "https://otlp.example.com", "/p/.forge/obs/otlp-headers"
	derived := map[string]string{
		clickstack.EnvOTLPEndpoint:         "http://127.0.0.1:4318",
		clickstack.EnvOTLPProtocol:         "http/protobuf",
		clickstack.EnvOTLPProfilesEndpoint: "http://127.0.0.1:4328/v1development/profiles",
	}
	got := hostedTelemetryLaunchEnv(derived, nil, ep, hf)
	want := map[string]string{
		clickstack.EnvOTLPEndpoint:         ep,
		clickstack.EnvOTLPProtocol:         "http/protobuf",
		"OTEL_EXPORTER_OTLP_HEADERS_FILE":  hf,
		"OTEL_LOGS_EXPORTER":               "otlp",
		clickstack.EnvOTLPProfilesEndpoint: "",
		"OTEL_PROFILES_EXPORTER":           "none",
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("derived loopback:\n got %v\nwant %v", got, want)
	}
	// No local endpoint was derived at all: still exported, as a unit.
	if got := hostedTelemetryLaunchEnv(map[string]string{}, nil, ep, hf); got[clickstack.EnvOTLPEndpoint] != ep || got["OTEL_EXPORTER_OTLP_HEADERS_FILE"] != hf {
		t.Errorf("nothing derived: %v", got)
	}
	// Logs switched off by the env stay off.
	if got := hostedTelemetryLaunchEnv(map[string]string{"OTEL_LOGS_EXPORTER": "none"}, nil, ep, hf); got["OTEL_LOGS_EXPORTER"] != "" {
		t.Errorf("a declared OTEL_LOGS_EXPORTER=none was overridden: %v", got)
	}
	// A user-set transport wins whole.
	for name, c := range map[string]struct {
		declared map[string]string
		shell    []string
	}{
		"author's endpoint":      {map[string]string{clickstack.EnvOTLPEndpoint: "https://my.collector"}, nil},
		"author's headers file":  {map[string]string{"OTEL_EXPORTER_OTLP_HEADERS_FILE": "/mine"}, nil},
		"shell endpoint":         {derived, []string{"OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4317"}},
		"shell protocol":         {derived, []string{"OTEL_EXPORTER_OTLP_PROTOCOL=grpc"}},
		"shell per-signal":       {derived, []string{"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT=http://x"}},
		"shell headers":          {derived, []string{"OTEL_EXPORTER_OTLP_HEADERS=authorization=Bearer mine"}},
		"author's logs endpoint": {map[string]string{"OTEL_EXPORTER_OTLP_LOGS_ENDPOINT": "http://x"}, nil},
	} {
		if got := hostedTelemetryLaunchEnv(c.declared, c.shell, ep, hf); len(got) != 0 {
			t.Errorf("%s: forge overrode a user-set transport: %v", name, got)
		}
	}
	// An empty shell value is unset.
	if got := hostedTelemetryLaunchEnv(derived, []string{"OTEL_EXPORTER_OTLP_ENDPOINT="}, ep, hf); got[clickstack.EnvOTLPEndpoint] != ep {
		t.Errorf("an empty shell endpoint blocked the hosted one: %v", got)
	}
}

func TestApplyHostedDevTelemetry(t *testing.T) {
	compose := func(name, svc string, requires ...string) WorkloadEntity {
		w := WorkloadEntity{Name: name, Requires: requires}
		w.Runtime.Type = RuntimeCompose
		w.Runtime.Compose = &ComposeRuntime{Service: svc, Optional: true}
		return w
	}
	host := func(name string, env map[string]string) WorkloadEntity {
		w := WorkloadEntity{Name: name}
		w.Runtime.Type = RuntimeHost
		w.Runtime.Host = &HostRuntime{LaunchEnv: env}
		return w
	}
	e := &KCLEntities{
		Workloads: []WorkloadEntity{
			compose("clickstack", clickstack.Service),
			compose("otel-profiles", clickstack.ProfilesService),
			compose("alertmanager", clickstack.AlertmanagerService),
			compose("redis", "redis"),
			host("api", map[string]string{clickstack.EnvOTLPEndpoint: "http://127.0.0.1:4318"}),
			host("custom", map[string]string{clickstack.EnvOTLPEndpoint: "https://mine"}),
			func() WorkloadEntity {
				w := host("alerts", nil)
				w.Requires = []string{"clickstack", "alertmanager"}
				return w
			}(),
		},
		Observability: &ObservabilityEntity{Enabled: true, LocalEndpoint: "http://127.0.0.1:4318", AlertmanagerPort: 9093, AlertsPort: 9095},
	}
	g := devTelemetryGrant{Endpoint: "https://otlp.example.com"}
	skipped := applyHostedDevTelemetry(e, "/p", g, nil)
	if want := "[alertmanager alerts clickstack otel-profiles]"; fmt.Sprint(skipped) != want {
		t.Errorf("skipped %v, want %s", skipped, want)
	}
	var names []string
	for _, w := range e.Workloads {
		names = append(names, w.Name)
	}
	if fmt.Sprint(names) != "[redis api custom]" {
		t.Errorf("kept %v", names)
	}
	if fmt.Sprint(e.OptionalSkipped) != fmt.Sprint(skipped) {
		t.Errorf("OptionalSkipped %v: the deploy phase must drop the same", e.OptionalSkipped)
	}
	api := e.Workloads[1].HostEnv()
	if api[clickstack.EnvOTLPEndpoint] != g.Endpoint || api["OTEL_EXPORTER_OTLP_HEADERS_FILE"] != filepath.Join("/p", ".forge", "obs", "otlp-headers") || api["OTEL_LOGS_EXPORTER"] != "otlp" {
		t.Errorf("api env = %v", api)
	}
	if e.Workloads[2].HostEnv()[clickstack.EnvOTLPEndpoint] != "https://mine" {
		t.Errorf("an author's endpoint was replaced")
	}
	fe := frontendOTLPEnv(e)
	if fe[clickstack.EnvOTLPEndpoint] != g.Endpoint || fe[frontendOTLPHeadersFileEnv] != filepath.Join("/p", ".forge", "obs", "otlp-headers-browser") {
		t.Errorf("frontend env = %v", fe)
	}
	if e.Observability.AlertsPort != 0 || e.Observability.AlertmanagerPort != 0 {
		t.Errorf("the local alerting loop must be off: %+v", e.Observability)
	}
	// A developer's exported endpoint wins whole for a dev server too.
	env := withDefaultEnv([]string{"OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318"}, fe)
	for _, kv := range env {
		if strings.HasPrefix(kv, frontendOTLPHeadersFileEnv+"=") {
			t.Errorf("the browser token was pointed at a developer's own endpoint: %v", env)
		}
	}
}

// TestUpSessionEnsuresTheLocalEnvironmentFirst: ReportLocalSession answers
// NotFound for an environment nothing created, so a hosted session ensures it
// before its first report — retried until it succeeds, never on teardown.
func TestUpSessionEnsuresTheLocalEnvironmentFirst(t *testing.T) {
	var log strings.Builder
	reporter := &fakeSessionReporter{}
	s := testUpSession(reporter, &log, time.Hour)
	ensures := 0
	s.ensure = func(context.Context) error {
		ensures++
		if ensures == 1 {
			return errors.New("control plane unavailable")
		}
		return nil
	}
	_ = s.reportLive(devTelNow)
	_ = s.reportLive(devTelNow)
	_ = s.reportLive(devTelNow)
	s.stop(devTelNow)
	if ensures != 2 {
		t.Errorf("ensure ran %d times, want 2 (once failing, once succeeding, then never again)", ensures)
	}
	if n := len(reporter.snapshot()); n != 4 {
		t.Errorf("%d reports, want 4: a failed ensure must not drop the report", n)
	}
	if !strings.Contains(log.String(), "control plane unavailable") {
		t.Errorf("the ensure failure was not noted: %q", log.String())
	}
}

// keeperSpecForTest is a keeper spec whose tokens were minted at devTelNow.
// It also records the env as hosted on that instance, as `env up` does
// before it starts a keeper: a keeper runs only while that record stands.
func keeperSpecForTest(dir string) devTelemetryKeeperSpec {
	var log strings.Builder
	sess := testUpSession(&fakeSessionReporter{}, &log, time.Hour).session
	if err := devtelemetry.Write(dir, devtelemetry.Record{Env: "dev", Backend: "cloud", InstanceID: "inst-row-42", DecidedAt: devTelNow}); err != nil {
		panic(err)
	}
	return devTelemetryKeeperSpec{
		ProjectDir: dir, Env: "dev", Project: "demo",
		Endpoint: cloud.Endpoint{URL: "https://cp.example.com", Env: "dev"},
		Session:  sess, MintedAt: devTelNow, ExpiresAt: devTelNow.Add(time.Hour),
		Granted:    devtelemetry.Retention{TracesDays: 3, MetricsDays: 3, LogsDays: 3, BrowserDays: 3},
		InstanceID: "inst-row-42",
	}
}

// fakeDevClock is a devTelemetryClock that moves only when the test says.
// armed receives one value each time a timer is set, so a test waits for the
// scheduler to be parked on its timer instead of sleeping.
type fakeDevClock struct {
	mu      sync.Mutex
	now     time.Time
	waiters []fakeDevTimer
	armed   chan time.Duration
}

type fakeDevTimer struct {
	at time.Time
	ch chan time.Time
}

func newFakeDevClock(at time.Time) *fakeDevClock {
	return &fakeDevClock{now: at, armed: make(chan time.Duration, 64)}
}

func (c *fakeDevClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeDevClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	ch := make(chan time.Time, 1)
	if d <= 0 {
		ch <- c.now
	} else {
		c.waiters = append(c.waiters, fakeDevTimer{at: c.now.Add(d), ch: ch})
	}
	c.mu.Unlock()
	c.armed <- d
	return ch
}

// Advance moves the clock and fires every timer now due.
func (c *fakeDevClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	kept := c.waiters[:0]
	for _, w := range c.waiters {
		if !w.at.After(c.now) {
			w.ch <- c.now
		} else {
			kept = append(kept, w)
		}
	}
	c.waiters = kept
}

// recv waits for one value on ch, failing the test if none comes. The bound
// is a hang guard only; nothing is ordered by it.
func recv[T any](t *testing.T, what string, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		var zero T
		return zero
	}
}

// startKeeper runs the keeper until the returned stop is called; stop waits
// for it to return.
func startTestKeeper(t *testing.T, spec devTelemetryKeeperSpec, caller cloudCaller, reporter sessionReporter, clock devTelemetryClock) (stop func()) {
	stop, _ = startTestKeeperDone(t, spec, caller, reporter, clock)
	return stop
}

// startTestKeeperDone is startTestKeeper, also returning the channel that
// closes when the keeper returns (on its own, or stopped).
func startTestKeeperDone(t *testing.T, spec devTelemetryKeeperSpec, caller cloudCaller, reporter sessionReporter, clock devTelemetryClock) (stop func(), done <-chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan struct{})
	var log strings.Builder
	go func() {
		defer close(finished)
		// The heartbeat ticker is an hour: the only beat is the first.
		runDevTelemetryKeeper(ctx, spec, caller, reporter, time.Hour, clock, &log)
	}()
	var once sync.Once
	stop = func() {
		once.Do(func() {
			cancel()
			recv(t, "the keeper to return", finished)
		})
	}
	t.Cleanup(stop)
	return stop, finished
}

// TestDevTelemetryKeeperRenewsADetachedStack is the detached stack's whole
// lifetime: with no `env up` process left, the keeper renews both tokens at
// every half-life, on its own timer, while the presence report is BLOCKED —
// a slow or failing report cannot hold renewal back.
//
// MUTATION: run the renewal after the report (on the heartbeat) and no
// token is minted while the report hangs.
func TestDevTelemetryKeeperRenewsADetachedStack(t *testing.T) {
	dir := t.TempDir()
	spec := keeperSpecForTest(dir)
	clock := newFakeDevClock(devTelNow)
	caller := &fakeMintCaller{clock: clock.Now, minted: make(chan struct{}, 16)}
	reporter := &hangingReporter{entered: make(chan struct{}, 1), release: make(chan struct{})}
	startTestKeeper(t, spec, caller, reporter, clock)
	t.Cleanup(func() { close(reporter.release) }) // before the keeper's stop (cleanups run last-first)
	recv(t, "the first presence report to hang", reporter.entered)

	if d := recv(t, "the first renewal timer", clock.armed); d != 30*time.Minute {
		t.Fatalf("first renewal in %v, want the half-life (30m)", d)
	}
	clock.Advance(30 * time.Minute)
	recv(t, "the first renewal", caller.minted)
	if d := recv(t, "the next renewal timer", clock.armed); d != 30*time.Minute {
		t.Fatalf("next renewal in %v, want the new grant's half-life", d)
	}
	if reporter.returned.Load() {
		t.Fatal("the presence report returned: this test no longer proves renewal does not wait on it")
	}
	clock.Advance(30 * time.Minute)
	recv(t, "the second renewal", caller.minted)
	recv(t, "the third renewal timer", clock.armed)

	if caller.count() != 2 {
		t.Errorf("%d mints, want 2", caller.count())
	}
	b, err := os.ReadFile(devtelemetry.HeadersPath(dir))
	if err != nil || string(b) != "authorization=Bearer proc-2\n" {
		t.Errorf("headers file %q, %v; want the second token", b, err)
	}
	st, ok, _ := devtelemetry.ReadState(dir, "dev")
	if !ok || st.RenewError != "" || !st.ExpiresAt.Equal(devTelNow.Add(2*time.Hour)) {
		t.Errorf("state %+v", st)
	}
	caller.mu.Lock()
	body := caller.calls[0]
	caller.mu.Unlock()
	if body["sessionId"] != spec.Session.ID || body["project"] != "demo" {
		t.Errorf("mint request %v", body)
	}
}

// hangingReporter's report hangs until released, and says when it began.
type hangingReporter struct {
	entered  chan struct{}
	release  chan struct{}
	returned atomic.Bool
}

func (h *hangingReporter) ReportSession(ctx context.Context, _ release.LocalSession) error {
	h.entered <- struct{}{}
	defer h.returned.Store(true)
	select {
	case <-h.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// TestDevTelemetryKeeperRetriesWithBackoff: a refused renewal is retried at
// 15s, then 30s, … — not at the next half-life, which would be after expiry.
func TestDevTelemetryKeeperRetriesWithBackoff(t *testing.T) {
	dir := t.TempDir()
	clock := newFakeDevClock(devTelNow)
	caller := &fakeMintCaller{clock: clock.Now, minted: make(chan struct{}, 16)}
	caller.setErr(&cloud.Error{Code: cloud.CodeFailedPrecondition, Reason: reasonSessionNotLive})
	startTestKeeper(t, keeperSpecForTest(dir), caller, &fakeSessionReporter{}, clock)

	recv(t, "the renewal timer", clock.armed)
	clock.Advance(30 * time.Minute)
	recv(t, "the first attempt", caller.minted)
	for _, want := range []time.Duration{15 * time.Second, 30 * time.Second, time.Minute} {
		if d := recv(t, "a retry timer", clock.armed); d != want {
			t.Fatalf("retry in %v, want %v", d, want)
		}
		clock.Advance(want)
		recv(t, "a retry", caller.minted)
	}
	st, _, _ := devtelemetry.ReadState(dir, "dev")
	if !strings.Contains(st.RenewError, reasonSessionNotLive) {
		t.Errorf("the refusal is not recorded: %+v", st)
	}
	if d := recv(t, "a retry timer", clock.armed); d != 2*time.Minute {
		t.Fatalf("retry in %v, want 2m", d)
	}
	caller.setErr(nil)
	clock.Advance(2 * time.Minute)
	recv(t, "the successful retry", caller.minted)
	// Success resets the schedule to the new grant's half-life.
	if d := recv(t, "the next renewal timer", clock.armed); d != 30*time.Minute {
		t.Fatalf("after a success the next renewal is in %v, want 30m", d)
	}
	st, _, _ = devtelemetry.ReadState(dir, "dev")
	if st.RenewError != "" {
		t.Errorf("a success did not clear the renewal error: %+v", st)
	}
}

// TestDevTelemetryKeeperStopsWithTheStack: stopping the stack signals the
// keeper, whose signal context cancels — and after that nothing is minted or
// reported however much time passes, and the session is NOT reported stopped
// by the keeper (whoever stopped the stack reports that, after the keeper is
// gone).
func TestDevTelemetryKeeperStopsWithTheStack(t *testing.T) {
	dir := t.TempDir()
	clock := newFakeDevClock(devTelNow)
	caller := &fakeMintCaller{clock: clock.Now, minted: make(chan struct{}, 16)}
	reporter := &fakeSessionReporter{}
	stop := startTestKeeper(t, keeperSpecForTest(dir), caller, reporter, clock)
	recv(t, "the renewal timer", clock.armed)
	clock.Advance(30 * time.Minute)
	recv(t, "a renewal", caller.minted)
	recv(t, "the next renewal timer", clock.armed)

	stop() // returns only once the keeper has
	mints, reports := caller.count(), len(reporter.snapshot())
	clock.Advance(5 * time.Hour) // every timer it could have set is now due
	if caller.count() != mints || len(reporter.snapshot()) != reports {
		t.Errorf("work after stop: mints %d→%d, reports %d→%d", mints, caller.count(), reports, len(reporter.snapshot()))
	}
	if reports != 1 {
		t.Errorf("%d presence reports, want the keeper's first beat", reports)
	}
	for _, r := range reporter.snapshot() {
		if r.StoppedAt != nil {
			t.Errorf("the keeper reported the session stopped")
		}
	}
	// A keeper started on a stack already stopping does nothing at all.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var log strings.Builder
	runDevTelemetryKeeper(ctx, keeperSpecForTest(dir), caller, reporter, time.Hour, clock, &log)
	if caller.count() != mints || len(reporter.snapshot()) != reports {
		t.Errorf("a keeper started on a stopped stack did work")
	}
}

// TestDevTelemetryKeeperCmd: the keeper is this forge binary running
// `env telemetry-keeper <spec>`, with a spec that holds no token.
func TestDevTelemetryKeeperCmd(t *testing.T) {
	dir := t.TempDir()
	spec := keeperSpecForTest(dir)
	cmd, err := devTelemetryKeeperCmd(spec)
	if err != nil {
		t.Fatal(err)
	}
	path := devTelemetryKeeperSpecPath(dir, "dev")
	if n := len(cmd.Args); n < 3 || cmd.Args[n-3] != "env" || cmd.Args[n-2] != devTelemetryKeeperName || cmd.Args[n-1] != path {
		t.Errorf("argv %v", cmd.Args)
	}
	if cmd.Dir != dir {
		t.Errorf("dir %q", cmd.Dir)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "Bearer") || strings.Contains(string(b), "proc-") {
		t.Errorf("the keeper spec carries a token: %s", b)
	}
	var back devTelemetryKeeperSpec
	if err := json.Unmarshal(b, &back); err != nil || back.Session.ID != spec.Session.ID || back.Endpoint.URL != spec.Endpoint.URL || !back.ExpiresAt.Equal(spec.ExpiresAt) {
		t.Errorf("spec round trip: %+v, %v", back, err)
	}
	// Windows has no POSIX permission bits: every file reads 0666.
	if fi, _ := os.Stat(path); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("spec mode %v", fi.Mode().Perm())
	}
	// The command is registered (hidden) under `forge env`.
	found := false
	for _, c := range newEnvCmd().Commands() {
		if c.Name() == devTelemetryKeeperName && c.Hidden {
			found = true
		}
	}
	if !found {
		t.Error("forge env telemetry-keeper is not registered as a hidden command")
	}
}

// TestDevTelemetrySetupFallbackLeavesNoHostedState: a stack that went hosted,
// then (next run) fails its mint and falls back to local under auto, keeps no
// hosted token on disk, and its record says local with the reason — `forge
// env status` and the processes cannot inherit the stale grant. A failed
// hosted-mode setup settles the same way before it returns its error.
func TestDevTelemetrySetupFallbackLeavesNoHostedState(t *testing.T) {
	t.Setenv(devTelemetryEnvVar, "")
	prev := devTelemetryCredential
	devTelemetryCredential = func(string, *cloud.Declaration) error { return nil }
	t.Cleanup(func() { devTelemetryCredential = prev })
	stubKeeperStop(t, func(string, string) error { return nil })

	dir := t.TempDir()
	caller := &fakeMintCaller{now: devTelNow}
	entities := func() *KCLEntities {
		return &KCLEntities{
			ControlPlane:  &ControlPlaneEntity{Endpoint: "https://cp.example.com"},
			Observability: &ObservabilityEntity{Enabled: true, DevTelemetry: devTelemetryAuto, LocalEndpoint: "http://127.0.0.1:4318"},
		}
	}
	target := sessionTarget{Report: true, Hosted: true, Client: caller, Project: "demo"}
	setup := func(flag string) (*devTelemetryRun, string, error) {
		var log, out strings.Builder
		sess := testUpSession(&fakeSessionReporter{}, &log, time.Hour)
		run, err := setupDevTelemetry(context.Background(), entities(), devTelemetrySetupIn{ProjectDir: dir, Env: "dev", Flag: flag, Session: sess, Target: target, Out: &out, Now: devTelNow})
		return run, out.String(), err
	}
	noTokens := func(stage string) {
		t.Helper()
		for _, p := range []string{devtelemetry.HeadersPath(dir), devtelemetry.BrowserHeadersPath(dir), devtelemetry.StatePath(dir, "dev"), devTelemetryKeeperSpecPath(dir, "dev")} {
			if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("%s: %s survived the fallback (%v)", stage, filepath.Base(p), err)
			}
		}
	}

	// 1. Hosted: tokens, state and the keeper spec on disk, recorded cloud.
	run, _, err := setup("")
	if err != nil || run == nil {
		t.Fatalf("first run: %v, %v", run, err)
	}
	if _, err := devTelemetryKeeperCmd(run.keeperSpec()); err != nil {
		t.Fatal(err)
	}
	if rec, _, _ := devtelemetry.Read(dir, "dev"); rec.Backend != "cloud" || rec.InstanceID != "inst-row-42" {
		t.Fatalf("first run recorded %+v", rec)
	}
	if _, err := os.Stat(devtelemetry.HeadersPath(dir)); err != nil {
		t.Fatalf("no token after a hosted run: %v", err)
	}

	// 2. The mint now fails: auto falls back, and nothing hosted remains.
	caller.setErr(&cloud.Error{Code: cloud.CodeUnimplemented})
	run, out, err := setup("")
	if err != nil || run != nil {
		t.Fatalf("fallback run: %v, %v", run, err)
	}
	noTokens("auto fallback")
	rec, ok, _ := devtelemetry.Read(dir, "dev")
	if !ok || rec.Backend != "local" || !strings.Contains(rec.Reason, "predates hosted dev telemetry") || rec.InstanceID != "" || rec.OrgID != "" {
		t.Errorf("fallback recorded %+v", rec)
	}
	if !strings.Contains(out, "telemetry: local (") {
		t.Errorf("the fallback was not printed: %q", out)
	}
	if line := devTelemetryStatusLine(dir, "dev", devTelNow); !strings.HasPrefix(line, "telemetry: local (") {
		t.Errorf("status line after fallback = %q", line)
	}

	// 3. Hosted again, then a hosted-mode failure: the run fails, and it
	// too leaves the record local and no token behind.
	caller.setErr(nil)
	if _, _, err := setup(""); err != nil {
		t.Fatal(err)
	}
	caller.setErr(&cloud.Error{Code: cloud.CodeFailedPrecondition, Reason: reasonDevTelemetryDisabled})
	if _, _, err := setup(devTelemetryHosted); err == nil {
		t.Fatal("--telemetry=hosted with a refused mint did not fail")
	}
	noTokens("hosted failure")
	rec, _, _ = devtelemetry.Read(dir, "dev")
	if rec.Backend != "local" || !strings.Contains(rec.Reason, "dev_telemetry=hosted failed") || !strings.Contains(rec.Reason, reasonDevTelemetryDisabled) {
		t.Errorf("hosted failure recorded %+v", rec)
	}
}

// TestClearCredentialsKeepsAnotherHostedEnvsHeaders: the headers files are
// per project, so an env going local leaves them for another env recorded
// hosted, and removes only its own state.
func TestClearCredentialsKeepsAnotherHostedEnvsHeaders(t *testing.T) {
	dir := t.TempDir()
	if err := devtelemetry.WriteHeaders(dir, "p", "b"); err != nil {
		t.Fatal(err)
	}
	_ = devtelemetry.WriteState(dir, devtelemetry.State{Env: "dev"})
	_ = devtelemetry.Write(dir, devtelemetry.Record{Env: "dev2", Backend: "cloud"})
	if err := devtelemetry.ClearCredentials(dir, "dev"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(devtelemetry.HeadersPath(dir)); err != nil {
		t.Errorf("dev2's headers were removed: %v", err)
	}
	if _, err := os.Stat(devtelemetry.StatePath(dir, "dev")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("dev's state survived: %v", err)
	}
	_ = devtelemetry.Write(dir, devtelemetry.Record{Env: "dev2", Backend: "local"})
	if err := devtelemetry.ClearCredentials(dir, "dev"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(devtelemetry.HeadersPath(dir)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("headers survived with no env hosted: %v", err)
	}
}

// stubKeeperStop replaces the stop of env's tracked keeper for one test.
func stubKeeperStop(t *testing.T, stop func(projectDir, env string) error) {
	t.Helper()
	prev := stopDevTelemetryKeeper
	stopDevTelemetryKeeper = stop
	t.Cleanup(func() { stopDevTelemetryKeeper = prev })
}

// TestDevTelemetryLocalDecisionStopsTheRunningKeeper: a stack that went
// hosted has a keeper running. The next setup decides local — by auto
// fallback, or because a hosted setup failed — and must stop that keeper
// (through the ledger's stop path) and wait for it BEFORE it records the
// decision and clears the tokens; afterwards no renewal, no token file and
// no presence report can come from it, however much time passes.
func TestDevTelemetryLocalDecisionStopsTheRunningKeeper(t *testing.T) {
	for _, tc := range []struct {
		name, flag string
		wantErr    bool
	}{
		{"auto fallback", "", false},
		{"failed hosted setup", devTelemetryHosted, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(devTelemetryEnvVar, "")
			prev := devTelemetryCredential
			devTelemetryCredential = func(string, *cloud.Declaration) error { return nil }
			t.Cleanup(func() { devTelemetryCredential = prev })

			dir := t.TempDir()
			clock := newFakeDevClock(devTelNow)
			caller := &fakeMintCaller{clock: clock.Now, minted: make(chan struct{}, 16)}
			target := sessionTarget{Report: true, Hosted: true, Client: caller, Project: "demo"}
			entities := func() *KCLEntities {
				return &KCLEntities{
					ControlPlane:  &ControlPlaneEntity{Endpoint: "https://cp.example.com"},
					Observability: &ObservabilityEntity{Enabled: true, LocalEndpoint: "http://127.0.0.1:4318"},
				}
			}
			setup := func(flag string) (*devTelemetryRun, error) {
				var log, out strings.Builder
				return setupDevTelemetry(context.Background(), entities(), devTelemetrySetupIn{ProjectDir: dir, Env: "dev", Flag: flag, Session: testUpSession(&fakeSessionReporter{}, &log, time.Hour), Target: target, Out: &out, Now: devTelNow})
			}

			// A hosted run, and its keeper (the stack's tracked child).
			run, err := setup("")
			if err != nil || run == nil {
				t.Fatalf("hosted run: %v %v", run, err)
			}
			recv(t, "the setup's mint", caller.minted)
			reporter := &fakeSessionReporter{}
			stopKeeper, keeperDone := startTestKeeperDone(t, run.keeperSpec(), caller, reporter, clock)
			recv(t, "the keeper's renewal timer", clock.armed)

			stops := 0
			stubKeeperStop(t, func(projectDir, env string) error {
				stops++
				if rec, _, _ := devtelemetry.Read(projectDir, env); rec.Backend != "cloud" {
					t.Errorf("the keeper was stopped after the local decision was recorded (%+v)", rec)
				}
				if _, err := os.Stat(devtelemetry.HeadersPath(projectDir)); err != nil {
					t.Errorf("the tokens were cleared before the keeper was stopped: %v", err)
				}
				stopKeeper() // the ledger stop: signal, then wait for the exit
				return nil
			})

			caller.setErr(&cloud.Error{Code: cloud.CodeUnimplemented})
			if _, err := setup(tc.flag); (err != nil) != tc.wantErr {
				t.Fatalf("local setup: err = %v, want error %v", err, tc.wantErr)
			}
			if stops != 1 {
				t.Fatalf("the running keeper was stopped %d times, want once", stops)
			}
			recv(t, "the keeper to have exited", keeperDone)
			recv(t, "the failed setup's mint", caller.minted)

			mints, reports := caller.count(), len(reporter.snapshot())
			caller.setErr(nil)
			clock.Advance(5 * time.Hour)
			if caller.count() != mints || len(reporter.snapshot()) != reports {
				t.Errorf("the stopped keeper still worked: mints %d→%d, reports %d→%d", mints, caller.count(), reports, len(reporter.snapshot()))
			}
			for _, p := range []string{devtelemetry.HeadersPath(dir), devtelemetry.BrowserHeadersPath(dir), devtelemetry.StatePath(dir, "dev")} {
				if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("%s exists after the local decision (%v)", filepath.Base(p), err)
				}
			}
			if rec, _, _ := devtelemetry.Read(dir, "dev"); rec.Backend != "local" {
				t.Errorf("record %+v, want local", rec)
			}
		})
	}
}

// TestDevTelemetryKeeperStopsItselfWhenSuperseded is the defence in depth: a
// keeper nothing managed to signal still stops once backend-<env>.json no
// longer records its grant (local, or another instance), and never writes a
// token or reports presence after that.
func TestDevTelemetryKeeperStopsItselfWhenSuperseded(t *testing.T) {
	for name, supersede := range map[string]func(dir string){
		"decided local": func(dir string) {
			_ = devtelemetry.Write(dir, devtelemetry.Record{Env: "dev", Backend: "local", Reason: "dev_telemetry=local"})
		},
		"another instance": func(dir string) {
			_ = devtelemetry.Write(dir, devtelemetry.Record{Env: "dev", Backend: "cloud", InstanceID: "inst-row-99"})
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			clock := newFakeDevClock(devTelNow)
			caller := &fakeMintCaller{clock: clock.Now, minted: make(chan struct{}, 16)}
			reporter := &notifyingReporter{seen: make(chan struct{}, 16)}
			_, done := startTestKeeperDone(t, keeperSpecForTest(dir), caller, reporter, clock)
			recv(t, "the renewal timer", clock.armed)
			recv(t, "the keeper's first beat", reporter.seen)
			reports := len(reporter.snapshot())

			supersede(dir)
			_ = devtelemetry.ClearCredentials(dir, "dev")
			clock.Advance(30 * time.Minute) // the half-life: it would renew now
			recv(t, "the keeper to stop itself", done)
			if caller.count() != 0 {
				t.Errorf("a superseded keeper minted %d time(s)", caller.count())
			}
			if len(reporter.snapshot()) != reports {
				t.Errorf("a superseded keeper reported presence")
			}
			if _, err := os.Stat(devtelemetry.HeadersPath(dir)); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("a superseded keeper recreated the tokens (%v)", err)
			}
		})
	}
}

// TestDevTelemetryKeeperDiscardsAMintRacingALocalDecision: a decision taken
// while a renewal's mint is in flight wins — the minted tokens are dropped,
// not written over the cleared files, and the keeper stops.
func TestDevTelemetryKeeperDiscardsAMintRacingALocalDecision(t *testing.T) {
	dir := t.TempDir()
	clock := newFakeDevClock(devTelNow)
	caller := &fakeMintCaller{clock: clock.Now, minted: make(chan struct{}, 16)}
	caller.during = func() {
		_ = devtelemetry.Write(dir, devtelemetry.Record{Env: "dev", Backend: "local", Reason: "fallback"})
		_ = devtelemetry.ClearCredentials(dir, "dev")
	}
	_, done := startTestKeeperDone(t, keeperSpecForTest(dir), caller, &fakeSessionReporter{}, clock)
	recv(t, "the renewal timer", clock.armed)
	clock.Advance(30 * time.Minute)
	recv(t, "the mint", caller.minted)
	recv(t, "the keeper to stop itself", done)
	for _, p := range []string{devtelemetry.HeadersPath(dir), devtelemetry.BrowserHeadersPath(dir), devtelemetry.StatePath(dir, "dev")} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s was written after the local decision (%v)", filepath.Base(p), err)
		}
	}
}

// notifyingReporter is a fakeSessionReporter that says when each report is in.
type notifyingReporter struct {
	fakeSessionReporter
	seen chan struct{}
}

func (n *notifyingReporter) ReportSession(ctx context.Context, sess release.LocalSession) error {
	err := n.fakeSessionReporter.ReportSession(ctx, sess)
	n.seen <- struct{}{}
	return err
}
