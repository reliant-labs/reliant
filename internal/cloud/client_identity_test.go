package cloud

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"runtime"
	"testing"

	"github.com/reliant-labs/forge/internal/buildinfo"
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
