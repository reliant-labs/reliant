package cloud

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"runtime"
	"testing"

	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/pkg/observe/cliev"
)

func TestClientHeaderValue(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  map[string]string
		ci   bool
		want string
	}{
		{"no agent", nil, false, "harness=none; ci=false"},
		{"no agent on CI", nil, true, "harness=none; ci=true"},
		{"claude", map[string]string{"CLAUDECODE": "1"}, false, "harness=claude; ci=false"},
		{"codex", map[string]string{"CODEX_THREAD_ID": "thr_secret_123"}, false, "harness=codex; ci=false"},
		{"cursor", map[string]string{"CURSOR_AGENT": "1"}, false, "harness=cursor; ci=false"},
		{"gemini", map[string]string{"GEMINI_CLI": "1"}, false, "harness=gemini; ci=false"},
		{"reliant", map[string]string{"RELIANT_AGENT_SHELL": "1", "CLAUDECODE": "1"}, true, "harness=reliant; ci=true"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := clientHeaderValue(func(k string) string { return tc.env[k] }, tc.ci)
			if got != tc.want {
				t.Fatalf("clientHeaderValue = %q, want %q", got, tc.want)
			}
		})
	}
}

// The header is built from fixed vocabularies only: a marker's VALUE (a codex
// thread id, a path someone exported) must never reach the wire.
func TestClientHeaderValue_CarriesNoEnvValues(t *testing.T) {
	env := map[string]string{"CODEX_SESSION_ID": "/home/alice/.codex/sess-42"}
	got := clientHeaderValue(func(k string) string { return env[k] }, false)
	if !regexp.MustCompile(`^harness=[a-z]+; ci=(true|false)$`).MatchString(got) {
		t.Fatalf("clientHeaderValue = %q: want only fixed vocabulary", got)
	}
}

func TestUserAgent(t *testing.T) {
	want := "forge/" + buildinfo.Version() + " (" + runtime.GOOS + "/" + runtime.GOARCH + ")"
	if got := UserAgent(); got != want {
		t.Fatalf("UserAgent = %q, want %q", got, want)
	}
}

// Every Connect call carries the identity headers, including an
// unauthenticated one (a claimable deploy has no credential yet).
func TestClient_Call_SendsClientIdentity(t *testing.T) {
	t.Setenv("CLAUDECODE", "1")
	t.Setenv("RELIANT_AGENT_SHELL", "")
	buildinfo.SetCI(false)
	t.Cleanup(buildinfo.ClearCI)
	var gotUA, gotClient string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		gotClient = r.Header.Get(ClientHeader)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	ep, err := ResolveEndpoint("prod", &Declaration{Endpoint: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if err := NewClient(ep, Credential{}).Call(context.Background(), "controlplane.v1.ClaimableService/GetClaimableChallenge", map[string]any{}, nil); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if gotUA != UserAgent() {
		t.Errorf("User-Agent = %q, want %q", gotUA, UserAgent())
	}
	if gotClient != "harness=claude; ci=false" {
		t.Errorf("%s = %q, want %q", ClientHeader, gotClient, "harness=claude; ci=false")
	}
}

func TestRevokeIdentity_SendsClientIdentity(t *testing.T) {
	var gotUA, gotClient string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		gotClient = r.Header.Get(ClientHeader)
	}))
	defer srv.Close()
	if err := RevokeIdentity(context.Background(), Endpoint{URL: srv.URL}, "tok"); err != nil {
		t.Fatalf("RevokeIdentity: %v", err)
	}
	if gotUA != UserAgent() || gotClient != ClientHeaderValue() {
		t.Errorf("headers = (%q, %q), want (%q, %q)", gotUA, gotClient, UserAgent(), ClientHeaderValue())
	}
}

// The login token exchange goes through identifiedDoer.
func TestIdentifiedDoer_StampsHeaders(t *testing.T) {
	var gotUA, gotClient string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		gotClient = r.Header.Get(ClientHeader)
	}))
	defer srv.Close()
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/oauth/token", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := identifiedDoer{next: srv.Client()}.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if gotUA != UserAgent() || gotClient != ClientHeaderValue() {
		t.Errorf("headers = (%q, %q), want (%q, %q)", gotUA, gotClient, UserAgent(), ClientHeaderValue())
	}
}

func TestInstallIdentityOnlyWhileTelemetryIsOn(t *testing.T) {
	dir := t.TempDir()
	on := cliev.New(cliev.Config{App: "forge", Endpoint: "http://127.0.0.1:1/v1/logs", ConfigDir: dir, Stderr: io.Discard, Getenv: func(string) string { return "" }})
	if err := on.SetInstallAttribution("utm_source=docs"); err != nil {
		t.Fatal(err)
	}
	h := http.Header{}
	setInstallIdentity(h, on)
	if h.Get(InstallIDHeader) != on.InstallID() || h.Get(AttributionHeader) != "install_id="+on.InstallID()+"&utm_source=docs" {
		t.Errorf("headers = %v", h)
	}
	off := cliev.New(cliev.Config{App: "forge", Endpoint: "http://127.0.0.1:1/v1/logs", ConfigDir: dir, Stderr: io.Discard,
		Getenv: func(k string) string { return map[string]string{"DO_NOT_TRACK": "1"}[k] }})
	h = http.Header{}
	setInstallIdentity(h, off)
	setInstallIdentity(h, nil)
	if len(h) != 0 {
		t.Errorf("telemetry off must send neither header: %v", h)
	}
}

// A fresh install's first run with the notice suppressed (--json, quiet) has
// telemetry on but has not shown the notice: it must not send the install id
// or its attribution until a run has.
func TestInstallIdentityWaitsForTheTelemetryNotice(t *testing.T) {
	dir := t.TempDir()
	cfg := cliev.Config{App: "forge", Endpoint: "http://127.0.0.1:1/v1/logs", ConfigDir: dir, Stderr: io.Discard,
		Getenv: func(string) string { return "" }, Quiet: true}
	quiet := cliev.New(cfg)
	if !quiet.Enabled() {
		t.Fatal("telemetry must be on for this test")
	}
	if err := quiet.SetInstallAttribution("utm_source=docs"); err != nil {
		t.Fatal(err)
	}
	h := http.Header{}
	setInstallIdentity(h, quiet)
	if len(h) != 0 {
		t.Errorf("a quiet first run sent identity headers before the notice: %v", h)
	}
	cfg.Quiet = false
	shown := cliev.New(cfg)
	setInstallIdentity(h, shown)
	if h.Get(InstallIDHeader) == "" || h.Get(InstallIDHeader) != shown.InstallID() {
		t.Errorf("after the notice the install id is sent: %v", h)
	}
}

// A process that is still running when another forge process runs
// `forge telemetry off` stops sending the install id and attribution.
func TestInstallIdentityStopsWhenAnotherProcessOptsOut(t *testing.T) {
	cfg := cliev.Config{App: "forge", Endpoint: "http://127.0.0.1:1/v1/logs", ConfigDir: t.TempDir(), Stderr: io.Discard,
		Getenv: func(string) string { return "" }}
	running := cliev.New(cfg)
	h := http.Header{}
	setInstallIdentity(h, running)
	if h.Get(InstallIDHeader) == "" {
		t.Fatalf("telemetry on: want the install id header, got %v", h)
	}
	if err := cliev.New(cfg).SetEnabled(false); err != nil {
		t.Fatal(err)
	}
	h = http.Header{}
	setInstallIdentity(h, running)
	if len(h) != 0 {
		t.Errorf("after another process opted out, the running one still sent %v", h)
	}
}
