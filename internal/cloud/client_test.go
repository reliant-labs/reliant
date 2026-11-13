package cloud

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/pkg/activity"
)

// TestClient_Call_AuthenticatesAndReturnsData drives the real transport
// against a test server that speaks Connect's HTTP+JSON binding: the
// procedure path, the Authorization header, the JSON request and the
// JSON reply.
func TestClient_Call_AuthenticatesAndReturnsData(t *testing.T) {
	var gotPath, gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		gotBody = string(buf)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"releases":[{"id":"rel_1","version":"v1.4.0","gitCommit":"abc123def456"}]}`))
	}))
	defer srv.Close()

	ep, err := ResolveEndpoint("prod", &Declaration{Endpoint: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	client := NewClient(ep, Credential{Token: "tok-123", Source: SourceEnv, From: "ACME_TOKEN"})

	var out struct {
		Releases []struct {
			ID      string `json:"id"`
			Version string `json:"version"`
		} `json:"releases"`
	}
	if err := client.Call(context.Background(),
		"controlplane.v1.DeployService/ListReleases", map[string]any{"limit": 5}, &out); err != nil {
		t.Fatalf("Call: %v", err)
	}

	if gotPath != "/controlplane.v1.DeployService/ListReleases" {
		t.Errorf("procedure path: got %q", gotPath)
	}
	if gotAuth != "Bearer tok-123" {
		t.Errorf("Authorization header: got %q, want %q", gotAuth, "Bearer tok-123")
	}
	if !strings.Contains(gotBody, `"limit":5`) {
		t.Errorf("request body should carry the limit; got %q", gotBody)
	}
	if len(out.Releases) != 1 || out.Releases[0].Version != "v1.4.0" {
		t.Fatalf("decoded response: %+v", out)
	}
}

// TestClient_Call_AuthFailureIsActionable: a bare 401 leaves a user
// unable to tell a missing credential from a wrong one, so the message
// must name the endpoint, where the credential came from, and both ways
// to supply a better one.
func TestClient_Call_AuthFailureIsActionable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"code": "unauthenticated", "message": "token expired",
		})
	}))
	defer srv.Close()

	ep, err := ResolveEndpoint("prod", &Declaration{Endpoint: srv.URL, TokenEnv: "ACME_PROD_TOKEN"})
	if err != nil {
		t.Fatal(err)
	}
	client := NewClient(ep, Credential{Token: "stale", Source: SourceLogin, From: "/home/u/.forge/login.json"})

	err = client.Call(context.Background(), "controlplane.v1.DeployService/ListReleases", map[string]any{}, &struct{}{})
	if err == nil {
		t.Fatal("expected an error on 401")
	}
	msg := err.Error()
	for _, want := range []string{"forge login", "ACME_PROD_TOKEN", srv.URL, "/home/u/.forge/login.json", "token expired"} {
		if !strings.Contains(msg, want) {
			t.Errorf("auth error should contain %q; got:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "stale") {
		t.Errorf("the error must never echo the credential itself; got:\n%s", msg)
	}
}

// TestClient_Call_UnimplementedIsReportedAsSuch — the hosted API has
// real scaffold stubs, so hitting one must read as "the server does not
// implement this", not as a forge bug.
func TestClient_Call_UnimplementedIsReportedAsSuch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotImplemented)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"code": "unimplemented", "message": "handler for ListDeployments not yet implemented",
		})
	}))
	defer srv.Close()

	ep, _ := ResolveEndpoint("prod", &Declaration{Endpoint: srv.URL})
	client := NewClient(ep, Credential{Token: "t"})
	err := client.Call(context.Background(), "controlplane.v1.DeployService/ListDeployments", map[string]any{}, &struct{}{})
	if err == nil || !strings.Contains(err.Error(), "not implemented") {
		t.Fatalf("want an unimplemented-shaped error; got %v", err)
	}
}

// Every call names its client in Forge-Via, which the control plane's
// activity stream records as "via": "cli" from a terminal, "ci" under a
// pipeline.
func TestClient_Call_SendsForgeVia(t *testing.T) {
	for _, tc := range []struct {
		ci   bool
		want string
	}{{false, "cli"}, {true, "ci"}} {
		buildinfo.SetCI(tc.ci)
		t.Cleanup(buildinfo.ClearCI)
		var got string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got = r.Header.Get(activity.ViaHeader)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		}))
		ep, err := ResolveEndpoint("prod", &Declaration{Endpoint: srv.URL})
		if err != nil {
			t.Fatal(err)
		}
		if err := NewClient(ep, Credential{Token: "t"}).Call(context.Background(), "controlplane.v1.DeployService/ListReleases", map[string]any{}, nil); err != nil {
			t.Fatalf("Call: %v", err)
		}
		srv.Close()
		if got != tc.want {
			t.Errorf("CI=%v: %s = %q, want %q", tc.ci, activity.ViaHeader, got, tc.want)
		}
	}
}

// Every call names this forge's release in Forge-Version, which a control
// plane compares with the oldest release it serves.
func TestClient_Call_SendsForgeVersion(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get(VersionHeader)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	ep, err := ResolveEndpoint("prod", &Declaration{Endpoint: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if err := NewClient(ep, Credential{Token: "t"}).Call(context.Background(), "controlplane.v1.DeployService/ListReleases", map[string]any{}, nil); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if want := buildinfo.Version(); got != want || got == "" {
		t.Errorf("%s = %q, want %q", VersionHeader, got, want)
	}
}

// A control plane that no longer serves this forge answers FailedPrecondition
// with reason client_upgrade_required. The error is the server's upgrade
// instruction, typed by its reason, with none of the procedure/HTTP noise of a
// failed call.
func TestClient_Call_UpgradeRequiredIsPrintedCleanly(t *testing.T) {
	const instruction = "forge v1.0.4 is too old for this control plane: the oldest forge served is v1.0.7; " +
		"install a newer forge and, in a project that pins forge, raise its pin with `forge upgrade`, then retry"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(ReasonHeader, ReasonClientUpgradeRequired)
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "failed_precondition", "message": instruction})
	}))
	defer srv.Close()
	ep, _ := ResolveEndpoint("prod", &Declaration{Endpoint: srv.URL})
	err := NewClient(ep, Credential{Token: "t"}).Call(context.Background(), "controlplane.v1.DeployService/DeleteEnvironment", map[string]any{}, nil)

	var apiErr *Error
	if !errors.As(err, &apiErr) || !apiErr.HasReason(ReasonClientUpgradeRequired) || !apiErr.HasCode(CodeFailedPrecondition) {
		t.Fatalf("want a typed client_upgrade_required error; got %v", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, instruction) || !strings.Contains(msg, srv.URL) {
		t.Errorf("error must carry the server's instruction and name the control plane; got:\n%s", msg)
	}
	if strings.Contains(msg, "DeleteEnvironment") || strings.Contains(msg, "HTTP 400") {
		t.Errorf("error should not read as a failed call; got:\n%s", msg)
	}
}
