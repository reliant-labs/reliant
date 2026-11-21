package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/deploytarget"
	"github.com/reliant-labs/forge/pkg/release"
)

// severalIdentitiesServer is a control plane for --identity: it records every
// identity request body, and advertises CapabilityConvergesSeveralIdentities
// when names is true.
type severalIdentitiesServer struct {
	mu       sync.Mutex
	names    bool
	caps     int
	requests map[string][]map[string]any
}

func newSeveralIdentitiesServer(t *testing.T, names bool) (*severalIdentitiesServer, *httptest.Server) {
	t.Helper()
	s := &severalIdentitiesServer{names: names, requests: map[string][]map[string]any{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		proc := strings.TrimPrefix(r.URL.Path, "/")
		s.mu.Lock()
		defer s.mu.Unlock()
		switch proc {
		case cloud.ProcGetOrganization:
			_, _ = fmt.Fprintf(w, `{"%s":{"orgId":"org"}}`, orgResponseKey)
		case "controlplane.v1.DeployService/ListEnvironments":
			_, _ = w.Write([]byte(`{"environments":[{"id":"env-1","name":"prod","project":"shop"}]}`))
		case deploytarget.ProcDeployCapabilities:
			s.caps++
			features := release.BundleDocFeatures()
			if s.names {
				features = append(features, release.CapabilityConvergesSeveralIdentities)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"bundleDocumentFeatures": features})
		case procIdentityStatus:
			s.requests[proc] = append(s.requests[proc], body)
			_, _ = fmt.Fprintf(w, `{"hasInstance":true,"state":"IDENTITY_STATE_READY","issuer":"https://%v.example"}`, body["identity"])
		case procIdentityUsers:
			s.requests[proc] = append(s.requests[proc], body)
			_, _ = w.Write([]byte(`{"users":[{"userId":"u1","email":"a@acme.test"}]}`))
		case procIdentityInvite:
			s.requests[proc] = append(s.requests[proc], body)
			_, _ = w.Write([]byte(`{"userId":"u1","created":true}`))
		case procIdentityResetPassword:
			s.requests[proc] = append(s.requests[proc], body)
			_, _ = w.Write([]byte(`{"userId":"u1","code":{"code":"c0de"}}`))
		default:
			t.Errorf("unexpected endpoint %s", proc)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return s, srv
}

func (s *severalIdentitiesServer) sent(proc string) []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests[proc]
}

func runIdentityAgainst(t *testing.T, names bool, declared []string, args ...string) (*severalIdentitiesServer, string, error) {
	t.Helper()
	s, srv := newSeveralIdentitiesServer(t, names)
	identityTestTarget(t, srv.URL, declared...)
	t.Setenv("IDENTITY_TEST_TOKEN", "human-token")
	out, err := executeIdentity(args...)
	return s, out, err
}

func identityOf(body map[string]any) (any, bool) {
	v, ok := body["identity"]
	return v, ok
}

// TestCloudIdentityOneIdentitySendsTodaysRequest: an env of one identity
// sends the request every control plane knows, with no `identity` field and no
// capability probe, whether or not --identity names it.
func TestCloudIdentityOneIdentitySendsTodaysRequest(t *testing.T) {
	for _, args := range [][]string{
		{"invite", "prod", "--email", "a@acme.test", "--role", "member"},
		{"invite", "prod", "--email", "a@acme.test", "--role", "member", "--identity", "identity"},
	} {
		s, _, err := runIdentityAgainst(t, true, []string{"identity"}, args...)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		got := s.sent(procIdentityInvite)
		if len(got) != 1 {
			t.Fatalf("%v: invites = %v", args, got)
		}
		if v, ok := identityOf(got[0]); ok {
			t.Errorf("%v: the request names identity %v; one identity named %q must send today's request", args, v, release.DefaultIdentityName)
		}
		if s.caps != 0 {
			t.Errorf("%v: asked the control plane's capabilities %d times for an env of one default identity", args, s.caps)
		}
	}
	// No identity at all: today's request too.
	s, _, err := runIdentityAgainst(t, true, nil, "invite", "prod", "--email", "a@acme.test", "--role", "member")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := identityOf(s.sent(procIdentityInvite)[0]); ok {
		t.Error("an env with no identity named one")
	}
}

// TestCloudIdentityOneRenamedIdentity: an env whose one identity is not named
// "identity" names it only to a control plane that addresses identities by
// name. An older one serves its one identity, so it gets today's request.
func TestCloudIdentityOneRenamedIdentity(t *testing.T) {
	for _, names := range []bool{true, false} {
		s, _, err := runIdentityAgainst(t, names, []string{"ops"}, "invite", "prod", "--email", "a@acme.test", "--role", "member")
		if err != nil {
			t.Fatalf("names=%v: %v", names, err)
		}
		v, ok := identityOf(s.sent(procIdentityInvite)[0])
		if names && v != "ops" {
			t.Errorf("a control plane addressing names was sent identity %v, want ops", v)
		}
		if !names && ok {
			t.Errorf("an older control plane was sent identity %v", v)
		}
	}
}

// TestCloudIdentitySeveralNeedAName: with several, a verb refuses to guess,
// and names the identities to choose from, before it sends anything.
func TestCloudIdentitySeveralNeedAName(t *testing.T) {
	s, _, err := runIdentityAgainst(t, true, []string{"identity", "users"}, "invite", "prod", "--email", "a@acme.test", "--role", "member")
	if err == nil || !strings.Contains(err.Error(), "declares 2 managed identities (identity, users): name one with --identity <name>") {
		t.Fatalf("err = %v", err)
	}
	if len(s.sent(procIdentityInvite)) != 0 {
		t.Error("an ambiguous invite was sent")
	}
	_, _, err = runIdentityAgainst(t, true, []string{"identity", "users"}, "invite", "prod", "--email", "a@acme.test", "--role", "member", "--identity", "staff")
	if err == nil || !strings.Contains(err.Error(), `--identity "staff": env "prod" declares no identity of that name (declared: identity, users)`) {
		t.Fatalf("unknown --identity: err = %v", err)
	}
	_, _, err = runIdentityAgainst(t, true, nil, "invite", "prod", "--email", "a@acme.test", "--role", "member", "--identity", "users")
	if err == nil || !strings.Contains(err.Error(), "declares no managed identity") {
		t.Fatalf("--identity with none declared: err = %v", err)
	}
}

// TestCloudIdentitySeveralNamed: --identity names the instance in every
// request of the verb, the email lookup included.
func TestCloudIdentitySeveralNamed(t *testing.T) {
	s, _, err := runIdentityAgainst(t, true, []string{"identity", "users"}, "reset", "prod", "--user", "a@acme.test", "--identity", "users", "--return-code")
	if err != nil {
		t.Fatal(err)
	}
	for _, proc := range []string{procIdentityUsers, procIdentityResetPassword} {
		got := s.sent(proc)
		if len(got) != 1 {
			t.Fatalf("%s: %v", proc, got)
		}
		if v, _ := identityOf(got[0]); v != "users" {
			t.Errorf("%s names identity %v, want users", proc, v)
		}
	}
}

// TestCloudIdentitySeveralRefusedByAnOlderControlPlane: a control plane that
// does not address identities by name would ignore the field and act on the
// env's one identity, so a verb that needs the name is refused before it
// writes anything.
func TestCloudIdentitySeveralRefusedByAnOlderControlPlane(t *testing.T) {
	s, _, err := runIdentityAgainst(t, false, []string{"identity", "users"}, "invite", "prod", "--email", "a@acme.test", "--role", "member", "--identity", "users")
	if err == nil || !strings.Contains(err.Error(), "addresses one identity per environment") || !strings.Contains(err.Error(), release.CapabilityConvergesSeveralIdentities) {
		t.Fatalf("err = %v", err)
	}
	if len(s.sent(procIdentityInvite)) != 0 {
		t.Error("the invite was sent to a control plane that cannot tell the identities apart")
	}
}

// TestCloudIdentityStatusShowsEach: status with several and no --identity
// asks for each by name and prints them all; --json is one document listing
// them.
func TestCloudIdentityStatusShowsEach(t *testing.T) {
	s, out, err := runIdentityAgainst(t, true, []string{"identity", "users"}, "status", "prod")
	if err != nil {
		t.Fatal(err)
	}
	got := s.sent(procIdentityStatus)
	if len(got) != 2 || got[0]["identity"] != "identity" || got[1]["identity"] != "users" {
		t.Fatalf("status requests = %v", got)
	}
	for _, want := range []string{"Identity: identity\n", "Issuer: https://identity.example", "Identity: users\n", "Issuer: https://users.example"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	_, out, err = runIdentityAgainst(t, true, []string{"identity", "users"}, "status", "prod", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		OK         bool `json:"ok"`
		Identities []struct {
			Identity string `json:"identity"`
			Issuer   string `json:"issuer"`
		} `json:"identities"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if !doc.OK || len(doc.Identities) != 2 || doc.Identities[1].Identity != "users" || doc.Identities[1].Issuer != "https://users.example" {
		t.Errorf("json = %+v", doc)
	}
	// --identity narrows it to one, in today's document shape.
	s, out, err = runIdentityAgainst(t, true, []string{"identity", "users"}, "status", "prod", "--identity", "users", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if got := s.sent(procIdentityStatus); len(got) != 1 || got[0]["identity"] != "users" || !strings.Contains(out, `"issuer": "https://users.example"`) || strings.Contains(out, `"identities"`) {
		t.Errorf("status --identity users: requests %v, output %s", got, out)
	}
}

// TestCloudIdentityStatusOnAnOlderControlPlane: one that serves one identity
// per environment is asked once, as today, and the output says so.
func TestCloudIdentityStatusOnAnOlderControlPlane(t *testing.T) {
	s, out, err := runIdentityAgainst(t, false, []string{"identity", "users"}, "status", "prod")
	if err != nil {
		t.Fatal(err)
	}
	got := s.sent(procIdentityStatus)
	if len(got) != 1 {
		t.Fatalf("status requests = %v", got)
	}
	if _, ok := identityOf(got[0]); ok {
		t.Errorf("an older control plane was sent a name: %v", got[0])
	}
	if !strings.Contains(out, "serves one identity per environment") || !strings.Contains(out, "showing the one it serves") || strings.Contains(out, "Identity: users") {
		t.Errorf("output:\n%s", out)
	}
}
